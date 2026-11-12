package httpapp_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/httpapp"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/identity"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/mcptools"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/testoidc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixture struct {
	s        *store.Store
	auth     *identity.Manager
	provider *testoidc.Issuer
	server   *httptest.Server
	client   *http.Client
	origin   string
}

func newFixture(t *testing.T, browser bool) *fixture {
	t.Helper()
	p := testoidc.New(t)
	s, err := store.Open(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.Enroll(t.Context(), p.URL(), "test-subject", "Synthetic owner"); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewUnstartedServer(nil)
	if browser {
		if _, err = s.Enroll(t.Context(), p.URL(), "other-test-subject", "Other synthetic owner"); err != nil {
			t.Fatal(err)
		}
		backend.Listener.Close()
		backend.Listener, err = net.Listen("tcp", "127.0.0.1:8099")
		if err != nil {
			t.Fatal(err)
		}
	}
	origin := "http://" + backend.Listener.Addr().String()
	if browser {
		origin = "https://" + backend.Listener.Addr().String()
	}
	raw, _ := json.Marshal(map[string]any{"active": "test", "keys": map[string]string{"test": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))}})
	keys, err := identity.ParseKeys(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := identity.Config{Origins: []string{origin}, Issuer: p.URL(), WebClientID: "vocab-web", WebClientSecret: "synthetic-secret", APIAudience: origin + "/api/v1", MCPMode: "direct", MCPResource: origin + "/mcp", MCPAudience: origin + "/mcp", MCPClients: []string{"vocab-test"}, Keys: keys}
	auth, err := identity.New(t.Context(), cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	if browser {
		handler = httpapp.New(s, auth, mcptools.New(s), httpapp.StaticDir("../../web/out"))
	} else {
		handler = httpapp.New(s, auth, mcptools.New(s), fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>VocabBank</title>")}})
	}
	if browser {
		testMux := http.NewServeMux()
		testMux.Handle("GET /__test__/agent-token", auth.Browser(store.Read, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": p.Access("test-subject", "vocab-test", origin+"/mcp", store.Read+" "+store.Write, 300)})
		})))
		testMux.Handle("/", handler)
		handler = auth.RestrictOrigin(testMux)
	}
	backend.Config.Handler = handler
	if browser {
		backend.StartTLS()
	} else {
		backend.Start()
	}
	t.Cleanup(backend.Close)
	client := backend.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &fixture{s: s, auth: auth, provider: p, server: backend, client: client, origin: origin}
}
func (f *fixture) request(t *testing.T, method, path string, body io.Reader, content string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.origin+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if content != "" {
		req.Header.Set("Content-Type", content)
	}
	if method != "GET" {
		req.Header.Set("Origin", f.origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}
func (f *fixture) login(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	res := f.request(t, "GET", "/auth/login", nil, "", nil, "")
	u, _ := url.Parse(res.Header.Get("Location"))
	f.provider.Prepare(u.Query())
	var login *http.Cookie
	for _, c := range res.Cookies() {
		if strings.Contains(c.Name, "login") {
			login = c
		}
	}
	res = f.request(t, "GET", "/auth/callback?code=synthetic-code&state="+u.Query().Get("state")+"&iss="+url.QueryEscape(f.provider.URL()), nil, "", login, "")
	if res.StatusCode != 303 {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("callback %d %s", res.StatusCode, raw)
	}
	var session *http.Cookie
	for _, c := range res.Cookies() {
		if strings.Contains(c.Name, "session") && c.Value != "" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("session missing")
	}
	res = f.request(t, "GET", "/api/v1/session", nil, "", session, "")
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil || out.CSRF == "" {
		t.Fatal("CSRF missing")
	}
	return session, out.CSRF
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(copy)
}
func decodeResponse[T any](t *testing.T, response *http.Response, status int) T {
	t.Helper()
	defer response.Body.Close()
	var result T
	if response.StatusCode != status {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP status=%d want %d: %s", response.StatusCode, status, raw)
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
func jsonInput(value any) io.Reader { raw, _ := json.Marshal(value); return bytes.NewReader(raw) }
func sdkSession(t *testing.T, f *fixture, subject, scope string) *sdk.ClientSession {
	t.Helper()
	token := f.provider.Access(subject, "vocab-test", f.origin+"/mcp", scope, 300)
	client := sdk.NewClient(&sdk.Implementation{Name: "vocabbank-integration", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: f.origin + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
func callTool[T any](t *testing.T, session *sdk.ClientSession, name string, input any) T {
	t.Helper()
	result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: input})
	if err != nil || result.IsError {
		t.Fatalf("tool %s=%+v %v", name, result, err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func syntheticSenses() []store.Sense {
	return []store.Sense{
		{PartOfSpeech: "adjective", Definition: "Able to express ideas clearly.", WhenToUse: "Describe a clear speaker or explanation.", Examples: []store.Example{{Context: "A meeting", Sentence: "She gave an articulate explanation of the plan."}, {Context: "A presentation", Sentence: "The articulate speaker answered our questions."}}, CommonPhrases: []string{"an articulate speaker"}, UsageTip: "This describes clear expression."},
		{PartOfSpeech: "verb", Definition: "To express an idea clearly in words.", WhenToUse: "Describe putting a thought or feeling into words.", Examples: []store.Example{{Context: "Sharing a concern", Sentence: "Can you articulate your concern?"}, {Context: "Explaining an idea", Sentence: "He took a moment to articulate the idea."}}, CommonPhrases: []string{"articulate an idea"}},
	}
}
func TestAuthenticatedAPIAndOfficialMCPSharePersistence(t *testing.T) {
	f := newFixture(t, false)
	unauth := f.request(t, "GET", "/api/v1/words", nil, "", nil, "")
	if unauth.StatusCode != 401 {
		t.Fatal("anonymous HTTP read")
	}
	cookie, csrf := f.login(t)
	in := store.AddInput{IdempotencyKey: "http-create-001", Capture: store.Capture{Term: "articulate", OriginalSentence: "My captured sentence.", SourceURL: "https://example.test/source", PersonalNote: "My personal note."}}
	word := decodeResponse[store.Word](t, f.request(t, "POST", "/api/v1/words", jsonInput(in), "application/json", cookie, csrf), 200)
	if word.Status != store.NeedsDetails || word.Revision != 1 {
		t.Fatal("bare word not saved")
	}
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	list := callTool[store.Page](t, session, "list_words", store.Filter{Status: store.NeedsDetails})
	if list.Total != 1 || list.Items[0].ID != word.ID {
		t.Fatal("SDK cannot see HTTP draft")
	}
	completion := store.CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "sdk-complete-001", Senses: syntheticSenses()}
	filled := callTool[store.Word](t, session, "complete_word", completion)
	if filled.PersonalNote != in.PersonalNote || filled.OriginalSentence != in.OriginalSentence || filled.SourceURL != in.SourceURL {
		t.Fatal("MCP completion changed captures")
	}
	reread := decodeResponse[store.Word](t, f.request(t, "GET", "/api/v1/words/"+word.ID, nil, "", cookie, ""), 200)
	if reread.Status != store.Complete || len(reread.Senses) != 2 || reread.Revision != 2 {
		t.Fatal("HTTP cannot see SDK completion")
	}
	stale := store.UpdateInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "stale-http-001", Capture: in.Capture}
	failure := decodeResponse[map[string]string](t, f.request(t, "PUT", "/api/v1/words/"+word.ID, jsonInput(stale), "application/json", cookie, csrf), 409)
	if failure["code"] != "stale_revision" {
		t.Fatal(failure)
	}
	created := callTool[store.Word](t, session, "add_word", store.AddInput{IdempotencyKey: "sdk-create-001", Capture: store.Capture{Term: "synthetic expression"}})
	viaHTTP := decodeResponse[store.Word](t, f.request(t, "GET", "/api/v1/words/"+created.ID, nil, "", cookie, ""), 200)
	if viaHTTP.Term != created.Term {
		t.Fatal("SDK add missing in browser")
	}
	edited := callTool[store.Word](t, session, "update_word", store.UpdateInput{ID: word.ID, ExpectedRevision: 2, IdempotencyKey: "sdk-update-001", Capture: store.Capture{Term: word.Term, OriginalSentence: word.OriginalSentence, SourceURL: word.SourceURL, PersonalNote: "User-directed note update."}})
	replay := callTool[store.Word](t, session, "complete_word", completion)
	if replay.Revision != 2 {
		t.Fatal("SDK retry is not exact snapshot")
	}
	latest := callTool[store.Word](t, session, "get_word", map[string]any{"id": word.ID})
	if latest.Revision != edited.Revision || latest.PersonalNote != edited.PersonalNote {
		t.Fatal("retry overwrote later edit")
	}
	changed := completion
	changed.Senses = syntheticSenses()
	changed.Senses[0].Definition = "Changed input."
	denied, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "complete_word", Arguments: changed})
	if err != nil || !denied.IsError {
		t.Fatal("changed retry accepted")
	}
	bad := completion
	bad.ExpectedRevision = latest.Revision
	bad.IdempotencyKey = "bad-completion-001"
	bad.Senses = syntheticSenses()
	bad.Senses[0].Examples = bad.Senses[0].Examples[:1]
	denied, err = session.CallTool(t.Context(), &sdk.CallToolParams{Name: "complete_word", Arguments: bad})
	if err != nil || !denied.IsError {
		t.Fatal("invalid SDK completion accepted")
	}
	previous := callTool[store.Word](t, session, "get_word", map[string]any{"id": word.ID})
	if previous.Revision != latest.Revision || previous.PersonalNote != latest.PersonalNote {
		t.Fatal("failed completion changed word")
	}
}
func TestTransportAuthorizationUnknownFieldsAndLimits(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	word := decodeResponse[store.Word](t, f.request(t, "POST", "/api/v1/words", strings.NewReader(`{"term":"synthetic term","idempotency_key":"transport-add-001"}`), "application/json", cookie, csrf), 200)
	for _, token := range []string{"", "wrong-csrf"} {
		res := f.request(t, "POST", "/api/v1/words", strings.NewReader(`{"term":"denied","idempotency_key":"transport-denied"}`), "application/json", cookie, token)
		if res.StatusCode != 403 {
			t.Fatal("missing/incorrect CSRF accepted")
		}
	}
	unknown := decodeResponse[map[string]string](t, f.request(t, "POST", "/api/v1/words", strings.NewReader(`{"term":"denied","idempotency_key":"transport-denied","owner":"another-owner"}`), "application/json", cookie, csrf), 400)
	if unknown["code"] != "invalid_input" {
		t.Fatal(unknown)
	}
	for _, payload := range []string{`{"term":"denied","idempotency_key":"transport-denied"} {}`, `{"term":"` + strings.Repeat("x", (256<<10)+1) + `","idempotency_key":"transport-denied"}`} {
		res := f.request(t, "POST", "/api/v1/words", strings.NewReader(payload), "application/json", cookie, csrf)
		if res.StatusCode != 400 {
			t.Fatal("oversized/multiple JSON accepted")
		}
	}
	_, err := f.s.Enroll(t.Context(), f.provider.URL(), "other-test-subject", "Other synthetic owner")
	if err != nil {
		t.Fatal(err)
	}
	other := sdkSession(t, f, "other-test-subject", store.Read)
	for name, args := range map[string]any{"get_word": map[string]any{"id": word.ID}, "add_word": store.AddInput{IdempotencyKey: "other-add-001", Capture: store.Capture{Term: "other word"}}, "complete_word": store.CompleteInput{ID: word.ID, ExpectedRevision: 1, IdempotencyKey: "other-complete-001", Senses: syntheticSenses()}} {
		result, err := other.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || !result.IsError {
			t.Fatalf("cross owner/forbidden %s accepted", name)
		}
	}
	owned := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	badOwner, err := owned.CallTool(t.Context(), &sdk.CallToolParams{Name: "add_word", Arguments: map[string]any{"term": "bad owner", "owner": "other", "idempotency_key": "mcp-owner-001"}})
	if err != nil || !badOwner.IsError {
		t.Fatal("MCP owner input accepted")
	}
	for _, scope := range []string{"", store.Write, "unknown:scope"} {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", f.origin+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+f.provider.Access("test-subject", "vocab-test", f.origin+"/mcp", scope, 300))
		res, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatalf("forbidden MCP scope status=%d", res.StatusCode)
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", f.origin+"/mcp", strings.NewReader(`{}`))
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("anonymous MCP allowed")
	}
	wrongHost, _ := http.NewRequestWithContext(t.Context(), "GET", f.origin+"/api/v1/words", nil)
	wrongHost.Host = "evil.example.test"
	wrongHost.Header.Set("X-Forwarded-Host", strings.TrimPrefix(f.origin, "http://"))
	wrongHost.AddCookie(cookie)
	res, err = f.client.Do(wrongHost)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 421 {
		t.Fatal("forwarded host bypassed exact origin")
	}
	for _, path := range []string{"/api/v1/words?owner=other", "/api/v1/words?page=-1", "/api/v1/words?page_size=101", "/api/v1/words?status=complete&status=needs_details"} {
		res = f.request(t, "GET", path, nil, "", cookie, "")
		if res.StatusCode != 400 {
			t.Fatalf("filter accepted %s", path)
		}
	}
	for _, path := range []string{"/.env", "/api/v1/not-found", "/nonexistent"} {
		res = f.request(t, "GET", path, nil, "", nil, "")
		if res.StatusCode != 404 {
			t.Fatalf("unknown path served=%s", path)
		}
	}
	unchanged := decodeResponse[store.Word](t, f.request(t, "GET", "/api/v1/words/"+word.ID, nil, "", cookie, ""), 200)
	if unchanged.Revision != 1 || unchanged.Term != word.Term {
		t.Fatal("rejected input changed word")
	}
}

// Only make browser-test starts this isolated fixture. These synthetic token
// helpers are compiled into tests, never into the production application.
func TestBrowserFixture(t *testing.T) {
	if os.Getenv("VOCAB_BROWSER_FIXTURE") != "1" {
		t.Skip("only started by make browser-test")
	}
	f := newFixture(t, true)
	t.Log("Production export fixture ready at " + f.origin)
	select {
	case <-t.Context().Done():
	case <-time.After(9 * time.Minute):
	}
}
