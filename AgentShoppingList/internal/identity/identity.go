package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"agentshoppinglist/internal/store/sqlite"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("permission denied")
)

const (
	accessLifetime = 5 * time.Minute
	refreshEvery   = time.Minute
	sessionTTL     = 12 * time.Hour
)

// Principal is built only after credential verification and enrollment lookup.
// Adapters pass it into services; request bodies never select an owner or client.
type Principal struct {
	owner    sqlite.Owner
	identity sqlite.ExternalIdentity
	clientID string
	scopes   []string
}

func (p Principal) Owner() sqlite.Owner               { return p.owner }
func (p Principal) Identity() sqlite.ExternalIdentity { return p.identity }
func (p Principal) ClientID() string                  { return p.clientID }
func (p Principal) Allows(scope string) bool {
	return p.owner.ID() != "" && slices.Contains(p.scopes, scope)
}
func (p Principal) Require(scopes ...string) error {
	if p.owner.ID() == "" {
		return ErrUnauthenticated
	}
	for _, scope := range scopes {
		if !p.Allows(scope) {
			return ErrForbidden
		}
	}
	return nil
}

type principalKey struct{}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok && principal.owner.ID() != ""
}

type providerMetadata struct {
	Issuer      string   `json:"issuer"`
	JWKS        string   `json:"jwks_uri"`
	Revocation  string   `json:"revocation_endpoint"`
	EndSession  string   `json:"end_session_endpoint"`
	PKCEMethods []string `json:"code_challenge_methods_supported"`
	ResponseISS bool     `json:"authorization_response_iss_parameter_supported"`
}

type Manager struct {
	cfg        Config
	store      *sqlite.Store
	client     *http.Client
	oauth      oauth2.Config
	metadata   providerMetadata
	keys       *oidc.RemoteKeySet
	idVerifier *oidc.IDTokenVerifier
	now        func() time.Time
	// One application instance owns SQLite. Serialize refresh for a given cookie
	// so rotating refresh tokens cannot race. Stripes keep memory bounded.
	sessionLocks [64]sync.Mutex
}

func New(ctx context.Context, cfg Config, store *sqlite.Store) (*Manager, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("identity requires durable storage")
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed")
	}
	var metadata providerMetadata
	if err := provider.Claims(&metadata); err != nil || metadata.Issuer != cfg.Issuer || !slices.Contains(metadata.PKCEMethods, "S256") {
		return nil, fmt.Errorf("OIDC discovery must match the exact issuer and support PKCE S256")
	}
	issuer, _ := url.Parse(cfg.Issuer)
	endpoint := provider.Endpoint()
	for _, raw := range []string{endpoint.AuthURL, endpoint.TokenURL, metadata.JWKS, metadata.Revocation, metadata.EndSession} {
		u, err := secureURL(raw)
		if err != nil || u.Scheme != issuer.Scheme || u.Host != issuer.Host {
			return nil, fmt.Errorf("OIDC endpoints must use the configured issuer origin")
		}
	}
	m := &Manager{cfg: cfg, store: store, client: client, metadata: metadata, now: time.Now}
	endpoint.AuthStyle = oauth2.AuthStyleInHeader
	m.oauth = oauth2.Config{
		ClientID:     cfg.WebClientID,
		ClientSecret: cfg.WebClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  cfg.Origin + "/auth/callback",
		Scopes:       []string{"openid", Read, ListsWrite},
	}
	m.keys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), metadata.JWKS)
	m.idVerifier = oidc.NewVerifier(cfg.Issuer, m.keys, &oidc.Config{ClientID: cfg.WebClientID, SupportedSigningAlgs: []string{oidc.RS256}, Now: func() time.Time { return m.now() }})
	return m, nil
}

type accessClaims struct {
	Issuer    string       `json:"iss"`
	Subject   string       `json:"sub"`
	Audience  jwt.Audience `json:"aud"`
	Expiry    int64        `json:"exp"`
	IssuedAt  int64        `json:"iat"`
	NotBefore int64        `json:"nbf"`
	ClientID  string       `json:"azp"`
	Type      string       `json:"typ"`
	Scope     string       `json:"scope"`
}

// verifyAccess validates a Keycloak access JWT, never an ID token. Restrict the
// JOSE algorithm before using the rotating remote key set, then validate all
// resource-server claims independently of OIDC ID-token semantics.
func (m *Manager) verifyAccess(ctx context.Context, raw, audience string) (accessClaims, error) {
	if len(raw) == 0 || len(raw) > 32<<10 {
		return accessClaims{}, ErrUnauthenticated
	}
	signed, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(signed.Signatures) != 1 {
		return accessClaims{}, ErrUnauthenticated
	}
	headerType := signed.Signatures[0].Header.ExtraHeaders[jose.HeaderType]
	if headerType != "JWT" && headerType != "at+jwt" {
		return accessClaims{}, ErrUnauthenticated
	}
	payload, err := m.keys.VerifySignature(ctx, raw)
	if err != nil {
		return accessClaims{}, ErrUnauthenticated
	}
	var claims accessClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return accessClaims{}, ErrUnauthenticated
	}
	now := m.now().Unix()
	if claims.Issuer != m.cfg.Issuer || claims.Subject == "" || len(claims.Subject) > 255 || strings.TrimSpace(claims.Subject) != claims.Subject ||
		len(claims.Audience) != 1 || claims.Audience[0] != audience || claims.Type != "Bearer" ||
		claims.IssuedAt <= 0 || claims.IssuedAt > now+30 || claims.Expiry <= now || claims.Expiry > now+int64(accessLifetime/time.Second) || claims.Expiry <= claims.IssuedAt ||
		claims.Expiry-claims.IssuedAt > int64(accessLifetime/time.Second) || claims.NotBefore > now {
		return accessClaims{}, ErrUnauthenticated
	}
	if audience == m.cfg.APIAudience {
		if claims.ClientID != m.cfg.WebClientID {
			return accessClaims{}, ErrUnauthenticated
		}
	} else if audience != m.cfg.MCPAudience || !slices.Contains(m.cfg.MCPClientIDs, claims.ClientID) {
		return accessClaims{}, ErrUnauthenticated
	}
	for scope := range strings.FieldsSeq(claims.Scope) {
		if scope == "openid" || scope == "profile" || scope == "email" || (scope == "offline_access" && audience == m.cfg.MCPAudience) {
			continue
		}
		if !slices.Contains(shoppingScopes, scope) || (audience == m.cfg.APIAudience && scope != Read && scope != ListsWrite) {
			return accessClaims{}, ErrForbidden
		}
	}
	return claims, nil
}

func (m *Manager) principal(ctx context.Context, claims accessClaims) (Principal, error) {
	identity := sqlite.ExternalIdentity{Issuer: claims.Issuer, Subject: claims.Subject}
	owner, err := m.store.OwnerForIdentity(ctx, identity)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{owner: owner, identity: identity, clientID: claims.ClientID, scopes: strings.Fields(claims.Scope)}, nil
}

func (m *Manager) AuthenticateBearer(ctx context.Context, raw, audience string) (Principal, error) {
	claims, err := m.verifyAccess(ctx, raw, audience)
	if err != nil {
		return Principal{}, err
	}
	return m.principal(ctx, claims)
}

func (m *Manager) APIAudience() string { return m.cfg.APIAudience }
func (m *Manager) MCPAudience() string { return m.cfg.MCPAudience }
func (m *Manager) IssuerOrigin() string {
	issuer, _ := url.Parse(m.cfg.Issuer)
	return issuer.Scheme + "://" + issuer.Host
}
