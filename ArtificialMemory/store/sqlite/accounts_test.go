package sqlite

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

func TestExplicitEnrollmentAndStableAccount(t *testing.T) {
	s, _ := openTest(t)
	id := account.Identity{Issuer: "https://identity.example/realms/test", Subject: "synthetic-alice"}
	if _, active, err := s.ResolveAccount(t.Context(), id); err != nil || active {
		t.Fatalf("unknown identity became active: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM accounts").Scan(&count); err != nil || count != 0 {
		t.Fatal("lookup auto-enrolled an account")
	}
	first, err := s.EnrollAccount(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	actor := memory.Actor{AccountID: first, ClientID: "synthetic-agent"}
	written := remember(t, s, actor, memory.RememberRequest{Space: "GuestUp", Key: "enrollment", Entries: []memory.Entry{topic("Before disable")}})
	if found, err := s.DisableAccount(t.Context(), id); err != nil || !found {
		t.Fatalf("disable: %v", err)
	}
	if _, active, err := s.ResolveAccount(t.Context(), id); err != nil || active {
		t.Fatalf("disabled identity still active: %v", err)
	}
	again, err := s.EnrollAccount(t.Context(), id)
	if err != nil || again != first {
		t.Fatalf("re-enrollment changed account ID: %v", err)
	}
	page := retrieve(t, s, again, memory.Query{Spaces: []string{"GuestUp"}, TopicIDs: []string{written.TopicID}})
	if len(page.Matches) != 1 || page.Matches[0].SpanID != written.TopicID {
		t.Fatal("re-enrollment lost the journal")
	}
	other, err := s.EnrollAccount(t.Context(), account.Identity{Issuer: "https://other.example", Subject: id.Subject})
	if err != nil || other == first {
		t.Fatal("equal subject from another issuer shared an account")
	}
	if err := s.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateCheckpointOnePreservesJournalAndContinuation(t *testing.T) {
	// Construct the actual v1 schema and use the unchanged journal adapter to
	// populate it. No identity tables exist before Open performs the v2 migration.
	path := filepath.Join(t.TempDir(), "legacy.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), schema+"INSERT INTO settings VALUES(1,randomblob(32)); PRAGMA application_id=1095583053; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"settings", "spaces", "nodes", "tags", "links", "idempotency"} {
		if err := immutable(t.Context(), tx, table); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.QueryRowContext(t.Context(), "SELECT cursor_key FROM settings").Scan(&legacy.cursorKey); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	request := memory.RememberRequest{Space: "Legacy", Key: "durable", Entries: []memory.Entry{topic("Go and SQLite discussion"), decision("SQLite", "SQLite", "PostgreSQL")}}
	written := remember(t, legacy, alice, request)
	query := memory.Query{Spaces: []string{"Legacy"}, Keywords: "SQLite", Limit: 1, ContextLimit: 1, LinkLimit: 1}
	first := retrieve(t, legacy, alice.AccountID, query)
	if first.NextCursor == "" {
		t.Fatal("fixture needs continuation")
	}
	key := bytes.Clone(legacy.cursorKey)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !bytes.Equal(key, s.cursorKey) {
		t.Fatal("migration replaced the persisted cursor signing key")
	}
	replayed := remember(t, s, alice, request)
	if !reflect.DeepEqual(written, replayed) {
		t.Fatal("migration changed durable replay")
	}
	query.Cursor = first.NextCursor
	if next := retrieve(t, s, alice.AccountID, query); next.SnapshotSequence != first.SnapshotSequence {
		t.Fatal("pre-migration cursor lost its snapshot")
	}
	page := retrieve(t, s, alice.AccountID, memory.Query{Spaces: []string{"Legacy"}, Keywords: "PostgreSQL"})
	if len(page.Matches) != 1 || page.Matches[0].SpanID != written.Records[1].SpanID || len(page.Context) != 1 {
		t.Fatal("migration lost records, FTS or ancestry")
	}
	if _, active, err := s.ResolveAccount(t.Context(), account.Identity{Issuer: "https://identity.example", Subject: alice.AccountID}); err != nil || active {
		t.Fatal("migration inferred enrollment from a trusted legacy account ID")
	}
	if err := s.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
}
