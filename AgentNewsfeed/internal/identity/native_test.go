package identity

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const nativeClient = "newsfeed-android"

// withNativeClients builds a second manager over the same issuer, store and
// session keys with native app clients configured. The original manager keeps
// the default configuration, in which native app access is disabled.
func withNativeClients(t *testing.T, m *Manager, clients ...string) *Manager {
	t.Helper()
	cfg := m.cfg
	cfg.NativeClientIDs = clients
	native, err := New(t.Context(), cfg, m.store)
	if err != nil {
		t.Fatal(err)
	}
	native.now = m.now
	return native
}

func TestNativeAppTokensAuthenticateOnlyFromNativeClientsAtTheAPI(t *testing.T) {
	browserOnly, issuer, _, _ := testManager(t)
	m := withNativeClients(t, browserOnly, nativeClient)
	alice := enroll(t, m, "alice")
	token := issuer.bearer("alice", nativeClient, m.APIAudience(), Read, nil)
	principal, err := m.AuthenticateBearer(t.Context(), token, m.APIAudience())
	if err != nil || principal.Owner() != alice || principal.ClientID() != nativeClient || principal.Require(Read) != nil || principal.Allows(ArticlesWrite) {
		t.Fatalf("native app principal: %v", err)
	}
	now := issuer.clock()
	for name, change := range map[string]func(map[string]any){
		"browser client":         func(c map[string]any) { c["azp"] = "newsfeed-web" },
		"MCP client":             func(c map[string]any) { c["azp"] = "newsfeed-codex" },
		"unknown client":         func(c map[string]any) { c["azp"] = "newsfeed-desktop" },
		"missing client":         func(c map[string]any) { delete(c, "azp") },
		"wrong issuer":           func(c map[string]any) { c["iss"] = issuer.server.URL + "/other" },
		"MCP audience":           func(c map[string]any) { c["aud"] = []string{m.MCPAudience()} },
		"mixed audience":         func(c map[string]any) { c["aud"] = []string{m.APIAudience(), m.MCPAudience()} },
		"expired":                func(c map[string]any) { c["exp"] = now.Unix() },
		"long lifetime":          func(c map[string]any) { c["exp"] = now.Add(6 * time.Minute).Unix() },
		"future issued":          func(c map[string]any) { c["iat"] = now.Add(time.Minute).Unix() },
		"not yet valid":          func(c map[string]any) { c["nbf"] = now.Add(time.Second).Unix() },
		"ID token":               func(c map[string]any) { c["typ"] = "ID" },
		"SSH scope":              func(c map[string]any) { c["scope"] = Read + " sshmcp:admin" },
		"unknown newsfeed scope": func(c map[string]any) { c["scope"] = Read + " newsfeed:admin" },
		"empty subject":          func(c map[string]any) { c["sub"] = "" },
		"unenrolled":             func(c map[string]any) { c["sub"] = "not-enrolled" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := issuer.bearer("alice", nativeClient, m.APIAudience(), Read, change)
			if _, err := m.AuthenticateBearer(t.Context(), raw, m.APIAudience()); err == nil {
				t.Fatal("invalid native app token accepted")
			}
		})
	}
	if _, err := m.verifySessionAccess(t.Context(), token); err == nil {
		t.Fatal("browser session accepted a native app token")
	}
	if _, err := m.AuthenticateBearer(t.Context(), issuer.bearer("alice", nativeClient, m.MCPAudience(), Read, nil), m.MCPAudience()); err == nil {
		t.Fatal("MCP accepted a native app token")
	}
	if _, err := browserOnly.AuthenticateBearer(t.Context(), token, browserOnly.APIAudience()); err == nil {
		t.Fatal("API accepted a native app token without configured native app clients")
	}
}

func TestBrowserOrNativeAppMiddlewareUsesExactlyOneCredential(t *testing.T) {
	browserOnly, issuer, _, _ := testManager(t)
	m := withNativeClients(t, browserOnly, nativeClient)
	enroll(t, m, "alice")
	cookie := loginAs(t, m, issuer, "alice")
	nativeToken := issuer.bearer("alice", nativeClient, m.APIAudience(), Read, nil)
	browserToken := issuer.bearer("alice", "newsfeed-web", m.APIAudience(), Read, nil)
	writeOnly := issuer.bearer("alice", nativeClient, m.APIAudience(), ArticlesWrite, nil)
	challenge := `Bearer resource_metadata="` + m.cfg.Origin + `/.well-known/oauth-protected-resource/api/v1", scope="` + Read + `"`
	// serve reports the response and the client of the principal that reached the route.
	serve := func(auth *Manager, cookie *http.Cookie, authorization ...string) (*httptest.ResponseRecorder, string) {
		var client string
		handler := auth.RestrictOrigin(auth.BrowserOrNativeAppMiddleware([]string{Read}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, _ := FromContext(r.Context())
			client = principal.ClientID()
			w.WriteHeader(http.StatusNoContent)
		})))
		request := httptest.NewRequest(http.MethodGet, auth.cfg.Origin+"/api/v1/articles", nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		for _, value := range authorization {
			request.Header.Add("Authorization", value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response, client
	}
	for _, tc := range []struct {
		name          string
		cookie        *http.Cookie
		authorization []string
		status        int
		client        string
		challenge     string
	}{
		{"native app token", nil, []string{"Bearer " + nativeToken}, 204, nativeClient, ""},
		{"browser session", cookie, nil, 204, "newsfeed-web", ""},
		{"header decides over a valid session", cookie, []string{"Bearer " + nativeToken}, 204, nativeClient, ""},
		{"browser token in a header", nil, []string{"Bearer " + browserToken}, 401, "", challenge},
		{"rejected header never falls back to the session", cookie, []string{"Bearer " + browserToken}, 401, "", challenge},
		{"empty header never falls back to the session", cookie, []string{""}, 401, "", challenge},
		{"other scheme never falls back to the session", cookie, []string{"Basic " + nativeToken}, 401, "", challenge},
		{"repeated header", nil, []string{"Bearer " + nativeToken, "Bearer " + nativeToken}, 401, "", challenge},
		{"missing read scope", nil, []string{"Bearer " + writeOnly}, 403, "", challenge + `, error="insufficient_scope"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, client := serve(m, tc.cookie, tc.authorization...)
			if response.Code != tc.status || client != tc.client || response.Header().Get("WWW-Authenticate") != tc.challenge {
				t.Fatalf("status %d client %q challenge %q", response.Code, client, response.Header().Get("WWW-Authenticate"))
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("bearer authentication changed browser cookies")
			}
		})
	}
	// Browser failures keep the sign-in response and clear the session cookie.
	response, _ := serve(m, nil)
	cleared := response.Result().Cookies()
	if response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" || len(cleared) != 1 || cleared[0].Name != sessionCookie || cleared[0].MaxAge >= 0 {
		t.Fatalf("browser failure response changed: status %d", response.Code)
	}
	// Without native app clients the route is browser-only and ignores Authorization, as before.
	if response, _ := serve(browserOnly, nil, "Bearer "+nativeToken); response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("disabled native app access answered a bearer token: status %d", response.Code)
	}
	if response, client := serve(browserOnly, cookie, "Bearer "+nativeToken); response.Code != 204 || client != "newsfeed-web" {
		t.Fatalf("disabled native app access stopped ignoring Authorization: status %d", response.Code)
	}
}

func TestNativeAppClientConfiguration(t *testing.T) {
	settings := map[string]string{
		"NEWSFEED_ORIGIN": "https://newsfeed.example", "NEWSFEED_OIDC_ISSUER": "https://issuer.example/realms/newsfeed",
		"NEWSFEED_WEB_CLIENT_ID": "newsfeed-web", "NEWSFEED_WEB_CLIENT_SECRET_FILE": writePrivate(t, "secret", []byte(rand.Text())),
		"NEWSFEED_API_AUDIENCE": "https://newsfeed.example/api/v1", "NEWSFEED_MCP_AUDIENCE": "https://newsfeed.example/mcp",
		"NEWSFEED_MCP_CLIENT_IDS": "newsfeed-codex,newsfeed-chatgpt", "NEWSFEED_SESSION_KEYS_FILE": privateKeyFile(t),
	}
	lookup := func(name string) (string, bool) { value, ok := settings[name]; return value, ok }
	if cfg, err := LoadConfig(lookup); err != nil || cfg.NativeClientIDs != nil {
		t.Fatalf("unset native app clients: %v", err)
	}
	clients := make([]string, 17)
	for i := range clients {
		clients[i] = fmt.Sprintf("newsfeed-native-%d", i)
	}
	for value, want := range map[string][]string{
		"":                              nil,
		nativeClient:                    {nativeClient},
		nativeClient + ",newsfeed-ios":  {nativeClient, "newsfeed-ios"},
		strings.Join(clients[:16], ","): clients[:16],
	} {
		settings["NEWSFEED_NATIVE_CLIENT_IDS"] = value
		if cfg, err := LoadConfig(lookup); err != nil || !slices.Equal(cfg.NativeClientIDs, want) {
			t.Fatalf("native app clients %q: %v", value, err)
		}
	}
	for name, invalid := range map[string]string{
		"web client":        "newsfeed-web",
		"MCP client":        nativeClient + ",newsfeed-chatgpt",
		"duplicate":         nativeClient + "," + nativeClient,
		"empty entry":       nativeClient + ",",
		"surrounding space": nativeClient + ", newsfeed-ios",
		"seventeen clients": strings.Join(clients, ","),
	} {
		t.Run(name, func(t *testing.T) {
			settings["NEWSFEED_NATIVE_CLIENT_IDS"] = invalid
			if _, err := LoadConfig(lookup); err == nil {
				t.Fatal("invalid native app clients accepted")
			}
		})
	}
	// Like the other optional identity settings, it cannot configure identity alone.
	onlyNative := func(name string) (string, bool) {
		if name == "NEWSFEED_NATIVE_CLIENT_IDS" {
			return nativeClient, true
		}
		return "", false
	}
	if _, err := LoadConfig(onlyNative); err == nil {
		t.Fatal("native app clients without the identity bundle accepted")
	}
}
