package managementhttp

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

type signingIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newIssuer(t *testing.T) *signingIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &signingIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": i.server.URL, "jwks_uri": i.server.URL + "/jwks", "authorization_endpoint": i.server.URL + "/authorize", "token_endpoint": i.server.URL + "/token", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "synthetic", Algorithm: "RS256", Use: "sig"}}})
	})
	i.server = httptest.NewServer(mux)
	t.Cleanup(i.server.Close)
	return i
}

func (i *signingIssuer) token(t *testing.T, typ string, claims map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	options := new(jose.SignerOptions)
	if typ != "" {
		options.WithType(jose.ContentType(typ))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "synthetic"}}, options)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(claims)
	signed, err := signer.Sign(raw)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRealJWTVerifierAccessTokenProfileAndNegatives(t *testing.T) {
	i := newIssuer(t)
	config := OAuthConfig{Issuer: i.server.URL, Resource: "https://management.example/mcp", AllowInsecureLoopback: true}
	verify, err := NewTokenVerifier(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	badKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, typ string
		change    func(map[string]any)
		key       *rsa.PrivateKey
		valid     bool
	}{
		{name: "access token", typ: "at+jwt", key: i.key, valid: true},
		{name: "wrong issuer", typ: "at+jwt", key: i.key, change: func(c map[string]any) { c["iss"] = "https://wrong.example" }},
		{name: "wrong audience", typ: "at+jwt", key: i.key, change: func(c map[string]any) { c["aud"] = "https://wrong.example/mcp" }},
		{name: "expired", typ: "at+jwt", key: i.key, change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{name: "bad signature", typ: "at+jwt", key: badKey},
		{name: "ID token", typ: "JWT", key: i.key},
		{name: "ID token use", typ: "at+jwt", key: i.key, change: func(c map[string]any) { c["token_use"] = "id" }},
		{name: "missing type", key: i.key},
		{name: "missing subject", typ: "at+jwt", key: i.key, change: func(c map[string]any) { delete(c, "sub") }},
		{name: "missing expiry", typ: "at+jwt", key: i.key, change: func(c map[string]any) { delete(c, "exp") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := map[string]any{"iss": i.server.URL, "aud": config.Resource, "sub": "synthetic-subject", "exp": time.Now().Add(time.Hour).Unix(), "scope": Scope, "token_use": "access"}
			if test.change != nil {
				test.change(claims)
			}
			raw := i.token(t, test.typ, claims, test.key)
			info, err := verify(t.Context(), raw, nil)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				actor, err := identity(info)
				if err != nil || actor.Issuer != i.server.URL || actor.Subject != "synthetic-subject" || len(info.Scopes) != 1 || info.Scopes[0] != Scope {
					t.Fatalf("info=%+v err=%v", info, err)
				}
			} else if err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
	config.AllowInsecureLoopback = false
	if _, err := NewTokenVerifier(t.Context(), config); err == nil {
		t.Fatal("production accepted HTTP issuer")
	}
	if _, err := trustedURL("http://issuer.external.example", true); err == nil {
		t.Fatal("insecure test option allowed non-loopback issuer")
	}
}
