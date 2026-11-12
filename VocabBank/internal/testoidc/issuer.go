// Package testoidc provides a synthetic, rotating OIDC issuer for security tests.
// No real identity, vocabulary, key material or provider credentials are fixtures.
package testoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type Issuer struct {
	Server                            *httptest.Server
	Key                               *rsa.PrivateKey
	keyMu                             sync.RWMutex
	keyID                             string
	idKey                             *rsa.PrivateKey
	Clock                             atomic.Int64
	mu                                sync.Mutex
	nonce, challenge, redirect, scope string
	loginSubject                      string
	codeUsed                          bool
	grants                            map[string]string
	sequence                          int
	Refreshes                         atomic.Int64
	Revocations                       atomic.Int64
	Mode                              atomic.Int64
	JWKSMode                          atomic.Int64
}

func New(t *testing.T) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &Issuer{Key: key, keyID: "test", grants: map[string]string{}}
	f.Clock.Store(time.Now().Unix())
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": f.URL(), "authorization_endpoint": f.URL() + "/authorize", "token_endpoint": f.URL() + "/token", "jwks_uri": f.URL() + "/keys", "revocation_endpoint": f.URL() + "/revoke", "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		if status := f.JWKSMode.Load(); status != 0 {
			http.Error(w, "synthetic signing-key outage", int(status))
			return
		}
		f.keyMu.RLock()
		defer f.keyMu.RUnlock()
		keys := []jose.JSONWebKey{{Key: &f.Key.PublicKey, KeyID: f.keyID, Use: "sig", Algorithm: "RS256"}}
		if f.idKey != nil {
			keys = append(keys, jose.JSONWebKey{Key: &f.idKey.PublicKey, KeyID: "test-id", Use: "sig", Algorithm: "RS256"})
		}
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: keys})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		f.Prepare(r.URL.Query())
		destination, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := destination.Query()
		q.Set("code", "synthetic-code")
		q.Set("state", r.URL.Query().Get("state"))
		q.Set("iss", f.URL())
		destination.RawQuery = q.Encode()
		http.Redirect(w, r, destination.String(), 302)
	})
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		if f.Mode.Load() == 502 {
			http.Error(w, "synthetic revoke outage", 503)
			return
		}
		r.ParseForm()
		f.mu.Lock()
		delete(f.grants, r.Form.Get("token"))
		f.mu.Unlock()
		f.Revocations.Add(1)
		w.WriteHeader(200)
	})
	return f
}
func (f *Issuer) URL() string    { return f.Server.URL }
func (f *Issuer) Now() time.Time { return time.Unix(f.Clock.Load(), 0) }
func (f *Issuer) Prepare(q url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonce = q.Get("nonce")
	f.challenge = q.Get("code_challenge")
	f.redirect = q.Get("redirect_uri")
	f.scope = q.Get("scope")
	f.loginSubject = q.Get("login_hint")
	if f.loginSubject == "" {
		f.loginSubject = "test-subject"
	}
	f.codeUsed = false
}
func (f *Issuer) Sign(claims any, typ string) string {
	f.keyMu.RLock()
	defer f.keyMu.RUnlock()
	return sign(f.Key, f.keyID, claims, typ)
}

func sign(key *rsa.PrivateKey, keyID string, claims any, typ string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", keyID))
	if err != nil {
		panic(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		panic(err)
	}
	return raw
}

func (f *Issuer) RotateSigningKey(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.keyMu.Lock()
	defer f.keyMu.Unlock()
	f.Key, f.keyID = key, rand.Text()
}

func (f *Issuer) RotateIDSigningKey(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.keyMu.Lock()
	defer f.keyMu.Unlock()
	f.idKey = key
}

func (f *Issuer) signID(claims any) string {
	f.keyMu.RLock()
	defer f.keyMu.RUnlock()
	if f.idKey != nil {
		return sign(f.idKey, "test-id", claims, "JWT")
	}
	return sign(f.Key, f.keyID, claims, "JWT")
}
func (f *Issuer) Access(subject, client, audience, scope string, life int64) string {
	return f.Sign(map[string]any{"iss": f.URL(), "sub": subject, "azp": client, "aud": []string{audience}, "scope": scope, "typ": "Bearer", "iat": f.Now().Unix(), "exp": f.Now().Unix() + life}, "JWT")
}
func (f *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if f.Mode.Load() == 503 {
		http.Error(w, "unavailable", 503)
		return
	}
	if f.Mode.Load() == 400 {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	subject := f.loginSubject
	grant := r.Form.Get("grant_type")
	if grant == "authorization_code" {
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if f.codeUsed || r.Form.Get("code") != "synthetic-code" || r.Form.Get("redirect_uri") != f.redirect || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		f.codeUsed = true
	} else if grant == "refresh_token" {
		var ok bool
		subject, ok = f.grants[r.Form.Get("refresh_token")]
		if !ok {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		delete(f.grants, r.Form.Get("refresh_token"))
		f.Refreshes.Add(1)
	} else {
		w.WriteHeader(400)
		return
	}
	f.sequence++
	refresh := "synthetic-refresh-" + strconv.Itoa(f.sequence)
	f.grants[refresh] = subject
	access := f.Access(subject, "vocab-web", r.Form.Get("resource"), f.scope, 300)
	id := f.signID(map[string]any{"iss": f.URL(), "sub": subject, "aud": []string{"vocab-web"}, "azp": "vocab-web", "nonce": f.nonce, "iat": f.Now().Unix(), "exp": f.Now().Unix() + 300})
	json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "id_token": id, "token_type": "Bearer", "expires_in": 300, "scope": f.scope})
}
