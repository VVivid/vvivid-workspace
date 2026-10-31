package feed_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/thumbnail"
)

const scopes = identity.Read + " " + identity.ArticlesWrite

func setup(t *testing.T) (*testfixture.Identity, *feed.Service, identity.Principal, identity.Principal) {
	t.Helper()
	f := testfixture.New(t)
	f.Enroll(t, "alice")
	f.Enroll(t, "bob")
	policy, _ := thumbnail.NewPolicy("images.example.com")
	return f, feed.New(f.Store, policy), f.Principal(t, "alice", scopes), f.Principal(t, "bob", scopes)
}
func content() feed.Content {
	return feed.Content{URL: "https://example.com/story?utm_source=agent#top", Title: "A sourced story", Summary: "A short introduction supplied by the source."}
}

func TestOwnershipDedupRevisionsAndDurableReplay(t *testing.T) {
	f, s, alice, bob := setup(t)
	in := feed.AddInput{IdempotencyKey: "save", Article: content()}
	first, err := s.AddArticle(t.Context(), alice, in)
	if err != nil || !first.Created || first.Article.Revision != 1 {
		t.Fatalf("add: %#v %v", first, err)
	}
	if first.Article.URL != "https://example.com/story" {
		t.Fatal("canonical URL not stored")
	}
	duplicate := in
	duplicate.IdempotencyKey = "duplicate"
	duplicate.Article.Title = "New title should not overwrite"
	duplicate.Article.URL = "https://EXAMPLE.com:443/story?fbclid=other"
	same, err := s.AddArticle(t.Context(), alice, duplicate)
	if err != nil || same.Created || same.Article != first.Article {
		t.Fatalf("dedup: %#v %v", same, err)
	}
	owned, err := s.AddArticle(t.Context(), bob, in)
	if err != nil || owned.Article.ID == first.Article.ID || !owned.Created {
		t.Fatalf("per-owner dedup %v", err)
	}
	if _, err := s.GetArticle(t.Context(), bob, first.Article.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign read %v", err)
	}
	update := feed.UpdateInput{IdempotencyKey: "revise", ArticleID: first.Article.ID, ExpectedRevision: 1, Article: content()}
	update.Article.Title = "Revised story"
	if _, err := s.UpdateArticle(t.Context(), bob, update); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign update %v", err)
	}
	revised, err := s.UpdateArticle(t.Context(), alice, update)
	if err != nil || revised.Article.Revision != 2 || revised.Article.Title != "Revised story" {
		t.Fatalf("revision %v", err)
	}
	stale := update
	stale.IdempotencyKey = "stale"
	if _, err := s.UpdateArticle(t.Context(), alice, stale); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("stale write %v", err)
	}
	changed := in
	changed.Article.Title = "Different intent"
	if _, err := s.AddArticle(t.Context(), alice, changed); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("changed replay %v", err)
	}
	f.Reopen(t)
	policy, _ := thumbnail.NewPolicy("images.example.com")
	s = feed.New(f.Store, policy)
	replay, err := s.AddArticle(t.Context(), alice, in)
	if err != nil || replay != first {
		t.Fatalf("restart replay changed original outcome %#v %v", replay, err)
	}
	replay, err = s.UpdateArticle(t.Context(), alice, update)
	if err != nil || replay != revised {
		t.Fatalf("restart update replay %v", err)
	}
	if err := f.Store.DisableAccount(t.Context(), alice.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddArticle(t.Context(), alice, in); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("disabled cached actor wrote: %v", err)
	}
	if _, err := s.ListArticles(t.Context(), alice, feed.ListInput{}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("disabled cached actor read: %v", err)
	}
	page, err := s.ListArticles(t.Context(), bob, feed.ListInput{})
	if err != nil || len(page.Articles) != 1 {
		t.Fatal("disabling Alice affected Bob")
	}
}

func TestConcurrentDedupAndSingleRevisionWinner(t *testing.T) {
	_, s, alice, _ := setup(t)
	results := make(chan sqlite.MutationResult, 8)
	failures := make(chan error, 8)
	var group sync.WaitGroup
	for i := range 8 {
		group.Go(func() {
			out, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: fmt.Sprintf("save-%d", i), Article: content()})
			results <- out
			failures <- err
		})
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	var id string
	for out := range results {
		if out.Created {
			count++
		}
		if id != "" && id != out.Article.ID {
			t.Fatal("duplicate article inserted")
		}
		id = out.Article.ID
	}
	if count != 1 {
		t.Fatalf("created %d duplicates", count)
	}
	errorsOut := make(chan error, 8)
	for i := range 8 {
		group.Go(func() {
			in := feed.UpdateInput{IdempotencyKey: fmt.Sprintf("edit-%d", i), ArticleID: id, ExpectedRevision: 1, Article: content()}
			in.Article.Title = fmt.Sprintf("Edit %d", i)
			_, err := s.UpdateArticle(t.Context(), alice, in)
			errorsOut <- err
		})
	}
	group.Wait()
	close(errorsOut)
	wins, conflicts := 0, 0
	for err := range errorsOut {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, sqlite.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 7 {
		t.Fatalf("revision winners=%d conflicts=%d", wins, conflicts)
	}
}

func TestValidationScopesPaginationAndFailedKeys(t *testing.T) {
	f, s, alice, bob := setup(t)
	browser, _ := f.BrowserPrincipal(t, f.Cookie(t, "alice"))
	if _, err := s.AddArticle(t.Context(), browser, feed.AddInput{IdempotencyKey: "browser", Article: content()}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("browser write: %v", err)
	}
	writeOnly := f.Principal(t, "alice", identity.ArticlesWrite)
	if _, err := s.ListArticles(t.Context(), writeOnly, feed.ListInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("write scope implied read")
	}
	for _, change := range []func(*feed.Content){func(c *feed.Content) { c.URL = "javascript:alert(1)" }, func(c *feed.Content) { c.Title = "" }, func(c *feed.Content) { c.Summary = "intro\ncontrol" }, func(c *feed.Content) { c.ThumbnailURL = "https://attacker.example.com/a.jpg" }, func(c *feed.Content) { c.ThumbnailURL = "https://user:pass@images.example.com/a.jpg" }} {
		in := feed.AddInput{IdempotencyKey: "not-reserved", Article: content()}
		change(&in.Article)
		if _, err := s.AddArticle(t.Context(), alice, in); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("bad content: %v", err)
		}
	}
	good := content()
	good.ThumbnailURL = "https://images.example.com/source.jpg"
	if _, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "not-reserved", Article: good}); err != nil {
		t.Fatalf("validation reserved key: %v", err)
	}
	for i := range 4 {
		c := content()
		c.URL = fmt.Sprintf("https://example.com/story/%d", i)
		if _, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: fmt.Sprint(i), Article: c}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.ListArticles(t.Context(), alice, feed.ListInput{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Articles {
			if seen[a.ID] {
				t.Fatal("repeated cursor item")
			}
			seen[a.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("pagination lost articles %d", len(seen))
	}
	empty, err := s.ListArticles(t.Context(), bob, feed.ListInput{})
	if err != nil || len(empty.Articles) != 0 {
		t.Fatal("list leaked other owner")
	}
	if _, err := s.ListArticles(t.Context(), alice, feed.ListInput{Cursor: "untrusted"}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatal("bad cursor accepted")
	}
	if _, err := s.ListArticles(t.Context(), alice, feed.ListInput{Limit: 51}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatal("unbounded page accepted")
	}
}

func TestURLUpdateCollisionRollsBackAndDoesNotReserveKey(t *testing.T) {
	_, s, p, _ := setup(t)
	a, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "a", Article: content()})
	if err != nil {
		t.Fatal(err)
	}
	c := content()
	c.URL = "https://example.com/other"
	b, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "b", Article: c})
	if err != nil {
		t.Fatal(err)
	}
	in := feed.UpdateInput{IdempotencyKey: "collision", ArticleID: b.Article.ID, ExpectedRevision: 1, Article: content()}
	if _, err := s.UpdateArticle(t.Context(), p, in); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("URL collision %v", err)
	}
	unchanged, err := s.GetArticle(t.Context(), p, b.Article.ID)
	if err != nil || unchanged != b.Article {
		t.Fatal("failed update changed state")
	}
	in.Article = c
	in.Article.Title = "Corrected"
	updated, err := s.UpdateArticle(t.Context(), p, in)
	if err != nil || updated.Article.Revision != 2 {
		t.Fatalf("failed operation reserved key %v", err)
	}
	if _, err := s.GetArticle(t.Context(), p, a.Article.ID); err != nil {
		t.Fatal(err)
	}
}
