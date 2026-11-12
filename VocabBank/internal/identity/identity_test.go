package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-jose/go-jose/v4"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/testoidc"
)

func setup(t *testing.T) (*Manager, *testoidc.Issuer, http.Handler) {
	return setupAt(t, filepath.Join(t.TempDir(), "auth.sqlite"))
}

func setupAt(t *testing.T, path string) (*Manager, *testoidc.Issuer, http.Handler) {
	t.Helper()
	provider := testoidc.New(t)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.Enroll(t.Context(), provider.URL(), "test-subject", "Test owner"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"active": "test", "keys": map[string]string{"test": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	keys, err := ParseKeys(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Origins: []string{"https://work.example.test", "https://private.example.test"}, Issuer: provider.URL(), WebClientID: "vocab-web", WebClientSecret: "synthetic-secret", APIAudience: "https://work.example.test/api/v1", MCPMode: "direct", MCPResource: "https://work.example.test/mcp", MCPAudience: "https://work.example.test/mcp", MCPClients: []string{"vocab-test"}, Keys: keys}
	m, err := New(t.Context(), cfg, s)
	if err != nil {
		t.Fatal(err)
	}
	m.now = provider.Now
	return m, provider, browserHandler(m)
}

func browserHandler(m *Manager) http.Handler {
	mux := http.NewServeMux()
	m.Register(mux)
	mux.Handle("GET /protected", m.Browser(store.Read, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	return m.RestrictOrigin(mux)
}
func request(h http.Handler, method, host, path string, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://"+host+path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	if method != "GET" {
		r.Header.Set("Origin", "https://"+host)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func signIn(t *testing.T, m *Manager, p *testoidc.Issuer, h http.Handler, host string) *http.Cookie {
	t.Helper()
	start := request(h, "GET", host, "/auth/login", nil, "")
	if start.Code != 302 {
		t.Fatalf("login %d %s", start.Code, start.Body.String())
	}
	location, _ := url.Parse(start.Header().Get("Location"))
	q := location.Query()
	if !strings.Contains(q.Get("scope"), "offline_access") || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://"+host+"/auth/callback" {
		t.Fatalf("authorization contract: %+v", q)
	}
	p.Prepare(q)
	callback := "/auth/callback?code=synthetic-code&state=" + q.Get("state") + "&iss=" + url.QueryEscape(p.URL())
	res := request(h, "GET", host, callback, start.Result().Cookies(), "")
	if res.Code != 303 {
		t.Fatalf("callback %d %s", res.Code, res.Body.String())
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			if !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.MaxAge != int(IdleLifetime/time.Second) {
				t.Fatalf("cookie flags %+v", c)
			}
			return c
		}
	}
	t.Fatal("session cookie missing")
	return nil
}
func TestRotationSerializationRollingIdleAndLogout(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	initial, err := m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(initial.Cipher, []byte("synthetic-refresh")) {
		t.Fatal("refresh token stored in plaintext")
	}
	p.Clock.Add(250)
	var wg sync.WaitGroup
	codes := make(chan int, 12)
	for range 12 {
		wg.Go(func() { codes <- request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, "").Code })
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 204 {
			t.Fatalf("concurrent request: %d", code)
		}
	}
	if p.Refreshes.Load() != 1 {
		t.Fatalf("rotating grant refreshed %d times", p.Refreshes.Load())
	}
	renewed, err := m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now())
	if err != nil || !renewed.ExpiresAt.After(initial.ExpiresAt) {
		t.Fatalf("rolling idle not renewed: %v", err)
	}
	cross := request(h, "GET", "private.example.test", "/protected", []*http.Cookie{cookie}, "")
	if cross.Code != 401 {
		t.Fatalf("cross-host cookie %d", cross.Code)
	}
	session := request(h, "GET", "work.example.test", "/api/v1/session", []*http.Cookie{cookie}, "")
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(session.Body.Bytes(), &out)
	denied := request(h, "POST", "work.example.test", "/auth/logout", []*http.Cookie{cookie}, "bad-token")
	if denied.Code != 403 {
		t.Fatalf("logout CSRF %d", denied.Code)
	}
	logout := request(h, "POST", "work.example.test", "/auth/logout", []*http.Cookie{cookie}, out.CSRF)
	if logout.Code != 200 || p.Revocations.Load() != 1 {
		t.Fatalf("logout %d revocations %d", logout.Code, p.Revocations.Load())
	}
	if after := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); after.Code != 401 {
		t.Fatalf("logout session survived %d", after.Code)
	}
}

func TestSessionIdentityAndReauthenticationContract(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	read := func(cookie *http.Cookie) map[string]json.RawMessage {
		t.Helper()
		res := request(h, "GET", "work.example.test", "/api/v1/session", []*http.Cookie{cookie}, "")
		if res.Code != http.StatusOK {
			t.Fatalf("session status=%d", res.Code)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out) != 4 || out["owner_id"] == nil || out["label"] == nil || out["csrf_token"] == nil || out["scopes"] == nil {
			t.Fatal("session must contain only opaque owner ID, label, CSRF and scopes")
		}
		return out
	}
	first := read(cookie)
	owner, err := m.store.Owner(t.Context(), p.URL(), "test-subject")
	if err != nil {
		t.Fatal(err)
	}
	var ownerID string
	if json.Unmarshal(first["owner_id"], &ownerID) != nil || ownerID != owner.ID || ownerID == owner.Subject {
		t.Fatal("session owner ID must be the enrolled opaque identity")
	}
	secondCookie := signIn(t, m, p, h, "work.example.test")
	second := read(secondCookie)
	if !bytes.Equal(first["owner_id"], second["owner_id"]) || bytes.Equal(first["csrf_token"], second["csrf_token"]) {
		t.Fatal("reauthentication must keep the owner ID and rotate CSRF")
	}
	var oldCSRF string
	if err := json.Unmarshal(first["csrf_token"], &oldCSRF); err != nil {
		t.Fatal(err)
	}
	if res := request(h, "POST", "work.example.test", "/auth/logout", []*http.Cookie{secondCookie}, oldCSRF); res.Code != http.StatusForbidden {
		t.Fatal("an earlier session's CSRF must not authorize a new session")
	}
	start := request(h, "GET", "work.example.test", "/auth/login?reauthenticate=1&prompt=none&resource=https://evil.example", nil, "")
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil || location.Query().Get("prompt") != "login" || location.Query().Get("resource") != m.cfg.APIAudience {
		t.Fatal("explicit reauthentication must force provider login without changing its resource")
	}
}

func TestProviderFailureRetainsGrantUntilDefinitivelyInvalid(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	p.Clock.Add(301)
	p.Mode.Store(503)
	res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, "")
	if res.Code != 503 {
		t.Fatalf("provider outage = %d", res.Code)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			t.Fatal("outage cleared cookie")
		}
	}
	if _, err := m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now()); err != nil {
		t.Fatal("outage deleted durable grant")
	}
	p.Mode.Store(0)
	if res = request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 204 {
		t.Fatalf("recovery = %d %s", res.Code, res.Body.String())
	}
	p.Clock.Add(301)
	p.Mode.Store(400)
	if res = request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 401 {
		t.Fatalf("invalid_grant = %d", res.Code)
	}
	if _, err := m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now()); err == nil {
		t.Fatal("invalid grant survived")
	}
}
func TestAccessTokenAndOwnerBoundaries(t *testing.T) {
	m, p, _ := setup(t)
	valid := p.Access("test-subject", "vocab-web", m.cfg.APIAudience, store.Read+" offline_access", 300)
	if _, err := m.Authenticate(t.Context(), valid, false); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"overlong": p.Access("test-subject", "vocab-web", m.cfg.APIAudience, store.Read, 301), "client": p.Access("test-subject", "other", m.cfg.APIAudience, store.Read, 300), "audience": p.Access("test-subject", "vocab-web", m.cfg.MCPAudience, store.Read, 300), "unenrolled": p.Access("another-subject", "vocab-web", m.cfg.APIAudience, store.Read, 300), "scope": p.Access("test-subject", "vocab-web", m.cfg.APIAudience, "other:write", 300), "expired": p.Access("test-subject", "vocab-web", m.cfg.APIAudience, store.Read, -1)} {
		t.Run(name, func(t *testing.T) {
			if _, err := m.Authenticate(t.Context(), token, false); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}

	forgedIssuer := testoidc.New(t)
	if _, err := m.Authenticate(t.Context(), forgedIssuer.Sign(map[string]any{"iss": p.URL(), "sub": "test-subject", "aud": []string{m.cfg.APIAudience}, "azp": "vocab-web", "scope": store.Read, "typ": "Bearer", "iat": p.Now().Unix(), "exp": p.Now().Unix() + 300}, "JWT"), false); err == nil {
		t.Fatal("forged signature accepted")
	}
	c, err := m.verifyAccess(t.Context(), valid, m.cfg.APIAudience)
	if err != nil {
		t.Fatal(err)
	}
	c.Issuer = "https://wrong.example.test"
	if _, err = m.Authenticate(t.Context(), p.Sign(c, "JWT"), false); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong issuer: %v", err)
	}
}
func TestLoginHostStateAndIdleExpiry(t *testing.T) {
	m, p, h := setup(t)
	start := request(h, "GET", "work.example.test", "/auth/login", nil, "")
	loc, _ := url.Parse(start.Header().Get("Location"))
	q := loc.Query()
	p.Prepare(q)
	bad := request(h, "GET", "private.example.test", "/auth/callback?code=synthetic-code&state="+q.Get("state")+"&iss="+url.QueryEscape(p.URL()), start.Result().Cookies(), "")
	if bad.Code != 401 {
		t.Fatalf("cross-host callback = %d", bad.Code)
	}
	wrongState := request(h, "GET", "work.example.test", "/auth/callback?code=synthetic-code&state=wrong&iss="+url.QueryEscape(p.URL()), start.Result().Cookies(), "")
	if wrongState.Code != 401 {
		t.Fatalf("wrong state = %d", wrongState.Code)
	}
	cookie := signIn(t, m, p, h, "private.example.test")
	p.Clock.Add(int64(IdleLifetime/time.Second) + 1)
	if res := request(h, "GET", "private.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 401 {
		t.Fatalf("idle expiry = %d", res.Code)
	}
	spoof := httptest.NewRequest("GET", "https://evil.example.test/protected", nil)
	spoof.Header.Set("X-Forwarded-Host", "work.example.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, spoof)
	if rec.Code != 421 {
		t.Fatalf("forwarded Host trusted = %d", rec.Code)
	}
}

func TestGatewayContractIsSeparateFromBrowserGrants(t *testing.T) {
	m, p, _ := setup(t)
	m.cfg.MCPMode = "gateway"
	m.cfg.MCPAudience = "vocabbank-mcp"
	m.cfg.GatewayIssuer = "https://mcp.example.test"
	m.gateway = jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.Key.PublicKey, KeyID: "test", Use: "sig", Algorithm: "RS256"}}}
	claims := map[string]any{"iss": m.cfg.GatewayIssuer, "identity_issuer": p.URL(), "sub": "test-subject", "aud": []string{m.cfg.MCPAudience}, "client_id": "vocab-test", "scope": store.Read + " " + store.Write, "iat": p.Now().Unix(), "nbf": p.Now().Unix(), "exp": p.Now().Unix() + 60}
	valid := p.Sign(claims, "mcp-gateway+jwt")
	actor, err := m.Authenticate(t.Context(), valid, true)
	if err != nil || actor.Subject != "test-subject" {
		t.Fatalf("gateway principal %+v %v", actor, err)
	}
	if _, err = m.Authenticate(t.Context(), valid, false); err == nil {
		t.Fatal("gateway grant accepted by browser API")
	}
	for name, change := range map[string]func(map[string]any){"audience": func(c map[string]any) { c["aud"] = []string{"other"} }, "issuer": func(c map[string]any) { c["iss"] = "https://other.example.test" }, "identity issuer": func(c map[string]any) { c["identity_issuer"] = "https://other.example.test" }, "client": func(c map[string]any) { c["client_id"] = "other" }, "lifetime": func(c map[string]any) { c["exp"] = p.Now().Unix() + 61 }, "missing nbf": func(c map[string]any) { delete(c, "nbf") }, "scope": func(c map[string]any) { c["scope"] = "other:read" }, "unenrolled": func(c map[string]any) { c["sub"] = "other-owner" }} {
		t.Run(name, func(t *testing.T) {
			copy := maps.Clone(claims)
			change(copy)
			if _, err = m.Authenticate(t.Context(), p.Sign(copy, "mcp-gateway+jwt"), true); err == nil {
				t.Fatal("invalid gateway identity accepted")
			}
		})
	}
}

func TestActiveOfflineSessionOutlivesSharedOnlineSSOMaximum(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	p.Clock.Add(12 * 60 * 60)
	if res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 204 {
		t.Fatalf("offline session had arbitrary online maximum: %d", res.Code)
	}
	if p.Refreshes.Load() != 1 {
		t.Fatal("offline grant was not refreshed")
	}
}

type tokenNetworkFailure struct{ fallback http.RoundTripper }

func (f tokenNetworkFailure) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/token" {
		return nil, fmt.Errorf("synthetic network unavailable")
	}
	return f.fallback.RoundTrip(r)
}
func TestNetworkFailureKeepsExpiredSessionRetryable(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	m.client.Transport = tokenNetworkFailure{http.DefaultTransport}
	p.Clock.Add(301)
	if res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 503 {
		t.Fatalf("network failure = %d", res.Code)
	}
	if _, err := m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now()); err != nil {
		t.Fatal("network error deleted encrypted session")
	}
	m.client.Transport = http.DefaultTransport
	if res := request(h, "GET", "work.example.test", "/protected", []*http.Cookie{cookie}, ""); res.Code != 204 {
		t.Fatalf("network recovery = %d", res.Code)
	}
}

func TestLogoutReportsProviderFailureButDestroysLocalSession(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	res := request(h, "GET", "work.example.test", "/api/v1/session", []*http.Cookie{cookie}, "")
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(res.Body.Bytes(), &out)
	p.Mode.Store(502)
	res = request(h, "POST", "work.example.test", "/auth/logout", []*http.Cookie{cookie}, out.CSRF)
	if res.Code != 502 {
		t.Fatalf("unconfirmed provider revocation = %d", res.Code)
	}
	if _, err := m.store.Session(t.Context(), hash(cookie.Value), "work.example.test", p.Now()); err == nil {
		t.Fatal("local session survived sign-out")
	}
	cleared := false
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("local session cookie was not cleared")
	}
}
func TestInvalidCallbackDoesNotClearExistingSession(t *testing.T) {
	m, p, h := setup(t)
	cookie := signIn(t, m, p, h, "work.example.test")
	res := request(h, "GET", "work.example.test", "/auth/callback?state=bad&code=bad", []*http.Cookie{cookie}, "")
	if res.Code != 401 {
		t.Fatalf("invalid callback %d", res.Code)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			t.Fatal("invalid callback cleared a different valid session")
		}
	}
}
