// Package agent contains only synthetic example/test identities and SDK calls.
// Production commands never import it, and the image build excludes examples.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
	"github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	GatewayIssuer  = "https://synthetic-gateway.example.test"
	IdentityIssuer = "https://synthetic-identity.example.test/realms/example"
	Audience       = "artificial-memory-mcp"
	ClientID       = "synthetic-coding-agent"
	BackendOrigin  = "http://memory.internal:8080"
)

// Identity's private key is ephemeral and stays in the example/test process.
type Identity struct {
	key     *rsa.PrivateKey
	Subject string
}

func NewIdentity() (*Identity, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &Identity{key: key, Subject: "synthetic-agent-owner"}, nil
}

func (i *Identity) AccountIdentity() account.Identity {
	return account.Identity{Issuer: IdentityIssuer, Subject: i.Subject}
}

func (i *Identity) PublicJWKS() ([]byte, error) {
	return json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &i.key.PublicKey, KeyID: "synthetic", Use: "sig", Algorithm: "RS256"}}})
}

func (i *Identity) token(scopes string) (string, error) {
	now := time.Now().Unix()
	claims, err := json.Marshal(map[string]any{
		"iss": GatewayIssuer, "aud": []string{Audience}, "identity_issuer": IdentityIssuer,
		"sub": i.Subject, "client_id": ClientID, "scope": scopes,
		"iat": now, "nbf": now, "exp": now + 60, "jti": "synthetic-example",
	})
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: i.key}, (&jose.SignerOptions{}).WithType(identity.TokenType).WithHeader(jose.HeaderKey("kid"), "synthetic"))
	if err != nil {
		return "", err
	}
	signed, err := signer.Sign(claims)
	if err != nil {
		return "", err
	}
	return signed.CompactSerialize()
}

type signedTransport struct {
	identity *Identity
	scopes   string
	base     http.RoundTripper
}

func (s signedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	token, err := s.identity.token(s.scopes)
	if err != nil {
		return nil, err
	}
	request = request.Clone(request.Context())
	request.Header = request.Header.Clone()
	request.Host = "memory.internal:8080"
	request.Header.Set("Authorization", "Bearer "+token)
	return s.base.RoundTrip(request)
}

func (i *Identity) Connect(ctx context.Context, endpoint, scopes string) (*mcp.ClientSession, error) {
	// Only loopback endpoints are supplied by the demo and harness. Explicitly
	// disable proxy lookup so fixture tokens cannot leave via ambient proxies.
	base := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	client := mcp.NewClient(&mcp.Implementation{Name: ClientID, Version: "synthetic-1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint + "/mcp", DisableStandaloneSSE: true,
		HTTPClient: &http.Client{Transport: signedTransport{i, scopes, base}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		base.CloseIdleConnections()
		return nil, err
	}
	return session, nil
}

func Call[T any](ctx context.Context, session *mcp.ClientSession, name string, arguments any) (T, error) {
	var output T
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return output, err
	}
	if result.IsError {
		return output, fmt.Errorf("synthetic %s call was rejected", name)
	}
	if len(result.Content) != 1 {
		return output, errors.New("synthetic tool response has no text result")
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return output, errors.New("synthetic tool response is not text")
	}
	err = json.Unmarshal([]byte(content.Text), &output)
	return output, err
}
