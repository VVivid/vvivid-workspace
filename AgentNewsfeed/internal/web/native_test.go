package web_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/web"
)

const nativeClient = "newsfeed-android"

// nativeApp composes the handler with one native app client configured, as
// NEWSFEED_NATIVE_CLIENT_IDS does in production. The fixture's own manager stays
// unchanged for sign-in and test principals. Saved thumbnails are stored JPEGs;
// inline Markdown images are fetched through inline.
func nativeApp(t *testing.T, inline *recordingImageFetcher) (*testfixture.Identity, *feed.Service, http.Handler) {
	t.Helper()
	f := testfixture.New(t)
	f.Enroll(t, "alice")
	f.Enroll(t, "bob")
	settings := f.Environment()
	settings["NEWSFEED_NATIVE_CLIENT_IDS"] = nativeClient
	cfg, err := identity.LoadConfig(func(name string) (string, bool) { value, present := settings[name]; return value, present })
	if err != nil {
		t.Fatal(err)
	}
	auth, err := identity.New(t.Context(), *cfg, f.Store)
	if err != nil {
		t.Fatal(err)
	}
	var source bytes.Buffer
	if err := jpeg.Encode(&source, image.NewRGBA(image.Rect(0, 0, 80, 80)), &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	policy, _ := thumbnail.NewPolicy("")
	service := feed.NewWithThumbnailFetcher(f.Store, policy, &storedImageFetcher{body: source.Bytes()})
	handler, err := web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), auth, service, inline)
	if err != nil {
		t.Fatal(err)
	}
	return f, service, handler
}

func TestNativeAppBearerReadsOwnedArticlesAndImagesOnly(t *testing.T) {
	inline := &recordingImageFetcher{}
	f, s, h := nativeApp(t, inline)
	figure := "https://images.example.com/figure.png"
	added, err := s.AddArticle(t.Context(), f.Principal(t, "alice", allScopes), feed.AddInput{IdempotencyKey: "native-read", Article: feed.Content{
		URL: "https://example.com/native-story", Title: "Native story", Summary: "A sourced introduction.",
		ThumbnailURL: "https://images.example.com/card.png", ContentMarkdown: "# Source\n\n![Figure](" + figure + ")",
	}})
	if err != nil || added.Article.ThumbnailStatus != "stored" {
		t.Fatalf("saved article: %v", err)
	}
	id := added.Article.ID
	figureID := sha256.Sum256([]byte(figure))
	list, detail := "/api/v1/articles", "/api/v1/articles/"+id
	images := []string{"/thumbnails/" + id, "/thumbnails/" + id + "/content/" + hex.EncodeToString(figureID[:])}
	articleReads := append([]string{detail}, images...)
	reads := append([]string{list}, articleReads...)

	alice := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.Read)
	var page feed.Page
	response := request(h, http.MethodGet, list, nil, alice)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Articles) != 1 || page.Articles[0].ID != id || page.Articles[0].ContentMarkdown != "" {
		t.Fatalf("native list: HTTP %d", response.Code)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("native reads must stay private and grant no CORS access")
	}
	var article sqlite.Article
	if response := request(h, http.MethodGet, detail, nil, alice); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &article) != nil || !reflect.DeepEqual(article, added.Article) {
		t.Fatalf("native detail: HTTP %d", response.Code)
	}
	for _, path := range images {
		if response := request(h, http.MethodGet, path, nil, alice); response.Code != 200 || response.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("native image %s: HTTP %d", path, response.Code)
		}
	}
	if len(inline.urls) != 1 || inline.urls[0] != figure {
		t.Fatalf("inline image fetches: %v", inline.urls)
	}

	bob := f.Bearer("bob", nativeClient, f.Manager.APIAudience(), identity.Read)
	var bobPage feed.Page
	if response := request(h, http.MethodGet, list, nil, bob); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &bobPage) != nil || len(bobPage.Articles) != 0 {
		t.Fatal("native list exposed another owner's article")
	}
	for _, path := range articleReads {
		if response := request(h, http.MethodGet, path, nil, bob); response.Code != 404 {
			t.Fatalf("native read of another owner's %s: HTTP %d", path, response.Code)
		}
	}

	challenge := `Bearer resource_metadata="` + testfixture.Origin + `/.well-known/oauth-protected-resource/api/v1", scope="` + identity.Read + `"`
	for name, token := range map[string]string{
		"browser client token": f.Bearer("alice", "newsfeed-web", f.Manager.APIAudience(), identity.Read),
		"MCP client token":     f.Bearer("alice", "newsfeed-codex", f.Manager.APIAudience(), identity.Read),
		"unknown client":       f.Bearer("alice", "newsfeed-desktop", f.Manager.APIAudience(), identity.Read),
		"MCP audience":         f.Bearer("alice", nativeClient, f.Manager.MCPAudience(), identity.Read),
		"unenrolled subject":   f.Bearer("mallory", nativeClient, f.Manager.APIAudience(), identity.Read),
		"not a JWT":            "opaque-credential",
	} {
		for _, path := range reads {
			if response := request(h, http.MethodGet, path, nil, token); response.Code != 401 || response.Header().Get("WWW-Authenticate") != challenge {
				t.Fatalf("%s at %s: HTTP %d challenge %q", name, path, response.Code, response.Header().Get("WWW-Authenticate"))
			}
		}
	}
	writeOnly := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.ArticlesWrite)
	for _, path := range reads {
		if response := request(h, http.MethodGet, path, nil, writeOnly); response.Code != 403 || response.Header().Get("WWW-Authenticate") != challenge+`, error="insufficient_scope"` {
			t.Fatalf("write-only native token at %s: HTTP %d", path, response.Code)
		}
	}
	var metadata struct {
		Resource string `json:"resource"`
	}
	if response := request(h, http.MethodGet, "/.well-known/oauth-protected-resource/api/v1", nil, ""); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &metadata) != nil || metadata.Resource != f.Manager.APIAudience() {
		t.Fatal("challenge metadata does not describe the API resource")
	}

	// The browser session keeps working, but never rescues a rejected bearer token.
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	browserToken := f.Bearer("alice", "newsfeed-web", f.Manager.APIAudience(), identity.Read)
	for _, path := range reads {
		if response := request(h, http.MethodGet, path, cookie, ""); response.Code != 200 {
			t.Fatalf("browser read %s: HTTP %d", path, response.Code)
		}
		fetched := len(inline.urls)
		if response := request(h, http.MethodGet, path, cookie, browserToken); response.Code != 401 || len(inline.urls) != fetched {
			t.Fatalf("rejected bearer at %s fell back to the session: HTTP %d", path, response.Code)
		}
	}

	// To-do and settings writes stay browser-only; native apps change only article state.
	writer := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	for _, path := range []string{"/api/v1/todos", "/api/v1/settings/preferences", "/api/v1/settings/suggestions/accept", "/api/v1/settings/suggestions/dismiss"} {
		if response := request(h, http.MethodPost, path, nil, writer); response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("native token reached browser-only write %s: HTTP %d", path, response.Code)
		}
	}
	_, csrf := f.BrowserPrincipal(t, cookie)
	if response := postArticleForm(h, detail+"/archive", cookie, csrf, testfixture.Origin); response.Code != 303 {
		t.Fatalf("browser archive form beside native reads: HTTP %d", response.Code)
	}
}

func TestNativeAppBearerIsIgnoredWithoutConfiguredNativeClients(t *testing.T) {
	f, s, h := app(t)
	added, err := s.AddArticle(t.Context(), f.Principal(t, "alice", allScopes), feed.AddInput{IdempotencyKey: "browser-only", Article: feed.Content{
		URL: "https://example.com/browser-only", Title: "Browser only", Summary: "A sourced introduction.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := added.Article.ID
	token := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.Read)
	for _, path := range []string{"/api/v1/articles", "/api/v1/articles/" + id, "/thumbnails/" + id, "/thumbnails/" + id + "/content/unknown"} {
		// Default routes stay browser-only: the token is ignored and sign-in is required.
		if response := request(h, http.MethodGet, path, nil, token); response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("native token at %s: HTTP %d", path, response.Code)
		}
	}
	if response := request(h, http.MethodGet, "/api/v1/articles", f.Cookie(t, "alice"), token); response.Code != 200 {
		t.Fatalf("browser-only route stopped ignoring Authorization: HTTP %d", response.Code)
	}

	// Article state changes stay browser forms, which ignore Authorization too.
	path := "/api/v1/articles/" + id
	writer := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	for i, action := range articleStateActions {
		response := post(h, path+action, nativeAppHeaders(writer), stateBody(1, fmt.Sprintf("browser-only-%d", i)), nil)
		if response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("native token at %s: HTTP %d", action, response.Code)
		}
	}
	if current, err := s.GetArticle(t.Context(), f.Principal(t, "alice", allScopes), id); err != nil || !reflect.DeepEqual(current, added.Article) {
		t.Fatal("native write attempt changed the article")
	}
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	form := http.Header{"Authorization": {"Bearer " + writer}, "Content-Type": {"application/x-www-form-urlencoded"}, "Origin": {testfixture.Origin}}
	if response := post(h, path+"/archive", form, url.Values{"csrf_token": {csrf}}.Encode(), cookie); response.Code != 303 {
		t.Fatalf("browser archive form stopped ignoring Authorization: HTTP %d", response.Code)
	}
}

// articleStateActions are the article state routes native apps share with browser forms.
var articleStateActions = []string{"/archive", "/restore", "/trash", "/restore-trash"}

// post sends a POST with exactly these headers, body and optional cookie.
func post(h http.Handler, path string, header http.Header, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, testfixture.Origin+path, strings.NewReader(body))
	maps.Copy(r.Header, header)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}

// nativeAppHeaders are the request headers of a native app's JSON state change:
// its bearer token and the JSON content type. stateBody supplies the body.
func nativeAppHeaders(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}}
}

// stateBody is a native app's JSON state change body. Go's %q quoting equals
// JSON string quoting only for the plain ASCII keys these tests use.
func stateBody(revision int64, key string) string {
	return fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":%q}`, revision, key)
}

// changeState applies one valid native app state change and returns the
// article from its JSON success response.
func changeState(t *testing.T, h http.Handler, path, token string, revision int64, key string) sqlite.Article {
	t.Helper()
	response := post(h, path, nativeAppHeaders(token), stateBody(revision, key), nil)
	var article sqlite.Article
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Location") != "" ||
		response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &article) != nil {
		t.Fatalf("native state change %s: HTTP %d %s", path, response.Code, response.Body)
	}
	return article
}

// requireNativeAppError checks a JSON error answer: exactly {"error": message}.
func requireNativeAppError(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	var body map[string]string
	if response.Code != status || response.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body) != 1 || body["error"] == "" {
		t.Fatalf("native app error: HTTP %d, want %d: %s", response.Code, status, response.Body)
	}
}

func TestNativeAppStateChangesMatchMCPSemantics(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	alice := f.Principal(t, "alice", allScopes)
	added, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "native-state", Article: feed.Content{
		URL: "https://example.com/native-state", Title: "Native state", Summary: "A sourced introduction.", ContentMarkdown: "# Source\n\nOriginal text.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := added.Article.ID
	path := "/api/v1/articles/" + id
	writer := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	reader := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), identity.Read)
	readDetail := func(route string) sqlite.Article {
		t.Helper()
		var article sqlite.Article
		if response := request(h, http.MethodGet, route, nil, reader); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &article) != nil {
			t.Fatalf("native detail %s: HTTP %d", route, response.Code)
		}
		return article
	}

	// The result has the detail route's shape, including the saved body.
	archived := changeState(t, h, path+"/archive", writer, 1, "native-archive")
	if archived.ArchivedAt == 0 || archived.Revision != 2 || archived.ContentMarkdown != added.Article.ContentMarkdown || !reflect.DeepEqual(readDetail(path), archived) {
		t.Fatalf("native archive result: %+v", archived)
	}
	if again := changeState(t, h, path+"/archive", writer, 1, "native-archive"); !reflect.DeepEqual(again, archived) {
		t.Fatal("exact native retry changed the original result")
	}
	// MCP set_article_archived is the same operation, so the same key and input replay it.
	server := httptest.NewServer(h)
	defer server.Close()
	mcp := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	replayed := decode[sqlite.MutationResult](t, call(t, mcp, "set_article_archived", feed.ArchiveInput{IdempotencyKey: "native-archive", ArticleID: id, ExpectedRevision: 1, Archived: true}))
	if !reflect.DeepEqual(replayed.Article, archived) {
		t.Fatal("MCP did not replay the native archive result")
	}

	// A stale revision conflicts without reserving its key, and a used key cannot
	// carry a new intent.
	requireNativeAppError(t, post(h, path+"/restore", nativeAppHeaders(writer), stateBody(1, "native-restore"), nil), 409)
	requireNativeAppError(t, post(h, path+"/restore", nativeAppHeaders(writer), stateBody(2, "native-archive"), nil), 409)
	if !reflect.DeepEqual(readDetail(path), archived) {
		t.Fatal("rejected native restore changed the article")
	}
	restored := changeState(t, h, path+"/restore", writer, 2, "native-restore")
	if restored.ArchivedAt != 0 || restored.Revision != 3 {
		t.Fatalf("native restore result: %+v", restored)
	}
	if unchanged := changeState(t, h, path+"/restore", writer, 3, "native-restore-again"); !reflect.DeepEqual(unchanged, restored) {
		t.Fatal("setting the current archive state was not a no-op")
	}

	// Trash keeps the archive state, and no archive action edits Trash.
	rearchived := changeState(t, h, path+"/archive", writer, 3, "native-rearchive")
	trashed := changeState(t, h, path+"/trash", writer, 4, "native-trash")
	if trashed.TrashedAt == 0 || trashed.ArchivedAt != rearchived.ArchivedAt || trashed.Revision != 5 || !reflect.DeepEqual(readDetail(path+"?trashed=true"), trashed) {
		t.Fatalf("native Trash result: %+v", trashed)
	}
	for i, action := range []string{"/archive", "/restore", "/trash"} {
		requireNativeAppError(t, post(h, path+action, nativeAppHeaders(writer), stateBody(5, fmt.Sprintf("native-edit-trash-%d", i)), nil), 404)
	}
	untrashed := changeState(t, h, path+"/restore-trash", writer, 5, "native-restore-trash")
	if untrashed.TrashedAt != 0 || untrashed.ArchivedAt != rearchived.ArchivedAt || untrashed.Revision != 6 || !reflect.DeepEqual(readDetail(path), untrashed) {
		t.Fatalf("native restore-trash lost the original archive state: %+v", untrashed)
	}
	if unchanged := changeState(t, h, path+"/restore-trash", writer, 6, "native-restore-trash-again"); !reflect.DeepEqual(unchanged, untrashed) {
		t.Fatal("restoring an article outside Trash was not a no-op")
	}
	// An exact retry still returns its original result after later changes, without reapplying it.
	if again := changeState(t, h, path+"/trash", writer, 4, "native-trash"); !reflect.DeepEqual(again, trashed) {
		t.Fatal("exact native Trash retry changed the original result")
	}
	if current, err := s.GetArticle(t.Context(), alice, id); err != nil || !reflect.DeepEqual(current, untrashed) {
		t.Fatal("exact native Trash retry applied the old transition again")
	}
}

func TestNativeAppStateChangesRequireBothScopesOwnerAndOnlyTheBearerToken(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	alice := f.Principal(t, "alice", allScopes)
	added, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "native-guarded", Article: feed.Content{
		URL: "https://example.com/native-guarded", Title: "Guarded", Summary: "A sourced introduction.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/articles/" + added.Article.ID
	api := f.Manager.APIAudience()
	challenge := `Bearer resource_metadata="` + testfixture.Origin + `/.well-known/oauth-protected-resource/api/v1", scope="` + allScopes + `"`
	for name, tc := range map[string]struct {
		token     string
		status    int
		challenge string
	}{
		"read-only token":      {f.Bearer("alice", nativeClient, api, identity.Read), 403, challenge + `, error="insufficient_scope"`},
		"write-only token":     {f.Bearer("alice", nativeClient, api, identity.ArticlesWrite), 403, challenge + `, error="insufficient_scope"`},
		"browser client token": {f.Bearer("alice", "newsfeed-web", api, allScopes), 401, challenge},
		"MCP client token":     {f.Bearer("alice", "newsfeed-codex", api, allScopes), 401, challenge},
		"MCP audience":         {f.Bearer("alice", nativeClient, f.Manager.MCPAudience(), allScopes), 401, challenge},
		"unenrolled subject":   {f.Bearer("mallory", nativeClient, api, allScopes), 401, challenge},
	} {
		for i, action := range articleStateActions {
			response := post(h, path+action, nativeAppHeaders(tc.token), stateBody(1, fmt.Sprintf("guarded-%d", i)), nil)
			if response.Code != tc.status || response.Header().Get("WWW-Authenticate") != tc.challenge {
				t.Fatalf("%s at %s: HTTP %d challenge %q", name, action, response.Code, response.Header().Get("WWW-Authenticate"))
			}
		}
	}

	// Another owner's article answers exactly like an unknown one.
	bob := f.Bearer("bob", nativeClient, api, allScopes)
	for i, action := range articleStateActions {
		requireNativeAppError(t, post(h, path+action, nativeAppHeaders(bob), stateBody(1, fmt.Sprintf("foreign-%d", i)), nil), 404)
		requireNativeAppError(t, post(h, "/api/v1/articles/unknown"+action, nativeAppHeaders(bob), stateBody(1, fmt.Sprintf("unknown-%d", i)), nil), 404)
	}

	// The Authorization header alone decides: a rejected header never falls back to
	// a valid session, CSRF token and Origin sent with it. Each body is a form the
	// route's browser handler would accept: archive and restore forms carry only
	// csrf_token, and Trash forms add a revision and key.
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	trashForm := url.Values{"csrf_token": {csrf}, "expected_revision": {"1"}, "idempotency_key": {"fallback"}}
	for _, authorization := range []string{"Bearer " + f.Bearer("alice", "newsfeed-web", api, allScopes), "Bearer", "", "Basic " + csrf} {
		header := http.Header{"Authorization": {authorization}, "Content-Type": {"application/x-www-form-urlencoded"}, "Origin": {testfixture.Origin}}
		for _, action := range articleStateActions {
			body := trashForm
			if action == "/archive" || action == "/restore" {
				body = url.Values{"csrf_token": {csrf}}
			}
			if response := post(h, path+action, header, body.Encode(), cookie); response.Code != 401 || response.Header().Get("WWW-Authenticate") != challenge {
				t.Fatalf("Authorization %q at %s fell back to the session: HTTP %d", authorization, action, response.Code)
			}
		}
	}
	if current, err := s.GetArticle(t.Context(), alice, added.Article.ID); err != nil || !reflect.DeepEqual(current, added.Article) {
		t.Fatal("rejected native state changes changed the article")
	}
}

// The Android app asks Keycloak for offline_access to stay signed in, so its
// access tokens list that scope. It grants nothing: native app and browser
// grants carrying it work exactly as their newsfeed scopes allow, and any other
// unknown scope is still refused.
func TestOfflineAccessScopeGrantsNothingToNativeAppsOrBrowsers(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	alice := f.Principal(t, "alice", allScopes)
	added, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "offline-access", Article: feed.Content{
		URL: "https://example.com/offline-access", Title: "Offline access", Summary: "A sourced introduction.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := added.Article.ID
	list, path := "/api/v1/articles", "/api/v1/articles/"+id
	api := f.Manager.APIAudience()

	// The Android app's grant reads and changes article state.
	android := f.Bearer("alice", nativeClient, api, "openid offline_access "+allScopes)
	var page feed.Page
	if response := request(h, http.MethodGet, list, nil, android); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Articles) != 1 || page.Articles[0].ID != id {
		t.Fatalf("native list with offline_access: HTTP %d", response.Code)
	}
	if response := request(h, http.MethodGet, path, nil, android); response.Code != 200 {
		t.Fatalf("native detail with offline_access: HTTP %d", response.Code)
	}
	archived := changeState(t, h, path+"/archive", android, 1, "offline-access-archive")
	if archived.ArchivedAt == 0 || archived.Revision != 2 {
		t.Fatalf("native archive with offline_access: %+v", archived)
	}

	// offline_access adds no permission, and other unknown scopes are refused.
	resource := `Bearer resource_metadata="` + testfixture.Origin + `/.well-known/oauth-protected-resource/api/v1"`
	readChallenge := resource + `, scope="` + identity.Read + `", error="insufficient_scope"`
	stateChallenge := resource + `, scope="` + allScopes + `", error="insufficient_scope"`
	readOnly := f.Bearer("alice", nativeClient, api, "openid offline_access "+identity.Read)
	if response := post(h, path+"/restore", nativeAppHeaders(readOnly), stateBody(2, "offline-access-read-only"), nil); response.Code != 403 || response.Header().Get("WWW-Authenticate") != stateChallenge {
		t.Fatalf("native restore with a read-only offline_access grant: HTTP %d challenge %q", response.Code, response.Header().Get("WWW-Authenticate"))
	}
	for i, unknown := range []string{"newsfeed:admin", "offline"} {
		token := f.Bearer("alice", nativeClient, api, "openid offline_access "+allScopes+" "+unknown)
		if response := request(h, http.MethodGet, list, nil, token); response.Code != 403 || response.Header().Get("WWW-Authenticate") != readChallenge {
			t.Fatalf("native read with %s: HTTP %d challenge %q", unknown, response.Code, response.Header().Get("WWW-Authenticate"))
		}
		if response := post(h, path+"/restore", nativeAppHeaders(token), stateBody(2, fmt.Sprintf("offline-access-unknown-%d", i)), nil); response.Code != 403 || response.Header().Get("WWW-Authenticate") != stateChallenge {
			t.Fatalf("native restore with %s: HTTP %d challenge %q", unknown, response.Code, response.Header().Get("WWW-Authenticate"))
		}
	}
	if current, err := s.GetArticle(t.Context(), alice, id); err != nil || !reflect.DeepEqual(current, archived) {
		t.Fatal("refused grants changed the article")
	}

	// A browser grant carrying offline_access signs in, reads and restores alike.
	cookie := f.CookieWithScopes(t, "alice", "openid offline_access "+allScopes)
	if response := request(h, http.MethodGet, path, cookie, ""); response.Code != 200 {
		t.Fatalf("browser read with offline_access: HTTP %d", response.Code)
	}
	_, csrf := f.BrowserPrincipal(t, cookie)
	if response := postArticleForm(h, path+"/restore", cookie, csrf, testfixture.Origin); response.Code != 303 {
		t.Fatalf("browser restore with offline_access: HTTP %d", response.Code)
	}
	if current, err := s.GetArticle(t.Context(), alice, id); err != nil || current.ArchivedAt != 0 || current.Revision != 3 {
		t.Fatalf("browser restore with offline_access: %+v", current)
	}
}

func TestNativeAppStateChangesRejectInvalidRequestsWithoutReservingKeys(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	alice := f.Principal(t, "alice", allScopes)
	added, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "native-input-add", Article: feed.Content{
		URL: "https://example.com/native-input", Title: "Input", Summary: "A sourced introduction.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/articles/" + added.Article.ID
	writer := f.Bearer("alice", nativeClient, f.Manager.APIAudience(), allScopes)
	valid := stateBody(1, "native-input")
	for name, tc := range map[string]struct{ contentType, body string }{
		"unknown field":        {"application/json", `{"expected_revision":1,"idempotency_key":"native-input","article_id":"other"}`},
		"duplicate field":      {"application/json", `{"expected_revision":1,"expected_revision":2,"idempotency_key":"native-input"}`},
		"escaped duplicate":    {"application/json", `{"expected_revision":1,"idempotency_key":"native-input","idempotency_\u006bey":"other"}`},
		"case-folded field":    {"application/json", `{"Expected_Revision":1,"idempotency_key":"native-input"}`},
		"missing revision":     {"application/json", `{"idempotency_key":"native-input"}`},
		"missing key":          {"application/json", `{"expected_revision":1}`},
		"null revision":        {"application/json", `{"expected_revision":null,"idempotency_key":"native-input"}`},
		"string revision":      {"application/json", `{"expected_revision":"1","idempotency_key":"native-input"}`},
		"fractional revision":  {"application/json", `{"expected_revision":1.5,"idempotency_key":"native-input"}`},
		"zero revision":        {"application/json", stateBody(0, "native-input")},
		"number key":           {"application/json", `{"expected_revision":1,"idempotency_key":7}`},
		"empty key":            {"application/json", stateBody(1, "")},
		"padded key":           {"application/json", stateBody(1, " native-input")},
		"long key":             {"application/json", stateBody(1, strings.Repeat("k", 129))},
		"invalid UTF-8":        {"application/json", "{\"expected_revision\":1,\"idempotency_key\":\"native-\xff\"}"},
		"array":                {"application/json", `[]`},
		"null":                 {"application/json", `null`},
		"empty body":           {"application/json", ``},
		"trailing value":       {"application/json", valid + ` {}`},
		"form body":            {"application/json", `expected_revision=1&idempotency_key=native-input`},
		"oversized":            {"application/json", `{"expected_revision":1,` + strings.Repeat(" ", 4096) + `"idempotency_key":"native-input"}`},
		"form content type":    {"application/x-www-form-urlencoded", valid},
		"text content type":    {"text/plain", valid},
		"JSON-based type":      {"application/merge-patch+json", valid},
		"missing content type": {"", valid},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{"Authorization": {"Bearer " + writer}}
			if tc.contentType != "" {
				header.Set("Content-Type", tc.contentType)
			}
			for _, action := range articleStateActions {
				requireNativeAppError(t, post(h, path+action, header, tc.body, nil), 400)
			}
		})
	}
	requireNativeAppError(t, post(h, "/api/v1/articles/"+strings.Repeat("a", 65)+"/archive", nativeAppHeaders(writer), valid, nil), 400)
	if current, err := s.GetArticle(t.Context(), alice, added.Article.ID); err != nil || !reflect.DeepEqual(current, added.Article) {
		t.Fatal("rejected native state change changed the article")
	}
	// Rejected requests reserve no key, and a media type parameter is accepted.
	header := nativeAppHeaders(writer)
	header.Set("Content-Type", "application/json; charset=utf-8")
	var archived sqlite.Article
	if response := post(h, path+"/archive", header, valid, nil); response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &archived) != nil || archived.ArchivedAt == 0 {
		t.Fatalf("valid native archive after rejected requests: HTTP %d", response.Code)
	}
}

func TestBrowserStateFormsAreUnchangedBesideNativeApps(t *testing.T) {
	f, s, h := nativeApp(t, &recordingImageFetcher{})
	alice := f.Principal(t, "alice", allScopes)
	added, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "browser-state", Article: feed.Content{
		URL: "https://example.com/browser-state", Title: "Browser state", Summary: "A sourced introduction.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := added.Article.ID
	path := "/api/v1/articles/" + id
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)

	// Cookie requests still need their CSRF token and exact Origin, and cannot
	// use the native app's JSON body instead of a form.
	jsonHeader := http.Header{"Content-Type": {"application/json"}, "Origin": {testfixture.Origin}, "X-Csrf-Token": {csrf}}
	for name, send := range map[string]func() *httptest.ResponseRecorder{
		"missing CSRF token": func() *httptest.ResponseRecorder {
			return postArticleForm(h, path+"/archive", cookie, "", testfixture.Origin)
		},
		"missing Origin": func() *httptest.ResponseRecorder { return postArticleForm(h, path+"/archive", cookie, csrf, "") },
		"wrong CSRF token": func() *httptest.ResponseRecorder {
			return stateForm(h, path+"/trash", cookie, "wrong", "browser-bad-csrf", 1, testfixture.Origin)
		},
		"JSON archive": func() *httptest.ResponseRecorder {
			return post(h, path+"/archive", jsonHeader, stateBody(1, "browser-json"), cookie)
		},
		"JSON trash": func() *httptest.ResponseRecorder {
			return post(h, path+"/trash", jsonHeader, stateBody(1, "browser-json"), cookie)
		},
	} {
		if response := send(); response.Code != 403 {
			t.Fatalf("%s: HTTP %d", name, response.Code)
		}
	}
	if current, err := s.GetArticle(t.Context(), alice, id); err != nil || !reflect.DeepEqual(current, added.Article) {
		t.Fatal("unverified browser state change changed the article")
	}

	// Verified forms keep their redirects to the matching reader.
	redirectsTo := func(response *httptest.ResponseRecorder, location string) {
		t.Helper()
		if response.Code != 303 || response.Header().Get("Location") != location {
			t.Fatalf("browser form: HTTP %d to %q, want %q", response.Code, response.Header().Get("Location"), location)
		}
	}
	redirectsTo(postArticleForm(h, path+"/archive", cookie, csrf, testfixture.Origin), "/articles/"+id+"?from=archive")
	redirectsTo(postArticleForm(h, path+"/restore", cookie, csrf, testfixture.Origin), "/articles/"+id+"?from=feed")
	redirectsTo(stateForm(h, path+"/trash", cookie, csrf, "browser-trash", 3, testfixture.Origin), "/articles/"+id+"?from=trash")
	redirectsTo(stateForm(h, path+"/restore-trash", cookie, csrf, "browser-restore", 4, testfixture.Origin), "/articles/"+id+"?from=feed")
	if current, err := s.GetArticle(t.Context(), alice, id); err != nil || current.Revision != 5 || current.ArchivedAt != 0 {
		t.Fatalf("browser forms beside native apps: %+v", current)
	}
}
