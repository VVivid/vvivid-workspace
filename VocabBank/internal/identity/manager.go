package identity

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

var (
	ErrUnauthenticated = errors.New("sign in required")
	ErrUnavailable     = errors.New("identity provider temporarily unavailable")
)

const IdleLifetime = 30 * 24 * time.Hour
const sessionCookie = "__Host-vocabbank-session"
const loginCookie = "__Host-vocabbank-login"

type Principal struct {
	Actor                   store.Actor
	Issuer, Subject, Client string
	Label                   string
}
type principalKey struct{}

func FromContext(ctx context.Context) (Principal, bool) {
	v, ok := ctx.Value(principalKey{}).(Principal)
	return v, ok && v.Actor.Owner != ""
}

type metadata struct {
	Issuer      string   `json:"issuer"`
	Auth        string   `json:"authorization_endpoint"`
	Token       string   `json:"token_endpoint"`
	JWKS        string   `json:"jwks_uri"`
	Revoke      string   `json:"revocation_endpoint"`
	PKCE        []string `json:"code_challenge_methods_supported"`
	ResponseISS bool     `json:"authorization_response_iss_parameter_supported"`
}
type Manager struct {
	cfg      Config
	store    *store.Store
	client   *http.Client
	metadata metadata
	keys     *oidc.RemoteKeySet
	gateway  jose.JSONWebKeySet
	now      func() time.Time
	locks    [128]sync.Mutex
}

func New(ctx context.Context, cfg Config, s *store.Store) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery unavailable")
	}
	var md metadata
	if provider.Claims(&md) != nil || md.Issuer != cfg.Issuer || !slices.Contains(md.PKCE, "S256") {
		return nil, fmt.Errorf("OIDC discovery issuer or PKCE mismatch")
	}
	issuer, _ := url.Parse(cfg.Issuer)
	for _, v := range []string{md.Auth, md.Token, md.JWKS, md.Revoke} {
		u, err := secureURL(v)
		if err != nil || u.Host != issuer.Host || u.Scheme != issuer.Scheme {
			return nil, fmt.Errorf("OIDC endpoints must share the configured issuer origin")
		}
	}
	m := &Manager{cfg: cfg, store: s, client: client, metadata: md, now: time.Now}
	m.keys = providerKeys(client, md.JWKS)
	if cfg.MCPMode == "gateway" {
		m.gateway, err = loadGatewayKeys(cfg.GatewayJWKSFile)
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

type Claims struct {
	Issuer    string       `json:"iss"`
	Subject   string       `json:"sub"`
	Audience  jwt.Audience `json:"aud"`
	Expiry    int64        `json:"exp"`
	IssuedAt  int64        `json:"iat"`
	NotBefore int64        `json:"nbf"`
	Client    string       `json:"azp"`
	Type      string       `json:"typ"`
	Scope     string       `json:"scope"`
}

func (m *Manager) verifyAccess(ctx context.Context, raw, audience string) (Claims, error) {
	return m.verifyAccessAt(ctx, raw, audience, m.now())
}

func (m *Manager) verifyAccessAt(ctx context.Context, raw, audience string, at time.Time) (Claims, error) {
	var c Claims
	if len(raw) == 0 || len(raw) > 32<<10 || strings.Count(raw, ".") != 2 {
		return c, ErrUnauthenticated
	}
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(signed.Signatures) != 1 {
		return c, ErrUnauthenticated
	}
	typ := signed.Signatures[0].Header.ExtraHeaders[jose.HeaderType]
	if typ != "JWT" && typ != "at+jwt" {
		return c, ErrUnauthenticated
	}
	payload, err := m.keys.VerifySignature(ctx, raw)
	if err != nil {
		if signatureUnavailable(err) {
			return c, ErrUnavailable
		}
		return c, ErrUnauthenticated
	}
	if json.Unmarshal(payload, &c) != nil {
		return c, ErrUnauthenticated
	}
	now := at.Unix()
	if c.Issuer != m.cfg.Issuer || !identifier(c.Subject) || len(c.Subject) > 255 ||
		len(c.Audience) != 1 || c.Audience[0] != audience || c.Type != "Bearer" ||
		c.Expiry <= now || c.Expiry > now+300 ||
		c.IssuedAt <= 0 || c.IssuedAt > now+30 || c.IssuedAt < c.Expiry-300 ||
		c.Expiry <= c.IssuedAt || c.NotBefore > now {
		return c, ErrUnauthenticated
	}
	if audience == m.cfg.APIAudience {
		if c.Client != m.cfg.WebClientID {
			return c, ErrUnauthenticated
		}
	} else if audience != m.cfg.MCPAudience || !slices.Contains(m.cfg.MCPClients, c.Client) {
		return c, ErrUnauthenticated
	}
	if !validScopes(c.Scope, true) {
		return c, store.ErrForbidden
	}
	return c, nil
}

func validScopes(raw string, oidcScopes bool) bool {
	if len(raw) > 4096 {
		return false
	}
	for v := range strings.FieldsSeq(raw) {
		if v == store.Read || v == store.Write {
			continue
		}
		if oidcScopes && slices.Contains([]string{"openid", "offline_access", "profile", "email"}, v) {
			continue
		}
		return false
	}
	return true
}

func (m *Manager) principal(ctx context.Context, c Claims) (Principal, error) {
	o, err := m.store.Owner(ctx, c.Issuer, c.Subject)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Actor: store.Actor{Owner: o.ID, Scopes: strings.Fields(c.Scope)}, Issuer: c.Issuer, Subject: c.Subject, Client: c.Client, Label: o.Label}, nil
}

func (m *Manager) Authenticate(ctx context.Context, token string, mcp bool) (Principal, error) {
	var c Claims
	var err error
	if mcp && m.cfg.MCPMode == "gateway" {
		c, err = m.verifyGateway(token)
	} else {
		aud := m.cfg.APIAudience
		if mcp {
			aud = m.cfg.MCPAudience
		}
		c, err = m.verifyAccess(ctx, token, aud)
	}
	if err != nil {
		return Principal{}, err
	}
	return m.principal(ctx, c)
}

func loadGatewayKeys(path string) (jose.JSONWebKeySet, error) {
	var keys jose.JSONWebKeySet
	f, err := os.Open(path)
	if err != nil {
		return keys, fmt.Errorf("gateway public JWKS unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 64<<10 {
		return keys, fmt.Errorf("invalid gateway JWKS")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || json.Unmarshal(raw, &keys) != nil || len(keys.Keys) < 1 || len(keys.Keys) > 8 {
		return keys, fmt.Errorf("invalid gateway JWKS")
	}
	seen := map[string]bool{}
	for _, k := range keys.Keys {
		pub, ok := k.Key.(*rsa.PublicKey)
		if !ok || !k.IsPublic() || !k.Valid() || pub.N.BitLen() < 2048 ||
			!identifier(k.KeyID) || seen[k.KeyID] || k.Use != "sig" || k.Algorithm != "RS256" {
			return keys, fmt.Errorf("gateway keys require distinct public RS256 signing keys of at least 2048 bits")
		}
		seen[k.KeyID] = true
	}
	return keys, nil
}

func (m *Manager) verifyGateway(raw string) (Claims, error) {
	var empty Claims
	if len(raw) == 0 || len(raw) > 32<<10 || strings.Count(raw, ".") != 2 {
		return empty, ErrUnauthenticated
	}
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(token.Headers) != 1 || token.Headers[0].ExtraHeaders[jose.HeaderType] != "mcp-gateway+jwt" {
		return empty, ErrUnauthenticated
	}
	keys := m.gateway.Key(token.Headers[0].KeyID)
	if len(keys) != 1 {
		return empty, ErrUnauthenticated
	}
	var c struct {
		jwt.Claims
		IdentityIssuer string `json:"identity_issuer"`
		Client         string `json:"client_id"`
		Scope          string `json:"scope"`
	}
	if token.Claims(keys[0].Key, &c) != nil {
		return empty, ErrUnauthenticated
	}
	// The gateway authenticates this hop; ownership still uses the original
	// provider issuer and subject rather than the gateway's own identity.
	if c.Issuer != m.cfg.GatewayIssuer || c.IdentityIssuer != m.cfg.Issuer ||
		!identifier(c.Subject) || len(c.Subject) > 255 ||
		len(c.Audience) != 1 || c.Audience[0] != m.cfg.MCPAudience ||
		!slices.Contains(m.cfg.MCPClients, c.Client) ||
		c.Expiry == nil || c.IssuedAt == nil || c.NotBefore == nil {
		return empty, ErrUnauthenticated
	}
	now := m.now().Unix()
	exp, iat, nbf := int64(*c.Expiry), int64(*c.IssuedAt), int64(*c.NotBefore)
	if exp <= now || exp > now+60 ||
		iat <= 0 || iat > now+5 || iat < exp-60 || exp <= iat ||
		nbf <= 0 || nbf > now || nbf >= exp {
		return empty, ErrUnauthenticated
	}
	if !validScopes(c.Scope, false) {
		return empty, store.ErrForbidden
	}
	return Claims{Issuer: c.IdentityIssuer, Subject: c.Subject, Client: c.Client, Scope: c.Scope}, nil
}
