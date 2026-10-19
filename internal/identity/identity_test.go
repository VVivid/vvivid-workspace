package identity

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"agentshoppinglist/internal/store/sqlite"
	"github.com/go-jose/go-jose/v4"
)

func TestBearerValidationAndOwnerIsolation(t *testing.T) {
	m, issuer, store, _ := testManager(t)
	alice, bob := enroll(t, m, "alice"), enroll(t, m, "bob")
	for _, owner := range []sqlite.Owner{alice, bob} {
		_, err := store.Write(t.Context(), owner, sqlite.Mutation{Key: "seed", Operation: "seed", Payload: []byte(`{}`), RequestID: "fixture-request", ClientID: "fixture-operator"}, func(tx *sqlite.Tx) ([]byte, error) {
			return []byte(`{}`), tx.InsertProduct(sqlite.Product{ID: owner.ID() + "-private", Name: "Private drink", Category: "drinks"})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	aliceToken := issuer.bearer("alice", "shopping-codex", m.MCPAudience(), Read+" "+ListsWrite, nil)
	bobToken := issuer.bearer("bob", "shopping-chatgpt", m.MCPAudience(), Read, nil)
	principal, err := m.AuthenticateBearer(t.Context(), aliceToken, m.MCPAudience())
	if err != nil || principal.Owner() != alice || principal.ClientID() != "shopping-codex" || principal.Identity().Subject != "alice" {
		t.Fatalf("verified principal: %v", err)
	}
	other, err := m.AuthenticateBearer(t.Context(), bobToken, m.MCPAudience())
	if err != nil || other.Owner() != bob {
		t.Fatalf("second principal: %v", err)
	}
	if _, err := store.Product(t.Context(), other.Owner(), alice.ID()+"-private"); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("cross-user product access: %v", err)
	}
	if other.Require(ProductsWrite) == nil || (Principal{}).Require(Read) == nil {
		t.Fatal("insufficient or empty principal authorized")
	}
	now := issuer.clock()
	for name, change := range map[string]func(map[string]any){
		"wrong issuer":           func(c map[string]any) { c["iss"] = issuer.server.URL + "/other" },
		"issuer trailing slash":  func(c map[string]any) { c["iss"] = issuer.server.URL + "/" },
		"wrong audience":         func(c map[string]any) { c["aud"] = []string{"https://ssh.example/mcp"} },
		"mixed audience":         func(c map[string]any) { c["aud"] = []string{m.MCPAudience(), "https://ssh.example/mcp"} },
		"API token at MCP":       func(c map[string]any) { c["aud"] = []string{m.APIAudience()} },
		"expiry":                 func(c map[string]any) { c["exp"] = now.Unix() },
		"missing expiry":         func(c map[string]any) { delete(c, "exp") },
		"future issued":          func(c map[string]any) { c["iat"] = now.Add(time.Minute).Unix() },
		"not yet valid":          func(c map[string]any) { c["nbf"] = now.Add(time.Second).Unix() },
		"long lifetime":          func(c map[string]any) { c["exp"] = now.Add(6 * time.Minute).Unix() },
		"ID token":               func(c map[string]any) { c["typ"] = "ID" },
		"missing type":           func(c map[string]any) { delete(c, "typ") },
		"unknown client":         func(c map[string]any) { c["azp"] = "ssh-codex" },
		"browser client":         func(c map[string]any) { c["azp"] = "shopping-web" },
		"missing client":         func(c map[string]any) { delete(c, "azp") },
		"SSH scope":              func(c map[string]any) { c["scope"] = Read + " sshmcp:admin" },
		"unknown shopping scope": func(c map[string]any) { c["scope"] = Read + " shopping:admin" },
		"unenrolled":             func(c map[string]any) { c["sub"] = "not-enrolled" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := issuer.bearer("alice", "shopping-codex", m.MCPAudience(), Read, change)
			if _, err := m.AuthenticateBearer(t.Context(), raw, m.MCPAudience()); err == nil {
				t.Fatal("invalid bearer accepted")
			}
		})
	}
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer.mu.Lock()
	forged := issuer.sign(issuer.access("alice", "shopping-codex", m.MCPAudience(), Read), wrongKey, issuer.kid)
	issuer.mu.Unlock()
	if _, err := m.AuthenticateBearer(t.Context(), forged, m.MCPAudience()); err == nil {
		t.Fatal("wrong signature accepted")
	}
	if _, err := m.AuthenticateBearer(t.Context(), "e30.e30.", m.MCPAudience()); err == nil {
		t.Fatal("unsigned token accepted")
	}
	if err := store.DisableAccount(t.Context(), principal.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AuthenticateBearer(t.Context(), aliceToken, m.MCPAudience()); err == nil {
		t.Fatal("disabled account accepted")
	}
	if _, err := m.AuthenticateBearer(t.Context(), bobToken, m.MCPAudience()); err != nil {
		t.Fatal("disabling Alice affected Bob")
	}
}

func TestBearerMiddlewareScopeChallenge(t *testing.T) {
	m, issuer, _, _ := testManager(t)
	enroll(t, m, "alice")
	var sawOwner bool
	handler := m.RestrictOrigin(m.BearerMiddleware(m.MCPAudience(), []string{ProductsWrite}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := FromContext(r.Context())
		sawOwner = ok && principal.Identity().Subject == "alice"
		w.WriteHeader(http.StatusNoContent)
	})))
	for _, tc := range []struct {
		scope  string
		status int
	}{{Read, 403}, {ProductsWrite, 204}} {
		request := httptest.NewRequest("POST", m.cfg.Origin+"/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+issuer.bearer("alice", "shopping-codex", m.MCPAudience(), tc.scope, nil))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("scope %s: status %d", tc.scope, response.Code)
		}
		if tc.status == 403 && (!strings.Contains(response.Header().Get("WWW-Authenticate"), ProductsWrite) || !strings.Contains(response.Header().Get("WWW-Authenticate"), "insufficient_scope")) {
			t.Fatal("missing scope challenge")
		}
	}
	if !sawOwner {
		t.Fatal("verified owner missing from middleware context")
	}
	request := httptest.NewRequest("POST", m.cfg.Origin+"/mcp?access_token="+issuer.bearer("alice", "shopping-codex", m.MCPAudience(), ProductsWrite, nil), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 || !strings.Contains(response.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatal("query token accepted or metadata challenge missing")
	}
}

func TestBrowserSessionsCSRFRestartRefreshAndLogout(t *testing.T) {
	m, issuer, store, path := testManager(t)
	alice := enroll(t, m, "alice")
	bob := enroll(t, m, "bob")
	aliceCookie, bobCookie := loginAs(t, m, issuer, "alice"), loginAs(t, m, issuer, "bob")
	principal, csrf, err := m.BrowserSession(sessionRequest(m, aliceCookie))
	if err != nil || principal.Owner() != alice || csrf == "" || principal.Allows(ProductsWrite) || principal.Allows(PurchasesWrite) {
		t.Fatalf("browser principal: %v", err)
	}
	other, otherCSRF, err := m.BrowserSession(sessionRequest(m, bobCookie))
	if err != nil || other.Owner() != bob || otherCSRF == csrf {
		t.Fatal("browser users were not isolated")
	}
	stored, err := store.Session(t.Context(), sha256.Sum256([]byte(aliceCookie.Value)), issuer.clock())
	if err != nil || bytes.Contains(stored.EncryptedState, []byte("access_token")) || bytes.Contains(stored.EncryptedState, []byte(csrf)) {
		t.Fatal("session state was not encrypted")
	}
	mutations := 0
	handler := m.RestrictOrigin(m.BrowserMiddleware([]string{ListsWrite}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mutations++; w.WriteHeader(204) })))
	for _, tc := range []struct {
		origin, token string
		status        int
	}{{m.cfg.Origin, "", 403}, {m.cfg.Origin, otherCSRF, 403}, {"https://evil.example", csrf, 403}, {"", csrf, 403}, {m.cfg.Origin, csrf, 204}} {
		request := httptest.NewRequest("PATCH", m.cfg.Origin+"/api/v1/lists/x/items/y", nil)
		request.AddCookie(aliceCookie)
		if tc.origin != "" {
			request.Header.Set("Origin", tc.origin)
		}
		request.Header.Set("X-CSRF-Token", tc.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("CSRF status %d want %d", response.Code, tc.status)
		}
	}
	if mutations != 1 {
		t.Fatalf("ran %d mutations", mutations)
	}
	if response := postLogout(m, aliceCookie, ""); response.Code != 403 {
		t.Fatal("logout without CSRF accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := New(t.Context(), m.cfg, reopened)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = issuer.clock
	principal, restartedCSRF, err := restarted.BrowserSession(sessionRequest(restarted, aliceCookie))
	if err != nil || principal.Owner() != alice || restartedCSRF != csrf {
		t.Fatalf("restart lost session: %v", err)
	}
	issuer.advance(61 * time.Second)
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		group.Go(func() { _, _, err := restarted.BrowserSession(sessionRequest(restarted, aliceCookie)); results <- err })
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent refresh: %v", err)
		}
	}
	issuer.mu.Lock()
	refreshes := issuer.refreshes
	issuer.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("rotating refresh ran %d times", refreshes)
	}
	response := postLogout(restarted, aliceCookie, csrf)
	if response.Code != 303 || !strings.HasPrefix(response.Header().Get("Location"), issuer.server.URL+"/logout?") {
		t.Fatalf("logout status %d", response.Code)
	}
	if _, _, err := restarted.BrowserSession(sessionRequest(restarted, aliceCookie)); err == nil {
		t.Fatal("logged-out session survived")
	}
	if _, _, err := restarted.BrowserSession(sessionRequest(restarted, bobCookie)); err != nil {
		t.Fatal("Alice logout affected Bob")
	}
	issuer.mu.Lock()
	revokes := issuer.revokes
	issuer.mu.Unlock()
	if revokes != 1 {
		t.Fatalf("revocation calls %d", revokes)
	}
}

func TestBrowserPKCEStateNonceAndEnrollmentFailures(t *testing.T) {
	m, issuer, _, _ := testManager(t)
	enroll(t, m, "alice")
	for _, name := range []string{"state", "issuer", "missing issuer", "PKCE", "nonce", "unenrolled", "replay"} {
		t.Run(name, func(t *testing.T) {
			subject := "alice"
			if name == "unenrolled" {
				subject = "mallory"
			}
			cookie, callback := beginLogin(t, m, issuer, subject)
			u, _ := url.Parse(callback)
			query := u.Query()
			switch name {
			case "state":
				query.Set("state", rand.Text())
			case "issuer":
				query.Set("iss", "https://other.example")
			case "missing issuer":
				query.Del("iss")
			case "PKCE", "nonce":
				issuer.mu.Lock()
				grant := issuer.codes[query.Get("code")]
				if name == "PKCE" {
					grant.challenge = rand.Text()
				} else {
					grant.nonce = rand.Text()
				}
				issuer.codes[query.Get("code")] = grant
				issuer.mu.Unlock()
			case "replay":
				if response := finishLogin(t, m, cookie, callback); response.Code != 303 {
					t.Fatal("initial callback failed")
				}
			}
			u.RawQuery = query.Encode()
			if response := finishLogin(t, m, cookie, u.String()); response.Code != 401 {
				t.Fatalf("%s accepted status %d", name, response.Code)
			}
		})
	}
}

func TestRefreshRevocationSubjectResourceAndAbsoluteExpiry(t *testing.T) {
	for _, name := range []string{"revoked grant", "subject switch", "wrong resource", "absolute expiry", "disabled account"} {
		t.Run(name, func(t *testing.T) {
			m, issuer, store, _ := testManager(t)
			enroll(t, m, "alice")
			enroll(t, m, "bob")
			cookie := loginAs(t, m, issuer, "alice")
			issuer.mu.Lock()
			switch name {
			case "revoked grant":
				clear(issuer.refresh)
			case "subject switch":
				issuer.changeSubject = true
			case "wrong resource":
				issuer.wrongRefreshAudience = true
			}
			issuer.mu.Unlock()
			if name == "absolute expiry" {
				issuer.advance(sessionTTL + time.Second)
			} else {
				issuer.advance(61 * time.Second)
			}
			if name == "disabled account" {
				if err := store.DisableAccount(t.Context(), sqlite.ExternalIdentity{Issuer: m.cfg.Issuer, Subject: "alice"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := m.BrowserSession(sessionRequest(m, cookie)); err == nil {
				t.Fatalf("%s accepted", name)
			}
			if _, _, err := m.BrowserSession(sessionRequest(m, cookie)); err == nil {
				t.Fatal("failed session recovered without a new login")
			}
		})
	}
}

func TestSigningAndEncryptionKeyRotation(t *testing.T) {
	m, issuer, _, _ := testManager(t)
	enroll(t, m, "alice")
	cookie := loginAs(t, m, issuer, "alice")
	oldToken := issuer.bearer("alice", "shopping-codex", m.MCPAudience(), Read, nil)
	if _, err := m.AuthenticateBearer(t.Context(), oldToken, m.MCPAudience()); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer.mu.Lock()
	issuer.retiringKeys = append(issuer.retiringKeys, jose.JSONWebKey{Key: &issuer.key.PublicKey, KeyID: issuer.kid, Algorithm: "RS256", Use: "sig"})
	issuer.key, issuer.kid = key, rand.Text()
	before := issuer.keyReads
	issuer.mu.Unlock()
	newToken := issuer.bearer("alice", "shopping-codex", m.MCPAudience(), Read, nil)
	if _, err := m.AuthenticateBearer(t.Context(), newToken, m.MCPAudience()); err != nil {
		t.Fatalf("new signing key rejected: %v", err)
	}
	issuer.mu.Lock()
	after := issuer.keyReads
	issuer.mu.Unlock()
	if after <= before {
		t.Fatal("new signing key did not trigger JWKS reload")
	}
	newKey := make([]byte, 32)
	if _, err := rand.Read(newKey); err != nil {
		t.Fatal(err)
	}
	rotated, err := parseKeys([]byte(`{"active":"second","keys":{"second":"` + base64.StdEncoding.EncodeToString(newKey) + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	rotated.keys["first"] = m.cfg.Keys.keys["first"]
	m.cfg.Keys = rotated
	// Retained decrypt-only key reads sessions issued before process rotation.
	if _, _, err := m.BrowserSession(sessionRequest(m, cookie)); err != nil {
		t.Fatalf("old encryption key not retained: %v", err)
	}
	issuer.advance(61 * time.Second)
	if _, _, err := m.BrowserSession(sessionRequest(m, cookie)); err != nil {
		t.Fatalf("rotated session refresh: %v", err)
	}
	delete(rotated.keys, "first")
	if _, _, err := m.BrowserSession(sessionRequest(m, cookie)); err != nil {
		t.Fatalf("refreshed session did not migrate to active key: %v", err)
	}
	request := sessionRequest(m, cookie)
	request.Host = "attacker.example"
	response := httptest.NewRecorder()
	testRoutes(m).ServeHTTP(response, request)
	if response.Code != 421 {
		t.Fatal("untrusted Host accepted")
	}
}

func TestIdentityConfigurationAndEncryptionTamper(t *testing.T) {
	if cfg, err := LoadConfig(func(string) (string, bool) { return "", false }); err != nil || cfg != nil {
		t.Fatal("unconfigured shell failed")
	}
	keys := privateKeyFile(t)
	secret := writePrivate(t, "secret", []byte(rand.Text()))
	settings := map[string]string{"SHOPPING_ORIGIN": "https://shopping.vvivid.dev", "SHOPPING_OIDC_ISSUER": "https://issuer.example/realms/shopping", "SHOPPING_WEB_CLIENT_ID": "shopping-web", "SHOPPING_WEB_CLIENT_SECRET_FILE": secret, "SHOPPING_API_AUDIENCE": "https://shopping.vvivid.dev/api/v1", "SHOPPING_MCP_AUDIENCE": "https://shopping.vvivid.dev/mcp", "SHOPPING_MCP_CLIENT_IDS": "shopping-codex,shopping-chatgpt", "SHOPPING_SESSION_KEYS_FILE": keys}
	lookup := func(name string) (string, bool) { value, ok := settings[name]; return value, ok }
	cfg, err := LoadConfig(lookup)
	if err != nil || cfg == nil {
		t.Fatalf("complete config: %v", err)
	}
	for key, invalid := range map[string]string{"SHOPPING_ORIGIN": "http://shopping.vvivid.dev", "SHOPPING_MCP_AUDIENCE": "https://ssh.example/mcp", "SHOPPING_WEB_CLIENT_ID": "", "SHOPPING_MCP_CLIENT_IDS": "shopping-web", "SHOPPING_OIDC_ISSUER": "https://issuer.example/realm?query=yes"} {
		previous := settings[key]
		settings[key] = invalid
		if _, err := LoadConfig(lookup); err == nil {
			t.Fatalf("invalid %s accepted", key)
		}
		settings[key] = previous
	}
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(lookup); err == nil {
		t.Fatal("world-readable secret accepted")
	}
	encrypted, err := cfg.Keys.seal(map[string]string{"value": "private"}, "session:A")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if cfg.Keys.open(encrypted, "session:B", &decoded) == nil {
		t.Fatal("ciphertext moved between sessions")
	}
	encrypted[len(encrypted)-3] ^= 1
	if cfg.Keys.open(encrypted, "session:A", &decoded) == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
