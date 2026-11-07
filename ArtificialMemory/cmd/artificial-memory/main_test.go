package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUnconfiguredCommandFailsAndAdminIsExplicit(t *testing.T) {
	var out, log bytes.Buffer
	if code := run(t.Context(), []string{"serve"}, func(string) string { return "" }, &out, &log); code != 1 {
		t.Fatal("unconfigured service did not fail")
	}
	path := filepath.Join(t.TempDir(), "private", "memory.db")
	subjectFile := filepath.Join(t.TempDir(), "subject")
	const subject = "synthetic-private-subject"
	if err := os.WriteFile(subjectFile, []byte(subject+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"admin", "enroll", "--database", path, "--issuer", "https://identity.example/realms/test", "--subject-file", subjectFile}
	call := func(args []string, want int) []byte {
		t.Helper()
		out.Reset()
		log.Reset()
		if code := run(t.Context(), args, func(string) string { return "" }, &out, &log); code != want {
			t.Fatalf("command exit %d, want %d: %s", code, want, log.String())
		}
		if strings.Contains(out.String()+log.String(), subject) || strings.Contains(log.String(), path) || strings.Contains(log.String(), subjectFile) {
			t.Fatal("admin leaked subject or private path")
		}
		return bytes.Clone(out.Bytes())
	}
	first := call(args, 0)
	if !bytes.Equal(first, call(args, 0)) {
		t.Fatal("repeat enrollment changed account")
	}
	disable := slices.Clone(args)
	disable[1] = "disable"
	call(disable, 0)
	if !bytes.Equal(first, call(args, 0)) {
		t.Fatal("re-enrollment changed account")
	}
	var result struct {
		AccountID string `json:"account_id"`
		Active    bool   `json:"active"`
	}
	if json.Unmarshal(first, &result) != nil || result.AccountID == "" || !result.Active {
		t.Fatal("missing safe enrollment result")
	}
	call(append(args, "--subject", subject), 2)
	if err := os.Chmod(subjectFile, 0644); err != nil {
		t.Fatal(err)
	}
	call(args, 1)
	if err := os.Chmod(subjectFile, 0600); err != nil {
		t.Fatal(err)
	}
	link := subjectFile + ".link"
	if err := os.Symlink(subjectFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSubject(link); err == nil {
		t.Fatal("symlink subject file accepted")
	}
}

func TestOperatorBackupAndIsolatedRestoreCommands(t *testing.T) {
	var out, log bytes.Buffer
	private := t.TempDir()
	database := filepath.Join(private, "source.db")
	subject := filepath.Join(private, "subject")
	if err := os.WriteFile(subject, []byte("synthetic-backup-subject"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args []string, want int) {
		t.Helper()
		out.Reset()
		log.Reset()
		if code := run(t.Context(), args, func(string) string { return "" }, &out, &log); code != want {
			t.Fatalf("exit %d want %d: %s", code, want, log.String())
		}
		if strings.Contains(out.String()+log.String(), private) {
			t.Fatal("snapshot command exposed private paths")
		}
	}
	call([]string{"admin", "enroll", "--database", database, "--issuer", "https://identity.example", "--subject-file", subject}, 0)
	backup := filepath.Join(private, "backup.db")
	restored := filepath.Join(private, "restored.db")
	call([]string{"admin", "backup", "--database", database, "--output", backup}, 0)
	call([]string{"admin", "restore", "--backup", backup, "--database", restored}, 0)
	call([]string{"admin", "restore", "--backup", backup, "--database", restored}, 1)
	call([]string{"admin", "backup", "--database", filepath.Join(private, "absent"), "--output", filepath.Join(private, "new")}, 1)
	call([]string{"admin", "backup", "--database", database}, 2)
	call([]string{"admin", "restore", "--backup", backup, "--database", filepath.Join(private, "new"), "--timeout", "0"}, 2)
}
