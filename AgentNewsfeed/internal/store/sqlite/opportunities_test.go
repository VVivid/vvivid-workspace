package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
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
	if page, err := store.ListOpportunities(t.Context(), owner, OpportunityCursor{View: "active"}, 20); err != nil || len(page) != 0 {
		t.Fatal("migration seeded or converted News into opportunities")
	}
	legacyArticle := Article{URL: "https://example.com/legacy-add", Title: "Source title", Summary: "Source introduction", ThumbnailURL: "https://images.example.com/source.jpg", ContentMarkdown: "# Original source"}
	payload, _ := json.Marshal(legacyArticle)
	if out, err := store.MutateArticle(t.Context(), owner, "legacy-add", "add_article", payload, Article{}, 0); err != nil || out != first {
		t.Fatal("article retry failed after opportunity migration")
	}
}

func TestOpportunityStoreChecksOwnerOnReadsMutationsAndReplay(t *testing.T) {
	s, _, owner, other := thumbnailStore(t)
	c := OpportunityContent{Kind: "job", URL: "https://jobs.example.com/a", Title: "Engineer", Company: "Example"}
	o := Opportunity{OpportunityContent: c}
	payload, _ := json.Marshal(c)
	first, err := s.MutateOpportunity(t.Context(), owner, "key", "add_opportunity", payload, o, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := s.OpportunityInView(t.Context(), Owner{}, first.Opportunity.ID, false)
			return err
		},
		func() error {
			_, err := s.ListOpportunities(t.Context(), Owner{}, OpportunityCursor{View: "active"}, 20)
			return err
		},
		func() error {
			_, _, err := s.ReplayOpportunityMutation(t.Context(), Owner{}, "key", "add_opportunity", payload)
			return err
		},
		func() error {
			_, err := s.MutateOpportunity(t.Context(), Owner{}, "key", "add_opportunity", payload, o, 0, false)
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrOwnerRequired) {
			t.Fatal("unowned store operation accepted")
		}
	}
	if _, err := s.OpportunityInView(t.Context(), other, first.Opportunity.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("store detail crossed owners")
	}
	if _, found, err := s.ReplayOpportunityMutation(t.Context(), other, "key", "add_opportunity", payload); err != nil || found {
		t.Fatal("store replay crossed owners")
	}
	if _, err := s.MutateOpportunity(t.Context(), other, "foreign", "trash_opportunity", []byte("trash"), Opportunity{ID: first.Opportunity.ID}, 1, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("store mutation crossed owners")
	}
	bad := first.Opportunity
	bad.Company = ""
	if _, err := s.MutateOpportunity(t.Context(), owner, "failed", "update_opportunity", []byte("bad"), bad, 1, false); err == nil {
		t.Fatal("database accepted invalid content")
	}
	if current, err := s.OpportunityInView(t.Context(), owner, first.Opportunity.ID, false); err != nil || !reflect.DeepEqual(current, first.Opportunity) {
		t.Fatal("SQL failure partially updated content or revision")
	}
	good := first.Opportunity
	good.Title = "Updated source title"
	if _, err := s.MutateOpportunity(t.Context(), owner, "failed", "update_opportunity", []byte("good"), good, 1, false); err != nil {
		t.Fatal("SQL failure reserved retry key")
	}
	if err := s.DisableAccount(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ReplayOpportunityMutation(t.Context(), owner, "key", "add_opportunity", payload); !errors.Is(err, ErrOwnerRequired) || found {
		t.Fatal("disabled owner replayed stored result")
	}
	if _, err := s.MutateOpportunity(t.Context(), owner, "key", "add_opportunity", payload, o, 0, false); !errors.Is(err, ErrOwnerRequired) {
		t.Fatal("disabled owner mutated/replayed")
	}
	if _, err := s.OpportunityInView(t.Context(), owner, first.Opportunity.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabled owner read detail")
	}
	if rows, err := s.ListOpportunities(t.Context(), owner, OpportunityCursor{View: "active"}, 20); err != nil || len(rows) != 0 {
		t.Fatal("disabled owner listed private content")
	}
}
