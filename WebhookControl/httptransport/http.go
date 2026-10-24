package httptransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/clock"
	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

const MaxRetryAfter = time.Hour

// Resolver is injectable for DNS-policy tests. Connections always use a standard
// net.Dialer and the validated numeric address; events cannot supply a dialer.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Options struct {
	Resolver Resolver
	Clock    ports.Clock
	// RootCAs optionally supplies server trust configuration for private receivers.
	// TLS hostname verification is always enabled.
	RootCAs *x509.CertPool
}

type Transporter struct {
	policy   *Policy
	secrets  ports.SecretReader
	resolver Resolver
	clock    ports.Clock
	rootCAs  *x509.CertPool
}

var _ ports.Transporter = (*Transporter)(nil)

func New(policy *Policy, secrets ports.SecretReader, options Options) (*Transporter, error) {
	if policy == nil || secrets == nil {
		return nil, ErrOptions
	}
	if options.Resolver == nil {
		options.Resolver = net.DefaultResolver
	}
	if options.Clock == nil {
		options.Clock = clock.Real{}
	}
	var roots *x509.CertPool
	if options.RootCAs != nil {
		roots = options.RootCAs.Clone()
	}
	return &Transporter{policy: policy, secrets: secrets, resolver: options.Resolver, clock: options.Clock, rootCAs: roots}, nil
}

func (t *Transporter) Send(ctx context.Context, request ports.SendRequest) (ports.SendResult, error) {
	if ctx.Err() != nil {
		return ports.SendResult{}, ctx.Err()
	}
	snapshot := request.Snapshot
	config := snapshot.Transporter.HTTP
	if err := t.policy.ApproveDestination(ctx, snapshot.Source.ID, config); err != nil {
		return ports.SendResult{}, err
	}
	if !validID(string(request.DeliveryID)) || !validID(string(snapshot.Source.ID)) {
		return ports.SendResult{}, ports.ErrInvalidPayload
	}
	if snapshot.Transporter.SourceID != snapshot.Source.ID || snapshot.Transporter.Kind != domain.TransporterHTTP || !validHTTP(config) {
		return ports.SendResult{}, ports.ErrInvalidPayload
	}
	if len(snapshot.Body) == 0 || len(snapshot.Body) > 64<<10 || !json.Valid(snapshot.Body) || !bytes.HasPrefix(bytes.TrimSpace(snapshot.Body), []byte("{")) {
		return ports.SendResult{}, ports.ErrInvalidPayload
	}
	d, _ := parseDestination(config.URL) // Approved above, with identical parsing.
	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	headers, err := t.headers(ctx, config)
	if err != nil {
		return ports.SendResult{}, err
	}
	headers.Set("Idempotency-Key", string(request.DeliveryID))
	headers.Set("Content-Type", "application/json")
	if _, ok := headers["User-Agent"]; !ok {
		headers.Set("User-Agent", "WebhookControl/1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url.String(), bytes.NewReader(snapshot.Body))
	if err != nil {
		return ports.SendResult{}, ports.ErrInvalidPayload
	}
	req.Header = headers
	// A fresh transport prevents pooled connections from bypassing per-attempt
	// DNS checks. ProxyFromEnvironment is intentionally never installed.
	transport := &http.Transport{
		Proxy: nil, DialContext: t.pinnedDial(d, timeout), DisableKeepAlives: true,
		DisableCompression: true, TLSHandshakeTimeout: min(timeout, 5*time.Second),
		ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 16 << 10,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: d.hostname, RootCAs: t.rootCAs},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return ports.SendResult{}, safeNetworkError(ctx, err)
	}
	defer response.Body.Close()
	result := ports.SendResult{StatusCode: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"), t.clock.Now())}
	// Keep-alive is disabled, so closing without reading discards the body and
	// cannot let a stalled response delay an already observed HTTP acceptance.
	return result, nil
}

func (t *Transporter) pinnedDial(d destination, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	allowPrivate := t.policy.origins[d.origin]
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != net.JoinHostPort(d.hostname, d.port) {
			return nil, ports.ErrPolicyDenied
		}
		var addresses []netip.Addr
		if ip, err := netip.ParseAddr(d.hostname); err == nil {
			addresses = []netip.Addr{ip}
		} else {
			var err error
			addresses, err = t.resolver.LookupNetIP(ctx, "ip", d.hostname)
			if err != nil {
				return nil, safeNetworkError(ctx, err)
			}
		}
		if len(addresses) == 0 {
			return nil, ports.ErrNetwork
		}
		// Reject the complete answer before dialing any member. A mixed public
		// and forbidden answer must not fall back into a private destination.
		for _, ip := range addresses {
			if !allowedAddress(ip, allowPrivate) {
				return nil, ports.ErrPolicyDenied
			}
		}
		dialer := net.Dialer{Timeout: min(timeout, 5*time.Second)}
		for _, ip := range addresses {
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), d.port))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, ports.ErrNetwork
	}
}

func safeNetworkError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ports.ErrPolicyDenied) {
		return ports.ErrPolicyDenied
	}
	if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() {
		return context.DeadlineExceeded
	}
	return ports.ErrNetwork
}

func (t *Transporter) headers(ctx context.Context, config domain.HTTPConfig) (http.Header, error) {
	headers := make(http.Header, len(config.Headers)+len(config.SecretHeaders)+2)
	for key, value := range config.Headers {
		headers.Set(key, value)
	}
	for key, ref := range config.SecretHeaders {
		value, err := t.secrets.ReadSecret(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ports.ErrSecretUnavailable
		}
		if len(value) == 0 || !validHeaderValue(string(value)) {
			return nil, ports.ErrInvalidPayload
		}
		headers.Set(key, string(value))
	}
	return headers, nil
}

func validHTTP(config domain.HTTPConfig) bool {
	if config.TimeoutSeconds < 1 || config.TimeoutSeconds > 60 || len(config.Headers)+len(config.SecretHeaders) > 32 {
		return false
	}
	seen := map[string]bool{}
	for key, value := range config.Headers {
		canonical := http.CanonicalHeaderKey(key)
		if !validHeaderName(key) || seen[canonical] || !validHeaderValue(value) {
			return false
		}
		if canonical != "Accept" && canonical != "Content-Type" && canonical != "User-Agent" || canonical == "Content-Type" && value != "application/json" {
			return false
		}
		seen[canonical] = true
	}
	for key, ref := range config.SecretHeaders {
		canonical := http.CanonicalHeaderKey(key)
		if !validHeaderName(key) || seen[canonical] || !validID(ref.Name) || !validID(ref.Version) {
			return false
		}
		switch canonical {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Trailer", "Upgrade", "Te", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Content-Type", "Content-Encoding", "Idempotency-Key":
			return false
		}
		seen[canonical] = true
	}
	return true
}

func validID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validHeaderName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	if len(value) > 1024 {
		return false
	}
	for _, c := range value {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	digits := true
	for _, c := range value {
		if c < '0' || c > '9' {
			digits = false
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(MaxRetryAfter/time.Second) {
			return MaxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0
	}
	return min(date.Sub(now), MaxRetryAfter)
}
