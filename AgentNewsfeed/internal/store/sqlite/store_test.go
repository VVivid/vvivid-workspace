package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistentEnrollmentAndMigrationIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "feed.sqlite")
	store, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	identity := ExternalIdentity{Issuer: "https://issuer.example.com/realm", Subject: "exact-subject"}
	owner, err := store.EnrollAccount(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnrollAccount(t.Context(), identity); err == nil {
		t.Fatal("duplicate enrollment accepted")
	}
	for _, other := range []ExternalIdentity{{Issuer: identity.Issuer + "/", Subject: identity.Subject}, {Issuer: identity.Issuer, Subject: "Exact-subject"}, {Issuer: identity.Issuer, Subject: "not-enrolled"}} {
		if _, err := store.OwnerForIdentity(t.Context(), other); err == nil {
			t.Fatal("inexact identity resolved")
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private database mode lost")
	}
	if err := store.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.OwnerForIdentity(t.Context(), identity)
	if err != nil || got != owner {
		t.Fatal("restart lost enrollment")
	}
	if _, err := store.write.ExecContext(t.Context(), "UPDATE schema_migrations SET checksum = ? WHERE version = 1", strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(t.Context()); err == nil {
		t.Fatal("tampered history ready")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), path, time.Second); err == nil {
		reopened.Close()
		t.Fatal("altered migration history opened")
	}
}

func TestRejectMemoryAndUnknownDatabase(t *testing.T) {
	if s, err := Open(t.Context(), ":memory:", time.Second); err == nil {
		s.Close()
		t.Fatal("memory-only persistence accepted")
	}
	path := filepath.Join(t.TempDir(), "unknown.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE unrelated (id TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(t.Context(), path, time.Second); err == nil {
		s.Close()
		t.Fatal("unidentified database adopted")
	}
}
