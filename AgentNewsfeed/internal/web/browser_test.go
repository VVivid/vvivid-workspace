package web_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
)

const browserMediaType = "application/vnd.agentnewsfeed.browser+json"

func browserJSONRequest(h http.Handler, path string, cookie *http.Cookie, form url.Values, origin string) *httptest.ResponseRecorder {
	method := http.MethodGet
	if form != nil {
		method = http.MethodPost
	}
	r := httptest.NewRequest(method, testfixture.Origin+path, strings.NewReader(form.Encode()))
	r.Header.Set("Accept", browserMediaType)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, r)
	return response
}

func readBrowserJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != browserMediaType ||
		response.Header().Get("Vary") != "Accept" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid browser representation: status %d, headers %v", response.Code, response.Header())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrowserJSONPageAndStateForms(t *testing.T) {
	f, s, h := app(t)
	p := f.Principal(t, "alice", allScopes)
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	a, err := s.AddArticle(t.Context(), p, feed.AddInput{IdempotencyKey: "json-article", Article: feed.Content{
		URL: "https://example.com/story", Title: "Saved article", Summary: "Introduction",
		ContentMarkdown: "# Source\n\n<script>untrusted()</script>\n\n![Figure](https://images.example.com/figure.png)",
	}})
	if err != nil {
		t.Fatal(err)
	}
	type articlePage struct {
		Authenticated bool              `json:"authenticated"`
		CSRF          string            `json:"csrf"`
		MutationKey   string            `json:"mutation_key"`
		SignInURL     string            `json:"sign_in_url"`
		Article       *sqlite.Article   `json:"article"`
		FromTrash     bool              `json:"from_trash"`
		ContentImages map[string]string `json:"content_images"`
	}
	articlePath := "/articles/" + a.Article.ID
	response := request(h, http.MethodGet, articlePath, cookie, "")
	_, rest, found := strings.Cut(response.Body.String(), `data-bootstrap="`)
	encoded, _, closed := strings.Cut(rest, `"`)
	var native articlePage
	if !found || !closed || json.Unmarshal([]byte(html.UnescapeString(encoded)), &native) != nil {
		t.Fatalf("missing or invalid bootstrap in HTTP %d", response.Code)
	}
	negotiated := readBrowserJSON[articlePage](t, browserJSONRequest(h, articlePath, cookie, nil, ""))
	if negotiated.MutationKey == "" || negotiated.CSRF != csrf {
		t.Fatal("JSON page omitted the state form credentials")
	}
	native.MutationKey, negotiated.MutationKey = "", ""
	if !reflect.DeepEqual(native, negotiated) {
		t.Fatal("HTML and JSON pages selected different data")
	}
	gate := readBrowserJSON[articlePage](t, browserJSONRequest(h, articlePath, nil, nil, ""))
	if gate.Authenticated || gate.Article != nil || gate.CSRF != "" || len(gate.ContentImages) != 0 ||
		gate.SignInURL != "/auth/login?return_to="+url.QueryEscape(articlePath) {
		t.Fatal("JSON sign-in gate lost the return path or exposed private data")
	}
	if response := browserJSONRequest(h, articlePath, f.Cookie(t, "bob"), nil, ""); response.Code != http.StatusNotFound {
		t.Fatalf("foreign detail HTTP %d", response.Code)
	}

	type destination struct {
		Location string `json:"location"`
	}
	articleForm := url.Values{"csrf_token": {csrf}}
	archivePath := "/api/v1" + articlePath + "/archive"
	for _, origin := range []string{"", "https://attacker.example"} {
		if response := browserJSONRequest(h, archivePath, cookie, articleForm, origin); response.Code != http.StatusForbidden {
			t.Fatalf("JSON mutation accepted unsafe origin %q", origin)
		}
	}
	archive := browserJSONRequest(h, archivePath, cookie, articleForm, testfixture.Origin)
	if got := readBrowserJSON[destination](t, archive).Location; got != articlePath+"?from=archive" {
		t.Fatalf("article archive destination %q", got)
	}
	trashedArticle := url.Values{"csrf_token": {csrf}, "idempotency_key": {"json-trash"}, "expected_revision": {"2"}}
	trash := browserJSONRequest(h, "/api/v1"+articlePath+"/trash", cookie, trashedArticle, testfixture.Origin)
	trashPath := readBrowserJSON[destination](t, trash).Location
	if trashPath != articlePath+"?from=trash" {
		t.Fatalf("article Trash destination %q", trashPath)
	}
	pageResponse := browserJSONRequest(h, trashPath, cookie, nil, "")
	page := readBrowserJSON[articlePage](t, pageResponse)
	image := page.ContentImages["https://images.example.com/figure.png"]
	if !page.FromTrash || page.Article == nil || page.Article.ArchivedAt == 0 || page.Article.TrashedAt == 0 || page.Article.ContentMarkdown != a.Article.ContentMarkdown ||
		!strings.HasPrefix(image, "/thumbnails/"+a.Article.ID+"/content/") || !strings.HasSuffix(image, "?trashed=true") ||
		strings.Contains(pageResponse.Body.String(), "<script>") {
		t.Fatal("JSON Trash reader lost state, protected images, or escaping")
	}
}

func TestCompactShelfActionsKeepViewAndRejectUnsafeDestinationsBeforeWrite(t *testing.T) {
	f, s, h := app(t)
	ctx := t.Context()
	p := f.Principal(t, "alice", allScopes)
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	a, err := s.AddArticle(ctx, p, feed.AddInput{IdempotencyKey: "compact", Article: feed.Content{URL: "https://example.com/compact", Title: "Compact", Summary: "Saved source introduction"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/articles/" + a.Article.ID
	form := url.Values{"csrf_token": {csrf}}
	for _, target := range []string{"https://attacker.example/", "//attacker.example/", "/auth/logout", "/?cursor=a&before=b", "/archive?unknown=x"} {
		out := browserJSONRequest(h, path+"/archive?return_to="+url.QueryEscape(target), cookie, form, testfixture.Origin)
		if out.Code != 400 {
			t.Fatalf("unsafe return accepted %q: %d", target, out.Code)
		}
	}
	if got, err := s.GetArticle(ctx, p, a.Article.ID); err != nil || got.ArchivedAt != 0 || got.Revision != 1 {
		t.Fatalf("invalid return mutated: %#v %v", got, err)
	}
	archive := browserJSONRequest(h, path+"/archive?return_to="+url.QueryEscape("/?cursor=kept-position"), cookie, form, testfixture.Origin)
	if got := readBrowserJSON[struct {
		Location string `json:"location"`
	}](t, archive).Location; got != "/?cursor=kept-position" {
		t.Fatalf("archive returned %q", got)
	}
	trashForm := url.Values{"csrf_token": {csrf}, "expected_revision": {"2"}, "idempotency_key": {"compact-trash"}}
	trash := browserJSONRequest(h, path+"/trash?return_to=%2Farchive", cookie, trashForm, testfixture.Origin)
	if got := readBrowserJSON[struct {
		Location string `json:"location"`
	}](t, trash).Location; got != "/archive" {
		t.Fatalf("trash returned %q", got)
	}
	trashForm.Set("expected_revision", "3")
	trashForm.Set("idempotency_key", "compact-restore")
	restored := browserJSONRequest(h, path+"/restore-trash?return_to=%2Ftrash", cookie, trashForm, testfixture.Origin)
	if got := readBrowserJSON[struct {
		Location string `json:"location"`
	}](t, restored).Location; got != "/trash" {
		t.Fatalf("restore returned %q", got)
	}
	if got, err := s.GetArticle(ctx, p, a.Article.ID); err != nil || got.ArchivedAt == 0 || got.TrashedAt != 0 {
		t.Fatalf("restore lost original shelf: %#v %v", got, err)
	}
}
