package identity

import (
	"agentshoppinglist/internal/gatewaytrust"
	"agentshoppinglist/internal/store/sqlite"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGatewayEnrollmentScopesAndNoFallback(t *testing.T) {
	m, issuer, store, _ := testManager(t)
	alice := enroll(t, m, "alice")
	bob := enroll(t, m, "bob")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "gateway", Algorithm: "RS256", Use: "sig"}
	data, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	file := filepath.Join(t.TempDir(), "public.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := gatewaytrust.Config{Issuer: "https://gateway.example", Audience: "shopping-backend", JWKSFile: file}
	m.gateway, err = gatewaytrust.NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, (&jose.SignerOptions{}).WithType(gatewaytrust.TokenType))
	if err != nil {
		t.Fatal(err)
	}
	mint := func(subject, original, client, scope string) string {
		now := time.Now()
		c := gatewaytrust.Claims{Claims: jwt.Claims{Issuer: cfg.Issuer, Subject: subject, Audience: jwt.Audience{cfg.Audience}, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute))}, IdentityIssuer: original, ClientID: client, Scope: scope}
		raw, err := jwt.Signed(signer).Claims(c).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, tc := range []struct {
		subject, original, client, scope string
		accepted                         bool
	}{
		{"alice", m.cfg.Issuer, "shopping-codex", Read, true}, {"bob", m.cfg.Issuer, "shopping-chatgpt", Read, true},
		{"unenrolled", m.cfg.Issuer, "shopping-codex", Read, false}, {"alice", "https://other-issuer.example", "shopping-codex", Read, false},
		{"alice", m.cfg.Issuer, "ssh-client", Read, false}, {"alice", m.cfg.Issuer, "shopping-codex", "sshmcp:admin", false},
	} {
		p, err := m.AuthenticateBearer(t.Context(), mint(tc.subject, tc.original, tc.client, tc.scope), m.MCPAudience())
		if (err == nil) != tc.accepted {
			t.Fatalf("subject=%s scope=%s: %v", tc.subject, tc.scope, err)
		}
		if tc.accepted {
			want := alice
			if tc.subject == "bob" {
				want = bob
			}
			if p.Owner() != want || p.Require(ProductsWrite) == nil {
				t.Fatal("ownership or permissions changed")
			}
		}
	}
	external := issuer.bearer("alice", "shopping-codex", m.MCPAudience(), Read, nil)
	if _, err := m.AuthenticateBearer(t.Context(), external, m.MCPAudience()); err == nil {
		t.Fatal("gateway-only MCP accepted external token")
	}
	browser := issuer.bearer("alice", m.cfg.WebClientID, m.APIAudience(), Read, nil)
	if _, err := m.AuthenticateBearer(t.Context(), browser, m.APIAudience()); err != nil {
		t.Fatalf("gateway mode changed browser API: %v", err)
	}
	if err := store.DisableAccount(t.Context(), sqlite.ExternalIdentity{Issuer: m.cfg.Issuer, Subject: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AuthenticateBearer(t.Context(), mint("alice", m.cfg.Issuer, "shopping-codex", Read), m.MCPAudience()); err == nil {
		t.Fatal("disabled account accepted")
	}
}
