package identity

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentnewsfeed/internal/store/sqlite"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

// This issuer exists only in tests. Browser tests perform real discovery,
// authorization-code exchange, PKCE verification, JWKS fetch and refresh over
// loopback HTTP; the application has no runtime fixture identity path.
type testIssuer struct {
	mu                   sync.Mutex
	server               *httptest.Server
	now                  time.Time
	key                  *rsa.PrivateKey
	kid                  string
	retiringKeys         []jose.JSONWebKey
	secret               string
	codes                map[string]testGrant
	refresh              map[string]testGrant
	refreshes            int
	revokes              int
	keyReads             int
	changeSubject        bool
	wrongRefreshAudience bool
	failRevocation       bool
}

type testGrant struct{ subject, nonce, challenge string }

func newIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &testIssuer{now: time.Now().UTC().Truncate(time.Second), key: key, kid: rand.Text(), secret: rand.Text(), codes: make(map[string]testGrant), refresh: make(map[string]testGrant)}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *testIssuer) clock() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *testIssuer) advance(by time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(by)
}

func (f *testIssuer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/keys", "revocation_endpoint": f.server.URL + "/revoke", "end_session_endpoint": f.server.URL + "/logout", "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
	case "/keys":
		f.keyReads++
		keys := []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: f.kid, Algorithm: "RS256", Use: "sig"}}
		keys = append(keys, f.retiringKeys...)
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: keys})
	case "/authorize":
		query := r.URL.Query()
		if query.Get("client_id") != "newsfeed-web" || query.Get("redirect_uri") != "https://newsfeed.example/auth/callback" || query.Get("response_type") != "code" || query.Get("resource") != "https://newsfeed.example/api/v1" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" || query.Get("nonce") == "" || query.Get("scope") != "openid newsfeed:read newsfeed:articles:write" {
			http.Error(w, "invalid authorization request", http.StatusBadRequest)
			return
		}
		code := rand.Text()
		f.codes[code] = testGrant{subject: query.Get("test_subject"), nonce: query.Get("nonce"), challenge: query.Get("code_challenge")}
		callback, _ := url.Parse(query.Get("redirect_uri"))
		callback.RawQuery = url.Values{"code": {code}, "state": {query.Get("state")}, "iss": {f.server.URL}}.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	case "/token", "/revoke":
		id, secret, ok := r.BasicAuth()
		if !ok || id != "newsfeed-web" || subtle.ConstantTimeCompare([]byte(secret), []byte(f.secret)) != 1 || r.ParseForm() != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/revoke" {
			if f.failRevocation {
				http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
				return
			}
			delete(f.refresh, r.PostForm.Get("token"))
			f.revokes++
			return
		}
		var grant testGrant
		if r.PostForm.Get("resource") != "https://newsfeed.example/api/v1" {
			http.Error(w, "wrong resource", http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			code := r.PostForm.Get("code")
			var found bool
			grant, found = f.codes[code]
			delete(f.codes, code)
			if !found || r.PostForm.Get("redirect_uri") != "https://newsfeed.example/auth/callback" || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != grant.challenge {
				http.Error(w, "invalid grant", http.StatusBadRequest)
				return
			}
		case "refresh_token":
			var found bool
			grant, found = f.refresh[r.PostForm.Get("refresh_token")]
			delete(f.refresh, r.PostForm.Get("refresh_token"))
			if !found {
				http.Error(w, "invalid grant", http.StatusBadRequest)
				return
			}
			f.refreshes++
			if f.changeSubject {
				grant.subject = "bob"
			}
		default:
			http.Error(w, "invalid grant", http.StatusBadRequest)
			return
		}
		refresh := rand.Text()
		f.refresh[refresh] = grant
		audience := "https://newsfeed.example/api/v1"
		if f.wrongRefreshAudience && r.PostForm.Get("grant_type") == "refresh_token" {
			audience = "https://ssh.example/mcp"
		}
		access := f.sign(f.access(grant.subject, "newsfeed-web", audience, Read), f.key, f.kid)
		idClaims := map[string]any{"iss": f.server.URL, "sub": grant.subject, "aud": "newsfeed-web", "azp": "newsfeed-web", "iat": f.now.Unix(), "exp": f.now.Add(5 * time.Minute).Unix(), "nonce": grant.nonce, "typ": "ID"}
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, RefreshToken: refresh, IDToken: f.sign(idClaims, f.key, f.kid), TokenType: "Bearer"})
	default:
		http.NotFound(w, r)
	}
}

func (f *testIssuer) access(subject, client, audience, scope string) map[string]any {
	return map[string]any{"iss": f.server.URL, "sub": subject, "aud": []string{audience}, "azp": client, "iat": f.now.Unix(), "exp": f.now.Add(5 * time.Minute).Unix(), "typ": "Bearer", "scope": scope}
}

func (f *testIssuer) sign(claims map[string]any, key *rsa.PrivateKey, kid string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		panic(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	object, err := signer.Sign(payload)
	if err != nil {
		panic(err)
	}
	raw, err := object.CompactSerialize()
	if err != nil {
		panic(err)
	}
	return raw
}

func (f *testIssuer) bearer(subject, client, audience, scope string, change func(map[string]any)) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	claims := f.access(subject, client, audience, scope)
	if change != nil {
		change(claims)
	}
	return f.sign(claims, f.key, f.kid)
}

func testManager(t *testing.T) (*Manager, *testIssuer, *sqlite.Store, string) {
	t.Helper()
	f := newIssuer(t)
	path := filepath.Join(t.TempDir(), "auth.sqlite")
	store, err := sqlite.Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{"active": "first", "keys": map[string]string{"first": base64.StdEncoding.EncodeToString(key)}})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := parseKeys(encoded)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Origin: "https://newsfeed.example", Issuer: f.server.URL, WebClientID: "newsfeed-web", WebClientSecret: f.secret, APIAudience: "https://newsfeed.example/api/v1", MCPAudience: "https://newsfeed.example/mcp", MCPClientIDs: []string{"newsfeed-codex", "newsfeed-chatgpt"}, Keys: keys}
	m, err := New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	m.now = f.clock
	return m, f, store, path
}

func enroll(t *testing.T, m *Manager, subject string) sqlite.Owner {
	t.Helper()
	owner, err := m.store.EnrollAccount(t.Context(), sqlite.ExternalIdentity{Issuer: m.cfg.Issuer, Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func testRoutes(m *Manager) http.Handler {
	mux := http.NewServeMux()
	m.Register(mux)
	return m.RestrictOrigin(mux)
}

func beginLogin(t *testing.T, m *Manager, f *testIssuer, subject string) (*http.Cookie, string) {
	t.Helper()
	response := httptest.NewRecorder()
	testRoutes(m).ServeHTTP(response, httptest.NewRequest("GET", m.cfg.Origin+"/auth/login", nil))
	if response.Code != http.StatusFound {
		t.Fatalf("begin login status %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != loginCookie || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Domain != "" || cookies[0].Path != "/" {
		t.Fatal("login cookie is not host-only, secure, HttpOnly and SameSite Lax")
	}
	authorize, _ := url.Parse(response.Header().Get("Location"))
	query := authorize.Query()
	query.Set("test_subject", subject)
	authorize.RawQuery = query.Encode()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	result, err := client.Get(authorize.String())
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusFound {
		t.Fatalf("issuer authorize status %d", result.StatusCode)
	}
	return cookies[0], result.Header.Get("Location")
}

func finishLogin(t *testing.T, m *Manager, cookie *http.Cookie, callback string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest("GET", callback, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	testRoutes(m).ServeHTTP(response, request)
	return response
}

func loginAs(t *testing.T, m *Manager, f *testIssuer, subject string) *http.Cookie {
	t.Helper()
	cookie, callback := beginLogin(t, m, f, subject)
	response := finishLogin(t, m, cookie, callback)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("finish login status %d body %s", response.Code, response.Body)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookie {
			if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
				t.Fatal("insecure session cookie")
			}
			return cookie
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func sessionRequest(m *Manager, cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest("GET", m.cfg.Origin+"/", nil)
	request.AddCookie(cookie)
	return request
}

func writePrivate(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateKeyFile(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return writePrivate(t, "keys.json", []byte(fmt.Sprintf(`{"active":"test","keys":{"test":%q}}`, base64.StdEncoding.EncodeToString(key))))
}

func postLogout(m *Manager, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", m.cfg.Origin+"/auth/logout", strings.NewReader(url.Values{"csrf_token": {csrf}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", m.cfg.Origin)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	testRoutes(m).ServeHTTP(response, request)
	return response
}
