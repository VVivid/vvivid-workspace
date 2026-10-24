package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type fixture struct {
	config Config
	server *Server
	key    jose.JSONWebKey
	issuer string
	calls  atomic.Int32
	seen   chan trust.Principal
}

func writeJSON(t *testing.T, file string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "jwks_uri": issuer.URL + "/keys", "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token"})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.Close)
	dir := t.TempDir()
	privateFile := filepath.Join(dir, "private.json")
	publicFile := filepath.Join(dir, "public.json")
	writeJSON(t, privateFile, jwk)
	writeJSON(t, publicFile, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	f := &fixture{key: jwk, issuer: issuer.URL, seen: make(chan trust.Principal, 32)}
	verifier, err := trust.NewVerifier(trust.Config{Issuer: "https://gateway.example", Audience: "shopping-backend", JWKSFile: publicFile})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		p, err := verifier.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil {
			http.Error(w, "invalid", 401)
			return
		}
		f.seen <- p
		if r.Host != "shopping.example" || r.URL.Path != "/mcp" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-User") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("unsafe upstream request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=value")
		io.Copy(w, r.Body)
	}))
	t.Cleanup(upstream.Close)
	f.config = Config{Listen: "127.0.0.1:8080", Origin: "https://gateway.example", Issuer: issuer.URL, SigningKeyFile: privateFile, Routes: []Route{
		{Name: "shopping", Path: "/shopping/mcp", Upstream: upstream.URL + "/mcp", UpstreamHost: "shopping.example", Audience: "shopping-backend", RequiredScopes: []string{"shopping:read"}, ForwardScopes: []string{"shopping:read", "shopping:lists:write"}, ClientIDs: []string{"shopping-client"}},
		{Name: "ssh", Path: "/ssh/mcp", Upstream: upstream.URL + "/mcp", Audience: "ssh-backend", RequiredScopes: []string{"sshmcp:admin"}, ForwardScopes: []string{"sshmcp:admin"}, ClientIDs: []string{"ssh-client"}},
	}}
	f.server = f.build(t)
	return f
}
func (f *fixture) build(t *testing.T) *Server {
	t.Helper()
	a, err := NewOIDCAuthenticator(t.Context(), f.issuer)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(f.config, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f *fixture) token(t *testing.T, profile, route string, change func(map[string]any)) string {
	t.Helper()
	now := time.Now().Unix()
	c := map[string]any{"iss": f.issuer, "sub": "alice", "aud": []string{f.config.Origin + route}, "iat": now, "exp": now + 300, "scope": "shopping:read shopping:lists:write sshmcp:admin"}
	typ := "at+jwt"
	if profile == "keycloak" {
		typ = "JWT"
		c["typ"] = "Bearer"
		c["azp"] = "shopping-client"
	} else {
		c["client_id"] = "shopping-client"
	}
	if change != nil {
		change(c)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType(jose.ContentType(typ)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func request(s http.Handler, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://gateway.example"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Cookie", "secret=cookie")
	r.Header.Set("X-Forwarded-User", "mallory")
	r.Header.Set("Idempotency-Key", "must-not-forward")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestProviderProfilesAndIdentityForwarding(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"keycloak", "rfc9068"} {
		t.Run(profile, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
			w := request(f.server, "/shopping/mcp", f.token(t, profile, "/shopping/mcp", nil), body)
			if w.Code != 200 || w.Body.String() != body || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("response %d: %s", w.Code, w.Body.String())
			}
			p := <-f.seen
			if p.Subject != "alice" || p.Issuer != f.issuer || p.ClientID != "shopping-client" || p.Scope != "shopping:read shopping:lists:write" {
				t.Fatalf("identity or scope escalation: %+v", p)
			}
		})
	}
}
func TestAuthenticationAndRouteIsolation(t *testing.T) {
	f := newFixture(t)
	for name, change := range map[string]func(map[string]any){
		"issuer":   func(c map[string]any) { c["iss"] = "https://other.example" },
		"audience": func(c map[string]any) { c["aud"] = []string{f.config.Origin + "/ssh/mcp"} },
		"multiple audiences": func(c map[string]any) {
			c["aud"] = []string{f.config.Origin + "/shopping/mcp", f.config.Origin + "/ssh/mcp"}
		},
		"expired":         func(c map[string]any) { c["exp"] = time.Now().Unix() - 1 },
		"future":          func(c map[string]any) { c["nbf"] = time.Now().Unix() + 60 },
		"ID token":        func(c map[string]any) { delete(c, "typ") },
		"missing subject": func(c map[string]any) { delete(c, "sub") },
		"wrong client":    func(c map[string]any) { c["azp"] = "ssh-client" },
		"missing scope":   func(c map[string]any) { c["scope"] = "shopping:lists:write" },
	} {
		t.Run(name, func(t *testing.T) {
			w := request(f.server, "/shopping/mcp", f.token(t, "keycloak", "/shopping/mcp", change), `{}`)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("accepted: %d", w.Code)
			}
		})
	}
	if w := request(f.server, "/ssh/mcp", f.token(t, "keycloak", "/shopping/mcp", nil), `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if f.calls.Load() != 0 {
		t.Fatal("unauthorized request reached backend")
	}
}
func TestMetadataLimitsHostAndNoReplay(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, httptest.NewRequest("GET", "https://gateway.example/.well-known/oauth-protected-resource/shopping/mcp", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), f.config.Origin+"/shopping/mcp") {
		t.Fatal(w.Body.String())
	}
	token := f.token(t, "keycloak", "/shopping/mcp", nil)
	if w := request(f.server, "/shopping/mcp", token, strings.Repeat("x", MaxRequestBytes+1)); w.Code != 413 {
		t.Fatal(w.Code)
	}
	for _, header := range []string{"Host", "Origin"} {
		r := httptest.NewRequest("POST", "https://gateway.example/shopping/mcp", nil)
		if header == "Host" {
			r.Host = "evil.example"
		} else {
			r.Header.Set(header, "https://evil.example")
		}
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != 403 && w.Code != 421 {
			t.Fatal(w.Code)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("invalid request dispatched")
	}
	var count atomic.Int32
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer broken.Close()
	f.config.Routes[0].Upstream = broken.URL + "/mcp"
	s := f.build(t)
	if w := request(s, "/shopping/mcp", token, `{"method":"tools/call"}`); w.Code != 502 {
		t.Fatal(w.Code)
	}
	if count.Load() != 1 {
		t.Fatalf("write dispatched %d times", count.Load())
	}
}
func TestConfigRejectsAmbiguity(t *testing.T) {
	f := newFixture(t)
	for _, mutate := range []func(*Config){func(c *Config) { c.Routes[1].Path = c.Routes[0].Path }, func(c *Config) { c.Routes[0].RequiredScopes = nil }, func(c *Config) { c.Routes[0].Path = "/shopping/../ssh/mcp" }, func(c *Config) { c.Origin = "http://public.example" }, func(c *Config) { c.Routes[0].ClientIDs = nil }} {
		c := f.config
		c.Routes = append([]Route{}, c.Routes...)
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
func TestCancellationReachesBackend(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	f.config.Routes[0].Upstream = upstream.URL + "/mcp"
	s := f.build(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest("POST", f.config.Origin+"/shopping/mcp", strings.NewReader(`{}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+f.token(t, "keycloak", "/shopping/mcp", nil))
	done := make(chan struct{})
	go func() { s.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not dispatched")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("backend not cancelled")
	}
	<-done
}

func TestForwardedIdentityExpiryDoesNotExtendGrant(t *testing.T) {
	f := newFixture(t)
	expiry := time.Now().Add(5 * time.Second).Unix()
	token := f.token(t, "rfc9068", "/shopping/mcp", func(c map[string]any) { c["exp"] = expiry })
	w := request(f.server, "/shopping/mcp", token, `{}`)
	if w.Code != 200 {
		t.Fatalf("request: %d", w.Code)
	}
	if p := <-f.seen; p.ExpiresAt.Unix() > expiry {
		t.Fatal("gateway extended the external grant")
	}
}
