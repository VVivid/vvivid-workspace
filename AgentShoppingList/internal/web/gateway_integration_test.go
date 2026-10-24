package web_test

import (
	"agentshoppinglist/internal/gatewaytrust"
	"agentshoppinglist/internal/identity"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/go-jose/go-jose/v4"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startSharedGateway runs the independently built production gateway binary.
// All keys, identities, listeners and databases in this test are synthetic.
func startSharedGateway(t *testing.T, issuer, backend, host, scope, client, audience string) (string, string, string) {
	t.Helper()
	binary := os.Getenv("MCP_GATEWAY_BIN")
	if binary == "" {
		t.Skip("set MCP_GATEWAY_BIN to run cross-project acceptance")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "gateway-integration", Algorithm: "RS256", Use: "sig"}
	dir := t.TempDir()
	privateFile := filepath.Join(dir, "private.json")
	publicFile := filepath.Join(dir, "public.json")
	configFile := filepath.Join(dir, "config.json")
	write := func(file string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(privateFile, jwk)
	write(publicFile, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	origin := "http://" + address
	write(configFile, map[string]any{"listen": address, "origin": origin, "issuer": issuer, "signing_key_file": privateFile, "routes": []any{map[string]any{"name": "test", "path": "/service/mcp", "upstream": backend, "upstream_host": host, "audience": audience, "max_token_lifetime_seconds": 300, "required_scopes": []string{scope}, "forward_scopes": []string{scope}, "client_ids": []string{client}}}})
	command := exec.Command(binary, "--config", configFile)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			command.Process.Kill()
			<-done
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(origin + "/health/ready")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return origin, publicFile, origin + "/service/mcp"
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("gateway failed readiness")
	return "", "", ""
}

func TestSharedGatewayShoppingMCP(t *testing.T) {
	if os.Getenv("MCP_GATEWAY_BIN") == "" {
		t.Skip("cross-project acceptance requires gateway binary")
	}
	f, service, _, product, _ := setup(t)
	var handler http.Handler
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.Header.Get("Mcp-Protocol-Version") != "2026-07-28" || r.Header.Get("Mcp-Method") == "" {
				t.Error("Shopping backend did not receive current MCP protocol metadata")
			}
			if r.Header.Get("Mcp-Method") == "tools/call" && r.Header.Get("Mcp-Name") == "" {
				t.Error("Shopping backend did not receive tool name")
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer backend.Close()
	settings := f.Environment()
	origin, publicFile, resource := startSharedGateway(t, settings["SHOPPING_OIDC_ISSUER"], backend.URL+"/mcp", "shopping.example", identity.Read, "shopping-codex", "shopping-backend")
	cfg, err := identity.LoadConfig(func(name string) (string, bool) { v, ok := settings[name]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway = &gatewaytrust.Config{Issuer: origin, Audience: "shopping-backend", JWKSFile: publicFile}
	f.Manager, err = identity.New(t.Context(), *cfg, f.Store)
	if err != nil {
		t.Fatal(err)
	}
	handler = newHandler(t, f, service)
	connect := func(subject string) *sdk.ClientSession {
		token := f.Bearer(subject, "shopping-codex", resource, identity.Read)
		client := sdk.NewClient(&sdk.Implementation{Name: "gateway-test", Version: "1"}, nil)
		session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: resource, HTTPClient: &http.Client{Transport: gatewayClientTransport{token: token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
			t.Fatalf("Shopping negotiated protocol %q, want 2026-07-28", got)
		}
		return session
	}
	alice := connect("alice")
	catalog, err := alice.ListTools(t.Context(), nil)
	if err != nil || len(catalog.Tools) == 0 {
		t.Fatalf("catalog: %v", err)
	}
	result := call(t, alice, "get_product", map[string]any{"product_id": product.ID})
	if result.IsError {
		t.Fatal("owner read failed")
	}
	bob := connect("bob")
	result = call(t, bob, "get_product", map[string]any{"product_id": product.ID})
	if !result.IsError {
		t.Fatal("gateway bypassed ownership")
	}
	result = call(t, alice, "archive_product", map[string]any{"product_id": product.ID, "expected_revision": product.Revision, "request_id": "gateway-request", "idempotency_key": "gateway-key", "archived": true})
	if !result.IsError {
		t.Fatal("read token permitted write")
	}
}

type gatewayClientTransport struct{ token string }

func (a gatewayClientTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(copy)
}
