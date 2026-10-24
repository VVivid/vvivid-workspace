package gateway

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sshmcp/internal/gatewaytrust"
	"sshmcp/internal/runner"
	"strings"
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

func TestSharedGatewaySSHProtocol(t *testing.T) {
	if os.Getenv("MCP_GATEWAY_BIN") == "" {
		t.Skip("cross-project acceptance requires gateway binary")
	}
	key := testRSAKey(t)
	jwk := jose.JSONWebKey{Key: key, KeyID: "external", Algorithm: "RS256", Use: "sig"}
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/keys" {
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "jwks_uri": issuer.URL + "/keys", "authorization_endpoint": issuer.URL + "/auth", "token_endpoint": issuer.URL + "/token"})
	}))
	defer issuer.Close()
	backend := httptest.NewUnstartedServer(nil)
	address := backend.Listener.Addr().String()
	origin, publicFile, resource := startSharedGateway(t, issuer.URL, "http://"+address+"/mcp", address, AdminScope, "ssh-client", "ssh-backend")
	trustConfig := gatewaytrust.Config{Issuer: origin, Audience: "ssh-backend", JWKSFile: publicFile}
	verifier, err := NewBackendTokenVerifier(trustConfig)
	if err != nil {
		t.Fatal(err)
	}
	rpc := &recordingRPC{response: runner.Response{}}
	handler, err := NewHandler(Config{Listen: address, Resource: "http://" + address + "/mcp", Gateway: &trustConfig}, rpc, verifier)
	if err != nil {
		t.Fatal(err)
	}
	backend.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.Header.Get("Mcp-Protocol-Version") != "2026-07-28" || r.Header.Get("Mcp-Method") == "" {
				t.Error("SSH backend did not receive current MCP protocol metadata")
			}
			if r.Header.Get("Mcp-Method") == "tools/call" && r.Header.Get("Mcp-Name") == "" {
				t.Error("SSH backend did not receive tool name")
			}
		}
		handler.ServeHTTP(w, r)
	})
	backend.Start()
	defer backend.Close()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, (&jose.SignerOptions{}).WithType("at+jwt"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(map[string]any{"iss": issuer.URL, "sub": "alice", "aud": resource, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "client_id": "ssh-client", "scope": AdminScope}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: resource, HTTPClient: &http.Client{Transport: sharedAuthTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("SSH negotiated protocol %q, want 2026-07-28", got)
	}
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 3 {
		t.Fatalf("tool catalog: %v", err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "connect", Arguments: map[string]any{"host_alias": "lab-target"}})
	if err != nil || result.IsError {
		t.Fatalf("connect: %v", err)
	}
	if len(rpc.requests) != 1 || rpc.requests[0].Owner != (runner.Owner{Issuer: issuer.URL, Subject: "alice"}) {
		t.Fatal("original ownership not preserved")
	}
	// A gateway-shaped identity header cannot authenticate a direct request.
	request := httptest.NewRequest("POST", "http://"+address+"/mcp", strings.NewReader(`{}`))
	request.Header.Set("X-Forwarded-User", "alice")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("spoofed identity accepted: %d", response.Code)
	}
}

type sharedAuthTransport struct{ token string }

func (a sharedAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(copy)
}
