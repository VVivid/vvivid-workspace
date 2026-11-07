package identity

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Signing exists only in test fixtures. The production verifier holds public
// keys and implements the documented wire contract without a gateway import.
func compact(t *testing.T, key *rsa.PrivateKey, header string, payload []byte) string {
	t.Helper()
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func gatewayFixture(t *testing.T) (*Verifier, *rsa.PrivateKey, map[string]any) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "public.jwks")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{GatewayIssuer: "https://gateway.example", Audience: "https://gateway.example/memory/mcp", IdentityIssuer: "https://identity.example/realms/test", JWKSFile: file, AllowedClients: []string{"agent-one", "agent-two"}}
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	v.now = func() time.Time { return time.Unix(1800000000, 0) }
	now := v.now().Unix()
	return v, key, map[string]any{"iss": cfg.GatewayIssuer, "aud": []string{cfg.Audience}, "identity_issuer": cfg.IdentityIssuer, "sub": "synthetic-alice", "client_id": "agent-one", "scope": ReadScope + " " + WriteScope, "iat": now, "nbf": now, "exp": now + 60, "jti": "fixture-jti"}
}

func TestGatewayWireContract(t *testing.T) {
	v, key, original := gatewayFixture(t)
	header := `{"alg":"RS256","kid":"fixture","typ":"mcp-gateway+jwt"}`
	encode := func(c map[string]any) []byte {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	valid := compact(t, key, header, encode(original))
	p, err := v.Verify(valid)
	if err != nil || p.Identity.Issuer != v.config.IdentityIssuer || p.Identity.Subject != "synthetic-alice" || p.Actor.AccountID != "" || !p.HasScope(ReadScope) || !p.HasScope(WriteScope) {
		t.Fatalf("valid gateway identity rejected or owner invented: %v", err)
	}
	for name, change := range map[string]func(map[string]any){
		"gateway issuer":       func(c map[string]any) { c["iss"] = "https://other.example" },
		"audience":             func(c map[string]any) { c["aud"] = []string{"other"} },
		"two audiences":        func(c map[string]any) { c["aud"] = []string{v.config.Audience, "other"} },
		"provider issuer":      func(c map[string]any) { c["identity_issuer"] = "https://other.example" },
		"provider HTTP":        func(c map[string]any) { c["identity_issuer"] = "http://identity.example/realms/test" },
		"empty subject":        func(c map[string]any) { c["sub"] = "" },
		"subject NUL":          func(c map[string]any) { c["sub"] = "alice\x00bob" },
		"subject size":         func(c map[string]any) { c["sub"] = strings.Repeat("s", 256) },
		"client":               func(c map[string]any) { c["client_id"] = "untrusted-client" },
		"extra scope":          func(c map[string]any) { c["scope"] = "memory:read admin:all" },
		"empty scope":          func(c map[string]any) { c["scope"] = "" },
		"blank scope":          func(c map[string]any) { c["scope"] = "   " },
		"missing iat":          func(c map[string]any) { delete(c, "iat") },
		"missing nbf":          func(c map[string]any) { delete(c, "nbf") },
		"missing exp":          func(c map[string]any) { delete(c, "exp") },
		"zero iat":             func(c map[string]any) { c["iat"] = 0 },
		"negative nbf":         func(c map[string]any) { c["nbf"] = -1 },
		"expired":              func(c map[string]any) { c["exp"] = v.now().Unix() },
		"long remaining":       func(c map[string]any) { c["exp"] = v.now().Unix() + 61 },
		"long lifetime":        func(c map[string]any) { c["iat"] = v.now().Unix() - 1 },
		"future iat":           func(c map[string]any) { c["iat"] = v.now().Unix() + 6 },
		"future nbf":           func(c map[string]any) { c["nbf"] = v.now().Unix() + 1 },
		"iat after expiry":     func(c map[string]any) { c["iat"] = v.now().Unix() + 5; c["exp"] = v.now().Unix() + 4 },
		"fractional timestamp": func(c map[string]any) { c["iat"] = json.Number("1800000000.5") },
		"extreme expiry":       func(c map[string]any) { c["exp"] = json.Number("9223372036854775807") },
	} {
		t.Run(name, func(t *testing.T) {
			c := maps.Clone(original)
			change(c)
			if _, err := v.Verify(compact(t, key, header, encode(c))); err == nil {
				t.Fatal("invalid signed identity accepted")
			}
		})
	}
	for name, hdr := range map[string]string{
		"type":             `{"alg":"RS256","kid":"fixture","typ":"JWT"}`,
		"kid":              `{"alg":"RS256","kid":"unknown","typ":"mcp-gateway+jwt"}`,
		"missing kid":      `{"alg":"RS256","typ":"mcp-gateway+jwt"}`,
		"algorithm":        `{"alg":"HS256","kid":"fixture","typ":"mcp-gateway+jwt"}`,
		"duplicate header": `{"alg":"RS256","kid":"other","kid":"fixture","typ":"mcp-gateway+jwt"}`,
		"key URL":          `{"alg":"RS256","kid":"fixture","typ":"mcp-gateway+jwt","jku":"https://attacker.example/key"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(compact(t, key, hdr, encode(original))); err == nil {
				t.Fatal("invalid protected header accepted")
			}
		})
	}
	duplicate := strings.TrimSuffix(string(encode(original)), "}") + `,"sub":"synthetic-bob"}`
	if _, err := v.Verify(compact(t, key, header, []byte(duplicate))); err == nil {
		t.Fatal("duplicate claim accepted")
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(compact(t, other, header, encode(original))); err == nil {
		t.Fatal("unpinned signature accepted")
	}
	for _, raw := range []string{"", "external-provider-token", strings.Repeat("x", (32<<10)+1), `{"payload":"x","signatures":[]}`, valid + " "} {
		if _, err := v.Verify(raw); err == nil {
			t.Fatal("noncompact or malformed token accepted")
		}
	}
}

func TestGatewayKeyInstallationFailsClosed(t *testing.T) {
	v, key, _ := gatewayFixture(t)
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	public := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}
	for name, keys := range map[string][]jose.JSONWebKey{
		"empty": {}, "duplicate": {public, public},
		"private":     {{Key: key, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}},
		"weak":        {{Key: &weak.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}},
		"wrong use":   {{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "enc"}},
		"missing kid": {{Key: &key.PublicKey, Algorithm: "RS256", Use: "sig"}},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(jose.JSONWebKeySet{Keys: keys})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(v.config.JWKSFile, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewVerifier(v.config); err == nil {
				t.Fatal("invalid pinned keys accepted")
			}
		})
	}
	for _, data := range []string{`{"keys":[],"keys":[]}`, strings.Repeat(" ", (64<<10)+1)} {
		if err := os.WriteFile(v.config.JWKSFile, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewVerifier(v.config); err == nil {
			t.Fatal("ambiguous or oversized key file accepted")
		}
	}
}
