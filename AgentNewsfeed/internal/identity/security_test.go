package identity

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserClientRequiresExplicitNewsfeedPermissions(t *testing.T) {
	m, issuer, _, _ := testManager(t)
	enroll(t, m, "alice")
	for _, tc := range []struct {
		scope, client string
		allowed       bool
	}{
		{Read, "newsfeed-web", true},
		{Read + " " + ArticlesWrite, "newsfeed-web", true},
		{Read + " sshmcp:admin", "newsfeed-web", false},
		{Read, "newsfeed-codex", false},
	} {
		// The browser's API grant is only ever verified from its encrypted session.
		claims, err := m.verifySessionAccess(t.Context(), issuer.bearer("alice", tc.client, m.APIAudience(), tc.scope, nil))
		if (err == nil) != tc.allowed {
			t.Fatalf("browser grant %s accepted=%v", tc.scope, err == nil)
		}
		if err != nil {
			continue
		}
		principal, err := m.principal(t.Context(), claims)
		if err != nil || principal.Allows(ArticlesWrite) != (tc.scope == Read+" "+ArticlesWrite) {
			t.Fatal("browser write permission did not match its explicit grant")
		}
	}
	for _, audience := range []string{m.APIAudience(), m.MCPAudience()} {
		raw := issuer.bearer("alice", "newsfeed-web", audience, Read, nil)
		if _, err := m.AuthenticateBearer(t.Context(), raw, audience); err == nil {
			t.Fatalf("browser grant accepted in an Authorization header for %s", audience)
		}
	}
	writeOnly := issuer.bearer("alice", "newsfeed-codex", m.MCPAudience(), ArticlesWrite, nil)
	principal, err := m.AuthenticateBearer(t.Context(), writeOnly, m.MCPAudience())
	if err != nil || principal.Require(Read) == nil || principal.Require(ArticlesWrite) != nil {
		t.Fatal("write scope implied reading or was lost")
	}
}

// Keycloak lists offline_access in the scope claim when a client, such as the
// native app, asks for a long-lived refresh token. Like openid, profile and
// email it grants nothing on any path that verifies provider access tokens, and
// every other scope outside the newsfeed permissions is still forbidden.
func TestOfflineAccessGrantsNothingAndOtherUnknownScopesStayForbidden(t *testing.T) {
	browserOnly, issuer, _, _ := testManager(t)
	m := withNativeClients(t, browserOnly, nativeClient)
	enroll(t, m, "alice")
	for name, authenticate := range map[string]func(t *testing.T, scope string) (Principal, error){
		"native app bearer": func(t *testing.T, scope string) (Principal, error) {
			return m.AuthenticateBearer(t.Context(), issuer.bearer("alice", nativeClient, m.APIAudience(), scope, nil), m.APIAudience())
		},
		"direct MCP bearer": func(t *testing.T, scope string) (Principal, error) {
			return m.AuthenticateBearer(t.Context(), issuer.bearer("alice", "newsfeed-codex", m.MCPAudience(), scope, nil), m.MCPAudience())
		},
		"browser session grant": func(t *testing.T, scope string) (Principal, error) {
			claims, err := m.verifySessionAccess(t.Context(), issuer.bearer("alice", "newsfeed-web", m.APIAudience(), scope, nil))
			if err != nil {
				return Principal{}, err
			}
			return m.principal(t.Context(), claims)
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				scope       string
				read, write bool
			}{
				{"openid offline_access " + Read + " " + ArticlesWrite, true, true},
				{"offline_access profile email " + Read, true, false},
				{"offline_access", false, false},
			} {
				principal, err := authenticate(t, tc.scope)
				if err != nil || principal.Allows(Read) != tc.read || principal.Allows(ArticlesWrite) != tc.write {
					t.Fatalf("scope %q: read %v, write %v, error %v; want read %v, write %v, no error", tc.scope, principal.Allows(Read), principal.Allows(ArticlesWrite), err, tc.read, tc.write)
				}
			}
			// Every other scope is still forbidden. Scopes match exactly, so near
			// misses of offline_access are unknown too.
			for _, unknown := range []string{"newsfeed:admin", "offline", "OFFLINE_ACCESS"} {
				scope := "openid offline_access " + Read + " " + ArticlesWrite + " " + unknown
				if _, err := authenticate(t, scope); !errors.Is(err, ErrForbidden) {
					t.Fatalf("scope %q: error %v, want %v", scope, err, ErrForbidden)
				}
			}
		})
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
