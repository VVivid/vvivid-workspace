package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOpportunityMigrationPreservesArticlesThumbnailsHistoryAndRetryBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.sqlite")
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations[:3]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{read: db, write: db}
	external := ExternalIdentity{Issuer: "https://issuer.example", Subject: "legacy"}
	owner, err := legacy.EnrollAccount(t.Context(), external)
	if err != nil {
		t.Fatal(err)
	}
	jpeg := thumbnailJPEG(t, 150)
	first := storeArticle(t, legacy, owner, "legacy-add", jpeg)
	archived, err := legacy.MutateArticle(t.Context(), owner, "legacy-archive", "set_article_archived", []byte("legacy archive intent"), Article{ID: first.Article.ID, ArchivedAt: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	trashed, err := legacy.MutateArticle(t.Context(), owner, "legacy-trash", "trash_article", []byte("legacy trash intent"), Article{ID: first.Article.ID}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Article.ArchivedAt == 0 || trashed.Article.TrashedAt == 0 {
		t.Fatal("legacy fixture did not exercise reading state")
	}
	type retryRow struct {
		Key, Operation string
		Hash, Result   []byte
		CreatedAt      int64
	}
	readRetries := func(db *sql.DB) []retryRow {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), `SELECT key,operation,payload_hash,result,created_at FROM idempotency_results WHERE owner_id=? ORDER BY key`, owner.id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []retryRow
		for rows.Next() {
			var row retryRow
			if err := rows.Scan(&row.Key, &row.Operation, &row.Hash, &row.Result, &row.CreatedAt); err != nil {
				t.Fatal(err)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	originalRetries := readRetries(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, err := store.ArticleInView(t.Context(), owner, first.Article.ID, true); err != nil || got != trashed.Article {
		t.Fatalf("migration changed full article/content/image/state: %#v %v", got, err)
	}
	if image, err := store.Thumbnail(t.Context(), owner, first.Article.ID, true); err != nil || !bytes.Equal(image, jpeg) {
		t.Fatal("migration changed thumbnail BLOB/reference")
	}
	if got := readRetries(store.read); !reflect.DeepEqual(got, originalRetries) {
		t.Fatal("migration changed article retry operation/hash/result bytes or timestamps")
	}
	for _, previous := range migrations[:3] {
		var name, checksum string
		if err := store.read.QueryRowContext(t.Context(), `SELECT name,checksum FROM schema_migrations WHERE version=?`, previous.version).Scan(&name, &checksum); err != nil || name != previous.name || checksum != previous.checksum {
			t.Fatal("migration changed previous history")
		}
	}
	if _, err := migrationVersion(t.Context(), store.read, migrations[:3]); err == nil {
		t.Fatal("old binary accepted newer schema")
	}
	if err := store.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	var listings int
	if err := store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM opportunities").Scan(&listings); err != nil || listings != 0 {
		t.Fatalf("unexpected opportunity count after migration: %d, %v", listings, err)
	}
	legacyArticle := Article{URL: "https://example.com/legacy-add", Title: "Source title", Summary: "Source introduction", ThumbnailURL: "https://images.example.com/source.jpg", ContentMarkdown: "# Original source"}
	payload, _ := json.Marshal(legacyArticle)
	if out, err := store.MutateArticle(t.Context(), owner, "legacy-add", "add_article", payload, Article{}, 0); err != nil || out != first {
		t.Fatal("article retry failed after opportunity migration")
	}
}
