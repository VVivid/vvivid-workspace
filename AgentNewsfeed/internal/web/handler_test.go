package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

const allScopes = identity.Read + " " + identity.ArticlesWrite

func app(t *testing.T) (*testfixture.Identity, *feed.Service, http.Handler) {
	t.Helper()
	f := testfixture.New(t)
	f.Enroll(t, "alice")
	f.Enroll(t, "bob")
	policy, _ := thumbnail.NewPolicy("")
	s := feed.New(f.Store, policy)
	h, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, s, thumbnail.NewProxy(policy))
	if err != nil {
		t.Fatal(err)
	}
	return f, s, h
}
func request(h http.Handler, method, path string, cookie *http.Cookie, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, testfixture.Origin+path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}

type authTransport struct{ token string }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header = r.Header.Clone()
	c.Header.Set("Authorization", "Bearer "+a.token)
	c.Host = "newsfeed.example"
	return http.DefaultTransport.RoundTrip(c)
}
func client(t *testing.T, endpoint, token string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "newsfeed-test", Version: "1"}, nil)
	session, err := c.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: endpoint + "/mcp", HTTPClient: &http.Client{Transport: authTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
func call(t *testing.T, c *sdk.ClientSession, name string, in any) *sdk.CallToolResult {
	t.Helper()
	out, err := c.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: in})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func decode[T any](t *testing.T, out *sdk.CallToolResult) T {
	t.Helper()
	if out.IsError || out.StructuredContent == nil {
		t.Fatalf("tool failure: %#v", out)
	}
	raw, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestOfficialMCPAddListUpdateAndBrowserSharePrivateRecords(t *testing.T) {
	f, s, h := app(t)
	server := httptest.NewServer(h)
	defer server.Close()
	c := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	tools, err := c.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("untyped tool %s", tool.Name)
		}
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "add_article,list_articles,update_article" {
		t.Fatalf("unexpected tools %v", names)
	}
	in := feed.AddInput{IdempotencyKey: "mcp-add", Article: feed.Content{URL: "https://example.com/story?utm_source=agent", Title: "An interesting story", Summary: "A concise, sourced introduction."}}
	first := decode[sqlite.MutationResult](t, call(t, c, "add_article", in))
	if !first.Created {
		t.Fatal("MCP did not create article")
	}
	if again := decode[sqlite.MutationResult](t, call(t, c, "add_article", in)); again != first {
		t.Fatal("MCP replay changed original result")
	}
	page := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{}))
	if len(page.Articles) != 1 || page.Articles[0] != first.Article {
		t.Fatal("MCP list did not see write")
	}
	in.Article.Title = `<img src=x onerror=alert(1)>`
	edit := feed.UpdateInput{IdempotencyKey: "mcp-edit", ArticleID: first.Article.ID, ExpectedRevision: 1, Article: in.Article}
	updated := decode[sqlite.MutationResult](t, call(t, c, "update_article", edit))
	if updated.Article.Revision != 2 {
		t.Fatal("revision not incremented")
	}
	stale := edit
	stale.IdempotencyKey = "stale"
	if !call(t, c, "update_article", stale).IsError {
		t.Fatal("stale MCP update accepted")
	}
	cookie := f.Cookie(t, "alice")
	html := request(h, "GET", "/", cookie, "")
	if html.Code != 200 || bytes.Contains(html.Body.Bytes(), []byte(`<img src=x`)) || !bytes.Contains(html.Body.Bytes(), []byte(`\u003cimg src=x`)) {
		t.Fatalf("HTML escape failed %d %s", html.Code, html.Body)
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
		if html.Header().Get(header) == "" {
			t.Errorf("missing security header %s", header)
		}
	}
	if html.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private feed cacheable")
	}
	api := request(h, "GET", "/api/v1/articles", cookie, "")
	var browserPage feed.Page
	if api.Code != 200 || json.Unmarshal(api.Body.Bytes(), &browserPage) != nil || browserPage.Articles[0].Revision != 2 {
		t.Fatalf("browser did not see MCP update %d %s", api.Code, api.Body)
	}
	bob := client(t, server.URL, f.Bearer("bob", "newsfeed-chatgpt", f.Manager.MCPAudience(), allScopes))
	if got := decode[feed.Page](t, call(t, bob, "list_articles", feed.ListInput{})); len(got.Articles) != 0 {
		t.Fatal("MCP list exposed another owner")
	}
	if !call(t, bob, "update_article", edit).IsError {
		t.Fatal("foreign MCP update accepted")
	}
	readOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.Read))
	if !call(t, readOnly, "add_article", in).IsError {
		t.Fatal("read token wrote")
	}
	writeOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.ArticlesWrite))
	if !call(t, writeOnly, "list_articles", feed.ListInput{}).IsError {
		t.Fatal("write implied read")
	}
	if got := request(h, "GET", "/", f.Cookie(t, "bob"), ""); bytes.Contains(got.Body.Bytes(), []byte(`\u003cimg src=x`)) {
		t.Fatal("browser feed leaked another owner")
	}
	if _, err := s.GetArticle(t.Context(), f.Principal(t, "alice", allScopes), first.Article.ID); err != nil {
		t.Fatal(err)
	}
}

func TestComposedAuthenticationMetadataAndThumbnailBoundaries(t *testing.T) {
	f, s, h := app(t)
	cookie := f.Cookie(t, "alice")
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/api/v1/articles", "", 401},
		{"POST", "/mcp", "", 401},
		{"POST", "/mcp", f.Bearer("alice", "newsfeed-codex", "https://ssh.example/mcp", identity.Read), 401},
		{"POST", "/mcp", f.Bearer("mallory", "newsfeed-codex", f.Manager.MCPAudience(), identity.Read), 401},
		{"POST", "/api/v1/articles", "", 405},
	} {
		if out := request(h, tc.method, tc.path, nil, tc.token); out.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, out.Code)
		}
	}
	r := httptest.NewRequest("GET", testfixture.Origin+"/api/v1/articles", nil)
	r.Header.Set("X-Forwarded-User", "alice")
	r.Header.Set("X-Auth-Request-User", "alice")
	r.Header.Set("X-User-ID", "alice")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 401 {
		t.Fatal("identity headers trusted")
	}
	for _, header := range []string{"Host", "Origin"} {
		r := httptest.NewRequest("GET", testfixture.Origin+"/", nil)
		r.AddCookie(cookie)
		if header == "Host" {
			r.Host = "evil.example"
		} else {
			r.Header.Set(header, "https://evil.example")
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		if out.Code < 400 {
			t.Fatalf("bad %s accepted", header)
		}
	}
	metadata := request(h, "GET", "/.well-known/oauth-protected-resource/mcp", nil, "")
	if metadata.Code != 200 || !strings.Contains(metadata.Body.String(), f.Manager.MCPAudience()) || strings.Contains(metadata.Body.String(), "shopping:") {
		t.Fatalf("resource metadata %s", metadata.Body)
	}
	pageHeaders := request(h, "GET", "/", cookie, "").Header()
	if !strings.Contains(pageHeaders.Get("Content-Security-Policy"), "form-action 'self' "+f.Manager.IssuerOrigin()+";") {
		t.Fatal("logout redirect blocked by CSP")
	}
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer remote.Close()
	article, err := s.AddArticle(t.Context(), f.Principal(t, "alice", allScopes), feed.AddInput{IdempotencyKey: "source", Article: feed.Content{URL: "https://example.com/a", Title: "Private", Summary: "Private introduction."}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/thumbnails/" + article.Article.ID + "?url=" + remote.URL
	for _, c := range []*http.Cookie{nil, cookie, f.Cookie(t, "bob")} {
		response := request(h, "GET", path, c, "")
		if response.Code != 401 && response.Code != 404 {
			t.Fatalf("thumbnail status %d", response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("thumbnail endpoint fetched arbitrary query URL")
	}
	if result := request(h, "GET", "/", nil, ""); result.Code != 200 || strings.Contains(result.Body.String(), "Private introduction.") {
		t.Fatal("anonymous feed leaked data")
	}
	if result := request(h, "GET", "/health/ready", nil, ""); result.Code != 200 {
		t.Fatal("configured app not ready")
	}
	f.SetUnavailable(true)
	if result := request(h, "GET", "/health/ready", nil, ""); result.Code != 200 {
		t.Fatal("readiness unexpectedly contacts provider")
	}
	if err := f.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if result := request(h, "GET", "/health/ready", nil, ""); result.Code != 503 {
		t.Fatal("closed database still ready")
	}
	if result := request(h, "GET", "/health/live", nil, ""); result.Code != 200 {
		t.Fatal("liveness depends on persistence")
	}
}

func TestUnconfiguredShellFailsClosed(t *testing.T) {
	f := testfixture.New(t)
	policy, _ := thumbnail.NewPolicy("")
	h, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, feed.New(f.Store, policy), thumbnail.NewProxy(policy))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/archive", "/articles/anything", "/health/ready", "/mcp", "/api/v1/articles", "/auth/login", "/thumbnails/anything"} {
		if out := request(h, "GET", path, nil, ""); out.Code != 503 {
			t.Fatalf("unconfigured %s returned %d", path, out.Code)
		}
	}
	if out := request(h, "GET", "/health/live", nil, ""); out.Code != 200 {
		t.Fatal("shell liveness failed")
	}
	if out := request(h, "GET", "/", nil, ""); out.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("unconfigured shell content type lost")
	}
}

func postArticleForm(h http.Handler, path string, cookie *http.Cookie, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", testfixture.Origin+path, strings.NewReader(url.Values{"csrf_token": {token}}.Encode()))
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

func TestReaderDetailArchiveFormsRequireOwnerScopesAndCSRF(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	c := feed.Content{URL: "https://example.com/original", Title: "Original title", Summary: "Original introduction", ContentMarkdown: "# Original article\n\nSaved source text.\n\n![disabled](https://images.example.com/inline.png)\n\n<script>alert(1)</script>"}
	out, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "reader", Article: c})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Article.ID
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	legacy := f.Cookie(t, "alice")
	_, legacyCSRF := f.BrowserPrincipal(t, legacy)
	bob := f.CookieWithScopes(t, "bob", allScopes)
	_, bobCSRF := f.BrowserPrincipal(t, bob)
	path := "/api/v1/articles/" + id
	for _, route := range []string{"/articles/" + id, "/articles/" + id + "?from=archive", path} {
		if response := request(h, "GET", route, cookie, ""); response.Code != 200 || strings.Contains(response.Body.String(), "<script>alert(1)</script>") {
			t.Fatalf("unsafe or failed reader %s: %d", route, response.Code)
		}
	}
	var detail sqlite.Article
	response := request(h, "GET", path, cookie, "")
	if json.Unmarshal(response.Body.Bytes(), &detail) != nil || detail != out.Article {
		t.Fatal("detail omitted or changed source content")
	}
	if response := request(h, "GET", "/api/v1/articles", cookie, ""); strings.Contains(response.Body.String(), "content_markdown") {
		t.Fatal("list serialized article body")
	}
	for _, tc := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		status       int
	}{
		{nil, csrf, testfixture.Origin, 401},
		{legacy, legacyCSRF, testfixture.Origin, 403},
		{cookie, "", testfixture.Origin, 403},
		{cookie, strings.Repeat("a", 26), testfixture.Origin, 403},
		{cookie, csrf, "", 403},
		{cookie, csrf, "null", 403},
		{cookie, csrf, "https://evil.example", 403},
		{bob, bobCSRF, testfixture.Origin, 404},
		{cookie, strings.Repeat("a", 5000), testfixture.Origin, 403},
	} {
		if response := postArticleForm(h, path+"/archive", tc.cookie, tc.csrf, tc.origin); response.Code != tc.status {
			t.Fatalf("archive boundary status %d wanted %d", response.Code, tc.status)
		}
	}
	if current, err := s.GetArticle(t.Context(), p, id); err != nil || current != out.Article {
		t.Fatal("reader or failed forms changed source/state")
	}
	if response := request(h, "GET", path+"/archive", cookie, ""); response.Code != 405 {
		t.Fatal("GET changed archive state")
	}
	if response := request(h, "GET", path, bob, ""); response.Code != 404 {
		t.Fatal("detail disclosed foreign record")
	}
	if response := request(h, "GET", "/articles/"+id, bob, ""); response.Code != 404 {
		t.Fatal("reader disclosed foreign record")
	}
	if response := postArticleForm(h, path+"/archive", cookie, csrf, testfixture.Origin); response.Code != 303 || response.Header().Get("Location") != "/articles/"+id+"?from=archive" {
		t.Fatal("archive form failed or returned to wrong view")
	}
	var archive feed.Page
	response = request(h, "GET", "/api/v1/articles?archived=true", cookie, "")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &archive) != nil || len(archive.Articles) != 1 || archive.Articles[0].ArchivedAt == 0 {
		t.Fatal("archive filter did not include article")
	}
	var active feed.Page
	response = request(h, "GET", "/api/v1/articles", cookie, "")
	if json.Unmarshal(response.Body.Bytes(), &active) != nil || len(active.Articles) != 0 {
		t.Fatal("archive stayed in active feed")
	}
	if response := request(h, "GET", "/archive", cookie, ""); response.Code != 200 {
		t.Fatal("archive refresh failed")
	}
	if response := request(h, "GET", "/api/v1/articles?archived=invalid", cookie, ""); response.Code != 400 {
		t.Fatal("invalid archive filter accepted")
	}
	if response := postArticleForm(h, path+"/restore", bob, bobCSRF, testfixture.Origin); response.Code != 404 {
		t.Fatal("foreign restore accepted")
	}
	if response := postArticleForm(h, path+"/restore", legacy, legacyCSRF, testfixture.Origin); response.Code != 403 {
		t.Fatal("read-only restore accepted")
	}
	if response := postArticleForm(h, path+"/restore", cookie, csrf, testfixture.Origin); response.Code != 303 || response.Header().Get("Location") != "/articles/"+id+"?from=feed" {
		t.Fatal("restore form failed or returned to wrong view")
	}
	current, err := s.GetArticle(t.Context(), p, id)
	if err != nil || current.ContentMarkdown != c.ContentMarkdown || current.ArchivedAt != 0 || current.Revision != 3 {
		t.Fatal("archive/restore damaged content or revision")
	}
	if err := f.Store.DisableAccount(t.Context(), p.Identity()); err != nil {
		t.Fatal(err)
	}
	if response := postArticleForm(h, path+"/archive", cookie, csrf, testfixture.Origin); response.Code != 401 {
		t.Fatal("disabled session archived")
	}
}

func TestMCPAcceptsFullSourceBeyondOldRequestLimitAndListsSummaries(t *testing.T) {
	f, s, h := app(t)
	server := httptest.NewServer(h)
	defer server.Close()
	c := client(t, server.URL, f.Bearer("alice", "newsfeed-chatgpt", f.Manager.MCPAudience(), allScopes))
	in := feed.AddInput{IdempotencyKey: "long-original", Article: feed.Content{URL: "https://example.com/long", Title: "Source title", Summary: "Source introduction.", ContentMarkdown: "# Source\n\n" + strings.Repeat("Original source paragraph.\n\n", 3000)}}
	out := decode[sqlite.MutationResult](t, call(t, c, "add_article", in))
	if out.Article.ContentMarkdown != in.Article.ContentMarkdown {
		t.Fatal("MCP truncated original Markdown")
	}
	page := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{}))
	if len(page.Articles) != 1 || page.Articles[0].ContentMarkdown != "" {
		t.Fatal("MCP list included full body")
	}
	p := f.Principal(t, "alice", allScopes)
	if _, err := s.SetArchived(t.Context(), p, out.Article.ID, true); err != nil {
		t.Fatal(err)
	}
	if page := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{})); len(page.Articles) != 0 {
		t.Fatal("MCP default includes archived")
	}
	if page := decode[feed.Page](t, call(t, c, "list_articles", feed.ListInput{Archived: true})); len(page.Articles) != 1 || page.Articles[0].ContentMarkdown != "" {
		t.Fatal("MCP archive filter failed")
	}
	in.IdempotencyKey = "body-too-big"
	in.Article.ContentMarkdown = strings.Repeat("a", feed.MaxContentBytes+1)
	if !call(t, c, "add_article", in).IsError {
		t.Fatal("MCP accepted oversized content")
	}
}

func TestEmbeddedAssetsAndPrivateShellCSP(t *testing.T) {
	f, _, h := app(t)
	for _, asset := range []struct{ path, contentType string }{{"/static/assets/app.js", "javascript"}, {"/static/assets/app.css", "text/css"}} {
		response := request(h, "GET", asset.path, nil, "")
		if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), asset.contentType) || response.Body.Len() < 100 {
			t.Fatalf("missing embedded build asset %s", asset.path)
		}
	}
	for _, route := range []string{"/", "/archive", "/articles/private?from=archive"} {
		response := request(h, "GET", route, nil, "")
		if response.Code != 200 || strings.Contains(response.Body.String(), "content_markdown") {
			t.Fatal("anonymous deep link leaked content or failed gate")
		}
		if response.Header().Get("Referrer-Policy") != "same-origin" {
			t.Fatal("native form navigations require same-origin referrer policy to retain their Origin")
		}
		csp := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self';") || !strings.Contains(csp, "connect-src 'self';") || strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Fatal("SPA CSP is not restricted to self")
		}
	}
	response := request(h, "GET", "/", f.Cookie(t, "alice"), "")
	if !strings.Contains(response.Body.String(), `&#34;can_write&#34;:false`) {
		t.Fatal("legacy grant is not advertised read-only to UI")
	}
}
