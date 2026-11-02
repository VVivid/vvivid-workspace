package identity

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"agentnewsfeed/internal/store/sqlite"
	"github.com/go-jose/go-jose/v4"
)

func gatewayManager(t *testing.T) (*Manager, *testIssuer, *rsa.PrivateKey) {
	t.Helper()
	direct, issuer, store, _ := testManager(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "gateway-key", Algorithm: "RS256", Use: "sig"}
	data, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{public}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "public.jwks.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := direct.cfg
	cfg.MCPAuthMode, cfg.MCPAudience = "gateway", "agent-newsfeed-mcp"
	cfg.GatewayIssuer, cfg.GatewayJWKSFile = "https://mcp.rd.lab.vvivid.dev", path
	cfg.MCPClientIDs = []string{"newsfeed-chatgpt"}
	m, err := New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	m.now = issuer.clock
	return m, issuer, key
}

func gatewayToken(t *testing.T, m *Manager, key *rsa.PrivateKey, typ, kid string, change func(map[string]any)) string {
	t.Helper()
	now := m.now().Unix()
	claims := map[string]any{"iss": m.cfg.GatewayIssuer, "aud": []string{m.cfg.MCPAudience}, "sub": "alice", "identity_issuer": m.cfg.Issuer,
		"client_id": "newsfeed-chatgpt", "scope": Read + " " + ArticlesWrite, "iat": now, "nbf": now, "exp": now + 60, "jti": "fixture"}
	if change != nil {
		change(claims)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGatewayIdentityClaimsOwnershipAndDisabledAccounts(t *testing.T) {
	m, issuer, key := gatewayManager(t)
	owner := enroll(t, m, "alice")
	// Same subject enrolled under gateway iss must never supply ownership.
	gatewayOwner, err := m.store.EnrollAccount(t.Context(), sqlite.ExternalIdentity{Issuer: m.cfg.GatewayIssuer, Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	raw := gatewayToken(t, m, key, gatewayTokenType, "gateway-key", nil)
	principal, err := m.AuthenticateBearer(t.Context(), raw, m.MCPAudience())
	if err != nil || principal.Owner() != owner || principal.Owner() == gatewayOwner || principal.Identity().Issuer != issuer.server.URL || principal.ClientID() != "newsfeed-chatgpt" || principal.Require(Read, ArticlesWrite) != nil {
		t.Fatalf("gateway ownership: %v", err)
	}
	for name, change := range map[string]func(map[string]any){
		"wrong gateway":           func(c map[string]any) { c["iss"] = "https://other.example" },
		"wrong original issuer":   func(c map[string]any) { c["identity_issuer"] = m.cfg.GatewayIssuer },
		"missing original issuer": func(c map[string]any) { delete(c, "identity_issuer") },
		"wrong audience":          func(c map[string]any) { c["aud"] = []string{"demo-a-mcp"} },
		"mixed audience":          func(c map[string]any) { c["aud"] = []string{m.MCPAudience(), "demo-a-mcp"} },
		"wrong client":            func(c map[string]any) { c["client_id"] = "rd-chatgpt-demo-a" },
		"browser client":          func(c map[string]any) { c["client_id"] = "newsfeed-web" },
		"unknown scope":           func(c map[string]any) { c["scope"] = Read + " demo-a:use" },
		"identity scope":          func(c map[string]any) { c["scope"] = Read + " openid" },
		"expired":                 func(c map[string]any) { c["exp"] = m.now().Unix() },
		"overlong":                func(c map[string]any) { c["exp"] = m.now().Unix() + 61 },
		"old issued":              func(c map[string]any) { c["iat"] = m.now().Unix() - 1 },
		"future issued":           func(c map[string]any) { c["iat"] = m.now().Unix() + 6 },
		"future nbf":              func(c map[string]any) { c["nbf"] = m.now().Unix() + 1 },
		"missing nbf":             func(c map[string]any) { delete(c, "nbf") },
		"missing iat":             func(c map[string]any) { delete(c, "iat") },
		"missing exp":             func(c map[string]any) { delete(c, "exp") },
		"extreme iat":             func(c map[string]any) { c["iat"] = int64(-1 << 63) },
		"empty subject":           func(c map[string]any) { c["sub"] = "" },
		"null subject":            func(c map[string]any) { c["sub"] = "alice\x00" },
		"unenrolled":              func(c map[string]any) { c["sub"] = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := gatewayToken(t, m, key, gatewayTokenType, "gateway-key", change)
			if _, err := m.AuthenticateBearer(t.Context(), raw, m.MCPAudience()); err == nil {
				t.Fatal("invalid gateway identity accepted")
			}
		})
	}
	for _, typ := range []string{"JWT", "at+jwt", ""} {
		if _, err := m.AuthenticateBearer(t.Context(), gatewayToken(t, m, key, typ, "gateway-key", nil), m.MCPAudience()); err == nil {
			t.Fatalf("wrong type %q accepted", typ)
		}
	}
	for _, kid := range []string{"", "untrusted"} {
		if _, err := m.AuthenticateBearer(t.Context(), gatewayToken(t, m, key, gatewayTokenType, kid, nil), m.MCPAudience()); err == nil {
			t.Fatal("unknown key accepted")
		}
	}
	forgedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AuthenticateBearer(t.Context(), gatewayToken(t, m, forgedKey, gatewayTokenType, "gateway-key", nil), m.MCPAudience()); err == nil {
		t.Fatal("forged identity accepted")
	}
	if _, err := m.AuthenticateBearer(t.Context(), issuer.bearer("alice", "newsfeed-chatgpt", m.MCPAudience(), Read, nil), m.MCPAudience()); err == nil {
		t.Fatal("external token accepted at gateway-only MCP")
	}
	if err := m.store.DisableAccount(t.Context(), principal.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AuthenticateBearer(t.Context(), raw, m.MCPAudience()); err == nil {
		t.Fatal("disabled owner accepted")
	}
}

func TestGatewayModeKeepsBrowserLoginRefreshAndAPITrust(t *testing.T) {
	m, issuer, key := gatewayManager(t)
	owner := enroll(t, m, "alice")
	cookie := loginAs(t, m, issuer, "alice")
	issuer.advance(61 * time.Second)
	principal, _, err := m.BrowserSession(sessionRequest(m, cookie))
	if err != nil || principal.Owner() != owner {
		t.Fatalf("gateway mode browser refresh: %v", err)
	}
	api := issuer.bearer("alice", "newsfeed-web", m.APIAudience(), Read, nil)
	if _, err := m.AuthenticateBearer(t.Context(), api, m.APIAudience()); err != nil {
		t.Fatalf("direct API verification: %v", err)
	}
	if _, err := m.AuthenticateBearer(t.Context(), gatewayToken(t, m, key, gatewayTokenType, "gateway-key", nil), m.APIAudience()); err == nil {
		t.Fatal("gateway credential accepted at direct API")
	}
	for path, status := range map[string]int{"/.well-known/oauth-protected-resource/mcp": 404, "/.well-known/oauth-protected-resource/api/v1": 200} {
		r := httptest.NewRecorder()
		testRoutes(m).ServeHTTP(r, httptest.NewRequest("GET", m.cfg.Origin+path, nil))
		if r.Code != status {
			t.Fatalf("metadata %s: %d", path, r.Code)
		}
	}
	handler := m.BearerMiddleware(m.MCPAudience(), []string{ArticlesWrite}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := FromContext(r.Context())
		if p.Owner() != owner {
			t.Fatal("header supplied owner replaced verified identity")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"", 401},
		{gatewayToken(t, m, key, gatewayTokenType, "gateway-key", func(c map[string]any) { c["scope"] = Read }), 403},
		{gatewayToken(t, m, key, gatewayTokenType, "gateway-key", nil), 204},
	} {
		r := httptest.NewRequest("POST", m.cfg.Origin+"/mcp", nil)
		r.Header.Set("X-Identity-Issuer", m.cfg.GatewayIssuer)
		r.Header.Set("X-User-Id", "mallory")
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("gateway response: %d challenge %q", w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestGatewayConfigurationAndPinnedPublicKeys(t *testing.T) {
	m, _, _ := gatewayManager(t)
	for name, change := range map[string]func(*Config){
		"unknown mode":        func(c *Config) { c.MCPAuthMode = "proxy" },
		"missing JWKS":        func(c *Config) { c.GatewayJWKSFile = "" },
		"wrong audience":      func(c *Config) { c.MCPAudience = c.Origin + "/mcp" },
		"issuer path":         func(c *Config) { c.GatewayIssuer += "/" },
		"direct with gateway": func(c *Config) { c.MCPAuthMode = "direct" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := m.cfg
			change(&cfg)
			if cfg.validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	keys := m.gatewayKeys
	for name, change := range map[string]func(*jose.JSONWebKeySet){
		"empty":           func(k *jose.JSONWebKeySet) { k.Keys = nil },
		"duplicate":       func(k *jose.JSONWebKeySet) { k.Keys = append(k.Keys, k.Keys[0]) },
		"missing kid":     func(k *jose.JSONWebKeySet) { k.Keys[0].KeyID = "" },
		"wrong algorithm": func(k *jose.JSONWebKeySet) { k.Keys[0].Algorithm = "RS512" },
		"encryption":      func(k *jose.JSONWebKeySet) { k.Keys[0].Use = "enc" },
		"private": func(k *jose.JSONWebKeySet) {
			private, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			k.Keys[0].Key = private
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyKeys := jose.JSONWebKeySet{Keys: slices.Clone(keys.Keys)}
			change(&copyKeys)
			data, err := json.Marshal(copyKeys)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "keys.json")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadGatewayKeys(path); err == nil {
				t.Fatal("invalid key set accepted")
			}
		})
	}
}

func TestLoadGatewayConfigFromEnvironment(t *testing.T) {
	m, _, _ := gatewayManager(t)
	settings := map[string]string{
		"NEWSFEED_ORIGIN": m.cfg.Origin, "NEWSFEED_OIDC_ISSUER": m.cfg.Issuer,
		"NEWSFEED_WEB_CLIENT_ID":          m.cfg.WebClientID,
		"NEWSFEED_WEB_CLIENT_SECRET_FILE": writePrivate(t, "client", []byte(m.cfg.WebClientSecret)),
		"NEWSFEED_SESSION_KEYS_FILE":      privateKeyFile(t),
		"NEWSFEED_API_AUDIENCE":           m.cfg.APIAudience, "NEWSFEED_MCP_AUDIENCE": m.cfg.MCPAudience,
		"NEWSFEED_MCP_CLIENT_IDS": "newsfeed-chatgpt", "NEWSFEED_MCP_AUTH_MODE": "gateway",
		"NEWSFEED_MCP_GATEWAY_ISSUER": m.cfg.GatewayIssuer, "NEWSFEED_MCP_GATEWAY_JWKS_FILE": m.cfg.GatewayJWKSFile,
	}
	lookup := func(name string) (string, bool) { value, present := settings[name]; return value, present }
	cfg, err := LoadConfig(lookup)
	if err != nil || cfg.MCPAuthMode != "gateway" || cfg.GatewayIssuer != m.cfg.GatewayIssuer || cfg.GatewayJWKSFile != m.cfg.GatewayJWKSFile {
		t.Fatalf("gateway environment config: %v", err)
	}
	delete(settings, "NEWSFEED_MCP_GATEWAY_ISSUER")
	if _, err := LoadConfig(lookup); err == nil {
		t.Fatal("partial gateway config accepted")
	}
}
