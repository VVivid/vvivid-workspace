// Package identity verifies MCPGateway's v1 wire contract independently. This
// backend installs public keys only and never receives provider access tokens.
package identity

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/strictjson"
	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	TokenType  = "mcp-gateway+jwt"
	ReadScope  = "memory:read"
	WriteScope = "memory:write"
)

var ErrUnauthenticated = errors.New("invalid gateway identity")

type Config struct {
	GatewayIssuer  string
	Audience       string
	IdentityIssuer string
	JWKSFile       string
	AllowedClients []string
}

func (c Config) Validate() error {
	u, err := url.Parse(c.GatewayIssuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(c.GatewayIssuer) > 2048 {
		return errors.New("gateway issuer must be an exact HTTPS origin")
	}
	if (account.Identity{Issuer: c.IdentityIssuer, Subject: "validation"}).Validate() != nil {
		return errors.New("identity issuer must be an exact HTTPS OIDC issuer")
	}
	if !validIdentifier(c.Audience, 2048) || c.JWKSFile == "" || len(c.AllowedClients) == 0 || len(c.AllowedClients) > 32 {
		return errors.New("gateway audience, public JWKS and client allowlist are required")
	}
	seen := map[string]bool{}
	for _, id := range c.AllowedClients {
		if !validIdentifier(id, 256) || seen[id] {
			return errors.New("client allowlist must contain distinct bounded client IDs")
		}
		seen[id] = true
	}
	return nil
}

func validIdentifier(s string, size int) bool {
	return s != "" && len(s) <= size && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

type Verifier struct {
	config Config
	keys   jose.JSONWebKeySet
	now    func() time.Time
}

func NewVerifier(c Config) (*Verifier, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	invalid := errors.New("public JWKS must contain 1..8 distinct public RS256 RSA signing keys with kid (2048..8192 bits)")
	file, err := os.Open(c.JWKSFile)
	if err != nil {
		return nil, errors.New("cannot read gateway public JWKS")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	var keys jose.JSONWebKeySet
	if err != nil || len(data) > 64<<10 || strictjson.Check(data) != nil || json.Unmarshal(data, &keys) != nil || len(keys.Keys) < 1 || len(keys.Keys) > 8 {
		return nil, invalid
	}
	seen := map[string]bool{}
	for _, key := range keys.Keys {
		public, ok := key.Key.(*rsa.PublicKey)
		if !ok || !key.Valid() || !key.IsPublic() || public.N.BitLen() < 2048 || public.N.BitLen() > 8192 || public.E < 3 || public.E%2 == 0 ||
			!validIdentifier(key.KeyID, 128) || seen[key.KeyID] || key.Algorithm != "RS256" || key.Use != "sig" {
			return nil, invalid
		}
		seen[key.KeyID] = true
	}
	c.AllowedClients = slices.Clone(c.AllowedClients)
	return &Verifier{config: c, keys: keys, now: time.Now}, nil
}

type claims struct {
	Issuer         string          `json:"iss"`
	Audience       json.RawMessage `json:"aud"`
	Subject        string          `json:"sub"`
	IdentityIssuer string          `json:"identity_issuer"`
	ClientID       string          `json:"client_id"`
	Scope          string          `json:"scope"`
	IssuedAt       int64           `json:"iat"`
	NotBefore      int64           `json:"nbf"`
	Expiry         int64           `json:"exp"`
	ID             string          `json:"jti"`
}

type Principal struct {
	Identity account.Identity
	Actor    memory.Actor
	Scope    string
}

func (p Principal) HasScope(scope string) bool {
	for granted := range strings.FieldsSeq(p.Scope) {
		if granted == scope {
			return true
		}
	}
	return false
}

func (v *Verifier) Verify(raw string) (Principal, error) {
	if len(raw) == 0 || len(raw) > 32<<10 || strings.Count(raw, ".") != 2 {
		return Principal{}, ErrUnauthenticated
	}
	protected, _, _ := strings.Cut(raw, ".")
	headerBytes, err := base64.RawURLEncoding.DecodeString(protected)
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}
	if err != nil || strictjson.Decode(headerBytes, &header) != nil || header.Algorithm != "RS256" || header.Type != TokenType || header.KeyID == "" {
		return Principal{}, ErrUnauthenticated
	}
	keys := v.keys.Key(header.KeyID)
	if len(keys) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	token, err := jose.ParseSignedCompact(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	payload, err := token.Verify(keys[0].Key)
	var c claims
	if err != nil || strictjson.Decode(payload, &c) != nil {
		return Principal{}, ErrUnauthenticated
	}
	var audience jwt.Audience
	if json.Unmarshal(c.Audience, &audience) != nil || len(audience) != 1 || audience[0] != v.config.Audience || c.Issuer != v.config.GatewayIssuer {
		return Principal{}, ErrUnauthenticated
	}
	id := account.Identity{Issuer: c.IdentityIssuer, Subject: c.Subject}
	if id.Issuer != v.config.IdentityIssuer || id.Validate() != nil || !slices.Contains(v.config.AllowedClients, c.ClientID) {
		return Principal{}, ErrUnauthenticated
	}
	now := v.now().Unix()
	// Check expiry before subtraction so extreme signed timestamps cannot
	// overflow the lifetime check. Only issued-at receives five seconds of skew.
	if c.Expiry <= now || c.Expiry > now+60 {
		return Principal{}, ErrUnauthenticated
	}
	if c.IssuedAt <= 0 || c.IssuedAt > now+5 || c.IssuedAt < c.Expiry-60 || c.Expiry <= c.IssuedAt {
		return Principal{}, ErrUnauthenticated
	}
	if c.NotBefore <= 0 || c.NotBefore > now || c.NotBefore >= c.Expiry {
		return Principal{}, ErrUnauthenticated
	}
	if len(c.Scope) > 128 || c.Scope == "" {
		return Principal{}, ErrUnauthenticated
	}
	for scope := range strings.FieldsSeq(c.Scope) {
		if scope != ReadScope && scope != WriteScope {
			return Principal{}, ErrUnauthenticated
		}
	}
	p := Principal{Identity: id, Actor: memory.Actor{ClientID: c.ClientID}, Scope: c.Scope}
	if !p.HasScope(ReadScope) && !p.HasScope(WriteScope) {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok && p.Actor.AccountID != "" && p.Actor.ClientID != ""
}
