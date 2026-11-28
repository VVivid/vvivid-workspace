package web_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/web"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPArticleToolsDescribeAndEnforceTags(t *testing.T) {
	f, _, h := app(t)
	server := httptest.NewServer(h)
	defer server.Close()
	c := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	instructions := c.InitializeResult().Instructions
	for _, phrase := range []string{"Use explore for content found on the internet and generated for content you write yourself", "```mermaid", "hand-drawn", "never an agent rewrite", "Never invent facts"} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("instructions omit %q", phrase)
		}
	}
	tools, err := c.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, tool := range tools.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = &schema
	}
	for name, tags := range map[string]*jsonschema.Schema{
		"add_article":              schemas["add_article"].Properties["article"].Properties["tags"],
		"update_article":           schemas["update_article"].Properties["article"].Properties["tags"],
		"ingest_discovery_article": schemas["ingest_discovery_article"].Properties["article"].Properties["tags"],
		"patch_article":            schemas["patch_article"].Properties["patch"].Properties["tags"],
		"list_articles":            schemas["list_articles"].Properties["tags"],
	} {
		if tags == nil || tags.Type != "array" || len(tags.Types) != 0 || tags.MaxItems == nil || *tags.MaxItems != feed.MaxArticleTags ||
			tags.Items == nil || tags.Items.MinLength == nil || *tags.Items.MinLength != 1 || tags.Items.MaxLength == nil || *tags.Items.MaxLength != feed.MaxTagBytes {
			t.Fatalf("%s does not bound its tags: %#v", name, tags)
		}
	}
	// Every article an agent saves carries a narration script.
	for _, name := range []string{"add_article", "update_article", "ingest_discovery_article"} {
		article := schemas[name].Properties["article"]
		narration := article.Properties["narration"]
		if !slices.Contains(article.Required, "narration") || narration == nil || narration.MinItems == nil || *narration.MinItems != 1 || narration.Items == nil {
			t.Fatalf("%s does not require narration: %#v", name, article)
		}
		part := narration.Items
		if !slices.Equal(part.Required, []string{"say"}) || !slices.Equal(part.Properties["describe"].Enum, []any{feed.DescribeCode, feed.DescribeDiagram, feed.DescribeTable}) ||
			*part.Properties["say"].MaxLength != feed.MaxNarrationSayBytes || *part.Properties["text"].MaxLength != feed.MaxNarrationTextBytes {
			t.Fatalf("%s narration parts are unbounded: %#v", name, part)
		}
	}
	if schemas["patch_article"].Properties["patch"].Properties["narration"] == nil {
		t.Fatal("patch_article cannot replace narration")
	}
	if !strings.Contains(instructions, "Narration: every article you add, update or ingest needs narration") || !strings.Contains(instructions, "$100 as a hundred dollars") {
		t.Fatal("instructions do not teach narration")
	}
	if article := schemas["add_article"].Properties["article"]; slices.Contains(article.Required, "url") || !slices.Contains(article.Required, "title") {
		t.Fatalf("add_article must allow a generated article without url: %v", article.Required)
	}

	tagged := decode[sqlite.MutationResult](t, call(t, c, "add_article", map[string]any{"idempotency_key": "tagged", "article": map[string]any{
		"url": "https://example.com/go", "title": "Go story", "summary": "A sourced introduction.", "tags": []string{"Explore", "go"},
		"narration": feed.PlainNarration("Go story", ""),
	}}))
	if !slices.Equal(tagged.Article.Tags, []string{"explore", "go"}) {
		t.Fatalf("MCP add did not normalize tags: %#v", tagged.Article)
	}
	written := call(t, c, "add_article", feed.AddInput{IdempotencyKey: "written", Article: narrated(feed.Content{
		Title: "Two Sum, explained", Summary: "An agent-written walkthrough.", ContentMarkdown: "# Two Sum\n\n```mermaid\nflowchart LR\n  a --> b\n```", Tags: []string{"generated"},
	})})
	raw, err := json.Marshal(written.StructuredContent)
	if err != nil || written.IsError || strings.Contains(string(raw), `"url"`) || strings.Contains(string(raw), "no-url") {
		t.Fatalf("MCP result for an article without a URL: %s %v", raw, err)
	}
	for _, bad := range []map[string]any{
		{"url": "https://example.com/null-tags", "title": "Title", "summary": "Introduction", "tags": nil},
		{"url": "https://example.com/many-tags", "title": "Title", "summary": "Introduction", "tags": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}},
		{"url": "https://example.com/bad-tag", "title": "Title", "summary": "Introduction", "tags": []string{"not a slug"}},
		{"title": "Title", "summary": "Introduction", "content_markdown": "Body", "tags": []string{"explore"}},
	} {
		if result := call(t, c, "add_article", map[string]any{"idempotency_key": "rejected", "article": bad}); !result.IsError {
			t.Fatalf("MCP accepted %v", bad)
		}
	}

	if page := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{Tags: []string{"generated"}})); len(page.Articles) != 1 || page.Articles[0].URL != "" {
		t.Fatalf("MCP tag filter: %#v", page)
	}
	first := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{Tags: []string{"go", "generated"}, Limit: 1}))
	if len(first.Articles) != 1 || first.NextCursor == "" {
		t.Fatalf("MCP filtered page: %#v", first)
	}
	if !call(t, c, "list_articles", feed.ListInput{Tags: []string{"go"}, Limit: 1, Cursor: first.NextCursor}).IsError {
		t.Fatal("MCP cursor continued another tag filter")
	}
	if next := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{Tags: []string{"generated", "GO"}, Limit: 1, Cursor: first.NextCursor})); len(next.Articles) != 1 || next.NextCursor != "" {
		t.Fatalf("MCP cursor did not continue its own filter: %#v", next)
	}
	patched := decode[sqlite.MutationResult](t, call(t, c, "patch_article", map[string]any{
		"idempotency_key": "retag", "article_id": tagged.Article.ID, "expected_revision": 1, "patch": map[string]any{"tags": []string{"rust"}},
	}))
	if got := decode[sqlite.Article](t, call(t, c, "get_article", feed.GetInput{ArticleID: tagged.Article.ID})); !slices.Equal(patched.Article.Tags, []string{"rust"}) || !reflect.DeepEqual(got, patched.Article) {
		t.Fatalf("MCP patch did not replace tags: %#v", got)
	}
}

func TestArticleWithoutURLRendersSafelyInBrowserAPIAndMCP(t *testing.T) {
	f, s, _ := app(t)
	fetcher := &recordingImageFetcher{}
	h, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, s, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	const absoluteImage = "https://images.example.com/diagram.png"
	body := "# Agent notes\n\n![Diagram](" + absoluteImage + ")\n\n![Local](diagram.png)\n\n[Relative](notes.md)\n\n```mermaid\nflowchart LR\n  a --> b\n```\n"
	out, err := s.AddArticle(t.Context(), f.Principal(t, "alice", allScopes), feed.AddInput{IdempotencyKey: "written", Article: narrated(feed.Content{
		Title: "Agent notes", Summary: "Written by the agent.", ContentMarkdown: body, Tags: []string{"generated"},
	})})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Article.ID
	cookie := f.Cookie(t, "alice")
	for _, path := range []string{"/", "/?q=agent", "/articles/" + id, "/articles/" + id + "?from=feed", "/api/v1/articles", "/api/v1/articles/" + id, "/settings"} {
		response := request(h, http.MethodGet, path, cookie, "")
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "no-url") {
			t.Fatalf("%s: HTTP %d or exposed the placeholder", path, response.Code)
		}
	}

	reader := readBrowserJSON[struct {
		Article       map[string]any    `json:"article"`
		ContentImages map[string]string `json:"content_images"`
	}](t, browserJSONRequest(h, "/articles/"+id, cookie, nil, ""))
	if _, hasURL := reader.Article["url"]; hasURL || !reflect.DeepEqual(reader.Article["tags"], []any{"generated"}) || reader.Article["content_markdown"] != body {
		t.Fatalf("reader article: %#v", reader.Article)
	}
	route, ok := reader.ContentImages[absoluteImage]
	if !ok || len(reader.ContentImages) != 1 {
		t.Fatalf("only absolute images resolve without a source: %v", reader.ContentImages)
	}
	if response := request(h, http.MethodGet, route, cookie, ""); response.Code != http.StatusOK || !slices.Equal(fetcher.urls, []string{absoluteImage}) {
		t.Fatalf("absolute image route: HTTP %d, fetched %v", response.Code, fetcher.urls)
	}
	relative := sha256.Sum256([]byte("diagram.png"))
	if response := request(h, http.MethodGet, "/thumbnails/"+id+"/content/"+hex.EncodeToString(relative[:]), cookie, ""); response.Code != http.StatusNotFound || len(fetcher.urls) != 1 {
		t.Fatal("a relative image without a source reached the fetcher")
	}

	for query, want := range map[string]int{"no-url": 0, strings.ToLower(id): 0, "agent": 1} {
		page := readBrowserJSON[struct {
			Page feed.BrowserArticlesPage `json:"page"`
		}](t, browserJSONRequest(h, "/?q="+query, cookie, nil, ""))
		if page.Page.Total != want {
			t.Fatalf("search %q matched %d, want %d", query, page.Page.Total, want)
		}
	}
	var detail map[string]any
	if response := request(h, http.MethodGet, "/api/v1/articles/"+id, cookie, ""); json.Unmarshal(response.Body.Bytes(), &detail) != nil || detail["url"] != nil || detail["id"] != id {
		t.Fatalf("API detail: %s", response.Body)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	c := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	if got := decode[sqlite.Article](t, call(t, c, "get_article", feed.GetInput{ArticleID: id})); !reflect.DeepEqual(got, out.Article) || got.URL != "" {
		t.Fatalf("MCP get_article: %#v", got)
	}
}

// taggedShelves saves alice's tagged articles on every shelf, one of them
// agent-written without a URL, and one of bob's. It returns their IDs by key.
func taggedShelves(t *testing.T, f *testfixture.Identity, s *feed.Service) map[string]string {
	t.Helper()
	ctx := t.Context()
	alice := f.Principal(t, "alice", allScopes)
	ids := map[string]string{}
	add := func(p identity.Principal, key string, article feed.Content) {
		t.Helper()
		out, err := s.AddArticle(ctx, p, feed.AddInput{IdempotencyKey: key, Article: article})
		if err != nil {
			t.Fatal(err)
		}
		ids[key] = out.Article.ID
	}
	sourced := func(key string, tags ...string) feed.Content {
		return narrated(feed.Content{URL: "https://example.com/" + key, Title: "Story " + key, Summary: "A sourced introduction.", Tags: tags})
	}
	add(alice, "go", sourced("go", "explore", "go"))
	add(alice, "go-again", sourced("go-again", "go"))
	add(alice, "rust", sourced("rust", "explore", "rust"))
	add(alice, "written", narrated(feed.Content{Title: "Two Sum, explained", Summary: "An agent-written walkthrough.", ContentMarkdown: "# Two Sum", Tags: []string{"generated", "leetcode"}}))
	add(alice, "untagged", sourced("untagged"))
	add(alice, "archived", sourced("archived", "explore", "go"))
	if _, err := s.SetArchived(ctx, alice, ids["archived"], true); err != nil {
		t.Fatal(err)
	}
	add(alice, "trashed", sourced("trashed", "rust"))
	if _, err := s.TrashArticle(ctx, alice, feed.StateInput{IdempotencyKey: "trash", ArticleID: ids["trashed"], ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	add(f.Principal(t, "bob", allScopes), "foreign", sourced("foreign", "foreign", "go"))
	return ids
}

// articleReader authenticates alice's reads as one of the two API clients.
type articleReader struct {
	cookie *http.Cookie
	token  string
}

func aliceReaders(t *testing.T, f *testfixture.Identity) map[string]articleReader {
	t.Helper()
	return map[string]articleReader{
		"browser session": {cookie: f.Cookie(t, "alice")},
		"native app":      {token: f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.Read)},
	}
}

func TestArticleListTagFilterForBrowserAndNativeApp(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	ids := taggedShelves(t, f, s)
	keys := map[string]string{}
	for key, id := range ids {
		keys[id] = key
	}
	var elevenTags []string
	for i := range 11 {
		elevenTags = append(elevenTags, fmt.Sprintf("tag-%d", i))
	}
	for name, reader := range aliceReaders(t, f) {
		t.Run(name, func(t *testing.T) {
			get := func(query url.Values) *httptest.ResponseRecorder {
				return request(h, http.MethodGet, "/api/v1/articles?"+query.Encode(), reader.cookie, reader.token)
			}
			list := func(query url.Values) feed.Page {
				t.Helper()
				response := get(query)
				var page feed.Page
				if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || json.Unmarshal(response.Body.Bytes(), &page) != nil {
					t.Fatalf("%v: HTTP %d %s", query, response.Code, response.Body)
				}
				return page
			}
			listed := func(articles ...sqlite.Article) []string {
				found := []string{}
				for _, a := range articles {
					found = append(found, keys[a.ID])
				}
				slices.Sort(found)
				return found
			}
			for _, tc := range []struct {
				query url.Values
				want  []string
			}{
				{url.Values{}, []string{"go", "go-again", "rust", "untagged", "written"}},
				{url.Values{"tag": {"go"}}, []string{"go", "go-again"}},
				// Any requested tag matches, and tags normalize as in MCP list_articles.
				{url.Values{"tag": {"go", "rust"}}, []string{"go", "go-again", "rust"}},
				{url.Values{"tag": {" GO ", "Rust", "go"}}, []string{"go", "go-again", "rust"}},
				{url.Values{"tag": {"generated"}}, []string{"written"}},
				{url.Values{"tag": {"missing"}}, []string{}},
				{url.Values{"tag": {"go"}, "archived": {"true"}}, []string{"archived"}},
				{url.Values{"tag": {"rust"}, "trashed": {"true"}}, []string{"trashed"}},
			} {
				if got := listed(list(tc.query).Articles...); !slices.Equal(got, tc.want) {
					t.Errorf("%v listed %v, want %v", tc.query, got, tc.want)
				}
			}
			if response := get(url.Values{"tag": {"generated"}}); strings.Contains(response.Body.String(), `"url"`) {
				t.Fatalf("an article without a URL listed one: %s", response.Body)
			}
			// The bound counts tags as supplied, like the MCP schema's at most ten.
			for _, tags := range [][]string{{""}, {"not a slug"}, elevenTags, append(elevenTags[:10:10], "TAG-0")} {
				if response := get(url.Values{"tag": tags}); response.Code != http.StatusBadRequest || !strings.HasPrefix(response.Body.String(), "Invalid tags.") {
					t.Fatalf("tags %q: HTTP %d %s", tags, response.Code, response.Body)
				}
			}

			// A cursor continues only the filter it came from, however it is spelled.
			first := list(url.Values{"tag": {"go", "rust"}, "limit": {"2"}})
			if len(first.Articles) != 2 || first.NextCursor == "" {
				t.Fatalf("first filtered page: %#v", first)
			}
			rest := list(url.Values{"tag": {"rust", "GO"}, "limit": {"2"}, "cursor": {first.NextCursor}})
			if seen := listed(append(first.Articles, rest.Articles...)...); rest.NextCursor != "" || !slices.Equal(seen, []string{"go", "go-again", "rust"}) {
				t.Fatalf("filtered pages listed %v with next cursor %q", seen, rest.NextCursor)
			}
			for _, query := range []url.Values{
				{"limit": {"2"}, "cursor": {first.NextCursor}},
				{"tag": {"go"}, "limit": {"2"}, "cursor": {first.NextCursor}},
				{"tag": {"go", "rust"}, "archived": {"true"}, "limit": {"2"}, "cursor": {first.NextCursor}},
			} {
				if response := get(query); response.Code != http.StatusBadRequest {
					t.Fatalf("cursor continued another list %v: HTTP %d", query, response.Code)
				}
			}
		})
	}
	var page feed.Page
	bob := f.Bearer("bob", nativeClient, f.Manager.APIAudience(), identity.Read)
	if response := request(h, http.MethodGet, "/api/v1/articles?tag=go", nil, bob); response.Code != http.StatusOK ||
		json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Articles) != 1 || page.Articles[0].ID != ids["foreign"] {
		t.Fatalf("tag filter crossed owners: HTTP %d %s", response.Code, response.Body)
	}
}

func TestTagsRouteCountsOneShelfForBrowserAndNativeApp(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	taggedShelves(t, f, s)
	active := `{"tags":[{"tag":"explore","count":2},{"tag":"go","count":2},{"tag":"generated","count":1},{"tag":"leetcode","count":1},{"tag":"rust","count":1}]}`
	for name, reader := range aliceReaders(t, f) {
		// Most used first, equal counts by tag; archived and trashed articles
		// count only on their own shelves.
		for query, want := range map[string]string{
			"":                              active,
			"?archived=false&trashed=false": active,
			"?archived=true":                `{"tags":[{"tag":"explore","count":1},{"tag":"go","count":1}]}`,
			"?trashed=true":                 `{"tags":[{"tag":"rust","count":1}]}`,
		} {
			response := request(h, http.MethodGet, "/api/v1/tags"+query, reader.cookie, reader.token)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" ||
				response.Header().Get("Cache-Control") != "no-store" || strings.TrimSpace(response.Body.String()) != want {
				t.Fatalf("%s %q: HTTP %d %s", name, query, response.Code, response.Body)
			}
		}
		for _, query := range []string{"?archived=true&trashed=true", "?archived=yes", "?trashed=1"} {
			if response := request(h, http.MethodGet, "/api/v1/tags"+query, reader.cookie, reader.token); response.Code != http.StatusBadRequest {
				t.Fatalf("%s %q: HTTP %d", name, query, response.Code)
			}
		}
	}
	bob := f.Bearer("bob", nativeClient, f.Manager.APIAudience(), identity.Read)
	for query, want := range map[string]string{"": `{"tags":[{"tag":"foreign","count":1},{"tag":"go","count":1}]}`, "?archived=true": `{"tags":[]}`} {
		if response := request(h, http.MethodGet, "/api/v1/tags"+query, nil, bob); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != want {
			t.Fatalf("bob's tags %q: HTTP %d %s", query, response.Code, response.Body)
		}
	}

	// Like the other native reads: read permission is required, and a rejected
	// bearer token never falls back to the browser session.
	challenge := `Bearer resource_metadata="` + testfixture.Origin + `/.well-known/oauth-protected-resource/api/v1", scope="` + identity.Read + `"`
	for _, tc := range []struct {
		name      string
		cookie    *http.Cookie
		token     string
		status    int
		challenge string
	}{
		{"anonymous", nil, "", http.StatusUnauthorized, ""},
		{"write-only native app", nil, f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.ArticlesWrite), http.StatusForbidden, challenge + `, error="insufficient_scope"`},
		{"web client token beside the session", f.Cookie(t, "alice"), f.Bearer("alice", "newsfeed-web", f.Manager.APIAudience(), identity.Read), http.StatusUnauthorized, challenge},
		{"MCP audience", nil, f.Bearer("alice", nativeClient, f.Manager.MCPAudience(), identity.Read), http.StatusUnauthorized, challenge},
	} {
		if response := request(h, http.MethodGet, "/api/v1/tags", tc.cookie, tc.token); response.Code != tc.status || response.Header().Get("WWW-Authenticate") != tc.challenge {
			t.Fatalf("%s: HTTP %d, challenge %q", tc.name, response.Code, response.Header().Get("WWW-Authenticate"))
		}
	}

	// Without configured native app clients the route is browser-only.
	browserOnly, _, browserHandler := app(t)
	nativeToken := browserOnly.Bearer("alice", nativeClient, browserOnly.Manager.APIAudience(), identity.Read)
	if response := request(browserHandler, http.MethodGet, "/api/v1/tags", nil, nativeToken); response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("native token without native clients: HTTP %d", response.Code)
	}
	if response := request(browserHandler, http.MethodGet, "/api/v1/tags", browserOnly.Cookie(t, "alice"), ""); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"tags":[]}` {
		t.Fatalf("browser tags without native clients: HTTP %d %s", response.Code, response.Body)
	}
}

func TestMCPListTagsMatchesTheTagsRoute(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	taggedShelves(t, f, s)
	server := httptest.NewServer(h)
	defer server.Close()
	c := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.Read))
	if !strings.Contains(c.InitializeResult().Instructions, "list_tags") {
		t.Fatal("instructions do not point agents at list_tags")
	}
	tools, err := c.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	listTags := tools.Tools[slices.IndexFunc(tools.Tools, func(tool *sdk.Tool) bool { return tool.Name == "list_tags" })]
	if a := listTags.Annotations; a == nil || !a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || listTags.OutputSchema == nil {
		t.Fatalf("list_tags is not a typed read-only tool: %#v", listTags)
	}

	cookie := f.Cookie(t, "alice")
	for query, in := range map[string]feed.ListTagsInput{"": {}, "?archived=true": {Archived: true}, "?trashed=true": {Trashed: true}} {
		var route feed.TagList
		if response := request(h, http.MethodGet, "/api/v1/tags"+query, cookie, ""); json.Unmarshal(response.Body.Bytes(), &route) != nil || len(route.Tags) == 0 {
			t.Fatalf("tags route %q: HTTP %d %s", query, response.Code, response.Body)
		}
		if got := decode[feed.TagList](t, call(t, c, "list_tags", in)); !reflect.DeepEqual(got, route) {
			t.Fatalf("list_tags %+v = %#v, route answered %#v", in, got, route)
		}
	}
	for _, rejected := range []map[string]any{{"archived": true, "trashed": true}, {"archived": nil}, {"tag": "go"}} {
		if !call(t, c, "list_tags", rejected).IsError {
			t.Fatalf("list_tags accepted %v", rejected)
		}
	}
	writeOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.ArticlesWrite))
	if !call(t, writeOnly, "list_tags", feed.ListTagsInput{}).IsError {
		t.Fatal("write permission listed tags")
	}
}
