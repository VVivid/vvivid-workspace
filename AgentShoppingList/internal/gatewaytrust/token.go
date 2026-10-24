// Package trust implements the v1 gateway-to-backend identity contract.
// Backends may implement this wire contract independently of the gateway module.
package gatewaytrust

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const TokenType = "mcp-gateway+jwt"

type Principal struct {
	ExpiresAt time.Time `json:"-"`
	Issuer    string    `json:"identity_issuer"`
	Subject   string    `json:"sub"`
	ClientID  string    `json:"client_id"`
	Scope     string    `json:"scope"`
}

type Claims struct {
	jwt.Claims
	IdentityIssuer string `json:"identity_issuer"`
	ClientID       string `json:"client_id"`
	Scope          string `json:"scope"`
}

type Config struct {
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	JWKSFile string `json:"jwks_file"`
}

type Verifier struct {
	config Config
	keys   jose.JSONWebKeySet
}

func NewVerifier(config Config) (*Verifier, error) {
	if config.Issuer == "" || config.Audience == "" || config.JWKSFile == "" {
		return nil, errors.New("gateway trust requires issuer, audience and JWKS file")
	}
	data, err := os.ReadFile(config.JWKSFile)
	if err != nil {
		return nil, errors.New("cannot read gateway public keys")
	}
	var keys jose.JSONWebKeySet
	if len(data) > 64<<10 || json.Unmarshal(data, &keys) != nil || len(keys.Keys) == 0 || len(keys.Keys) > 8 {
		return nil, errors.New("invalid gateway JWKS")
	}
	seen := map[string]bool{}
	for _, key := range keys.Keys {
		public, ok := key.Key.(*rsa.PublicKey)
		if !ok || public.N.BitLen() < 2048 || !key.IsPublic() || key.KeyID == "" || seen[key.KeyID] || key.Algorithm != "RS256" || key.Use != "sig" {
			return nil, errors.New("gateway keys must be distinct public RSA signing keys with kid and RS256")
		}
		seen[key.KeyID] = true
	}
	return &Verifier{config, keys}, nil
}

func (v *Verifier) Verify(raw string) (Principal, error) {
	invalid := errors.New("invalid gateway identity")
	if len(raw) == 0 || len(raw) > 32<<10 {
		return Principal{}, invalid
	}
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(token.Headers) != 1 || token.Headers[0].ExtraHeaders[jose.HeaderType] != TokenType {
		return Principal{}, invalid
	}
	keys := v.keys.Key(token.Headers[0].KeyID)
	if len(keys) != 1 {
		return Principal{}, invalid
	}
	var c Claims
	if token.Claims(keys[0].Key, &c) != nil {
		return Principal{}, invalid
	}
	now := time.Now().Unix()
	if c.Issuer != v.config.Issuer || len(c.Audience) != 1 || c.Audience[0] != v.config.Audience || c.IdentityIssuer == "" || strings.ContainsRune(c.IdentityIssuer, 0) || c.Subject == "" || strings.ContainsRune(c.Subject, 0) || len(c.Subject) > 255 || c.ClientID == "" {
		return Principal{}, invalid
	}
	if c.Expiry == nil || c.IssuedAt == nil || c.NotBefore == nil {
		return Principal{}, invalid
	}
	expires, issued, notBefore := int64(*c.Expiry), int64(*c.IssuedAt), int64(*c.NotBefore)
	// Check expiry first so expires-60 is safe even for extreme signed timestamps.
	if expires <= now || issued > now+5 || notBefore > now || issued < expires-60 || expires <= issued {
		return Principal{}, invalid
	}
	return Principal{Issuer: c.IdentityIssuer, Subject: c.Subject, ClientID: c.ClientID, Scope: c.Scope, ExpiresAt: time.Unix(int64(*c.Expiry), 0)}, nil
}
