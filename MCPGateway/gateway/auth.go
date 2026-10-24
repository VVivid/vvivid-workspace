package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/MCPGateway/trust"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Authenticator is the provider boundary. The core router uses verified identities.
type Authenticator interface {
	Authenticate(context.Context, string, string) (trust.Principal, error)
}

type OIDCAuthenticator struct {
	issuer string
	keys   *oidc.RemoteKeySet
}

func NewOIDCAuthenticator(ctx context.Context, issuer string) (*OIDCAuthenticator, error) {
	if _, err := secureURL(issuer, false); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx = oidc.ClientContext(ctx, client)
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	var metadata struct {
		JWKS string `json:"jwks_uri"`
	}
	if p.Claims(&metadata) != nil {
		return nil, errors.New("invalid discovery")
	}
	if _, err := secureURL(metadata.JWKS, false); err != nil {
		return nil, errors.New("invalid signing key URL")
	}
	// Keep the bounded client after the startup context is cancelled.
	return &OIDCAuthenticator{issuer, oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), metadata.JWKS)}, nil
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, raw, resource string) (trust.Principal, error) {
	invalid := errors.New("invalid access token")
	if len(raw) == 0 || len(raw) > 32<<10 {
		return trust.Principal{}, invalid
	}
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(signed.Signatures) != 1 {
		return trust.Principal{}, invalid
	}
	typ := signed.Signatures[0].Header.ExtraHeaders[jose.HeaderType]
	if typ != "at+jwt" && typ != "JWT" {
		return trust.Principal{}, invalid
	}
	payload, err := a.keys.VerifySignature(ctx, raw)
	if err != nil {
		return trust.Principal{}, invalid
	}
	var c struct {
		jwt.Claims
		Scope    string `json:"scope"`
		ClientID string `json:"client_id"`
		AZP      string `json:"azp"`
		Type     string `json:"typ"`
	}
	if json.Unmarshal(payload, &c) != nil {
		return trust.Principal{}, invalid
	}
	now := time.Now().Unix()
	// RFC 9068 access JWTs, or Keycloak's explicitly marked Bearer access JWTs.
	// Ordinary OIDC ID tokens do not satisfy this access-token profile.
	if typ == "JWT" && c.Type != "Bearer" || c.Issuer != a.issuer || c.Subject == "" || len(c.Subject) > 255 || strings.ContainsRune(c.Subject, 0) || c.Expiry == nil || int64(*c.Expiry) <= now || c.IssuedAt == nil || int64(*c.IssuedAt) > now+30 || *c.Expiry <= *c.IssuedAt || c.NotBefore != nil && int64(*c.NotBefore) > now || len(c.Audience) != 1 || c.Audience[0] != resource {
		return trust.Principal{}, invalid
	}
	if c.ClientID == "" {
		c.ClientID = c.AZP
	} else if c.AZP != "" && c.ClientID != c.AZP {
		return trust.Principal{}, invalid
	}
	if c.ClientID == "" {
		return trust.Principal{}, invalid
	}
	return trust.Principal{Issuer: c.Issuer, Subject: c.Subject, ClientID: c.ClientID, Scope: c.Scope, ExpiresAt: time.Unix(int64(*c.Expiry), 0)}, nil
}

func allowed(p trust.Principal, r Route) bool {
	if !slices.Contains(r.ClientIDs, p.ClientID) {
		return false
	}
	scopes := strings.Fields(p.Scope)
	for _, required := range r.RequiredScopes {
		if !slices.Contains(scopes, required) {
			return false
		}
	}
	return true
}
