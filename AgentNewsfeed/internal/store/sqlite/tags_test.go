package sqlite

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// articleTagsDuringSeed lets current article code seed a database migrated
// only to an earlier version. That code reads and writes article_tags
// (migration 0009), checks article audio (migration 0010) and reads and writes
// the narration column (migration 0011), so those exist only until the
// returned function drops them. The seed must store no tags, audio or
// narration, leaving the earlier schema and rows.
func articleTagsDuringSeed(t *testing.T, db *sql.DB) (drop func()) {
	t.Helper()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0009_article_tags.sql", "0010_article_audio.sql", "0011_article_narration.sql"} {
		i := slices.IndexFunc(migrations, func(m migration) bool { return m.name == name })
		if i < 0 {
			t.Fatalf("migration %s is missing", name)
		}
		if _, err := db.ExecContext(t.Context(), migrations[i].sql); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		t.Helper()
		for _, table := range []string{"article_tags", "article_audio_segments", "article_audio"} {
			var stored int
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&stored); err != nil || stored != 0 {
				t.Fatalf("legacy seed stored %d rows in %s: %v", stored, table, err)
			}
			if _, err := db.ExecContext(t.Context(), "DROP TABLE "+table); err != nil {
				t.Fatal(err)
			}
		}
		var narrated int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM articles WHERE narration != ''").Scan(&narrated); err != nil || narrated != 0 {
			t.Fatalf("legacy seed stored %d narrations: %v", narrated, err)
		}
		if _, err := db.ExecContext(t.Context(), "ALTER TABLE articles DROP COLUMN narration"); err != nil {
			t.Fatal(err)
		}
	}
}

// saveTagged adds an article with already normalized tags, as the feed does.
// An empty URL saves an agent-written article.
func saveTagged(t *testing.T, s *Store, owner Owner, key, url string, tags ...string) MutationResult {
	t.Helper()
	a := Article{URL: url, Title: "Title " + key, Summary: "Introduction " + key, ContentMarkdown: "# Body " + key, Tags: tags}
	payload, _ := json.Marshal(a)
	out, err := s.MutateArticle(t.Context(), owner, key, "add_article", payload, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func listedIDs(t *testing.T, s *Store, owner Owner, cursor Cursor, tags ...string) []string {
	t.Helper()
	articles, err := s.ListArticles(t.Context(), owner, cursor, 10, tags...)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, a := range articles {
		ids = append(ids, a.ID)
	}
	// Equal save times order by random ID, so compare membership only.
	slices.Sort(ids)
	return ids
}

func sortedIDs(results ...MutationResult) []string {
	ids := []string{}
	for _, result := range results {
		ids = append(ids, result.Article.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestArticleTagsMigrationKeepsArticlesRetriesAndHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v8.sqlite")
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO accounts VALUES ('legacy',1,NULL);
 INSERT INTO identities VALUES ('https://issuer.example','subject','legacy');
 INSERT INTO articles(owner_id,id,canonical_url,title,summary,revision,created_at,updated_at,content_markdown)
 VALUES ('legacy','article','https://example.com/old','Original','Introduction',1,1,1,'# Full source')`); err != nil {
		t.Fatal(err)
	}
	// The pre-tags add_article encoding: no tags key, so the hash must still match.
	payload := []byte(`{"id":"","url":"https://example.com/old","title":"Original","summary":"Introduction","revision":0,"created_at":0,"updated_at":0,"content_markdown":"# Full source"}`)
	hash := sha256.Sum256(payload)
	result := []byte(`{"article":{"id":"article","url":"https://example.com/old","title":"Original","summary":"Introduction","revision":1,"created_at":1,"updated_at":1,"content_markdown":"# Full source"},"created":true}`)
	if _, err := db.ExecContext(ctx, "INSERT INTO idempotency_results VALUES ('legacy','saved','add_article',?,?,1)", hash[:], result); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner, err := s.OwnerForIdentity(ctx, ExternalIdentity{Issuer: "https://issuer.example", Subject: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	var expected MutationResult
	if err := json.Unmarshal(result, &expected); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Article(ctx, owner, "article"); err != nil || !reflect.DeepEqual(got, expected.Article) || got.Tags != nil {
		t.Fatalf("migration changed an existing article or invented tags: %#v %v", got, err)
	}
	var gotHash, gotResult []byte
	if err := s.read.QueryRowContext(ctx, "SELECT payload_hash,result FROM idempotency_results WHERE key='saved'").Scan(&gotHash, &gotResult); err != nil || !bytes.Equal(gotHash, hash[:]) || !bytes.Equal(gotResult, result) {
		t.Fatal("migration rewrote retry bytes or hash")
	}
	if replay, err := s.MutateArticle(ctx, owner, "saved", "add_article", payload, Article{}, 0); err != nil || !reflect.DeepEqual(replay, expected) {
		t.Fatalf("untagged retry no longer replays: %v", err)
	}
	for _, previous := range migrations[:8] {
		var name, checksum string
		if err := s.read.QueryRowContext(ctx, "SELECT name,checksum FROM schema_migrations WHERE version=?", previous.version).Scan(&name, &checksum); err != nil || name != previous.name || checksum != previous.checksum {
			t.Fatalf("migration changed history of version %d", previous.version)
		}
	}
	if _, err := migrationVersion(ctx, s.read, migrations[:8]); err == nil {
		t.Fatal("a v8 binary accepted the tagged schema")
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestArticleTagsSchemaAcceptsOnlySlugsOfSavedArticles(t *testing.T) {
	s, _, owner, _ := thumbnailStore(t)
	article := saveTagged(t, s, owner, "article", "https://example.com/article").Article
	insert := func(articleID, tag string) error {
		_, err := s.write.ExecContext(t.Context(), "INSERT INTO article_tags(owner_id,article_id,tag) VALUES (?,?,?)", owner.id, articleID, tag)
		return err
	}
	for _, tag := range []string{"a", "explore", "system-design", "a1-b2-c3", strings.Repeat("x", 32)} {
		if err := insert(article.ID, tag); err != nil {
			t.Fatalf("schema rejected slug %q: %v", tag, err)
		}
	}
	for _, tag := range []string{"", "A", "a_b", "a b", "a--b", "-a", "a-", "a,b", "é", strings.Repeat("x", 33)} {
		if err := insert(article.ID, tag); err == nil {
			t.Fatalf("schema accepted %q", tag)
		}
	}
	if err := insert("missing-article", "explore"); err == nil {
		t.Fatal("schema accepted a tag for an unknown article")
	}
	if err := insert(article.ID, "explore"); err == nil {
		t.Fatal("schema accepted a duplicate tag")
	}
}

func TestTagsAreSavedFilteredAndReplacedWithTheirArticle(t *testing.T) {
	s, _, owner, other := thumbnailStore(t)
	explore := saveTagged(t, s, owner, "explore", "https://example.com/explore", "explore", "go")
	design := saveTagged(t, s, owner, "design", "https://example.com/design", "system-design")
	saveTagged(t, s, owner, "untagged", "https://example.com/untagged")
	archived := saveTagged(t, s, owner, "archived", "https://example.com/archived", "go")
	if _, err := s.SetArchived(t.Context(), owner, archived.Article.ID, true); err != nil {
		t.Fatal(err)
	}
	saveTagged(t, s, other, "foreign", "https://example.com/foreign", "go")

	if got, err := s.Article(t.Context(), owner, explore.Article.ID); err != nil || !slices.Equal(got.Tags, []string{"explore", "go"}) || !reflect.DeepEqual(got, explore.Article) {
		t.Fatalf("saved tags differ from the add result: %#v %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		cursor Cursor
		tags   []string
		want   []string
	}{
		{"one tag", Cursor{}, []string{"go"}, sortedIDs(explore)},
		{"any of several tags", Cursor{}, []string{"go", "system-design"}, sortedIDs(explore, design)},
		{"unused tag", Cursor{}, []string{"rust"}, []string{}},
		{"archive view", Cursor{Archived: true}, []string{"go"}, sortedIDs(archived)},
	} {
		if got := listedIDs(t, s, owner, tc.cursor, tc.tags...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: listed %v, want %v", tc.name, got, tc.want)
		}
	}
	if len(listedIDs(t, s, owner, Cursor{})) != 3 {
		t.Fatal("unfiltered list changed")
	}
	page, err := s.ListArticles(t.Context(), owner, Cursor{}, 10, "go")
	if err != nil || len(page) != 1 || !slices.Equal(page[0].Tags, []string{"explore", "go"}) || page[0].ContentMarkdown != "" {
		t.Fatalf("filtered list lost tags or loaded the body: %#v %v", page, err)
	}

	// Patching replaces the whole set; state changes keep it.
	patched := explore.Article
	patched.Tags = []string{"rust"}
	replaced, err := s.MutateArticle(t.Context(), owner, "retag", "patch_article", []byte("retag"), patched, 1)
	if err != nil || !slices.Equal(replaced.Article.Tags, []string{"rust"}) {
		t.Fatalf("patch did not replace tags: %#v %v", replaced, err)
	}
	if got := listedIDs(t, s, owner, Cursor{}, "go"); len(got) != 0 {
		t.Fatal("replaced tag still matched")
	}
	trashed, err := s.MutateArticle(t.Context(), owner, "trash", "trash_article", []byte("trash"), Article{ID: explore.Article.ID}, 2)
	if err != nil || !slices.Equal(trashed.Article.Tags, []string{"rust"}) {
		t.Fatalf("Trash lost tags: %#v %v", trashed, err)
	}
	if got := listedIDs(t, s, owner, Cursor{Trashed: true}, "rust"); !slices.Equal(got, sortedIDs(explore)) {
		t.Fatal("Trash view did not filter by tag")
	}
	if _, err := s.MutateArticle(t.Context(), owner, "restore", "restore_article", []byte("restore"), Article{ID: explore.Article.ID}, 3); err != nil {
		t.Fatal(err)
	}

	// A canonical duplicate keeps the saved article and its tags.
	duplicate := Article{URL: design.Article.URL, Title: "Other", Summary: "Other", Tags: []string{"other"}}
	if out, err := s.MutateArticle(t.Context(), owner, "duplicate", "add_article", []byte("duplicate"), duplicate, 0); err != nil || out.Created || !reflect.DeepEqual(out.Article, design.Article) {
		t.Fatalf("duplicate add changed tags: %#v %v", out, err)
	}
	// Retries return their original tags after later changes.
	a := Article{URL: "https://example.com/explore", Title: "Title explore", Summary: "Introduction explore", ContentMarkdown: "# Body explore", Tags: []string{"explore", "go"}}
	payload, _ := json.Marshal(a)
	if replay, err := s.MutateArticle(t.Context(), owner, "explore", "add_article", payload, a, 0); err != nil || !reflect.DeepEqual(replay, explore) {
		t.Fatalf("add replay changed: %#v %v", replay, err)
	}

	// Tags commit only with the article and its retry result.
	if _, err := s.write.ExecContext(t.Context(), `CREATE TRIGGER reject_article_retry BEFORE INSERT ON idempotency_results
		BEGIN SELECT RAISE(ABORT, 'synthetic retry persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	cleared := design.Article
	cleared.Tags = nil
	if _, err := s.MutateArticle(t.Context(), owner, "failed", "patch_article", []byte("failed"), cleared, 1); err == nil {
		t.Fatal("patch succeeded without its retry result")
	}
	if got, err := s.Article(t.Context(), owner, design.Article.ID); err != nil || !reflect.DeepEqual(got, design.Article) {
		t.Fatalf("failed patch changed tags: %#v %v", got, err)
	}
}

// usesOwnerTagIndex reports whether SQLite plans query with the owner+tag
// index of migration 0009, and returns the plan's steps for failure messages.
func usesOwnerTagIndex(t *testing.T, s *Store, query string, args []any) (bool, []string) {
	t.Helper()
	rows, err := s.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(plan, func(step string) bool { return strings.Contains(step, "article_tags_by_owner_tag") }), plan
}

func TestTagFilterUsesOwnerTagIndex(t *testing.T) {
	s, _, owner, _ := thumbnailStore(t)
	filter, order, args := articleSelection(owner, Cursor{}, "", []string{"explore", "go"})
	page, pageArgs := pageRange(order, "a.id", 0, "", false, 21)
	if used, plan := usesOwnerTagIndex(t, s, "SELECT "+articleSummaryColumns+", ''"+articleFrom+"WHERE "+filter+page, append(args, pageArgs...)); !used {
		t.Fatalf("tag filter does not use the owner+tag index: %q", plan)
	}
}

func TestTagCountsCountOneViewOfOneOwnerMostUsedFirst(t *testing.T) {
	s, _, owner, other := thumbnailStore(t)
	ctx := t.Context()
	saveTagged(t, s, owner, "go-1", "https://example.com/go-1", "explore", "go")
	saveTagged(t, s, owner, "go-2", "https://example.com/go-2", "explore", "go")
	saveTagged(t, s, owner, "rust", "https://example.com/rust", "explore", "rust")
	saveTagged(t, s, owner, "written", "", "generated", "leetcode")
	saveTagged(t, s, owner, "untagged", "https://example.com/untagged")
	archived := saveTagged(t, s, owner, "archived", "https://example.com/archived", "explore", "system-design")
	if _, err := s.SetArchived(ctx, owner, archived.Article.ID, true); err != nil {
		t.Fatal(err)
	}
	trashed := saveTagged(t, s, owner, "trashed", "https://example.com/trashed", "rust")
	if _, err := s.MutateArticle(ctx, owner, "trash", "trash_article", []byte("trash"), Article{ID: trashed.Article.ID}, 1); err != nil {
		t.Fatal(err)
	}
	saveTagged(t, s, other, "foreign", "https://example.com/foreign", "foreign", "go")

	for _, tc := range []struct {
		name              string
		archived, trashed bool
		want              []TagCount
	}{
		// Equal counts sort by tag; the archived and trashed articles count only in their own views.
		{"active", false, false, []TagCount{{"explore", 3}, {"go", 2}, {"generated", 1}, {"leetcode", 1}, {"rust", 1}}},
		{"archive", true, false, []TagCount{{"explore", 1}, {"system-design", 1}}},
		{"trash", false, true, []TagCount{{"rust", 1}}},
	} {
		if got, err := s.TagCounts(ctx, owner, tc.archived, tc.trashed); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: counted %v (%v), want %v", tc.name, got, err, tc.want)
		}
	}
	if got, err := s.TagCounts(ctx, other, false, false); err != nil || !slices.Equal(got, []TagCount{{"foreign", 1}, {"go", 1}}) {
		t.Fatalf("another owner's counts: %v %v", got, err)
	}
	// An empty view lists no tags as an empty list, never null.
	if got, err := s.TagCounts(ctx, other, true, false); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty view: %#v %v", got, err)
	}
	if _, err := s.TagCounts(ctx, owner, true, true); !errors.Is(err, ErrLimit) {
		t.Fatalf("Archive and Trash together: %v", err)
	}
	if _, err := s.TagCounts(ctx, Owner{}, false, false); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("counted tags without an owner: %v", err)
	}
	if err := s.DisableAccount(ctx, ExternalIdentity{Issuer: "https://issuer.example", Subject: "alice"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TagCounts(ctx, owner, false, false); err != nil || len(got) != 0 {
		t.Fatalf("a disabled owner's tags were counted: %v %v", got, err)
	}
}

func TestTagCountsUseOwnerTagIndex(t *testing.T) {
	s, _, owner, _ := thumbnailStore(t)
	for _, view := range []struct{ archived, trashed bool }{{false, false}, {true, false}, {false, true}} {
		query, args := tagCountsQuery(owner, view.archived, view.trashed)
		if used, plan := usesOwnerTagIndex(t, s, query, args); !used {
			t.Fatalf("tag counts of view %+v do not use the owner+tag index: %q", view, plan)
		}
	}
}

func TestArticleWithoutURLKeepsItsPlaceholderPrivate(t *testing.T) {
	s, _, owner, _ := thumbnailStore(t)
	first := saveTagged(t, s, owner, "written-1", "", "generated")
	second := saveTagged(t, s, owner, "written-2", "", "generated")
	sourced := saveTagged(t, s, owner, "sourced", "https://example.com/sourced", "explore")
	if !first.Created || !second.Created || first.Article.ID == second.Article.ID || first.Article.URL != "" {
		t.Fatal("articles without a URL were deduplicated or exposed a URL")
	}
	var stored string
	if err := s.read.QueryRowContext(t.Context(), "SELECT canonical_url FROM articles WHERE owner_id=? AND id=?", owner.id, first.Article.ID).Scan(&stored); err != nil || stored != noURLPrefix+first.Article.ID {
		t.Fatalf("unexpected stored placeholder %q: %v", stored, err)
	}
	if got, err := s.Article(t.Context(), owner, first.Article.ID); err != nil || !reflect.DeepEqual(got, first.Article) {
		t.Fatalf("read exposed the placeholder: %#v %v", got, err)
	}
	listed, err := s.ListArticles(t.Context(), owner, Cursor{}, 10)
	if err != nil || len(listed) != 3 {
		t.Fatal(err)
	}
	for _, a := range listed {
		if strings.Contains(a.URL, noURLPrefix) {
			t.Fatal("list exposed the placeholder")
		}
	}
	if _, err := s.ArticleForURL(t.Context(), owner, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("an empty URL matched an article")
	}
	for query, want := range map[string]int{"no-url": 0, first.Article.ID: 0, "written-1": 1, "example.com/sourced": 1} {
		page, err := s.BrowseArticles(t.Context(), owner, Cursor{}, 10, strings.ToLower(query))
		if err != nil || page.Total != want {
			t.Fatalf("search %q matched %d articles, want %d: %v", query, page.Total, want, err)
		}
	}
	context, err := s.DiscoveryContext(t.Context(), owner)
	if err != nil || !slices.Equal(context.RecentURLs, []string{sourced.Article.URL}) {
		t.Fatalf("discovery context exposed placeholders: %v %v", context.RecentURLs, err)
	}
	rows, err := s.read.QueryContext(t.Context(), "SELECT result FROM idempotency_results WHERE owner_id=?", owner.id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var result []byte
		if err := rows.Scan(&result); err != nil || bytes.Contains(result, []byte(noURLPrefix)) {
			t.Fatalf("retry result exposed the placeholder: %s %v", result, err)
		}
	}

	// A URL, once added, deduplicates like any other; removing it restores the placeholder.
	withURL := first.Article
	withURL.URL = sourced.Article.URL
	if _, err := s.MutateArticle(t.Context(), owner, "collide", "patch_article", []byte("collide"), withURL, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("a URL shared with another article was accepted: %v", err)
	}
	withURL.URL = "https://example.com/now-sourced"
	if out, err := s.MutateArticle(t.Context(), owner, "add-url", "patch_article", []byte("add-url"), withURL, 1); err != nil || out.Article.URL != withURL.URL {
		t.Fatalf("adding a URL failed: %v", err)
	}
	if found, err := s.ArticleForURL(t.Context(), owner, withURL.URL); err != nil || found.ID != first.Article.ID {
		t.Fatal("added URL is not deduplicated")
	}
	withoutURL := withURL
	withoutURL.URL = ""
	if out, err := s.MutateArticle(t.Context(), owner, "remove-url", "patch_article", []byte("remove-url"), withoutURL, 2); err != nil || out.Article.URL != "" {
		t.Fatalf("removing the URL failed: %v", err)
	}
	if err := s.read.QueryRowContext(t.Context(), "SELECT canonical_url FROM articles WHERE owner_id=? AND id=?", owner.id, first.Article.ID).Scan(&stored); err != nil || stored != noURLPrefix+first.Article.ID {
		t.Fatalf("removing the URL did not restore the placeholder: %q %v", stored, err)
	}
}
