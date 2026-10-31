package identity

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserClientRequiresExplicitNewsfeedPermissions(t *testing.T) {
	m, issuer, _, _ := testManager(t)
	enroll(t, m, "alice")
	for _, tc := range []struct {
		scope, audience, client string
		allowed                 bool
	}{
		{Read, m.APIAudience(), "newsfeed-web", true},
		{Read + " " + ArticlesWrite, m.APIAudience(), "newsfeed-web", true},
		{Read + " sshmcp:admin", m.APIAudience(), "newsfeed-web", false},
		{Read, m.APIAudience(), "newsfeed-codex", false},
		{Read, m.MCPAudience(), "newsfeed-web", false},
	} {
		raw := issuer.bearer("alice", tc.client, tc.audience, tc.scope, nil)
		principal, err := m.AuthenticateBearer(t.Context(), raw, tc.audience)
		if (err == nil) != tc.allowed {
			t.Fatalf("browser grant %s accepted=%v", tc.scope, err == nil)
		}
		if err == nil && principal.Allows(ArticlesWrite) != (tc.scope == Read+" "+ArticlesWrite) {
			t.Fatal("browser write permission did not match its explicit grant")
		}
	}
	writeOnly := issuer.bearer("alice", "newsfeed-codex", m.MCPAudience(), ArticlesWrite, nil)
	principal, err := m.AuthenticateBearer(t.Context(), writeOnly, m.MCPAudience())
	if err != nil || principal.Require(Read) == nil || principal.Require(ArticlesWrite) != nil {
		t.Fatal("write scope implied reading or was lost")
	}
}

func TestCookieMixingAndCiphertextOwnerBinding(t *testing.T) {
	m, issuer, store, _ := testManager(t)
	enroll(t, m, "alice")
	enroll(t, m, "bob")
	alice, bob := loginAs(t, m, issuer, "alice"), loginAs(t, m, issuer, "bob")
	request := sessionRequest(m, alice)
	request.AddCookie(bob)
	if _, _, err := m.BrowserSession(request); err == nil {
		t.Fatal("ambiguous credential cookies accepted")
	}
	guard := m.BrowserMiddleware([]string{Read}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("bearer authenticated a browser route") }))
	request = httptest.NewRequest("GET", m.cfg.Origin+"/api/v1/me", nil)
	request.Header.Set("Authorization", "Bearer "+issuer.bearer("alice", "newsfeed-codex", m.MCPAudience(), Read, nil))
	response := httptest.NewRecorder()
	guard.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("browser route accepted a bearer credential")
	}
	bobSession, err := store.Session(t.Context(), sha256.Sum256([]byte(bob.Value)), issuer.clock())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSession(t.Context(), sha256.Sum256([]byte(alice.Value)), bobSession.EncryptedState, issuer.clock()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.BrowserSession(sessionRequest(m, alice)); err == nil {
		t.Fatal("ciphertext copied to another owner authenticated")
	}
	if _, _, err := m.BrowserSession(sessionRequest(m, bob)); err != nil {
		t.Fatal("tampering with Alice affected Bob")
	}
}

func TestLogoutFailureStillRevokesLocalSession(t *testing.T) {
	m, issuer, _, _ := testManager(t)
	enroll(t, m, "alice")
	cookie := loginAs(t, m, issuer, "alice")
	_, csrf, err := m.BrowserSession(sessionRequest(m, cookie))
	if err != nil {
		t.Fatal(err)
	}
	issuer.mu.Lock()
	issuer.failRevocation = true
	issuer.mu.Unlock()
	if response := postLogout(m, cookie, csrf); response.Code != 502 {
		t.Fatalf("revocation failure status %d", response.Code)
	}
	if _, _, err := m.BrowserSession(sessionRequest(m, cookie)); err == nil {
		t.Fatal("provider failure kept local session active")
	}
}

func TestLoginReturnPathsStayLocal(t *testing.T) {
	for _, raw := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example/", "/%5cevil.example", "/%2f%2fevil.example", "/\nmalformed"} {
		if got := localReturnPath(raw); got != "/" {
			t.Fatalf("unsafe return path %q accepted as %q", raw, got)
		}
	}
	for _, raw := range []string{"/archive", "/articles/owned?from=archive", "/?cursor=opaque"} {
		if got := localReturnPath(raw); got != raw {
			t.Fatalf("local return path lost %q", got)
		}
	}
}
