package web_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/web"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type unavailableThumbnailFetcher struct{}

func (unavailableThumbnailFetcher) FetchThumbnail(context.Context, string) ([]byte, error) {
	return nil, errors.New("controlled unavailable image")
}

type storedImageFetcher struct {
	body  []byte
	calls atomic.Int64
}

func (f *storedImageFetcher) FetchThumbnail(context.Context, string) ([]byte, error) {
	f.calls.Add(1)
	return bytes.Clone(f.body), nil
}

func TestOfficialMCPFullReadPatchTrashRestoreArchiveAndAuthorization(t *testing.T) {
	f, _, _ := app(t)
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 80, 80)), &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	images := &storedImageFetcher{body: jpegBytes.Bytes()}
	policy, _ := thumbnail.NewPolicy("")
	s := feed.NewWithThumbnailFetcher(f.Store, policy, images)
	inline := &recordingImageFetcher{}
	h, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, s, inline)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	alice := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	bob := client(t, server.URL, f.Bearer("bob", "newsfeed-chatgpt", f.Manager.MCPAudience(), allScopes))
	readOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.Read))
	writeOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.ArticlesWrite))
	add := feed.AddInput{IdempotencyKey: "full", Article: feed.Content{URL: "https://example.com/full", Title: "Source title", Summary: "Source introduction", ThumbnailURL: "https://images.example.com/a.png", ContentMarkdown: "# Complete original\n\n![Figure](https://images.example.com/figure.png)"}}
	first := decode[sqlite.MutationResult](t, call(t, alice, "add_article", add))
	get := feed.GetInput{ArticleID: first.Article.ID}
	if got := decode[sqlite.Article](t, call(t, alice, "get_article", get)); got != first.Article {
		t.Fatal("MCP full body read lost state")
	}
	if !call(t, bob, "get_article", get).IsError || !call(t, writeOnly, "get_article", get).IsError {
		t.Fatal("MCP full read crossed owner or scope")
	}
	patch := feed.PatchInput{IdempotencyKey: "partial", ArticleID: first.Article.ID, ExpectedRevision: 1, Patch: feed.Patch{Title: new("Corrected title")}}
	if !call(t, readOnly, "patch_article", patch).IsError || !call(t, writeOnly, "patch_article", patch).IsError || !call(t, bob, "patch_article", patch).IsError {
		t.Fatal("MCP patch crossed owner or scope")
	}
	updated := decode[sqlite.MutationResult](t, call(t, alice, "patch_article", patch))
	if updated.Article.ContentMarkdown != add.Article.ContentMarkdown || updated.Article.ThumbnailHash != first.Article.ThumbnailHash || images.calls.Load() != 1 {
		t.Fatal("MCP partial patch dropped omitted fields")
	}
	for _, bad := range []map[string]any{{"extra": "x"}, {"title": nil}, {"title": ""}, {"content_markdown": 1}, {}} {
		args := map[string]any{"article_id": first.Article.ID, "idempotency_key": "bad-patch", "expected_revision": 2, "patch": bad}
		if !call(t, alice, "patch_article", args).IsError {
			t.Fatalf("MCP accepted invalid patch %v", bad)
		}
	}
	clear := map[string]any{"article_id": first.Article.ID, "idempotency_key": "clear-body", "expected_revision": 2, "patch": map[string]any{"content_markdown": nil}}
	cleared := decode[sqlite.MutationResult](t, call(t, alice, "patch_article", clear))
	if cleared.Article.ContentMarkdown != "" || cleared.Article.ThumbnailHash != first.Article.ThumbnailHash {
		t.Fatal("MCP null clear not isolated")
	}
	archive := feed.ArchiveInput{IdempotencyKey: "archive", ArticleID: first.Article.ID, ExpectedRevision: 3, Archived: true}
	if !call(t, readOnly, "set_article_archived", archive).IsError || !call(t, writeOnly, "set_article_archived", archive).IsError || !call(t, bob, "set_article_archived", archive).IsError {
		t.Fatal("MCP archive crossed owner/scope")
	}
	archived := decode[sqlite.MutationResult](t, call(t, alice, "set_article_archived", archive))
	trash := feed.StateInput{IdempotencyKey: "trash", ArticleID: first.Article.ID, ExpectedRevision: 4}
	if !call(t, readOnly, "trash_article", trash).IsError || !call(t, writeOnly, "trash_article", trash).IsError || !call(t, bob, "trash_article", trash).IsError {
		t.Fatal("MCP Trash crossed owner/scope")
	}
	trashed := decode[sqlite.MutationResult](t, call(t, alice, "trash_article", trash))
	if trashed.Article.TrashedAt == 0 || trashed.Article.ArchivedAt != archived.Article.ArchivedAt {
		t.Fatal("MCP Trash damaged archive state")
	}
	if !call(t, alice, "get_article", get).IsError {
		t.Fatal("MCP default full read included Trash")
	}
	get.Trashed = true
	if got := decode[sqlite.Article](t, call(t, alice, "get_article", get)); got != trashed.Article {
		t.Fatal("MCP explicit Trash read failed")
	}
	if page := decode[feed.Page](t, call(t, alice, "list_articles", feed.ListInput{Trashed: true})); len(page.Articles) != 1 || page.Articles[0].ContentMarkdown != "" {
		t.Fatal("MCP Trash list failed")
	}
	if page := decode[feed.Page](t, call(t, alice, "list_articles", feed.ListInput{Archived: true})); len(page.Articles) != 0 {
		t.Fatal("MCP Trash remained in Archive")
	}
	trashPatch := patch
	trashPatch.IdempotencyKey, trashPatch.ExpectedRevision = "edit-trash", 5
	if !call(t, alice, "patch_article", trashPatch).IsError {
		t.Fatal("MCP edited Trash")
	}
	restore := feed.StateInput{IdempotencyKey: "restore", ArticleID: first.Article.ID, ExpectedRevision: 5}
	if !call(t, readOnly, "restore_article", restore).IsError || !call(t, writeOnly, "restore_article", restore).IsError || !call(t, bob, "restore_article", restore).IsError {
		t.Fatal("MCP Restore crossed owner/scope")
	}
	restored := decode[sqlite.MutationResult](t, call(t, alice, "restore_article", restore))
	if restored.Article.TrashedAt != 0 || restored.Article.ArchivedAt != archived.Article.ArchivedAt || restored.Article.Revision != 6 {
		t.Fatal("MCP Restore lost previous archive")
	}
	for _, retry := range []struct {
		tool  string
		input any
	}{{"patch_article", patch}, {"set_article_archived", archive}, {"trash_article", trash}, {"restore_article", restore}} {
		for _, result := range []*sdk.CallToolResult{
			call(t, readOnly, retry.tool, retry.input),
			call(t, writeOnly, retry.tool, retry.input),
		} {
			if !result.IsError || result.StructuredContent != nil {
				t.Fatalf("MCP %s exact retry exposed stored content without both scopes: %#v", retry.tool, result)
			}
		}
	}
	if replay := decode[sqlite.MutationResult](t, call(t, alice, "trash_article", trash)); replay != trashed {
		t.Fatal("MCP Trash replay changed result")
	}
	if current := decode[sqlite.Article](t, call(t, alice, "get_article", feed.GetInput{ArticleID: first.Article.ID})); current != restored.Article {
		t.Fatal("Trash replay applied stale state again")
	}
	if response := request(h, "GET", "/thumbnails/"+first.Article.ID, f.Cookie(t, "alice"), ""); response.Code != 200 || !bytes.Equal(response.Body.Bytes(), images.body) || len(inline.urls) != 0 || images.calls.Load() != 1 {
		t.Fatal("stored thumbnail route downloaded again or lost body")
	}
	archive.IdempotencyKey, archive.ExpectedRevision, archive.Archived = "unarchive", 6, false
	if out := decode[sqlite.MutationResult](t, call(t, alice, "set_article_archived", archive)); out.Article.ArchivedAt != 0 {
		t.Fatal("explicit MCP unarchive failed")
	}
	if err := f.Store.DisableAccount(t.Context(), f.Principal(t, "alice", allScopes).Identity()); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"get_article", "patch_article", "trash_article", "restore_article", "set_article_archived"} {
		response := request(h, "POST", "/mcp", nil, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
		if response.Code != 401 {
			t.Fatalf("disabled actor could reach %s", tool)
		}
	}
}

func TestRawMCPPatchRejectsAmbiguousFieldsBeforeSideEffects(t *testing.T) {
	f, _, _ := app(t)
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 80, 80)), &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	images := &storedImageFetcher{body: jpegBytes.Bytes()}
	policy, _ := thumbnail.NewPolicy("")
	s := feed.NewWithThumbnailFetcher(f.Store, policy, images)
	h, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, s, &recordingImageFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	token := f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes)
	c := client(t, server.URL, token)
	first := decode[sqlite.MutationResult](t, call(t, c, "add_article", feed.AddInput{
		IdempotencyKey: "raw-add",
		Article: feed.Content{URL: "https://example.com/raw-patch", Title: "Original title", Summary: "Original introduction",
			ContentMarkdown: "# Original body", ThumbnailURL: "https://images.example.com/original.jpg"},
	}))
	p := f.Principal(t, "alice", allScopes)

	// Keep duplicate keys in the actual HTTP bytes. A map or SDK client call
	// would normalize the arguments before the server gets a chance to reject them.
	patch := func(t *testing.T, arguments string) *sdk.CallToolResult {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"patch_article","arguments":` + arguments + `}}`
		req, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "newsfeed.example"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var envelope struct {
			Result *sdk.CallToolResult `json:"result"`
			Error  json.RawMessage     `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode HTTP %d MCP response: %v", response.StatusCode, err)
		}
		if response.StatusCode != http.StatusOK || envelope.Result == nil || envelope.Error != nil {
			t.Fatalf("expected MCP tool result, got HTTP %d or protocol error", response.StatusCode)
		}
		return envelope.Result
	}
	arguments := func(key string, revision int, fields string) string {
		return fmt.Sprintf(`{"idempotency_key":%q,"article_id":%q,"expected_revision":%d,%s}`, key, first.Article.ID, revision, fields)
	}

	// Compare every column, including the original result/hash bytes, so a
	// rejected request cannot silently reserve a key or rewrite an existing retry.
	uri := url.URL{Scheme: "file", Path: f.Path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	type retryRecord struct {
		OwnerID, Key, Operation, PayloadHash, Result string
		CreatedAt                                    int64
	}
	retry := func(t *testing.T) retryRecord {
		t.Helper()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM idempotency_results").Scan(&count); err != nil || count != 1 {
			t.Fatalf("rejected patch changed retry record count: %d, %v", count, err)
		}
		var row retryRecord
		if err := db.QueryRowContext(t.Context(), "SELECT owner_id, key, operation, payload_hash, result, created_at FROM idempotency_results").Scan(
			&row.OwnerID, &row.Key, &row.Operation, &row.PayloadHash, &row.Result, &row.CreatedAt); err != nil {
			t.Fatal(err)
		}
		return row
	}
	originalRetry := retry(t)
	for _, tc := range []struct {
		name, fields string
	}{
		{"duplicate title", `"patch":{"title":"First title","title":"Second title"}`},
		{"escaped duplicate title", `"patch":{"title":"First title","ti\u0074le":"Second title"}`},
		{"duplicate title hides null", `"patch":{"title":null,"title":"Second title"}`},
		{"duplicate summary", `"patch":{"summary":"First introduction","summary":"Second introduction"}`},
		{"duplicate URL", `"patch":{"url":"https://example.com/first","url":"https://example.com/second"}`},
		{"duplicate body", `"patch":{"content_markdown":"First body","content_markdown":"Second body"}`},
		{"duplicate body hides null", `"patch":{"content_markdown":null,"content_markdown":"Second body"}`},
		{"duplicate body ends null", `"patch":{"content_markdown":"First body","content_markdown":null}`},
		{"duplicate thumbnail", `"patch":{"thumbnail_url":"https://images.example.com/first.jpg","thumbnail_url":"https://images.example.com/second.jpg"}`},
		{"duplicate thumbnail hides null", `"patch":{"thumbnail_url":null,"thumbnail_url":"https://images.example.com/second.jpg"}`},
		{"duplicate thumbnail ends null", `"patch":{"thumbnail_url":"https://images.example.com/first.jpg","thumbnail_url":null}`},
		{"duplicate patch objects", `"patch":{"title":"First title"},"patch":{"title":"Second title","thumbnail_url":"https://images.example.com/second.jpg"}`},
		{"duplicate key", `"idempotency_key":"another-key","patch":{"title":"Changed title"}`},
		{"duplicate article ID", fmt.Sprintf(`"article_id":%q,"patch":{"title":"Changed title"}`, first.Article.ID)},
		{"duplicate revision", `"expected_revision":1,"patch":{"title":"Changed title"}`},
		{"unknown patch field", `"patch":{"title":"Changed title","extra":"unknown"}`},
		{"unknown argument", `"extra":"unknown","patch":{"title":"Changed title"}`},
		{"null title", `"patch":{"title":null}`},
		{"null summary", `"patch":{"summary":null}`},
		{"null URL", `"patch":{"url":null}`},
		{"wrong body type", `"patch":{"content_markdown":1}`},
		{"empty patch", `"patch":{}`},
		{"null patch", `"patch":null`},
		{"array patch", `"patch":[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result := patch(t, arguments("raw-rejected", 1, tc.fields)); !result.IsError || result.StructuredContent != nil {
				t.Fatal("ambiguous or invalid raw MCP patch succeeded")
			}
			if current, err := s.GetArticle(t.Context(), p, first.Article.ID); err != nil || current != first.Article {
				t.Fatal("rejected raw patch changed the article")
			}
			if images.calls.Load() != 1 {
				t.Fatal("rejected raw patch fetched an image")
			}
			if retry(t) != originalRetry {
				t.Fatal("rejected raw patch changed existing retry bytes")
			}
		})
	}
	if t.Failed() {
		return
	}

	// A rejected key remains usable. Omission still retains stored fields,
	// while each explicit null clears only its optional field through the schema.
	updated := decode[sqlite.MutationResult](t, patch(t, arguments("raw-rejected", 1, `"patch":{"title":"Accepted title"}`)))
	if updated.Article.ContentMarkdown != first.Article.ContentMarkdown || updated.Article.ThumbnailHash != first.Article.ThumbnailHash || images.calls.Load() != 1 {
		t.Fatal("raw patch lost omitted body or thumbnail")
	}
	clearedBody := decode[sqlite.MutationResult](t, patch(t, arguments("raw-clear-body", 2, `"patch":{"content_markdown":null}`)))
	if clearedBody.Article.ContentMarkdown != "" || clearedBody.Article.ThumbnailHash != first.Article.ThumbnailHash || clearedBody.Article.Title != updated.Article.Title {
		t.Fatal("raw null body clear changed another field")
	}
	clearedImage := decode[sqlite.MutationResult](t, patch(t, arguments("raw-clear-image", 3, `"patch":{"thumbnail_url":null}`)))
	if clearedImage.Article.ThumbnailURL != "" || clearedImage.Article.ThumbnailHash != "" || clearedImage.Article.ContentMarkdown != "" || images.calls.Load() != 1 {
		t.Fatal("raw null thumbnail clear fetched or retained an image")
	}
	if replay := decode[sqlite.MutationResult](t, patch(t, arguments("raw-clear-body", 2, `"patch":{"content_markdown":""}`))); replay != clearedBody {
		t.Fatal("raw empty/null clear retry changed the original result")
	}
}

func stateForm(h http.Handler, path string, cookie *http.Cookie, csrf, key string, revision int64, origin string) *httptest.ResponseRecorder {
	values := url.Values{"csrf_token": {csrf}, "idempotency_key": {key}, "expected_revision": {strconv.FormatInt(revision, 10)}}
	r := httptest.NewRequest("POST", testfixture.Origin+path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}

func TestBrowserTrashFormsViewsImagesAndCSRF(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	c := feed.Content{URL: "https://example.com/trash", Title: "Source title", Summary: "Source introduction", ContentMarkdown: "# Original source\n\n![Source figure](https://images.example.com/figure.png)"}
	first, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "save", Article: c})
	if err != nil {
		t.Fatal(err)
	}
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	bob := f.CookieWithScopes(t, "bob", allScopes)
	_, bobCSRF := f.BrowserPrincipal(t, bob)
	readOnly := f.Cookie(t, "alice")
	_, readCSRF := f.BrowserPrincipal(t, readOnly)
	path := "/api/v1/articles/" + first.Article.ID
	for i, tc := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		status       int
	}{{nil, csrf, testfixture.Origin, 401}, {bob, bobCSRF, testfixture.Origin, 404}, {readOnly, readCSRF, testfixture.Origin, 403}, {cookie, "wrong", testfixture.Origin, 403}, {cookie, csrf, "", 403}, {cookie, csrf, "https://attacker.example", 403}} {
		if response := stateForm(h, path+"/trash", tc.cookie, tc.csrf, fmt.Sprintf("bad-%d", i), 1, tc.origin); response.Code != tc.status {
			t.Fatalf("Trash form boundary: %d wanted %d", response.Code, tc.status)
		}
	}
	if response := request(h, "GET", path+"/trash", cookie, ""); response.Code != 405 {
		t.Fatal("GET trashed article")
	}
	if response := stateForm(h, path+"/trash", cookie, csrf, "trash", 1, testfixture.Origin); response.Code != 303 || response.Header().Get("Location") != "/articles/"+first.Article.ID+"?from=trash" {
		t.Fatal("Trash form did not redirect to Trash reader")
	}
	for _, route := range []string{path, "/articles/" + first.Article.ID, "/thumbnails/" + first.Article.ID} {
		if response := request(h, "GET", route, cookie, ""); response.Code != 404 {
			t.Fatalf("ordinary route included Trash: %s %d", route, response.Code)
		}
	}
	for _, route := range []string{path + "?trashed=true", "/articles/" + first.Article.ID + "?from=trash", "/trash"} {
		if response := request(h, "GET", route, cookie, ""); response.Code != 200 {
			t.Fatalf("explicit Trash route failed: %s %d", route, response.Code)
		}
		if route != "/trash" {
			if response := request(h, "GET", route, bob, ""); response.Code != 404 {
				t.Fatal("Trash route disclosed another owner")
			}
		}
	}
	response := request(h, "GET", "/articles/"+first.Article.ID+"?from=trash", cookie, "")
	_, after, _ := strings.Cut(response.Body.String(), `data-bootstrap="`)
	encoded, _, _ := strings.Cut(after, `"`)
	var bootstrap struct {
		FromTrash     bool              `json:"from_trash"`
		MutationKey   string            `json:"mutation_key"`
		ContentImages map[string]string `json:"content_images"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(encoded)), &bootstrap); err != nil || !bootstrap.FromTrash || len(bootstrap.MutationKey) < 26 || !strings.HasSuffix(bootstrap.ContentImages["https://images.example.com/figure.png"], "?trashed=true") {
		t.Fatal("Trash reader bootstrap did not bind actions/images")
	}
	for _, action := range []string{"archive", "restore"} {
		if response := postArticleForm(h, path+"/"+action, cookie, csrf, testfixture.Origin); response.Code != 404 {
			t.Fatal("legacy archive route revived Trash")
		}
	}
	if response := stateForm(h, path+"/restore-trash", cookie, csrf, "restore", 1, testfixture.Origin); response.Code != 409 {
		t.Fatal("stale browser restore accepted")
	}
	if response := stateForm(h, path+"/restore-trash", cookie, csrf, "restore", 2, testfixture.Origin); response.Code != 303 || response.Header().Get("Location") != "/articles/"+first.Article.ID+"?from=feed" {
		t.Fatal("browser restore did not return Feed")
	}
	if response := request(h, "GET", "/api/v1/articles?archived=true&trashed=true", cookie, ""); response.Code != 400 {
		t.Fatal("conflicting browser views accepted")
	}
	if response := request(h, "GET", path+"?trashed=invalid", cookie, ""); response.Code != 400 {
		t.Fatal("invalid Trash detail flag accepted")
	}
	if response := stateForm(h, path+"/trash", cookie, csrf, "trash", 1, testfixture.Origin); response.Code != 303 {
		t.Fatal("exact old browser Trash retry did not replay")
	}
	current, err := s.GetArticle(t.Context(), p, first.Article.ID)
	if err != nil || current.TrashedAt != 0 || current.Revision != 3 {
		t.Fatal("browser retry applied stale transition again")
	}
}
