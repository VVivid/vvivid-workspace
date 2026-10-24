// Package daemon composes the standalone server and owns its complete lifecycle.
package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/managementhttp"
	"github.com/VVivid/vvivid-workspace/WebhookControl/worker"
)

// Config is trusted operator input, never an agent-managed source snapshot.
// Times are whole seconds unless named otherwise. Zero selects the documented
// bounded default; negative and excessive limits are rejected before listening.
type Config struct {
	DatabasePath                  string                     `json:"database_path"`
	KeyFile                       string                     `json:"key_file"`
	DigestKeyFile                 string                     `json:"digest_key_file"`
	Listen                        string                     `json:"listen"`
	AllowNonLoopback              bool                       `json:"allow_non_loopback"`
	OAuth                         managementhttp.OAuthConfig `json:"oauth"`
	AllowedManagementOrigins      []string                   `json:"allowed_management_origins"`
	DestinationOrigins            []httptransport.OriginRule `json:"destination_origins"`
	MetadataRetentionSeconds      int64                      `json:"metadata_retention_seconds"`
	DeduplicationRetentionSeconds int64                      `json:"deduplication_retention_seconds"`
	CleanupIntervalSeconds        int                        `json:"cleanup_interval_seconds"`
	MaxDatabasePages              int64                      `json:"max_database_pages"`
	RequestsPerMinute             int                        `json:"requests_per_minute"`
	ImmediateBudgetSeconds        int                        `json:"immediate_budget_seconds"`
	WorkerConcurrency             int                        `json:"worker_concurrency"`
	WorkerPollMilliseconds        int                        `json:"worker_poll_milliseconds"`
	WorkerLeaseSeconds            int                        `json:"worker_lease_seconds"`
	WorkerRenewSeconds            int                        `json:"worker_renew_seconds"`
	WorkerMaxAttempts             uint64                     `json:"worker_max_attempts"`
	WorkerInitialBackoffSeconds   int                        `json:"worker_initial_backoff_seconds"`
	WorkerMaxBackoffSeconds       int                        `json:"worker_max_backoff_seconds"`
	MaxHeaderBytes                int                        `json:"max_header_bytes"`
	MaxConnections                int                        `json:"max_connections"`
	MaxInFlightRequests           int                        `json:"max_in_flight_requests"`
	ReadHeaderTimeoutSeconds      int                        `json:"read_header_timeout_seconds"`
	ReadTimeoutSeconds            int                        `json:"read_timeout_seconds"`
	WriteTimeoutSeconds           int                        `json:"write_timeout_seconds"`
	IdleTimeoutSeconds            int                        `json:"idle_timeout_seconds"`
	RequestTimeoutSeconds         int                        `json:"request_timeout_seconds"`
	ShutdownTimeoutSeconds        int                        `json:"shutdown_timeout_seconds"`
}

func LoadConfig(path string) (Config, error) {
	var config Config
	f, err := os.Open(path)
	if err != nil {
		return config, errors.New("cannot open daemon configuration")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return config, errors.New("configuration exceeds 256 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || d.Decode(new(any)) != io.EOF {
		return config, errors.New("invalid daemon configuration JSON")
	}
	return config.Validated()
}

// Validated returns a copy with explicit defaults. Validation never reads a key
// or creates a database, so invalid operator input has no storage side effects.
func (c Config) Validated() (Config, error) {
	invalid := errors.New("invalid daemon configuration")
	for _, path := range []string{c.DatabasePath, c.KeyFile, c.DigestKeyFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsRune(path, 0) {
			return c, invalid
		}
	}
	if c.DatabasePath == c.KeyFile || c.DatabasePath == c.DigestKeyFile || c.KeyFile == c.DigestKeyFile {
		return c, invalid
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if c.KeyFile == c.DatabasePath+suffix || c.DigestKeyFile == c.DatabasePath+suffix {
			return c, invalid
		}
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return c, invalid
	}
	ip, err := netip.ParseAddr(host)
	p, portErr := strconv.Atoi(port)
	if err != nil || ip.Zone() != "" || portErr != nil || p < 1 || p > 65535 || !ip.IsLoopback() && !c.AllowNonLoopback {
		return c, invalid
	}
	if len(c.DestinationOrigins) > 256 || len(c.AllowedManagementOrigins) > 64 {
		return c, invalid
	}
	if _, err := httptransport.NewPolicy(c.DestinationOrigins); err != nil {
		return c, invalid
	}
	// The HTTP adapter also validates these. Do it before opening persistent
	// state or contacting issuer discovery to keep startup failures predictable.
	for i, raw := range append([]string{c.OAuth.Issuer, c.OAuth.Resource}, c.AllowedManagementOrigins...) {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
			return c, invalid
		}
		if u.Port() != "" {
			n, err := strconv.Atoi(u.Port())
			if err != nil || n < 1 || n > 65535 {
				return c, invalid
			}
		}
		loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
		if u.Scheme != "https" && !(u.Scheme == "http" && c.OAuth.AllowInsecureLoopback && loopback) {
			return c, invalid
		}
		if i == 1 && u.Path != "/mcp" || i > 1 && u.Path != "" {
			return c, invalid
		}
	}
	if c.MetadataRetentionSeconds == 0 {
		c.MetadataRetentionSeconds = 604800
	}
	if c.DeduplicationRetentionSeconds == 0 {
		c.DeduplicationRetentionSeconds = 604800
	}
	if c.MetadataRetentionSeconds < 1 || c.MetadataRetentionSeconds > 31536000 || c.DeduplicationRetentionSeconds < 1 || c.DeduplicationRetentionSeconds > 31536000 || c.MaxDatabasePages < 0 || c.MaxDatabasePages > 4294967294 {
		return c, invalid
	}
	for _, bound := range []struct {
		value               *int
		fallback, low, high int
	}{
		{&c.CleanupIntervalSeconds, 60, 1, 3600}, {&c.RequestsPerMinute, 60, 1, 10000},
		{&c.ImmediateBudgetSeconds, 30, 1, 60}, {&c.WorkerConcurrency, 4, 1, 64},
		{&c.WorkerPollMilliseconds, 250, 10, 60000}, {&c.WorkerLeaseSeconds, 30, 1, 300},
		{&c.WorkerRenewSeconds, 10, 1, 299}, {&c.WorkerInitialBackoffSeconds, 1, 1, 3600},
		{&c.WorkerMaxBackoffSeconds, 60, 1, 3600}, {&c.MaxHeaderBytes, 16384, 1024, 65536},
		{&c.MaxConnections, 128, 1, 4096}, {&c.MaxInFlightRequests, 64, 1, 1024},
		{&c.ReadHeaderTimeoutSeconds, 5, 1, 30}, {&c.ReadTimeoutSeconds, 15, 1, 120},
		{&c.WriteTimeoutSeconds, 75, 1, 180}, {&c.IdleTimeoutSeconds, 30, 1, 120},
		{&c.RequestTimeoutSeconds, 70, 1, 120}, {&c.ShutdownTimeoutSeconds, 10, 1, 120},
	} {
		if *bound.value == 0 {
			*bound.value = bound.fallback
		}
		if *bound.value < bound.low || *bound.value > bound.high {
			return c, invalid
		}
	}
	if c.WorkerMaxAttempts == 0 {
		c.WorkerMaxAttempts = 8
	}
	if c.WorkerMaxAttempts > 100 || c.WorkerRenewSeconds >= c.WorkerLeaseSeconds || c.WorkerInitialBackoffSeconds > c.WorkerMaxBackoffSeconds || c.MaxInFlightRequests > c.MaxConnections || c.ReadHeaderTimeoutSeconds > c.ReadTimeoutSeconds || c.WriteTimeoutSeconds <= c.RequestTimeoutSeconds || c.RequestTimeoutSeconds <= c.ImmediateBudgetSeconds {
		return c, invalid
	}
	return c, nil
}

func (c Config) workerOptions() worker.Options {
	return worker.Options{
		Concurrency:    c.WorkerConcurrency,
		LeaseDuration:  time.Duration(c.WorkerLeaseSeconds) * time.Second,
		RenewInterval:  time.Duration(c.WorkerRenewSeconds) * time.Second,
		PollInterval:   time.Duration(c.WorkerPollMilliseconds) * time.Millisecond,
		MaxAttempts:    c.WorkerMaxAttempts,
		InitialBackoff: time.Duration(c.WorkerInitialBackoffSeconds) * time.Second,
		MaxBackoff:     time.Duration(c.WorkerMaxBackoffSeconds) * time.Second,
	}
}
