package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

func seedFixture(t *testing.T) (*sqlite.Store, sqlite.ExternalIdentity, sqlite.Owner, thumbnail.Policy) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "feed.sqlite"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	identity := sqlite.ExternalIdentity{Issuer: "https://issuer.example.com/realm", Subject: "synthetic-owner"}
	owner, err := store.EnrollAccount(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := thumbnail.NewPolicy("")
	return store, identity, owner, policy
}

func seedInput(key string) feed.AddInput {
	return feed.AddInput{IdempotencyKey: key, Article: feed.Content{URL: "https://example.com/story?utm_source=seed", Title: "Synthetic seed article", Summary: "A short synthetic introduction."}}
}

func seedFile(t *testing.T, input any) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSeedResolvesActiveOwnerDeduplicatesAndReplays(t *testing.T) {
	store, identity, owner, policy := seedFixture(t)
	input := []feed.AddInput{seedInput("seed-a"), seedInput("seed-b")}
	input[1].Article.URL = "https://EXAMPLE.com:443/story#section"
	path := seedFile(t, input)
	for range 2 {
		count, err := seedArticles(t.Context(), store, identity, path, policy)
		if err != nil || count != 2 {
			t.Fatalf("seed/replay count=%d error=%v", count, err)
		}
	}
	articles, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{}, 10)
	if err != nil || len(articles) != 1 || articles[0].URL != "https://example.com/story" || articles[0].Revision != 1 {
		t.Fatalf("seed dedup records=%#v error=%v", articles, err)
	}
	unknown := identity
	unknown.Subject = "not-enrolled"
	if count, err := seedArticles(t.Context(), store, unknown, path, policy); err == nil || count != 0 {
		t.Fatal("seed accepted unknown owner")
	}
	if _, err := store.OwnerForIdentity(t.Context(), unknown); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("seed implicitly enrolled an unknown identity")
	}
	if err := store.DisableAccount(t.Context(), identity); err != nil {
		t.Fatal(err)
	}
	if count, err := seedArticles(t.Context(), store, identity, path, policy); err == nil || count != 0 {
		t.Fatal("seed accepted disabled owner")
	}
}

func TestSeedPreflightsAllContentAndBoundsBeforeWriting(t *testing.T) {
	store, identity, owner, policy := seedFixture(t)
	for _, invalid := range []feed.AddInput{
		{IdempotencyKey: "bad", Article: feed.Content{URL: "javascript:alert(1)", Title: "Bad", Summary: "Bad"}},
		{IdempotencyKey: "bad", Article: feed.Content{URL: "https://example.com/other", Title: "", Summary: "Bad"}},
		{IdempotencyKey: "bad", Article: feed.Content{URL: "https://example.com/other", Title: "Bad", Summary: "Bad", ThumbnailURL: "https://images.example.com/tracker.png"}},
		seedInput("seed-a"),
	} {
		path := seedFile(t, []feed.AddInput{seedInput("seed-a"), invalid})
		if count, err := seedArticles(t.Context(), store, identity, path, policy); err == nil || count != 0 {
			t.Fatal("invalid later input partially wrote the file")
		}
	}
	for _, input := range []any{[]feed.AddInput{}, make([]feed.AddInput, 51), []map[string]any{{"owner_id": "foreign"}}} {
		if _, err := readSeedRequests(seedFile(t, input), policy); err == nil {
			t.Fatal("invalid count/unknown field accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxSeedBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSeedRequests(path, policy); err == nil {
		t.Fatal("unbounded manifest accepted")
	}
	articles, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{}, 10)
	if err != nil || len(articles) != 0 {
		t.Fatal("preflight failure left articles")
	}
}

func TestSeedReportsPartialDurableFailureAndKeepsRetrySemantics(t *testing.T) {
	store, identity, owner, policy := seedFixture(t)
	if _, err := seedArticles(t.Context(), store, identity, seedFile(t, []feed.AddInput{seedInput("existing")}), policy); err != nil {
		t.Fatal(err)
	}
	first := seedInput("new")
	first.Article.URL = "https://example.com/new"
	conflict := seedInput("existing")
	conflict.Article.Title = "Changed intent conflicts with its old stable key"
	path := seedFile(t, []feed.AddInput{first, conflict})
	for range 2 {
		count, err := seedArticles(t.Context(), store, identity, path, policy)
		if count != 1 || err == nil || !strings.Contains(err.Error(), "after 1 completed") {
			t.Fatalf("partial result count=%d error=%v", count, err)
		}
	}
	articles, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{}, 10)
	if err != nil || len(articles) != 2 {
		t.Fatal("retry duplicated or lost a committed article")
	}
}
