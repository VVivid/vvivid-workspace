// Package managementhttp exposes OAuth-protected REST and official SDK MCP
// transports over the same management service.
package managementhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/modelcontextprotocol/go-sdk/auth"
)

const Scope = "webhook-control.manage"

type OAuthConfig struct {
	Issuer   string `json:"issuer"`
	Resource string `json:"resource"`
	// Only trusted local test configuration can opt into HTTP on loopback.
	AllowInsecureLoopback bool `json:"allow_insecure_loopback"`
}

func trustedURL(raw string, allowLoopback bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("invalid trusted OAuth URL")
	}
	if u.Scheme == "https" {
		return u, nil
	}
	if allowLoopback && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return u, nil
	}
	return nil, errors.New("OAuth URLs require HTTPS; HTTP requires explicit loopback test configuration")
}

// NewTokenVerifier pins issuer discovery and JWKS, verifies RS256 signatures,
// issuer/audience/expiry/sub, and accepts only the at+jwt access-token profile.
// ID tokens with typ JWT (or token_use=id) are never management credentials.
func NewTokenVerifier(ctx context.Context, config OAuthConfig) (auth.TokenVerifier, error) {
	if _, err := trustedURL(config.Issuer, config.AllowInsecureLoopback); err != nil {
		return nil, err
	}
	if _, err := trustedURL(config.Resource, config.AllowInsecureLoopback); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many issuer redirects")
		}
		_, err := trustedURL(req.URL.String(), config.AllowInsecureLoopback)
		return err
	}}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), config.Issuer)
	if err != nil {
		return nil, errors.New("OAuth issuer discovery failed")
	}
	var metadata struct {
		JWKS string `json:"jwks_uri"`
	}
	if provider.Claims(&metadata) != nil {
		return nil, errors.New("invalid OAuth issuer metadata")
	}
	if _, err := trustedURL(metadata.JWKS, config.AllowInsecureLoopback); err != nil {
		return nil, err
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: config.Resource, SupportedSigningAlgs: []string{oidc.RS256}})
	return func(ctx context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
		parts := strings.Split(raw, ".")
		if len(parts) != 3 || len(raw) > 16<<10 {
			return nil, auth.ErrInvalidToken
		}
		headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, auth.ErrInvalidToken
		}
		var header struct {
			Type string `json:"typ"`
		}
		if json.Unmarshal(headerRaw, &header) != nil || header.Type != "at+jwt" {
			return nil, auth.ErrInvalidToken
		}
		token, err := verifier.Verify(ctx, raw)
		if err != nil || token.Subject == "" || token.Expiry.IsZero() || !time.Now().Before(token.Expiry) {
			return nil, auth.ErrInvalidToken
		}
		var claims struct {
			Scope    string `json:"scope"`
			TokenUse string `json:"token_use"`
		}
		if token.Claims(&claims) != nil || claims.TokenUse != "" && claims.TokenUse != "access" {
			return nil, auth.ErrInvalidToken
		}
		actor := management.Identity{Issuer: token.Issuer, Subject: token.Subject}
		return &auth.TokenInfo{Scopes: strings.Fields(claims.Scope), Expiration: token.Expiry, UserID: actor.PrincipalID(), Extra: map[string]any{"identity": actor}}, nil
	}, nil
}

func identity(info *auth.TokenInfo) (management.Identity, error) {
	if info != nil {
		if actor, ok := info.Extra["identity"].(management.Identity); ok && actor.PrincipalID() != "" {
			return actor, nil
		}
	}
	return management.Identity{}, auth.ErrInvalidToken
}
