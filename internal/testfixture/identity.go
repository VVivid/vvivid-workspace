// Package testfixture supplies loopback OIDC fixtures for integration tests only.
// Production commands never import it and expose no test identity route.
package testfixture

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

const Origin = "https://shopping.example"

type grant struct{ subject, nonce, challenge string }

type Identity struct {
	Manager *identity.Manager
	Store   *sqlite.Store
	Path    string
	config  identity.Config
	server  *httptest.Server
	key     *rsa.PrivateKey
	secret  string
	mu      sync.Mutex
	codes   map[string]grant
	refresh map[string]grant
}

func New(t *testing.T) *Identity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &Identity{key: key, secret: rand.Text(), codes: map[string]grant{}, refresh: map[string]grant{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	f.Path = filepath.Join(t.TempDir(), "integration.sqlite")
	f.Store, err = sqlite.Open(t.Context(), f.Path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Store.Close() })
	secretPath := filepath.Join(t.TempDir(), "secret")
	keyPath := filepath.Join(t.TempDir(), "keys")
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	keys, _ := json.Marshal(map[string]any{"active": "test", "keys": map[string]string{"test": base64.StdEncoding.EncodeToString(keyBytes)}})
	if err := os.WriteFile(secretPath, []byte(f.secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keys, 0600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"SHOPPING_ORIGIN": Origin, "SHOPPING_OIDC_ISSUER": f.server.URL, "SHOPPING_WEB_CLIENT_ID": "shopping-web", "SHOPPING_WEB_CLIENT_SECRET_FILE": secretPath, "SHOPPING_API_AUDIENCE": Origin + "/api/v1", "SHOPPING_MCP_AUDIENCE": Origin + "/mcp", "SHOPPING_MCP_CLIENT_IDS": "shopping-codex,shopping-chatgpt", "SHOPPING_SESSION_KEYS_FILE": keyPath}
	cfg, err := identity.LoadConfig(func(name string) (string, bool) { v, ok := settings[name]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	f.config = *cfg
	f.Manager, err = identity.New(t.Context(), *cfg, f.Store)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Reopen replaces the database connection and identity manager, preserving the
// same database, issuer and session keys for restart/replay integration tests.
func (f *Identity) Reopen(t *testing.T) {
	t.Helper()
	if err := f.Store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.Store, err = sqlite.Open(t.Context(), f.Path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.Manager, err = identity.New(t.Context(), f.config, f.Store)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *Identity) Enroll(t *testing.T, subject string) sqlite.Owner {
	t.Helper()
	owner, err := f.Store.EnrollAccount(t.Context(), sqlite.ExternalIdentity{Issuer: f.server.URL, Subject: subject}, sqlite.Preferences{Timezone: "America/New_York", Currency: "USD", PreferredRetailer: "Walmart", WeekStart: "monday"})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func (f *Identity) Principal(t *testing.T, subject, scopes string) identity.Principal {
	t.Helper()
	p, err := f.Manager.AuthenticateBearer(t.Context(), f.Bearer(subject, "shopping-codex", f.Manager.MCPAudience(), scopes), f.Manager.MCPAudience())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *Identity) Bearer(subject, client, audience, scopes string) string {
	return f.sign(f.claims(subject, client, audience, scopes))
}

func (f *Identity) claims(subject, client, audience, scopes string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": f.server.URL, "sub": subject, "azp": client, "aud": []string{audience}, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "typ": "Bearer", "scope": scopes}
}

func (f *Identity) sign(claims map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		panic(err)
	}
	raw, _ := json.Marshal(claims)
	object, err := signer.Sign(raw)
	if err != nil {
		panic(err)
	}
	token, err := object.CompactSerialize()
	if err != nil {
		panic(err)
	}
	return token
}

func (f *Identity) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/keys", "revocation_endpoint": f.server.URL + "/revoke", "end_session_endpoint": f.server.URL + "/logout", "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
	case "/keys":
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	case "/authorize":
		q := r.URL.Query()
		if q.Get("client_id") != "shopping-web" || q.Get("redirect_uri") != Origin+"/auth/callback" || q.Get("resource") != Origin+"/api/v1" || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "invalid authorization", 400)
			return
		}
		code := rand.Text()
		f.codes[code] = grant{subject: q.Get("test_subject"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
		callback, _ := url.Parse(Origin + "/auth/callback")
		callback.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {f.server.URL}}.Encode()
		http.Redirect(w, r, callback.String(), 302)
	case "/token", "/revoke":
		client, secret, ok := r.BasicAuth()
		if !ok || client != "shopping-web" || subtle.ConstantTimeCompare([]byte(secret), []byte(f.secret)) != 1 || r.ParseForm() != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.URL.Path == "/revoke" {
			delete(f.refresh, r.Form.Get("token"))
			return
		}
		if r.Form.Get("resource") != Origin+"/api/v1" {
			http.Error(w, "wrong resource", 400)
			return
		}
		var g grant
		var found bool
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			g, found = f.codes[r.Form.Get("code")]
			delete(f.codes, r.Form.Get("code"))
			found = found && r.Form.Get("redirect_uri") == Origin+"/auth/callback" && oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) == g.challenge
		case "refresh_token":
			g, found = f.refresh[r.Form.Get("refresh_token")]
			delete(f.refresh, r.Form.Get("refresh_token"))
		}
		if !found {
			http.Error(w, "invalid grant", 400)
			return
		}
		refresh := rand.Text()
		f.refresh[refresh] = g
		now := time.Now()
		id := f.sign(map[string]any{"iss": f.server.URL, "sub": g.subject, "aud": "shopping-web", "azp": "shopping-web", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": g.nonce, "typ": "ID"})
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.Bearer(g.subject, "shopping-web", Origin+"/api/v1", identity.Read+" "+identity.ListsWrite), "refresh_token": refresh, "id_token": id, "token_type": "Bearer", "expires_in": 300})
	default:
		http.NotFound(w, r)
	}
}

func (f *Identity) Cookie(t *testing.T, subject string) *http.Cookie {
	t.Helper()
	mux := http.NewServeMux()
	f.Manager.Register(mux)
	handler := f.Manager.RestrictOrigin(mux)
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, httptest.NewRequest("GET", Origin+"/auth/login", nil))
	if start.Code != 302 {
		t.Fatalf("login status %d: %s", start.Code, start.Body)
	}
	authorize, _ := url.Parse(start.Header().Get("Location"))
	q := authorize.Query()
	q.Set("test_subject", subject)
	authorize.RawQuery = q.Encode()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(authorize.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	request := httptest.NewRequest("GET", response.Header.Get("Location"), nil)
	request.AddCookie(start.Result().Cookies()[0])
	finish := httptest.NewRecorder()
	handler.ServeHTTP(finish, request)
	if finish.Code != 303 {
		t.Fatalf("callback status %d: %s", finish.Code, finish.Body)
	}
	for _, cookie := range finish.Result().Cookies() {
		if cookie.Name == "__Host-shopping-session" {
			return cookie
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func (f *Identity) BrowserPrincipal(t *testing.T, cookie *http.Cookie) (identity.Principal, string) {
	t.Helper()
	request := httptest.NewRequest("GET", Origin+"/", nil)
	request.AddCookie(cookie)
	p, csrf, err := f.Manager.BrowserSession(request)
	if err != nil {
		t.Fatal(err)
	}
	return p, csrf
}
