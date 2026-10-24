package daemon_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/VVivid/vvivid-workspace/WebhookControl/daemon"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
)

func TestTrustedConfigurationRejectsInvalidPathsURLsAndLimits(t *testing.T) {
	f := newFixture(t)
	for _, test := range []struct {
		name   string
		change func(*daemon.Config)
	}{
		{"relative database", func(c *daemon.Config) { c.DatabasePath = "state.db" }},
		{"shared keys", func(c *daemon.Config) { c.DigestKeyFile = c.KeyFile }},
		{"key in SQLite sidecar", func(c *daemon.Config) { c.KeyFile = c.DatabasePath + "-wal" }},
		{"nonloopback without opt-in", func(c *daemon.Config) { c.Listen = "0.0.0.0:8080" }},
		{"invalid port", func(c *daemon.Config) { c.Listen = "127.0.0.1:70000" }},
		{"insecure external issuer", func(c *daemon.Config) { c.OAuth.Issuer = "http://external.invalid" }},
		{"resource query", func(c *daemon.Config) { c.OAuth.Resource += "?audience=other" }},
		{"resource path", func(c *daemon.Config) { c.OAuth.Resource = "https://manage.example/other" }},
		{"origin with path", func(c *daemon.Config) { c.AllowedManagementOrigins = []string{"https://manage.example/path"} }},
		{"unapproved HTTP destination", func(c *daemon.Config) {
			c.DestinationOrigins = []httptransport.OriginRule{{Origin: "http://receiver.example"}}
		}},
		{"negative retention", func(c *daemon.Config) { c.MetadataRetentionSeconds = -1 }},
		{"excessive retention", func(c *daemon.Config) { c.DeduplicationRetentionSeconds = 1 << 62 }},
		{"unbounded headers", func(c *daemon.Config) { c.MaxHeaderBytes = 1 << 30 }},
		{"negative concurrency", func(c *daemon.Config) { c.WorkerConcurrency = -1 }},
		{"lease renewal after deadline", func(c *daemon.Config) { c.WorkerRenewSeconds = c.WorkerLeaseSeconds }},
		{"request exceeds write limit", func(c *daemon.Config) { c.RequestTimeoutSeconds = 100; c.WriteTimeoutSeconds = 75 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := f.cfg
			test.change(&c)
			if _, err := c.Validated(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	c := f.cfg
	c.AllowNonLoopback = true
	c.Listen = "0.0.0.0:8080"
	if _, err := c.Validated(); err != nil {
		t.Fatal("explicit container listen rejected")
	}
	path := f.file("unknown.json", []byte(`{"secret_auth_bypass":true}`))
	if _, err := daemon.LoadConfig(path); err == nil {
		t.Fatal("unknown config field accepted")
	}
	path = f.file("oversized.json", make([]byte, (256<<10)+1))
	if _, err := daemon.LoadConfig(path); err == nil {
		t.Fatal("oversized config accepted")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "state.db")); !os.IsNotExist(err) {
		t.Fatal("configuration validation created storage")
	}
}
