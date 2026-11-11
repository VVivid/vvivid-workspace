package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRecommendationMigrationFromV7PreservesArticlesTasksAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.sqlite")
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate(t.Context(), db, migrations[:7]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{read: db, write: db}
	owner, err := legacy.EnrollAccount(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	article := storeArticle(t, legacy, owner, "legacy", thumbnailJPEG(t, 90))
	var retry, hash []byte
	if err = db.QueryRowContext(t.Context(), "SELECT result,payload_hash FROM idempotency_results WHERE owner_id=? AND key='legacy'", owner.id).Scan(&retry, &hash); err != nil {
		t.Fatal(err)
	}
	if err = legacy.AddTodo(t.Context(), owner, TodoTask{ID: "task", Title: "Keep order", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Article(t.Context(), owner, article.Article.ID)
	if err != nil || got != article.Article {
		t.Fatal("article changed", got, err)
	}
	tasks, err := store.ListTodo(t.Context(), owner)
	if err != nil || len(tasks.Tasks) != 1 || tasks.Tasks[0].ID != "task" {
		t.Fatal("tasks changed", tasks, err)
	}
	var after, afterHash []byte
	if err = store.read.QueryRowContext(t.Context(), "SELECT result,payload_hash FROM idempotency_results WHERE owner_id=? AND key='legacy'", owner.id).Scan(&after, &afterHash); err != nil || !bytes.Equal(after, retry) || !bytes.Equal(afterHash, hash) {
		t.Fatal("retry bytes changed", err)
	}
	prefs, err := store.RecommendationPreferences(t.Context(), owner)
	if err != nil || prefs.Revision != 1 || len(prefs.Preferences.Interests) != 0 || len(prefs.Preferences.Sources) != 0 || prefs.Preferences.Discovery.Enabled {
		t.Fatal("migration seeded preferences", prefs, err)
	}
	var count int
	if err = store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM recommendation_preferences").Scan(&count); err != nil || count != 0 {
		t.Fatal("read seeded owner data", err)
	}
}
func TestDiscoveryLeaseExpiryDayBudgetAndDSTBoundaries(t *testing.T) {
	for _, test := range []struct {
		date  string
		hours int
	}{{"2026-03-08T12:00:00Z", 23}, {"2026-11-01T12:00:00Z", 25}, {"2026-11-11T12:00:00Z", 24}} {
		now, err := time.Parse(time.RFC3339, test.date)
		if err != nil {
			t.Fatal(err)
		}
		_, start, end := discoveryDay(now)
		if time.Duration(end-start)*time.Millisecond != time.Duration(test.hours)*time.Hour {
			t.Fatal("day length", test)
		}
	}
	store, _, owner, _ := thumbnailStore(t)
	prefs := DefaultPreferences().Preferences
	prefs.Discovery.Enabled = true
	prefs.Discovery.MaxPerRun = 10
	prefs.Discovery.MaxPerDay = 1
	if _, err := store.UpdateRecommendationPreferences(t.Context(), owner, "p", []byte("p"), 1, prefs); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartDiscoveryRun(t.Context(), owner, "r", []byte("r"), 2)
	if err != nil {
		t.Fatal(err)
	}
	a := Article{URL: "https://example.com/one", Title: "One", Summary: "Source introduction"}
	item := DiscoveryItem{WhyMatch: "Detailed source.", PublishedAt: time.Now().UnixMilli()}
	payload, _ := json.Marshal(a)
	if _, err = store.IngestDiscoveryArticle(t.Context(), owner, "i", payload, run.ID, 2, a, item, ThumbnailChange{Replace: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinishDiscoveryRun(t.Context(), owner, "f", []byte("f"), run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = store.write.ExecContext(t.Context(), "UPDATE discovery_runs SET hour_slot=hour_slot-1 WHERE owner_id=?", owner.id); err != nil {
		t.Fatal(err)
	}
	if _, err = store.StartDiscoveryRun(t.Context(), owner, "day-limit", []byte("day-limit"), 2); !errors.Is(err, ErrLimit) {
		t.Fatal("daily limit bypass", err)
	}
	// A saved item belongs to the New York date on which it was committed.
	_, today, _ := discoveryDay(time.Now())
	if _, err = store.write.ExecContext(t.Context(), "UPDATE discovery_articles SET saved_at=? WHERE owner_id=?", today-1, owner.id); err != nil {
		t.Fatal(err)
	}
	next, err := store.StartDiscoveryRun(t.Context(), owner, "next", []byte("next"), 2)
	if err != nil {
		t.Fatal("next NY day did not regain budget", err)
	}
	if _, err = store.write.ExecContext(t.Context(), "UPDATE discovery_runs SET expires_at=? WHERE owner_id=? AND id=?", time.Now().Add(-time.Minute).UnixMilli(), owner.id, next.ID); err != nil {
		t.Fatal(err)
	}
	a.URL = "https://example.com/two"
	if _, err = store.IngestDiscoveryArticle(t.Context(), owner, "expired", []byte("expired"), next.ID, 2, a, item, ThumbnailChange{Replace: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("expired lease admitted article", err)
	}
	var articles, retries int
	if err = store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM articles WHERE owner_id=?", owner.id).Scan(&articles); err != nil {
		t.Fatal(err)
	}
	if err = store.read.QueryRowContext(t.Context(), "SELECT count(*) FROM idempotency_results WHERE owner_id=? AND key='expired'", owner.id).Scan(&retries); err != nil || articles != 1 || retries != 0 {
		t.Fatal("failed lease wrote partial data", articles, retries, err)
	}
}
