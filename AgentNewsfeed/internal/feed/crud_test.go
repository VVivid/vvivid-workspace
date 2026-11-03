package feed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"sync/atomic"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

type preparedImages struct {
	body   []byte
	err    error
	calls  atomic.Int64
	before func()
}

func (f *preparedImages) FetchThumbnail(context.Context, string) ([]byte, error) {
	f.calls.Add(1)
	if f.before != nil {
		f.before()
	}
	return bytes.Clone(f.body), f.err
}

func preparedJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 160, 80))
	for x := range 160 {
		img.Set(x, 0, color.RGBA{R: 200, A: 255})
	}
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadModifyToolsRequireBothScopesBeforeMutationAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		archived        bool
	}{
		{name: "patch", operation: "patch_article"},
		{name: "trash", operation: "trash_article"},
		{name: "restore", operation: "restore_article"},
		{name: "archive", operation: "set_article_archived", archived: true},
		{name: "archive-noop", operation: "set_article_archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, alice, _ := setup(t)
			images := &preparedImages{body: preparedJPEG(t)}
			policy, _ := thumbnail.NewPolicy("images.example.com")
			s := feed.NewWithThumbnailFetcher(f.Store, policy, images)
			c := content()
			c.ContentMarkdown, c.ThumbnailURL = "# Private original body", "https://images.example.com/source.jpg"
			first, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "source", Article: c})
			if err != nil {
				t.Fatal(err)
			}
			current := first.Article
			if tc.operation == "restore_article" {
				trashed, err := s.TrashArticle(t.Context(), alice, feed.StateInput{IdempotencyKey: "prepare-trash", ArticleID: current.ID, ExpectedRevision: current.Revision})
				if err != nil {
					t.Fatal(err)
				}
				current = trashed.Article
			}
			state := feed.StateInput{IdempotencyKey: "mutation", ArticleID: current.ID, ExpectedRevision: current.Revision}
			mutate := func(p identity.Principal) (sqlite.MutationResult, error) {
				switch tc.operation {
				case "patch_article":
					return s.PatchArticle(t.Context(), p, feed.PatchInput{IdempotencyKey: state.IdempotencyKey, ArticleID: state.ArticleID, ExpectedRevision: state.ExpectedRevision, Patch: feed.Patch{Title: new("Corrected title"), ThumbnailURL: new(c.ThumbnailURL)}})
				case "trash_article":
					return s.TrashArticle(t.Context(), p, state)
				case "restore_article":
					return s.RestoreArticle(t.Context(), p, state)
				default:
					return s.SetArticleArchived(t.Context(), p, feed.ArchiveInput{IdempotencyKey: state.IdempotencyKey, ArticleID: state.ArticleID, ExpectedRevision: state.ExpectedRevision, Archived: tc.archived})
				}
			}
			principals := []struct {
				name string
				p    identity.Principal
			}{
				{"write-only", f.Principal(t, "alice", identity.ArticlesWrite)},
				{"read-only", f.Principal(t, "alice", identity.Read)},
			}
			assertDenied := func(stage string, want sqlite.Article, wantFetches int64) {
				t.Helper()
				for _, actor := range principals {
					out, err := mutate(actor.p)
					if !errors.Is(err, identity.ErrForbidden) || out != (sqlite.MutationResult{}) {
						t.Fatalf("%s %s exposed a mutation result: %#v %v", stage, actor.name, out, err)
					}
					got, err := s.GetArticleInView(t.Context(), alice, want.ID, want.TrashedAt != 0)
					if err != nil || got != want {
						t.Fatalf("%s %s changed content, revision or state: %#v %v", stage, actor.name, got, err)
					}
					body, err := s.Thumbnail(t.Context(), alice, want.ID, want.TrashedAt != 0)
					if err != nil || !bytes.Equal(body, images.body) || images.calls.Load() != wantFetches {
						t.Fatalf("%s %s fetched or changed the thumbnail: %v", stage, actor.name, err)
					}
				}
			}
			assertDenied("first attempt", current, 1)
			out, err := mutate(alice)
			if err != nil || out.Article.ContentMarkdown != c.ContentMarkdown || out.Article.ThumbnailHash != first.Article.ThumbnailHash {
				t.Fatalf("fully authorized mutation lost saved content: %#v %v", out, err)
			}
			if tc.operation == "set_article_archived" && !tc.archived && out.Article != current {
				t.Fatal("archive no-op changed the article")
			}
			fetches := images.calls.Load()
			assertDenied("exact retry", out.Article, fetches)
			if replay, err := mutate(alice); err != nil || replay != out || images.calls.Load() != fetches {
				t.Fatalf("fully authorized exact retry changed its result or fetched: %#v %v", replay, err)
			}
		})
	}
}

func TestLegacyAddUpdateKeepWriteOnlyContractAndReadsNeedOnlyRead(t *testing.T) {
	f, s, _, _ := setup(t)
	writeOnly := f.Principal(t, "alice", identity.ArticlesWrite)
	readOnly := f.Principal(t, "alice", identity.Read)
	c := content()
	c.ContentMarkdown = "# Original body"
	add := feed.AddInput{IdempotencyKey: "legacy-add", Article: c}
	first, err := s.AddArticle(t.Context(), writeOnly, add)
	if err != nil || first.Article.ContentMarkdown != c.ContentMarkdown {
		t.Fatalf("write-only add failed: %#v %v", first, err)
	}
	c.Title, c.ContentMarkdown = "Replacement title", "# Replacement body"
	update := feed.UpdateInput{IdempotencyKey: "legacy-update", ArticleID: first.Article.ID, ExpectedRevision: 1, Article: c}
	updated, err := s.UpdateArticle(t.Context(), writeOnly, update)
	if err != nil || updated.Article.Revision != 2 || updated.Article.ContentMarkdown != c.ContentMarkdown {
		t.Fatalf("write-only update failed: %#v %v", updated, err)
	}
	if replay, err := s.AddArticle(t.Context(), writeOnly, add); err != nil || replay != first {
		t.Fatalf("write-only add replay changed: %#v %v", replay, err)
	}
	if replay, err := s.UpdateArticle(t.Context(), writeOnly, update); err != nil || replay != updated {
		t.Fatalf("write-only update replay changed: %#v %v", replay, err)
	}
	if got, err := s.GetArticleTool(t.Context(), readOnly, feed.GetInput{ArticleID: first.Article.ID}); err != nil || got != updated.Article {
		t.Fatalf("read-only get failed: %#v %v", got, err)
	}
	page, err := s.ListArticles(t.Context(), readOnly, feed.ListInput{})
	if err != nil || len(page.Articles) != 1 || page.Articles[0].ID != first.Article.ID || page.Articles[0].ContentMarkdown != "" {
		t.Fatalf("read-only list failed: %#v %v", page, err)
	}
}

func TestPartialUpdatesStoredThumbnailsAndExactReplayAfterTrashAndRestart(t *testing.T) {
	f, _, alice, bob := setup(t)
	images := &preparedImages{body: preparedJPEG(t)}
	policy, _ := thumbnail.NewPolicy("images.example.com")
	s := feed.NewWithThumbnailFetcher(f.Store, policy, images)
	c := content()
	c.ContentMarkdown, c.ThumbnailURL = "# Original source\n\nKeep this body.", "https://images.example.com/a.png"
	in := feed.AddInput{IdempotencyKey: "stored", Article: c}
	first, err := s.AddArticle(t.Context(), alice, in)
	if err != nil || first.Article.ThumbnailStatus != "stored" || first.Article.ThumbnailHash == "" || first.Article.ThumbnailWidth != 160 || first.Article.ThumbnailBytes != len(images.body) {
		t.Fatalf("stored metadata: %#v %v", first, err)
	}
	if body, err := s.Thumbnail(t.Context(), alice, first.Article.ID, false); err != nil || !bytes.Equal(body, images.body) {
		t.Fatal("stored payload unavailable")
	}
	if _, err := s.Thumbnail(t.Context(), bob, first.Article.ID, false); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("foreign thumbnail exposed")
	}
	patch := feed.PatchInput{IdempotencyKey: "title-only", ArticleID: first.Article.ID, ExpectedRevision: 1, Patch: feed.Patch{Title: new("A corrected source title")}}
	updated, err := s.PatchArticle(t.Context(), alice, patch)
	if err != nil || updated.Article.ContentMarkdown != c.ContentMarkdown || updated.Article.ThumbnailHash != first.Article.ThumbnailHash || images.calls.Load() != 1 {
		t.Fatalf("partial update lost omitted fields: %v", err)
	}
	imagePatch := feed.PatchInput{IdempotencyKey: "image-only", ArticleID: first.Article.ID, ExpectedRevision: 2, Patch: feed.Patch{ThumbnailURL: new("https://images.example.com/b.jpg")}}
	refreshed, err := s.PatchArticle(t.Context(), alice, imagePatch)
	if err != nil || refreshed.Article.ContentMarkdown != c.ContentMarkdown || refreshed.Article.Title != updated.Article.Title || refreshed.Article.ThumbnailStatus != "stored" || images.calls.Load() != 2 {
		t.Fatalf("independent thumbnail update: %v", err)
	}
	duplicate := in
	duplicate.IdempotencyKey = "duplicate-no-fetch"
	duplicate.Article.ThumbnailURL = "https://images.example.com/unused.jpg"
	if out, err := s.AddArticle(t.Context(), alice, duplicate); err != nil || out.Created || out.Article != refreshed.Article || images.calls.Load() != 2 {
		t.Fatal("canonical duplicate fetched or replaced content")
	}
	stale := imagePatch
	stale.IdempotencyKey = "stale-no-fetch"
	if _, err := s.PatchArticle(t.Context(), alice, stale); !errors.Is(err, sqlite.ErrConflict) || images.calls.Load() != 2 {
		t.Fatal("stale image patch fetched or wrote")
	}
	trashed, err := s.TrashArticle(t.Context(), alice, feed.StateInput{IdempotencyKey: "trash", ArticleID: first.Article.ID, ExpectedRevision: 3})
	if err != nil || trashed.Article.TrashedAt == 0 {
		t.Fatalf("trash: %v", err)
	}
	if replay, err := s.PatchArticle(t.Context(), alice, patch); err != nil || replay != updated || images.calls.Load() != 2 {
		t.Fatal("patch replay depended on later Trash state")
	}
	if replay, err := s.AddArticle(t.Context(), alice, in); err != nil || replay != first {
		t.Fatal("add retry no longer returns original")
	}
	f.Reopen(t)
	s = feed.NewWithThumbnailFetcher(f.Store, policy, images)
	if replay, err := s.PatchArticle(t.Context(), alice, imagePatch); err != nil || replay != refreshed || images.calls.Load() != 2 {
		t.Fatal("restart image retry refetched or changed result")
	}
	if body, err := s.Thumbnail(t.Context(), alice, first.Article.ID, true); err != nil || !bytes.Equal(body, images.body) {
		t.Fatal("Trash/restart lost thumbnail")
	}
	page, err := s.ListArticles(t.Context(), alice, feed.ListInput{Trashed: true})
	if err != nil || len(page.Articles) != 1 || page.Articles[0].ContentMarkdown != "" || page.Articles[0].ThumbnailBytes != len(images.body) {
		t.Fatal("list loaded body or lost metadata")
	}
	raw, _ := json.Marshal(page)
	if bytes.Contains(raw, images.body) || bytes.Contains(raw, []byte("content_markdown")) || bytes.Contains(raw, []byte("/9j/")) {
		t.Fatal("list leaked body or image bytes")
	}
}

func TestPatchPresenceValidationScopesAndClear(t *testing.T) {
	f, s, alice, bob := setup(t)
	c := content()
	c.ContentMarkdown = "# Original body"
	first, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "first", Article: c})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `[]`, `{"unknown":"x"}`, `{"title":null}`, `{"summary":null}`, `{"url":null}`, `{"title":true}`, `{"title":"one","title":"two"}`} {
		var patch feed.Patch
		if json.Unmarshal([]byte(raw), &patch) == nil {
			t.Fatalf("invalid patch JSON accepted: %s", raw)
		}
	}
	for _, patch := range []feed.Patch{{}, {Title: new("")}, {Summary: new(" ")}, {URL: new("")}, {ContentMarkdown: new("bad\x00text")}, {ThumbnailURL: new("http://images.example.com/a")}} {
		if _, err := s.PatchArticle(t.Context(), alice, feed.PatchInput{IdempotencyKey: "not-reserved", ArticleID: first.Article.ID, ExpectedRevision: 1, Patch: patch}); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid patch accepted: %#v %v", patch, err)
		}
	}
	var patch feed.Patch
	if err := json.Unmarshal([]byte(`{"content_markdown":null,"thumbnail_url":""}`), &patch); err != nil {
		t.Fatal(err)
	}
	in := feed.PatchInput{IdempotencyKey: "not-reserved", ArticleID: first.Article.ID, ExpectedRevision: 1, Patch: patch}
	readOnly := f.Principal(t, "alice", identity.Read)
	if _, err := s.PatchArticle(t.Context(), readOnly, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("read-only patch accepted")
	}
	if _, err := s.PatchArticle(t.Context(), bob, in); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("foreign patch exposed existence")
	}
	if _, err := s.GetArticleTool(t.Context(), f.Principal(t, "alice", identity.ArticlesWrite), feed.GetInput{ArticleID: first.Article.ID}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("write scope implied full-body read")
	}
	cleared, err := s.PatchArticle(t.Context(), alice, in)
	if err != nil || cleared.Article.ContentMarkdown != "" || cleared.Article.Title != c.Title || cleared.Article.ThumbnailURL != "" || cleared.Article.Revision != 2 {
		t.Fatalf("explicit clear failed: %v", err)
	}
	in.Patch = feed.Patch{Summary: new("Different intent")}
	if _, err := s.PatchArticle(t.Context(), alice, in); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatal("key reused for a different patch")
	}
	if err := f.Store.DisableAccount(t.Context(), alice.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchArticle(t.Context(), alice, in); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled actor patched")
	}
	if _, err := s.GetArticleTool(t.Context(), alice, feed.GetInput{ArticleID: first.Article.ID}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled actor read")
	}
}

func TestTrashRestorePreservesArchiveAndCannotBeEditedOrRevived(t *testing.T) {
	f, s, alice, bob := setup(t)
	in := feed.AddInput{IdempotencyKey: "source", Article: content()}
	first, err := s.AddArticle(t.Context(), alice, in)
	if err != nil {
		t.Fatal(err)
	}
	archiveIn := feed.ArchiveInput{IdempotencyKey: "archive", ArticleID: first.Article.ID, ExpectedRevision: 1, Archived: true}
	archived, err := s.SetArticleArchived(t.Context(), alice, archiveIn)
	if err != nil || archived.Article.ArchivedAt == 0 || archived.Article.Revision != 2 {
		t.Fatalf("MCP archive: %v", err)
	}
	trashIn := feed.StateInput{IdempotencyKey: "trash", ArticleID: first.Article.ID, ExpectedRevision: 2}
	for _, p := range []identity.Principal{bob, f.Principal(t, "alice", identity.Read)} {
		if _, err := s.TrashArticle(t.Context(), p, trashIn); err == nil {
			t.Fatal("unowned or read-only Trash accepted")
		}
	}
	stale := trashIn
	stale.ExpectedRevision = 1
	if _, err := s.TrashArticle(t.Context(), alice, stale); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("stale Trash accepted")
	}
	trashed, err := s.TrashArticle(t.Context(), alice, trashIn)
	if err != nil || trashed.Article.ArchivedAt != archived.Article.ArchivedAt || trashed.Article.TrashedAt == 0 || trashed.Article.Revision != 3 {
		t.Fatal("Trash lost archive state")
	}
	if _, err := s.GetArticle(t.Context(), alice, first.Article.ID); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("ordinary read included Trash")
	}
	if got, err := s.GetArticleTool(t.Context(), alice, feed.GetInput{ArticleID: first.Article.ID, Trashed: true}); err != nil || got != trashed.Article {
		t.Fatal("explicit Trash read failed")
	}
	if _, err := s.GetArticleTool(t.Context(), bob, feed.GetInput{ArticleID: first.Article.ID, Trashed: true}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("foreign Trash read")
	}
	for _, filter := range []feed.ListInput{{}, {Archived: true}} {
		if page, err := s.ListArticles(t.Context(), alice, filter); err != nil || len(page.Articles) != 0 {
			t.Fatal("Trash leaked into Feed/Archive")
		}
	}
	patch := feed.PatchInput{IdempotencyKey: "edit-trash", ArticleID: first.Article.ID, ExpectedRevision: 3, Patch: feed.Patch{Title: new("Cannot edit Trash")}}
	if _, err := s.PatchArticle(t.Context(), alice, patch); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("Trash patched")
	}
	if _, err := s.UpdateArticle(t.Context(), alice, feed.UpdateInput{IdempotencyKey: "replace-trash", ArticleID: first.Article.ID, ExpectedRevision: 3, Article: content()}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("Trash replaced")
	}
	if _, err := s.SetArchived(t.Context(), alice, first.Article.ID, false); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("browser restore revived Trash")
	}
	archiveIn.IdempotencyKey, archiveIn.ExpectedRevision, archiveIn.Archived = "unarchive-trash", 3, false
	if _, err := s.SetArticleArchived(t.Context(), alice, archiveIn); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("archive tool revived Trash")
	}
	in.IdempotencyKey = "duplicate-trash"
	if out, err := s.AddArticle(t.Context(), alice, in); err != nil || out.Created || out.Article != trashed.Article {
		t.Fatal("canonical duplicate revived Trash")
	}
	restoreIn := feed.StateInput{IdempotencyKey: "restore", ArticleID: first.Article.ID, ExpectedRevision: 3}
	if _, err := s.RestoreArticle(t.Context(), bob, restoreIn); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("foreign restore accepted")
	}
	restored, err := s.RestoreArticle(t.Context(), alice, restoreIn)
	if err != nil || restored.Article.TrashedAt != 0 || restored.Article.ArchivedAt != archived.Article.ArchivedAt || restored.Article.Revision != 4 {
		t.Fatal("restore failed to restore Archive")
	}
	if replay, err := s.TrashArticle(t.Context(), alice, trashIn); err != nil || replay != trashed {
		t.Fatal("old Trash retry retrash changed state")
	}
	if current, _ := s.GetArticle(t.Context(), alice, first.Article.ID); current != restored.Article {
		t.Fatal("retry reapplied stale Trash transition")
	}
	if err := f.Store.DisableAccount(t.Context(), alice.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreArticle(t.Context(), alice, restoreIn); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled restore retry accepted")
	}
	if _, err := s.TrashArticle(t.Context(), alice, trashIn); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled Trash retry accepted")
	}
}

func TestThumbnailFetchDoesNotHoldWriteLockAndStaleResultIsDiscarded(t *testing.T) {
	f, s, alice, _ := setup(t)
	first, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "first", Article: content()})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	images := &preparedImages{body: preparedJPEG(t), before: func() { close(started); <-release }}
	policy, _ := thumbnail.NewPolicy("images.example.com")
	slow := feed.NewWithThumbnailFetcher(f.Store, policy, images)
	result := make(chan error, 1)
	go func() {
		_, err := slow.PatchArticle(t.Context(), alice, feed.PatchInput{IdempotencyKey: "slow", ArticleID: first.Article.ID, ExpectedRevision: 1, Patch: feed.Patch{ThumbnailURL: new("https://images.example.com/a.jpg")}})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	winner, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "winner", ArticleID: first.Article.ID, ExpectedRevision: 1, Patch: feed.Patch{Title: new("The concurrent edit wins")}})
	if err != nil {
		t.Fatalf("fetch held writer lock: %v", err)
	}
	release <- struct{}{}
	if err := <-result; !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("stale fetch overwrote article: %v", err)
	}
	if got, _ := s.GetArticle(t.Context(), alice, first.Article.ID); got != winner.Article {
		t.Fatal("stale prepared payload changed article")
	}
}

func TestTrashPaginationCursorIsBoundToOwnerAndView(t *testing.T) {
	_, s, alice, bob := setup(t)
	for i := range 5 {
		c := content()
		c.URL = fmt.Sprintf("https://example.com/trash/%d", i)
		out, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: fmt.Sprint(i), Article: c})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.TrashArticle(t.Context(), alice, feed.StateInput{IdempotencyKey: fmt.Sprintf("trash-%d", i), ArticleID: out.Article.ID, ExpectedRevision: 1}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListArticles(t.Context(), alice, feed.ListInput{Trashed: true, Limit: 2})
	if err != nil || first.NextCursor == "" {
		t.Fatal("Trash cursor missing")
	}
	for _, in := range []feed.ListInput{{Cursor: first.NextCursor}, {Cursor: first.NextCursor, Archived: true}, {Trashed: true, Archived: true}} {
		if _, err := s.ListArticles(t.Context(), alice, in); !errors.Is(err, feed.ErrInvalid) {
			t.Fatal("Trash cursor crossed view")
		}
	}
	if _, err := s.ListArticles(t.Context(), bob, feed.ListInput{Cursor: first.NextCursor, Trashed: true}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatal("Trash cursor crossed owner")
	}
	seen, cursor := map[string]bool{}, ""
	var previous sqlite.Article
	for {
		page, err := s.ListArticles(t.Context(), alice, feed.ListInput{Trashed: true, Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Articles {
			if seen[a.ID] || (previous.ID != "" && (a.TrashedAt > previous.TrashedAt || (a.TrashedAt == previous.TrashedAt && a.ID >= previous.ID))) {
				t.Fatal("Trash pagination reordered/repeated records")
			}
			seen[a.ID], previous = true, a
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatal("Trash pagination lost records")
	}
}
