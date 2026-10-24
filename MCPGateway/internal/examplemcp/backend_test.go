package examplemcp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestAuthenticatedBrowserRequestProtection(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "origin-test", Algorithm: "RS256", Use: "sig"}
	publicKeys, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "public.jwks.json")
	if err := os.WriteFile(keyFile, publicKeys, 0600); err != nil {
		t.Fatal(err)
	}
	config := trust.Config{Issuer: "https://gateway.example", Audience: "example-mcp", JWKSFile: keyFile}
	handler, err := New(config, "example-agent", "example:use", true)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, (&jose.SignerOptions{}).WithType(trust.TokenType))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := trust.Claims{
		Claims: jwt.Claims{
			Issuer: config.Issuer, Subject: "alice", Audience: jwt.Audience{config.Audience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
		},
		IdentityIssuer: "https://identity.example", ClientID: "example-agent", Scope: "example:use",
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		origin    string
		fetchSite string
		host      string
		status    int
		denial    string
	}{
		{name: "non-browser", status: http.StatusOK},
		{name: "matching origin", origin: "http://127.0.0.1:8099", status: http.StatusOK},
		{name: "same-origin browser", origin: "http://127.0.0.1:8099", fetchSite: "same-origin", status: http.StatusOK},
		{name: "foreign origin", origin: "https://attacker.example", status: http.StatusForbidden, denial: "cross-origin request"},
		{name: "cross-site browser", fetchSite: "cross-site", status: http.StatusForbidden, denial: "cross-origin request"},
		{name: "cross-site with matching origin", origin: "http://127.0.0.1:8099", fetchSite: "cross-site", status: http.StatusForbidden, denial: "cross-origin request"},
		{name: "foreign host on loopback", host: "attacker.example", status: http.StatusForbidden, denial: "invalid Host header"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": {
					"name": "whoami", "arguments": {},
					"_meta": {
						"io.modelcontextprotocol/protocolVersion": "2026-07-28",
						"io.modelcontextprotocol/clientInfo": {"name": "origin-test", "version": "1"},
						"io.modelcontextprotocol/clientCapabilities": {}
					}
				}
			}`
			// Match the loopback listener context supplied by net/http so the SDK's
			// independent Host protection is exercised without opening a socket.
			ctx := context.WithValue(t.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8099})
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8099/mcp", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("Mcp-Protocol-Version", "2026-07-28")
			request.Header.Set("Mcp-Method", "tools/call")
			request.Header.Set("Mcp-Name", "whoami")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			if test.host != "" {
				request.Host = test.host
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; response: %s", response.Code, test.status, response.Body.String())
			}
			if test.denial != "" {
				if !strings.Contains(response.Body.String(), test.denial) {
					t.Fatalf("expected %q denial, got %s", test.denial, response.Body.String())
				}
				return
			}
			var reply struct {
				Error  json.RawMessage `json:"error"`
				Result struct {
					IsError           bool            `json:"isError"`
					StructuredContent trust.Principal `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if len(reply.Error) != 0 || reply.Result.IsError {
				t.Fatalf("whoami failed: %s", response.Body.String())
			}
			principal := reply.Result.StructuredContent
			if principal.Issuer != claims.IdentityIssuer || principal.Subject != claims.Subject || principal.ClientID != claims.ClientID || principal.Scope != claims.Scope {
				t.Fatalf("whoami identity = %+v", principal)
			}
		})
	}
}
