package trust

import (
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

func TestIdentityContract(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "contract-key", Algorithm: "RS256", Use: "sig"}
	data, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
	file := filepath.Join(t.TempDir(), "public.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(Config{Issuer: "https://gateway.example", Audience: "backend", JWKSFile: file})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, (&jose.SignerOptions{}).WithType(TokenType))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	base := Claims{Claims: jwt.Claims{Issuer: "https://gateway.example", Subject: "alice", Audience: jwt.Audience{"backend"}, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute))}, IdentityIssuer: "https://identity.example", ClientID: "client", Scope: "read"}
	raw, err := jwt.Signed(signer).Claims(base).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	p, err := verifier.Verify(raw)
	if err != nil || p.Subject != "alice" || p.Issuer != "https://identity.example" {
		t.Fatalf("identity: %+v %v", p, err)
	}
	for name, change := range map[string]func(*Claims){
		"issuer": func(c *Claims) { c.Issuer = "other" }, "audience": func(c *Claims) { c.Audience = jwt.Audience{"other"} },
		"expired": func(c *Claims) { c.Expiry = jwt.NewNumericDate(now.Add(-time.Second)) }, "missing expiry": func(c *Claims) { c.Expiry = nil },
		"future": func(c *Claims) { c.NotBefore = jwt.NewNumericDate(now.Add(time.Minute)) }, "long lifetime": func(c *Claims) { c.Expiry = jwt.NewNumericDate(now.Add(2 * time.Minute)) },
		"missing subject": func(c *Claims) { c.Subject = "" }, "missing original issuer": func(c *Claims) { c.IdentityIssuer = "" }, "missing client": func(c *Claims) { c.ClientID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			change(&c)
			raw, err := jwt.Signed(signer).Claims(c).Serialize()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.Verify(raw); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	for _, raw := range []string{"e30.e30.", raw + "invalid"} {
		if _, err := verifier.Verify(raw); err == nil {
			t.Fatal("bad signature accepted")
		}
	}
}
