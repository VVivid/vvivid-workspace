package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oudavid/GoogleHealthMCP/internal/collector"
)

func TestOfflineStatusAndSanitizedArguments(t *testing.T) {
	var output bytes.Buffer
	dir := t.TempDir()
	args := []string{"status", "--db", filepath.Join(dir, "health.sqlite3"), "--tokens", filepath.Join(dir, "missing-tokens")}
	if code := run(t.Context(), args, &output); code != 0 {
		t.Fatal("offline status should work before credentials exist")
	}
	var status struct {
		Verified bool  `json:"identity_and_sample_verified"`
		Ready    bool  `json:"unattended_ready"`
		Streams  []any `json:"streams"`
	}
	if json.Unmarshal(output.Bytes(), &status) != nil || status.Verified || status.Ready || len(status.Streams) != 34 {
		t.Fatal("fresh status claimed readiness or omitted metrics")
	}
	lock, err := collector.AcquireLock(filepath.Join(dir, "health.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	output.Reset()
	if code := run(t.Context(), args, &output); code != 0 {
		t.Fatal("offline status blocked behind an active importer lock")
	}
	output.Reset()
	if code := run(t.Context(), []string{"sync", "--synthetic-private-token=DO_NOT_ECHO"}, &output); code != 2 || strings.Contains(output.String(), "DO_NOT_ECHO") {
		t.Fatal("invalid argument output leaked caller data")
	}
	output.Reset()
	args = append(args, "--require-authorized")
	if code := run(t.Context(), args, &output); code != 1 {
		t.Fatal("authorization gate accepted fresh credentials")
	}
}

func TestSharedTokenStoreIsLockedAcrossDifferentDatabases(t *testing.T) {
	dir := t.TempDir()
	tokenDir := filepath.Join(dir, "tokens")
	lock, err := collector.AcquireLock(filepath.Join(tokenDir, "token-store"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var output bytes.Buffer
	code := run(t.Context(), []string{"sync", "--db", filepath.Join(dir, "other.sqlite3"), "--tokens", tokenDir, "--client", filepath.Join(dir, "absent-client.json")}, &output)
	if code != 1 || !strings.Contains(output.String(), "collector_busy") {
		t.Fatal("a second database bypassed the shared token-store rotation lock")
	}
}
