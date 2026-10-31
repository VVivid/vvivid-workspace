package sqlite

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestReaderMigrationPreservesExistingArticlesAndRetryBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO accounts(id,created_at) VALUES ('legacy-owner',1);
		INSERT INTO identities(issuer,subject,owner_id) VALUES ('https://issuer.example','legacy-subject','legacy-owner');
		INSERT INTO articles(owner_id,id,canonical_url,title,summary,revision,created_at,updated_at)
		VALUES ('legacy-owner','legacy-article','https://example.com/legacy','Original title','Original introduction',1,11,11)`); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"","url":"https://example.com/legacy","title":"Original title","summary":"Original introduction","revision":0,"created_at":0,"updated_at":0}`)
	hash := sha256.Sum256(payload)
	result := []byte(`{"article":{"id":"legacy-article","url":"https://example.com/legacy","title":"Original title","summary":"Original introduction","revision":1,"created_at":11,"updated_at":11},"created":true}`)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO idempotency_results VALUES ('legacy-owner','legacy-key','add_article',?,?,11)`, hash[:], result); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := store.OwnerForIdentity(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "legacy-subject"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Article(t.Context(), owner, "legacy-article")
	if err != nil || a.ContentMarkdown != "" || a.ArchivedAt != 0 || a.Title != "Original title" || a.Revision != 1 {
		t.Fatalf("migration changed legacy article: %#v %v", a, err)
	}
	var archived sql.NullInt64
	if err := store.read.QueryRowContext(t.Context(), "SELECT archived_at FROM articles WHERE id='legacy-article'").Scan(&archived); err != nil || archived.Valid {
		t.Fatal("legacy record was not active with NULL archive time")
	}
	var storedResult, storedHash []byte
	if err := store.read.QueryRowContext(t.Context(), "SELECT payload_hash,result FROM idempotency_results WHERE key='legacy-key'").Scan(&storedHash, &storedResult); err != nil || !bytes.Equal(storedResult, result) || !bytes.Equal(storedHash, hash[:]) {
		t.Fatal("migration modified durable retry bytes")
	}
	var expected MutationResult
	if err := json.Unmarshal(result, &expected); err != nil {
		t.Fatal(err)
	}
	replay, err := store.MutateArticle(t.Context(), owner, "legacy-key", "add_article", payload, Article{}, 0)
	if err != nil || replay != expected {
		t.Fatalf("legacy retry lost: %v", err)
	}
	if err := store.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
}
