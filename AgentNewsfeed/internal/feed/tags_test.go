package feed_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

const diagramBody = "# Two Sum\n\nA hash map finds each complement in one pass.\n\n```mermaid\nflowchart LR\n  scan[Scan] --> lookup[Look up complement]\n```\n"

// written is an agent-written article without a source URL.
func written() feed.Content {
	return narrated(feed.Content{Title: "Two Sum, explained", Summary: "An agent-written walkthrough of the one-pass hash map solution.", ContentMarkdown: diagramBody, Tags: []string{"generated", "leetcode"}})
}

func tenTags() []string {
	tags := []string{}
	for i := range 10 {
		tags = append(tags, fmt.Sprintf("tag-%d", i))
	}
	return tags
}

func TestNormalizeTagsTrimsLowercasesSortsAndRejectsNonSlugs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in, want []string
	}{
		{"no tags", nil, nil},
		{"empty list", []string{}, nil},
		{"trimmed, lowercased, sorted and distinct", []string{" System-Design ", "explore", "EXPLORE", "go"}, []string{"explore", "go", "system-design"}},
		{"longest tag", []string{strings.Repeat("a", feed.MaxTagBytes)}, []string{strings.Repeat("a", feed.MaxTagBytes)}},
		{"ten distinct tags after a duplicate", append(tenTags(), "TAG-0"), tenTags()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags, err := feed.NormalizeTags(tc.in)
			if err != nil || !slices.Equal(tags, tc.want) || (tc.want == nil && tags != nil) {
				t.Fatalf("got %#v, %v; want %#v", tags, err, tc.want)
			}
			if again, err := feed.NormalizeTags(tags); err != nil || !slices.Equal(again, tags) {
				t.Fatalf("normalized tags changed on revalidation: %#v %v", again, err)
			}
		})
	}
	for _, in := range [][]string{
		{""}, {"   "}, {"two words"}, {"under_score"}, {"double--hyphen"}, {"-leading"}, {"trailing-"}, {"comma,tag"},
		{"café"}, {strings.Repeat("a", feed.MaxTagBytes+1)}, append(tenTags(), "eleventh"),
		// U+212A KELVIN SIGN lowercases to an ASCII k, so lowercasing before
		// matching the slug pattern would accept this as "kelvin".
		{"\u212Aelvin"},
	} {
		tags, err := feed.NormalizeTags(in)
		if !errors.Is(err, feed.ErrInvalid) || tags != nil || !strings.HasPrefix(feed.SafeError(err), "Invalid tags.") {
			t.Fatalf("accepted or mangled %q: %#v %v", in, tags, err)
		}
	}
}

func TestArticleTagsAreNormalizedReplacedAndReplayed(t *testing.T) {
	_, s, alice, _ := setup(t)
	ctx := t.Context()
	c := content()
	c.Tags = []string{" Explore ", "Go", "go"}
	first, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "tagged", Article: c})
	if err != nil || !slices.Equal(first.Article.Tags, []string{"explore", "go"}) {
		t.Fatalf("add did not normalize tags: %#v %v", first, err)
	}
	if got, err := s.GetArticle(ctx, alice, first.Article.ID); err != nil || !reflect.DeepEqual(got, first.Article) {
		t.Fatalf("saved tags differ from the add result: %#v %v", got, err)
	}
	// The same set in another order or case is the same intent.
	c.Tags = []string{"GO", "explore"}
	if replay, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "tagged", Article: c}); err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("equal tag set did not replay: %#v %v", replay, err)
	}
	both := content()
	both.URL, both.ContentMarkdown, both.Tags = "https://example.com/both", "# Body", []string{"explore", "generated"}
	both = narrated(both)
	if _, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "both", Article: both}); !errors.Is(err, feed.ErrInvalid) || !strings.HasPrefix(feed.SafeError(err), "Invalid tags.") {
		t.Fatalf("explore and generated together: %v", err)
	}

	id := first.Article.ID
	// update_article is a full replacement: omitted tags are cleared.
	replaced, err := s.UpdateArticle(ctx, alice, feed.UpdateInput{IdempotencyKey: "replace", ArticleID: id, ExpectedRevision: 1, Article: content()})
	if err != nil || replaced.Article.Tags != nil {
		t.Fatalf("update kept omitted tags: %#v %v", replaced, err)
	}
	retag := feed.PatchInput{IdempotencyKey: "retag", ArticleID: id, ExpectedRevision: 2, Patch: feed.Patch{Tags: &[]string{"Rust", " system-design"}}}
	retagged, err := s.PatchArticle(ctx, alice, retag)
	if err != nil || !slices.Equal(retagged.Article.Tags, []string{"rust", "system-design"}) {
		t.Fatalf("patch did not replace tags: %#v %v", retagged, err)
	}
	renamed, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "rename", ArticleID: id, ExpectedRevision: 3, Patch: feed.Patch{Title: new("Renamed story"), Narration: narrationFor("Renamed story", retagged.Article.ContentMarkdown)}})
	if err != nil || !slices.Equal(renamed.Article.Tags, retagged.Article.Tags) {
		t.Fatalf("patch without tags changed them: %#v %v", renamed, err)
	}
	clearTags := feed.PatchInput{IdempotencyKey: "clear", ArticleID: id, ExpectedRevision: 4, Patch: feed.Patch{Tags: &[]string{"not a slug"}}}
	if _, err := s.PatchArticle(ctx, alice, clearTags); !errors.Is(err, feed.ErrInvalid) {
		t.Fatalf("invalid patch tag accepted: %v", err)
	}
	// The rejected request reserved no key, so the same key can clear the tags.
	clearTags.Patch.Tags = &[]string{}
	cleared, err := s.PatchArticle(ctx, alice, clearTags)
	if err != nil || cleared.Article.Tags != nil || cleared.Article.Title != "Renamed story" {
		t.Fatalf("empty tag list did not clear only tags (or a rejected key was reserved): %#v %v", cleared, err)
	}
	// Exact retries return the original result even after later edits.
	retag.Patch.Tags = &[]string{"SYSTEM-DESIGN", "rust"}
	if replay, err := s.PatchArticle(ctx, alice, retag); err != nil || !reflect.DeepEqual(replay, retagged) {
		t.Fatalf("patch replay changed: %#v %v", replay, err)
	}
	retag.Patch.Tags = &[]string{"rust"}
	if _, err := s.PatchArticle(ctx, alice, retag); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("a key was reused for another tag set: %v", err)
	}
}

func TestGeneratedArticlesNeedABodyAndMayOmitTheURL(t *testing.T) {
	_, s, alice, _ := setup(t)
	ctx := t.Context()
	first, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "written", Article: written()})
	if err != nil || !first.Created || first.Article.URL != "" || first.Article.ContentMarkdown != diagramBody || !slices.Equal(first.Article.Tags, []string{"generated", "leetcode"}) {
		t.Fatalf("generated article without a URL: %#v %v", first, err)
	}
	if raw, _ := json.Marshal(first.Article); strings.Contains(string(raw), `"url"`) {
		t.Fatalf("article without a URL serialized one: %s", raw)
	}
	second, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "written-again", Article: written()})
	if err != nil || !second.Created || second.Article.ID == first.Article.ID {
		t.Fatal("articles without a URL were deduplicated")
	}
	for _, tc := range []struct {
		name   string
		change func(*feed.Content)
		prefix string
	}{
		{"generated without a body", func(c *feed.Content) { c.ContentMarkdown = "" }, "Invalid generated article."},
		{"generated with a blank body", func(c *feed.Content) { c.ContentMarkdown = " \n\t" }, "Invalid generated article."},
		{"no URL and no generated tag", func(c *feed.Content) { c.Tags = []string{"leetcode"} }, "Invalid article request."},
		{"no URL, tagged explore", func(c *feed.Content) { c.Tags = []string{"explore"} }, "Invalid article request."},
		{"explore and generated", func(c *feed.Content) { c.Tags = append(c.Tags, "explore") }, "Invalid tags."},
	} {
		c := written()
		tc.change(&c)
		if _, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "rejected", Article: c}); !errors.Is(err, feed.ErrInvalid) || !strings.HasPrefix(feed.SafeError(err), tc.prefix) {
			t.Fatalf("%s: %v %q", tc.name, err, feed.SafeError(err))
		}
	}
	if out, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "rejected", Article: written()}); err != nil || !out.Created {
		t.Fatalf("a rejected request reserved its key: %v", err)
	}

	// A URL, when a generated article has one, deduplicates like any other.
	withURL := written()
	withURL.URL = "https://leetcode.com/problems/two-sum/?utm_source=agent"
	linked, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "linked", Article: withURL})
	if err != nil || linked.Article.URL != "https://leetcode.com/problems/two-sum/" {
		t.Fatalf("generated article URL: %#v %v", linked, err)
	}
	sourced := content()
	sourced.URL = linked.Article.URL
	if out, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "same-url", Article: sourced}); err != nil || out.Created || out.Article.ID != linked.Article.ID {
		t.Fatalf("generated article URL was not deduplicated: %#v %v", out, err)
	}

	// Updates and patches revalidate the whole resulting article.
	id := first.Article.ID
	withoutGenerated := written()
	withoutGenerated.Tags = nil
	switchedToExplore := written()
	switchedToExplore.Tags = []string{"explore"}
	emptied := written()
	emptied.ContentMarkdown = ""
	for _, article := range []feed.Content{withoutGenerated, switchedToExplore, emptied} {
		if _, err := s.UpdateArticle(ctx, alice, feed.UpdateInput{IdempotencyKey: "invalid-update", ArticleID: id, ExpectedRevision: 1, Article: article}); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("update produced an invalid article: %#v %v", article, err)
		}
	}
	for _, patch := range []feed.Patch{{Tags: &[]string{"explore"}}, {Tags: &[]string{}}, {ContentMarkdown: new("")}, {URL: new("")}} {
		if _, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "invalid-patch", ArticleID: id, ExpectedRevision: 1, Patch: patch}); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("patch produced an invalid article: %#v %v", patch, err)
		}
	}
	if got, err := s.GetArticle(ctx, alice, id); err != nil || !reflect.DeepEqual(got, first.Article) {
		t.Fatal("rejected edits changed the article")
	}
	renamed, err := s.PatchArticle(ctx, alice, feed.PatchInput{IdempotencyKey: "invalid-patch", ArticleID: id, ExpectedRevision: 1, Patch: feed.Patch{Title: new("Two Sum, revisited"), Narration: narrationFor("Two Sum, revisited", diagramBody)}})
	if err != nil || renamed.Article.URL != "" || !slices.Equal(renamed.Article.Tags, first.Article.Tags) || renamed.Article.ContentMarkdown != diagramBody {
		t.Fatalf("valid patch of an article without a URL: %#v %v", renamed, err)
	}
	// Removing the URL of a sourced article is invalid; a generated one may drop it.
	plain, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "plain", Article: content()})
	if err != nil {
		t.Fatal(err)
	}
	unsourced := content()
	unsourced.URL = ""
	if _, err := s.UpdateArticle(ctx, alice, feed.UpdateInput{IdempotencyKey: "drop-url", ArticleID: plain.Article.ID, ExpectedRevision: 1, Article: unsourced}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatalf("sourced article lost its URL: %v", err)
	}
	unlinked, err := s.UpdateArticle(ctx, alice, feed.UpdateInput{IdempotencyKey: "drop-url", ArticleID: linked.Article.ID, ExpectedRevision: 1, Article: written()})
	if err != nil || unlinked.Article.URL != "" {
		t.Fatalf("generated article could not drop its URL: %#v %v", unlinked, err)
	}
	// The four generated articles (first, second, the "rejected" key's retry and
	// the unlinked one) now all lack a URL; the sourced "plain" one is not listed.
	page, err := s.ListArticles(ctx, alice, feed.ListInput{Tags: []string{"generated"}})
	if err != nil || len(page.Articles) != 4 {
		t.Fatalf("generated list: %#v %v", page, err)
	}
	for _, a := range page.Articles {
		if a.URL != "" || a.ContentMarkdown != "" {
			t.Fatalf("list exposed a URL or body for a generated article: %#v", a)
		}
	}
}

func TestListArticlesTagFilterMatchesAnyTagAndBindsItsCursor(t *testing.T) {
	_, s, alice, bob := setup(t)
	ctx := t.Context()
	add := func(p identity.Principal, key string, tags ...string) string {
		t.Helper()
		c := content()
		c.URL, c.Tags = "https://example.com/tags/"+key, tags
		out, err := s.AddArticle(ctx, p, feed.AddInput{IdempotencyKey: key, Article: c})
		if err != nil {
			t.Fatal(err)
		}
		return out.Article.ID
	}
	goOnly, goExplore, goAgain := add(alice, "go", "go"), add(alice, "go-explore", "explore", "go"), add(alice, "go-again", "go")
	rust := add(alice, "rust", "rust")
	add(alice, "untagged")
	add(bob, "foreign", "go")
	ids := func(in feed.ListInput) []string {
		t.Helper()
		page, err := s.ListArticles(ctx, alice, in)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, a := range page.Articles {
			out = append(out, a.ID)
		}
		slices.Sort(out)
		return out
	}
	sorted := func(ids ...string) []string { return slices.Sorted(slices.Values(ids)) }
	for _, tc := range []struct {
		tags, want []string
	}{
		{[]string{"go"}, sorted(goOnly, goExplore, goAgain)},
		{[]string{" GO ", "go"}, sorted(goOnly, goExplore, goAgain)},
		{[]string{"go", "rust"}, sorted(goOnly, goExplore, goAgain, rust)},
		{[]string{"explore", "rust"}, sorted(goExplore, rust)},
		{[]string{"missing"}, []string{}},
	} {
		if got := ids(feed.ListInput{Tags: tc.tags}); !slices.Equal(got, tc.want) {
			t.Errorf("tags %q listed %v, want %v", tc.tags, got, tc.want)
		}
	}
	if len(ids(feed.ListInput{})) != 5 {
		t.Fatal("unfiltered list changed")
	}

	filter := []string{"rust", "go"}
	first, err := s.ListArticles(ctx, alice, feed.ListInput{Tags: filter, Limit: 2})
	if err != nil || len(first.Articles) != 2 || first.NextCursor == "" || len(first.NextCursor) > 256 {
		t.Fatalf("first filtered page: %#v %v", first, err)
	}
	seen := map[string]bool{}
	for page := first; ; {
		for _, a := range page.Articles {
			if seen[a.ID] || (!slices.Contains(a.Tags, "go") && !slices.Contains(a.Tags, "rust")) {
				t.Fatalf("filtered pages repeated or included %#v", a)
			}
			seen[a.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		// The same filter, spelled differently, continues the list.
		page, err = s.ListArticles(ctx, alice, feed.ListInput{Tags: []string{"GO", "rust"}, Limit: 2, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("filtered pagination saw %d articles", len(seen))
	}
	unfiltered, err := s.ListArticles(ctx, alice, feed.ListInput{Limit: 2})
	if err != nil || unfiltered.NextCursor == "" {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		p  identity.Principal
		in feed.ListInput
	}{
		{alice, feed.ListInput{Limit: 2, Cursor: first.NextCursor}},
		{alice, feed.ListInput{Limit: 2, Cursor: first.NextCursor, Tags: []string{"go"}}},
		{alice, feed.ListInput{Limit: 2, Cursor: first.NextCursor, Tags: []string{"go", "rust", "explore"}}},
		{alice, feed.ListInput{Limit: 2, Cursor: first.NextCursor, Tags: filter, Archived: true}},
		{bob, feed.ListInput{Limit: 2, Cursor: first.NextCursor, Tags: filter}},
		{alice, feed.ListInput{Limit: 2, Cursor: unfiltered.NextCursor, Tags: filter}},
		{alice, feed.ListInput{Tags: []string{"not a slug"}}},
		{alice, feed.ListInput{Tags: append(tenTags(), "eleventh")}},
		// Like the MCP schema, a filter counts the tags as supplied: eleven
		// are too many even when they normalize to ten distinct tags.
		{alice, feed.ListInput{Tags: append(tenTags(), "TAG-0")}},
	} {
		if _, err := s.ListArticles(ctx, tc.p, tc.in); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("cursor or filter escaped its tag filter, owner or view: %#v %v", tc.in, err)
		}
	}
	// Unfiltered cursors keep their earlier encoding.
	for cursor, filtered := range map[string]bool{unfiltered.NextCursor: false, first.NextCursor: true} {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || strings.Contains(string(raw), "tags_hash") != filtered {
			t.Fatalf("unexpected cursor encoding %s: %v", raw, err)
		}
	}
	if got, err := s.ListArticles(ctx, bob, feed.ListInput{Tags: []string{"go"}}); err != nil || len(got.Articles) != 1 {
		t.Fatalf("filter crossed owners: %#v %v", got, err)
	}
}

func TestListTagsNeedsReadPermissionAndOneShelf(t *testing.T) {
	f, s, alice, bob := setup(t)
	ctx := t.Context()
	sourced := content()
	sourced.Tags = []string{"explore", "go"}
	for key, article := range map[string]feed.Content{"sourced": sourced, "written": written()} {
		if _, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: key, Article: article}); err != nil {
			t.Fatal(err)
		}
	}
	// Each tag is on one article, so the tags sort alphabetically.
	list, err := s.ListTags(ctx, alice, feed.ListTagsInput{})
	if raw, _ := json.Marshal(list); err != nil || string(raw) != `{"tags":[{"tag":"explore","count":1},{"tag":"generated","count":1},{"tag":"go","count":1},{"tag":"leetcode","count":1}]}` {
		t.Fatalf("active tags: %s %v", raw, err)
	}
	if list, err := s.ListTags(ctx, bob, feed.ListTagsInput{Archived: true}); err != nil || list.Tags == nil || len(list.Tags) != 0 {
		t.Fatalf("an owner without tags: %#v %v", list, err)
	}
	if _, err := s.ListTags(ctx, alice, feed.ListTagsInput{Archived: true, Trashed: true}); !errors.Is(err, feed.ErrInvalid) {
		t.Fatalf("Archive and Trash together: %v", err)
	}
	if _, err := s.ListTags(ctx, f.Principal(t, "alice", identity.ArticlesWrite), feed.ListTagsInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("write permission read tags: %v", err)
	}
	if err := f.Store.DisableAccount(ctx, alice.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListTags(ctx, alice, feed.ListTagsInput{}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("a disabled owner read tags: %v", err)
	}
}

// startDiscovery enables discovery for p and leases a run.
func startDiscovery(t *testing.T, s *feed.Service, p identity.Principal) (runID string, preferencesRevision int64) {
	t.Helper()
	prefs := sqlite.DefaultPreferences().Preferences
	prefs.Discovery.Enabled, prefs.Discovery.MaxPerRun, prefs.Discovery.MaxPerDay = true, 10, 50
	state, err := s.UpdateRecommendationPreferences(t.Context(), p, feed.UpdatePreferencesInput{IdempotencyKey: "enable-discovery", ExpectedRevision: 1, Preferences: prefs})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartDiscoveryRun(t.Context(), p, feed.StartDiscoveryInput{IdempotencyKey: "discovery-run", ExpectedPreferencesRevision: state.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return run.ID, state.Revision
}

func TestDiscoveryAddsExploreAndRejectsGenerated(t *testing.T) {
	_, s, alice, _ := setup(t)
	ctx := t.Context()
	runID, revision := startDiscovery(t, s, alice)
	ingest := func(key string, tags ...string) feed.IngestDiscoveryInput {
		return feed.IngestDiscoveryInput{IdempotencyKey: key, RunID: runID, ExpectedPreferencesRevision: revision,
			Article:  narrated(feed.Content{URL: "https://example.com/found/" + key, Title: "Found " + key, Summary: "A sourced introduction.", Tags: tags}),
			WhyMatch: "Explains a topic the reader follows.", PublishedAt: time.Now().UnixMilli()}
	}
	untagged, err := s.IngestDiscoveryArticle(ctx, alice, ingest("untagged"))
	if err != nil || !slices.Equal(untagged.Article.Tags, []string{feed.TagExplore}) {
		t.Fatalf("discovery did not add explore: %#v %v", untagged, err)
	}
	topical := ingest("topical", "Go", "explore")
	saved, err := s.IngestDiscoveryArticle(ctx, alice, topical)
	if err != nil || !slices.Equal(saved.Article.Tags, []string{"explore", "go"}) {
		t.Fatalf("discovery lost topic tags: %#v %v", saved, err)
	}
	if replay, err := s.IngestDiscoveryArticle(ctx, alice, topical); err != nil || !reflect.DeepEqual(replay, saved) {
		t.Fatalf("discovery retry changed: %#v %v", replay, err)
	}
	nine := tenTags()[:9]
	if full, err := s.IngestDiscoveryArticle(ctx, alice, ingest("nine-topics", nine...)); err != nil || len(full.Article.Tags) != feed.MaxArticleTags {
		t.Fatalf("nine topics plus explore: %#v %v", full, err)
	}
	withBody := ingest("generated-with-body", "generated")
	withBody.Article.ContentMarkdown = "# Agent writing"
	withBody.Article = narrated(withBody.Article)
	for _, in := range []feed.IngestDiscoveryInput{ingest("generated", "generated"), withBody, ingest("eleven", tenTags()...)} {
		if _, err := s.IngestDiscoveryArticle(ctx, alice, in); !errors.Is(err, feed.ErrInvalid) || !strings.HasPrefix(feed.RecommendationSafeError(err), "Invalid tags.") {
			t.Fatalf("discovery accepted invalid tags %q: %v %q", in.Article.Tags, err, feed.RecommendationSafeError(err))
		}
	}
	unsourced := ingest("unsourced")
	unsourced.Article.URL = ""
	if _, err := s.IngestDiscoveryArticle(ctx, alice, unsourced); !errors.Is(err, feed.ErrInvalid) {
		t.Fatalf("discovery saved an article without a URL: %v", err)
	}
}

// Requests without tags must hash exactly as they did before tags existed, so
// a retry from before the upgrade still replays. The literal payloads are the
// pre-tags encodings rather than encodings of the current structs.
func TestUntaggedRequestsKeepTheirPreTagPayloadHashes(t *testing.T) {
	f, s, alice, _ := setup(t)
	ctx := t.Context()
	owner := alice.Owner()
	article := sqlite.Article{URL: "https://example.com/story", Title: "A sourced story", Summary: "A short introduction supplied by the source.", ThumbnailURL: "https://images.example.com/source.jpg", ContentMarkdown: "# Original"}
	addPayload := []byte(`{"id":"","url":"https://example.com/story","title":"A sourced story","summary":"A short introduction supplied by the source.","thumbnail_url":"https://images.example.com/source.jpg","revision":0,"created_at":0,"updated_at":0,"content_markdown":"# Original"}`)
	added, err := f.Store.MutateArticle(ctx, owner, "legacy-add", "add_article", addPayload, article, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Retries from before scripts existed carry no narration and still replay.
	c := feed.Content{URL: article.URL, Title: article.Title, Summary: article.Summary, ThumbnailURL: article.ThumbnailURL, ContentMarkdown: article.ContentMarkdown}
	if replay, err := s.AddArticle(ctx, alice, feed.AddInput{IdempotencyKey: "legacy-add", Article: c}); err != nil || !reflect.DeepEqual(replay, added) {
		t.Fatalf("add_article hash changed: %v", err)
	}

	id := added.Article.ID
	article.ID, article.Title = id, "Revised story"
	updatePayload := fmt.Appendf(nil, `{"Article":{"id":%q,"url":"https://example.com/story","title":"Revised story","summary":"A short introduction supplied by the source.","thumbnail_url":"https://images.example.com/source.jpg","revision":0,"created_at":0,"updated_at":0,"content_markdown":"# Original"},"Expected":1}`, id)
	updated, err := f.Store.MutateArticle(ctx, owner, "legacy-update", "update_article", updatePayload, article, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Title = article.Title
	if replay, err := s.UpdateArticle(ctx, alice, feed.UpdateInput{IdempotencyKey: "legacy-update", ArticleID: id, ExpectedRevision: 1, Article: c}); err != nil || !reflect.DeepEqual(replay, updated) {
		t.Fatalf("update_article hash changed: %v", err)
	}

	article.Title, article.ContentMarkdown = "Patched story", ""
	patchPayload := fmt.Appendf(nil, `{"idempotency_key":"legacy-patch","article_id":%q,"expected_revision":2,"patch":{"title":"Patched story","content_markdown":""}}`, id)
	patched, err := f.Store.MutateArticle(ctx, owner, "legacy-patch", "patch_article", patchPayload, article, 2)
	if err != nil {
		t.Fatal(err)
	}
	patch := feed.PatchInput{IdempotencyKey: "legacy-patch", ArticleID: id, ExpectedRevision: 2, Patch: feed.Patch{Title: new("Patched story"), ContentMarkdown: new("")}}
	if replay, err := s.PatchArticle(ctx, alice, patch); err != nil || !reflect.DeepEqual(replay, patched) {
		t.Fatalf("patch_article hash changed: %v", err)
	}

	runID, revision := startDiscovery(t, s, alice)
	published := time.Now().UnixMilli()
	found := sqlite.Article{URL: "https://example.com/found", Title: "Found story", Summary: "A sourced introduction."}
	ingestPayload := fmt.Appendf(nil, `{"idempotency_key":"legacy-ingest","run_id":%q,"expected_preferences_revision":%d,"article":{"url":"https://example.com/found","title":"Found story","summary":"A sourced introduction."},"why_match":"Explains a topic the reader follows.","published_at":%d,"evergreen":false}`, runID, revision, published)
	item := sqlite.DiscoveryItem{WhyMatch: "Explains a topic the reader follows.", PublishedAt: published}
	ingested, err := f.Store.IngestDiscoveryArticle(ctx, owner, "legacy-ingest", ingestPayload, runID, revision, found, item, sqlite.ThumbnailChange{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	in := feed.IngestDiscoveryInput{IdempotencyKey: "legacy-ingest", RunID: runID, ExpectedPreferencesRevision: revision,
		Article: feed.Content{URL: found.URL, Title: found.Title, Summary: found.Summary}, WhyMatch: item.WhyMatch, PublishedAt: published}
	if replay, err := s.IngestDiscoveryArticle(ctx, alice, in); err != nil || !reflect.DeepEqual(replay, ingested) {
		t.Fatalf("ingest_discovery_article hash changed: %v", err)
	}
}
