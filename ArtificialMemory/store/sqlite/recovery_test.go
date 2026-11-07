package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

func TestLiveWALBackupAndIsolatedRestore(t *testing.T) {
	s, source := openTest(t)
	if _, err := s.db.ExecContext(t.Context(), "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	id := account.Identity{Issuer: "https://identity.example/realms/test", Subject: "synthetic-recovery-owner"}
	owner, err := s.EnrollAccount(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	disabled := account.Identity{Issuer: id.Issuer, Subject: "synthetic-disabled"}
	if _, err := s.EnrollAccount(t.Context(), disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DisableAccount(t.Context(), disabled); err != nil {
		t.Fatal(err)
	}
	actor := memory.Actor{AccountID: owner, ClientID: "synthetic-recovery-agent"}
	choice := decision("Original persistence", "SQLite", "PostgreSQL")
	choice.Payload = append(bytes.TrimSuffix(choice.Payload, []byte("}")), []byte(`,"precise":9007199254740993}`)...)
	choice.Provenance = memory.Provenance{Attribution: "synthetic-example", Sources: []memory.Source{{Reference: "synthetic://recovery/discussion", Excerpt: "Synthetic single-node requirement"}}}
	request := memory.RememberRequest{Space: "GuestUp", Key: "snapshot-original", Entries: []memory.Entry{topic("PAYM-123"), choice}}
	original := remember(t, s, actor, request)
	reuseRoot := topic("PAYM-1234")
	reuseRoot.Links = []memory.LinkInput{{Type: memory.DependsOn, Target: memory.Reference{SpanID: original.Records[1].SpanID}}}
	remember(t, s, actor, memory.RememberRequest{Space: "Planning", Key: "reuse", Entries: []memory.Entry{reuseRoot}})
	query := memory.Query{AllSpaces: true, Limit: 1, ContextLimit: 1, LinkLimit: 1}
	first := retrieve(t, s, owner, query)
	if first.NextCursor == "" {
		t.Fatal("fixture requires a persisted cursor")
	}
	query.Cursor = first.NextCursor
	expectedContinuation := retrieve(t, s, owner, query)
	search := memory.Query{Spaces: []string{"GuestUp"}, Keywords: "PostgreSQL", Tags: []string{"storage"}}
	expectedSearch := retrieve(t, s, owner, search)
	if wal, err := os.Stat(source + "-wal"); err != nil || wal.Size() == 0 {
		t.Fatal("fixture has no committed live WAL")
	}
	backup := filepath.Join(t.TempDir(), "snapshots", "backup.db")
	if err := Backup(t.Context(), source, backup); err != nil {
		t.Fatal(err)
	}
	// The live source keeps accepting writes; the backup must stay at its
	// earlier consistent state and never share the source's file or sidecars.
	remember(t, s, actor, memory.RememberRequest{Space: "GuestUp", Key: "after-backup", TopicID: original.TopicID, Entries: []memory.Entry{{Kind: memory.Note, Title: "After snapshot"}}})
	restoredPath := filepath.Join(t.TempDir(), "isolated", "restored.db")
	if err := Restore(t.Context(), backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{backup, restoredPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("snapshot is not private")
		}
	}
	restored, err := Open(t.Context(), restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got, active, err := restored.ResolveAccount(t.Context(), id); err != nil || !active || got != owner {
		t.Fatal("restore lost stable active enrollment")
	}
	if _, active, err := restored.ResolveAccount(t.Context(), disabled); err != nil || active {
		t.Fatal("restore reactivated disabled enrollment")
	}
	if got := remember(t, restored, actor, request); !reflect.DeepEqual(original, got) {
		t.Fatal("restore lost durable IDs, exact JSON, provenance or replay")
	}
	if got := retrieve(t, restored, owner, query); !reflect.DeepEqual(expectedContinuation, got) {
		t.Fatal("restored cursor changed snapshot or history")
	}
	if got := retrieve(t, restored, owner, search); !reflect.DeepEqual(expectedSearch, got) || len(got.Matches) != 1 || len(got.Relationships) != 1 || !bytes.Contains(got.Matches[0].Payload, []byte("9007199254740993")) {
		t.Fatal("restore lost rejected reasons, FTS, tags, ancestry, links or numeric fidelity")
	}
	if got := retrieve(t, restored, owner, memory.Query{AllSpaces: true}); len(got.Matches) != 3 {
		t.Fatal("backup included later writes or restore duplicated records")
	}
	if _, err := restored.db.ExecContext(t.Context(), "DELETE FROM nodes"); err == nil {
		t.Fatal("restore lost append-only schema guards")
	}
}

func TestRecoveryRefusesUnsafePathsAndInvalidSources(t *testing.T) {
	_, source := openTest(t)
	private := t.TempDir()
	missing := filepath.Join(private, "missing.db")
	if err := Backup(t.Context(), missing, filepath.Join(private, "out.db")); err == nil {
		t.Fatal("backup created a missing source")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("missing source was created")
	}
	existing := filepath.Join(private, "existing.db")
	if err := os.WriteFile(existing, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(private, "source.link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(private, "source.hardlink")
	if err := os.Link(source, hardlink); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{source, source}, {source, hardlink}, {source, existing}, {link, filepath.Join(private, "link-output.db")}, {":memory:", filepath.Join(private, "memory.db")}} {
		if err := Restore(t.Context(), pair[0], pair[1]); err == nil {
			t.Fatalf("unsafe restore accepted: %v", pair)
		}
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "keep me" {
		t.Fatal("existing destination was modified")
	}
	public := filepath.Join(private, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := Backup(t.Context(), source, filepath.Join(public, "out.db")); err == nil {
		t.Fatal("public snapshot directory accepted")
	}
	for _, contents := range []string{"", "not sqlite"} {
		bad := filepath.Join(t.TempDir(), "bad.db")
		if err := os.WriteFile(bad, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Restore(t.Context(), bad, filepath.Join(t.TempDir(), "out.db")); err == nil {
			t.Fatal("blank/corrupt snapshot initialized as a journal")
		}
	}
	// Application/schema mismatches and incomplete schemas fail before output.
	for _, statement := range []string{"PRAGMA application_id=7", "PRAGMA user_version=99", "DROP TABLE enrolled_identities"} {
		bad := filepath.Join(t.TempDir(), "bad.db")
		if err := Backup(t.Context(), source, bad); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if err := Restore(t.Context(), bad, filepath.Join(t.TempDir(), "out.db")); err == nil {
			t.Fatal("unsupported/incomplete schema accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output := filepath.Join(private, "canceled.db")
	if err := Backup(ctx, source, output); err == nil {
		t.Fatal("canceled backup succeeded")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("canceled operation retained an output")
	}
}
