package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	verifier, err := trust.NewVerifier(trust.Config{Issuer: "https://gateway.example", Audience: "service-a-backend", JWKSFile: publicFile})
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
		if r.Host != "service-a.example" || r.URL.Path != "/mcp" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-User") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("unsafe upstream request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=value")
		io.Copy(w, r.Body)
	}))
	t.Cleanup(upstream.Close)
	f.config = Config{Listen: "127.0.0.1:8080", Origin: "https://gateway.example", Issuer: issuer.URL, SigningKeyFile: privateFile, Routes: []Route{
		{Name: "service-a", Path: "/service-a/mcp", Upstream: upstream.URL + "/mcp", UpstreamHost: "service-a.example", Audience: "service-a-backend", RequiredScopes: []string{"service-a:use"}, ForwardScopes: []string{"service-a:use", "service-a:extra"}, ClientIDs: []string{"service-a-client"}, MaxTokenLifetimeSeconds: 300},
		{Name: "service-b", Path: "/service-b/mcp", Upstream: upstream.URL + "/mcp", Audience: "service-b-backend", RequiredScopes: []string{"service-b:use"}, ForwardScopes: []string{"service-b:use"}, ClientIDs: []string{"service-b-client"}, MaxTokenLifetimeSeconds: 300},
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
	c := map[string]any{"iss": f.issuer, "sub": "alice", "aud": []string{f.config.Origin + route}, "iat": now, "exp": now + 300, "scope": "service-a:use service-a:extra service-b:use"}
	typ := "at+jwt"
	if profile == "keycloak" {
		typ = "JWT"
		c["typ"] = "Bearer"
		c["azp"] = "service-a-client"
	} else {
		c["client_id"] = "service-a-client"
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
			w := request(f.server, "/service-a/mcp", f.token(t, profile, "/service-a/mcp", nil), body)
			if w.Code != 200 || w.Body.String() != body || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("response %d: %s", w.Code, w.Body.String())
			}
			p := <-f.seen
			if p.Subject != "alice" || p.Issuer != f.issuer || p.ClientID != "service-a-client" || p.Scope != "service-a:use service-a:extra" {
				t.Fatalf("identity or scope escalation: %+v", p)
			}
		})
	}
}
func TestAuthenticationAndRouteIsolation(t *testing.T) {
	f := newFixture(t)
	for name, change := range map[string]func(map[string]any){
		"issuer":   func(c map[string]any) { c["iss"] = "https://other.example" },
		"audience": func(c map[string]any) { c["aud"] = []string{f.config.Origin + "/service-b/mcp"} },
		"multiple audiences": func(c map[string]any) {
			c["aud"] = []string{f.config.Origin + "/service-a/mcp", f.config.Origin + "/service-b/mcp"}
		},
		"expired":         func(c map[string]any) { c["exp"] = time.Now().Unix() - 1 },
		"future":          func(c map[string]any) { c["nbf"] = time.Now().Unix() + 60 },
		"ID token":        func(c map[string]any) { delete(c, "typ") },
		"missing subject": func(c map[string]any) { delete(c, "sub") },
		"wrong client":    func(c map[string]any) { c["azp"] = "service-b-client" },
		"missing scope":   func(c map[string]any) { c["scope"] = "service-a:extra" },
	} {
		t.Run(name, func(t *testing.T) {
			w := request(f.server, "/service-a/mcp", f.token(t, "keycloak", "/service-a/mcp", change), `{}`)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("accepted: %d", w.Code)
			}
		})
	}
	if w := request(f.server, "/service-b/mcp", f.token(t, "keycloak", "/service-a/mcp", nil), `{}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if f.calls.Load() != 0 {
		t.Fatal("unauthorized request reached backend")
	}
}
func TestMetadataLimitsHostAndNoReplay(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, httptest.NewRequest("GET", "https://gateway.example/.well-known/oauth-protected-resource/service-a/mcp", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), f.config.Origin+"/service-a/mcp") {
		t.Fatal(w.Body.String())
	}
	token := f.token(t, "keycloak", "/service-a/mcp", nil)
	if w := request(f.server, "/service-a/mcp", token, strings.Repeat("x", MaxRequestBytes+1)); w.Code != 413 {
		t.Fatal(w.Code)
	}
	for _, header := range []string{"Host", "Origin"} {
		r := httptest.NewRequest("POST", "https://gateway.example/service-a/mcp", nil)
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
	if w := request(s, "/service-a/mcp", token, `{"method":"tools/call"}`); w.Code != 502 {
		t.Fatal(w.Code)
	}
	if count.Load() != 1 {
		t.Fatalf("write dispatched %d times", count.Load())
	}
}
func TestConfigRejectsAmbiguity(t *testing.T) {
	f := newFixture(t)
	for _, mutate := range []func(*Config){func(c *Config) { c.Routes[1].Path = c.Routes[0].Path }, func(c *Config) { c.Routes[0].RequiredScopes = nil }, func(c *Config) { c.Routes[0].Path = "/service-a/../service-b/mcp" }, func(c *Config) { c.Origin = "http://public.example" }, func(c *Config) { c.Routes[0].ClientIDs = nil }} {
		c := f.config
		c.Routes = slices.Clone(c.Routes)
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestConfigRejectsRoutePatternsBeforeMuxRegistration(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/service-a/{id}/mcp", "/service-a/{/mcp", "/service-a/}/mcp", "/service-a/\x00/mcp"} {
		t.Run(path, func(t *testing.T) {
			c := f.config
			c.Routes = slices.Clone(c.Routes)
			c.Routes[0].Path = path
			if c.Validate() == nil {
				t.Fatal("route pattern accepted as a fixed path")
			}
			if _, err := New(c, &OIDCAuthenticator{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
				t.Fatal("invalid route configuration accepted")
			}
		})
	}
}

func TestAccessProfilesRequireTheirClientClaim(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"keycloak", "rfc9068"} {
		t.Run(profile, func(t *testing.T) {
			token := f.token(t, profile, "/service-a/mcp", func(c map[string]any) {
				if profile == "keycloak" {
					delete(c, "azp")
					c["client_id"] = "service-a-client"
				} else {
					delete(c, "client_id")
					c["azp"] = "service-a-client"
				}
			})
			if w := request(f.server, "/service-a/mcp", token, `{}`); w.Code != http.StatusUnauthorized {
				t.Fatalf("incorrect profile accepted: %d", w.Code)
			}
		})
	}
	if f.calls.Load() != 0 {
		t.Fatal("invalid profile reached backend")
	}
}

func TestProxyStripsRequestAndResponseTrailers(t *testing.T) {
	f := newFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{}` {
			t.Errorf("upstream body: %q, %v", body, err)
		}
		if len(r.Trailer) != 0 || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-User") != "" {
			t.Error("client identity data reached backend")
		}
		w.Header().Set("Trailer", "Set-Cookie, X-Forwarded-User")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
		w.Header().Set("Set-Cookie", "private=backend")
		w.Header().Set("X-Forwarded-User", "private-backend-user")
	}))
	defer upstream.Close()
	f.config.Routes[0].Upstream = upstream.URL + "/mcp"
	gateway := httptest.NewServer(f.build(t))
	defer gateway.Close()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+"/service-a/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "gateway.example"
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer "+f.token(t, "rfc9068", "/service-a/mcp", nil))
	r.Trailer = http.Header{"Cookie": {"private=client"}, "X-Forwarded-User": {"mallory"}}
	resp, err := gateway.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != `{}` {
		t.Fatalf("gateway response: %d %q, %v", resp.StatusCode, body, err)
	}
	if len(resp.Trailer) != 0 || resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("X-Forwarded-User") != "" {
		t.Fatal("backend metadata reached client through trailers")
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
	r := httptest.NewRequest("POST", f.config.Origin+"/service-a/mcp", strings.NewReader(`{}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+f.token(t, "keycloak", "/service-a/mcp", nil))
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
	token := f.token(t, "rfc9068", "/service-a/mcp", func(c map[string]any) { c["exp"] = expiry })
	w := request(f.server, "/service-a/mcp", token, `{}`)
	if w.Code != 200 {
		t.Fatalf("request: %d", w.Code)
	}
	if p := <-f.seen; p.ExpiresAt.Unix() > expiry {
		t.Fatal("gateway extended the external grant")
	}
}

func TestExternalTokenLifetimeBeforeDispatch(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"keycloak", "rfc9068"} {
		for name, change := range map[string]func(map[string]any){
			"long remaining lifetime": func(c map[string]any) { c["exp"] = time.Now().Unix() + 86400 },
			"long issued lifetime near expiry": func(c map[string]any) {
				c["iat"] = time.Now().Unix() - 3600
				c["exp"] = time.Now().Unix() + 10
			},
			"future issuance":  func(c map[string]any) { c["iat"] = time.Now().Unix() + 10 },
			"missing issuance": func(c map[string]any) { delete(c, "iat") },
			"missing expiry":   func(c map[string]any) { delete(c, "exp") },
			"zero issuance":    func(c map[string]any) { c["iat"] = 0 },
			"extreme issuance": func(c map[string]any) { c["iat"] = int64(-1 << 63) },
			"extreme expiry":   func(c map[string]any) { c["exp"] = int64(1<<63 - 1) },
		} {
			t.Run(profile+"/"+name, func(t *testing.T) {
				w := request(f.server, "/service-a/mcp", f.token(t, profile, "/service-a/mcp", change), `{}`)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("invalid external lifetime accepted: %d", w.Code)
				}
			})
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("invalid lifetime reached backend")
	}
}

func TestRouteTokenLifetimePolicy(t *testing.T) {
	f := newFixture(t)
	for _, limit := range []int64{0, -1} {
		c := f.config
		c.Routes = slices.Clone(c.Routes)
		c.Routes[0].MaxTokenLifetimeSeconds = limit
		if c.Validate() == nil {
			t.Fatalf("accepted token lifetime policy %d", limit)
		}
	}
	// The limit belongs to the route, independent of the provider profile.
	f.config.Routes[0].MaxTokenLifetimeSeconds = 30
	s := f.build(t)
	for _, profile := range []string{"keycloak", "rfc9068"} {
		if w := request(s, "/service-a/mcp", f.token(t, profile, "/service-a/mcp", nil), `{}`); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s ignored route policy: %d", profile, w.Code)
		}
		short := f.token(t, profile, "/service-a/mcp", func(c map[string]any) { c["exp"] = c["iat"].(int64) + 30 })
		if w := request(s, "/service-a/mcp", short, `{}`); w.Code != http.StatusOK {
			t.Fatalf("%s rejected bounded grant: %d", profile, w.Code)
		}
	}
}

type fixedAuthenticator struct{ principal trust.Principal }

func (a fixedAuthenticator) Authenticate(context.Context, string, string) (trust.Principal, error) {
	return a.principal, nil
}

func TestCoreRejectsAuthenticatorWithoutValidTiming(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	valid := trust.Principal{Issuer: f.issuer, Subject: "alice", ClientID: "service-a-client", Scope: "service-a:use", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
	for name, mutate := range map[string]func(*trust.Principal){
		"missing issuance": func(p *trust.Principal) { p.IssuedAt = time.Time{} },
		"missing expiry":   func(p *trust.Principal) { p.ExpiresAt = time.Time{} },
		"future issuance":  func(p *trust.Principal) { p.IssuedAt = now.Add(time.Second * 30) },
		"long lifetime":    func(p *trust.Principal) { p.IssuedAt = now.Add(-time.Hour) },
		"extreme expiry":   func(p *trust.Principal) { p.ExpiresAt = time.Unix(1<<63-1, 0) },
		"expired":          func(p *trust.Principal) { p.ExpiresAt = now.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			mutate(&p)
			s, err := New(f.config, fixedAuthenticator{p}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if w := request(s, "/service-a/mcp", "synthetic", `{}`); w.Code != http.StatusUnauthorized {
				t.Fatalf("invalid normalized timing accepted: %d", w.Code)
			}
			if _, err := s.issue(p, f.config.Routes[0]); err == nil {
				t.Fatal("invalid identity issued at dispatch")
			}
		})
	}
	if f.calls.Load() != 0 {
		t.Fatal("invalid normalized timing reached backend")
	}
}

// Send distinct informational responses with metadata that must stay behind the
// gateway. Clear the server's header map between responses, just as real
// backends may send independent hints before their final response.
func writeBackendInformationalResponses(w http.ResponseWriter) {
	for _, code := range []int{http.StatusContinue, http.StatusProcessing, http.StatusEarlyHints, http.StatusEarlyHints} {
		w.Header().Set("Set-Cookie", "private=backend")
		w.Header().Set("Location", "http://private-backend.example/mcp")
		w.Header().Set("WWW-Authenticate", `Bearer realm="private-backend"`)
		w.Header().Set("Access-Control-Allow-Origin", "https://private-backend.example")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Mcp-Session-Id", "private-backend-session")
		w.Header().Set("Trailer", "Set-Cookie")
		w.Header().Set("Link", "<http://private-backend.example/assets>; rel=preload")
		w.Header().Set("X-Backend-Hint", strconv.Itoa(code))
		w.WriteHeader(code)
		clear(w.Header())
	}
}

func TestProxyDiscardsInformationalMetadata(t *testing.T) {
	f := newFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if string(body) != `{"hints":false}` {
			writeBackendInformationalResponses(w)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private")
		w.Header().Set("X-Content-Type-Options", "backend-option")
		w.Header().Set("X-Backend-Result", "ok")
		w.Header().Set("Set-Cookie", "private=final")
		w.Header().Set("Location", "http://private-backend.example/final")
		w.Header().Set("WWW-Authenticate", `Bearer realm="private-final"`)
		w.Header().Set("Access-Control-Allow-Origin", "https://private-backend.example")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Trailer", "Set-Cookie, X-Forwarded-User")
		w.Write(body)
		w.Header().Set("Set-Cookie", "private=trailer")
		w.Header().Set("X-Forwarded-User", "private-trailer-user")
	}))
	defer upstream.Close()
	f.config.Routes[0].Upstream = upstream.URL + "/mcp"
	server := f.build(t)
	var gotConn, gotFirstByte, gotInformational atomic.Int32
	// Reuse one trace across requests: the gateway must compose fresh hooks,
	// without replacing shared instrumentation or invoking it twice.
	sharedTrace := &httptrace.ClientTrace{
		GotConn:              func(httptrace.GotConnInfo) { gotConn.Add(1) },
		GotFirstResponseByte: func() { gotFirstByte.Add(1) },
		Got1xxResponse: func(_ int, header textproto.MIMEHeader) error {
			gotInformational.Add(1)
			if len(header) != 0 {
				t.Error("backend informational metadata reached inherited trace")
			}
			return nil
		},
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeHTTP(w, r.WithContext(httptrace.WithClientTrace(r.Context(), sharedTrace)))
	}))
	defer gateway.Close()
	token := f.token(t, "rfc9068", "/service-a/mcp", nil)
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+"/service-a/mcp", strings.NewReader(`{"hints":false}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "gateway.example"
	r.Header.Set("Authorization", "Bearer "+token)
	baseline, err := gateway.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, baseline.Body)
	baseline.Body.Close()
	if readErr != nil || baseline.StatusCode != http.StatusOK || baseline.Header.Get("Cache-Control") != "no-store" || baseline.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("response without informational headers: %d %+v, %v", baseline.StatusCode, baseline.Header, readErr)
	}
	const requests = 8
	t.Run("concurrent requests", func(t *testing.T) {
		for i := range requests {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				t.Parallel()
				var codes []int
				trace := &httptrace.ClientTrace{Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
					codes = append(codes, code)
					for name := range header {
						if name != "Cache-Control" && name != "X-Content-Type-Options" {
							t.Errorf("backend informational header %s reached client", name)
						}
					}
					return nil
				}}
				body := `{"id":` + strconv.Itoa(i) + `}`
				r, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodPost, gateway.URL+"/service-a/mcp", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				r.Host = "gateway.example"
				r.Header.Set("Authorization", "Bearer "+token)
				resp, err := gateway.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				got, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusOK || string(got) != body {
					t.Fatalf("final response: %d %q, %v", resp.StatusCode, got, err)
				}
				if !slices.Equal(codes, []int{http.StatusContinue, http.StatusProcessing, http.StatusEarlyHints, http.StatusEarlyHints}) {
					t.Errorf("informational status sequence = %v", codes)
				}
				if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("X-Backend-Result") != "ok" || !slices.Contains(resp.Header.Values("Cache-Control"), "private") {
					t.Error("final backend headers changed")
				}
				for _, name := range []string{"Cache-Control", "X-Content-Type-Options"} {
					if !slices.Equal(resp.Header.Values(name), baseline.Header.Values(name)) || resp.Header.Get(name) != baseline.Header.Get(name) {
						t.Errorf("final %s values = %v, without informational responses = %v", name, resp.Header.Values(name), baseline.Header.Values(name))
					}
				}
				for _, name := range []string{"Set-Cookie", "Location", "WWW-Authenticate", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Mcp-Session-Id", "Trailer", "Link", "X-Backend-Hint", "X-Forwarded-User"} {
					if resp.Header.Get(name) != "" {
						t.Errorf("backend header %s reached final response", name)
					}
				}
				if len(resp.Trailer) != 0 {
					t.Error("backend trailers reached client")
				}
			})
		}
	})
	if gotConn.Load() != requests+1 || gotFirstByte.Load() != requests+1 || gotInformational.Load() != requests*4 {
		t.Fatalf("trace calls: connections=%d, first bytes=%d, informational=%d", gotConn.Load(), gotFirstByte.Load(), gotInformational.Load())
	}
}

func TestProxyPreservesInformationalTraceError(t *testing.T) {
	f := newFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBackendInformationalResponses(w)
		w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	f.config.Routes[0].Upstream = upstream.URL + "/mcp"
	server := f.build(t)
	var calls atomic.Int32
	trace := &httptrace.ClientTrace{Got1xxResponse: func(_ int, header textproto.MIMEHeader) error {
		calls.Add(1)
		if len(header) != 0 {
			t.Error("backend informational metadata reached inherited trace")
		}
		return errors.New("instrumentation stopped request")
	}}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeHTTP(w, r.WithContext(httptrace.WithClientTrace(r.Context(), trace)))
	}))
	defer gateway.Close()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+"/service-a/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "gateway.example"
	r.Header.Set("Authorization", "Bearer "+f.token(t, "rfc9068", "/service-a/mcp", nil))
	resp, err := gateway.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "Do not replay writes automatically") || calls.Load() != 1 {
		t.Fatalf("trace error response: %d %q, calls=%d, %v", resp.StatusCode, body, calls.Load(), err)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("gateway error response policy lost after informational response")
	}
}

func TestInformationalResponsesPreserveSSEStreaming(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBackendInformationalResponses(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)
	f.config.Routes[0].Upstream = upstream.URL + "/mcp"
	gateway := httptest.NewServer(f.build(t))
	defer gateway.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/service-a/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "gateway.example"
	r.Header.Set("Authorization", "Bearer "+f.token(t, "rfc9068", "/service-a/mcp", nil))
	resp, err := gateway.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" || line != "data: first\n" {
		t.Fatalf("streamed response: %d %q, %v", resp.StatusCode, line, err)
	}
}
