package identity

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const gatewayTokenType = "mcp-gateway+jwt"

// loadGatewayKeys installs public trust at startup. No claim or request header
// can choose a key source, and no private gateway key belongs in this service.
func loadGatewayKeys(path string) (jose.JSONWebKeySet, error) {
	invalid := fmt.Errorf("gateway JWKS requires one to eight distinct public RS256 RSA signing keys with kid (at least 2048 bits)")
	file, err := os.Open(path)
	if err != nil {
		return jose.JSONWebKeySet{}, fmt.Errorf("cannot read gateway public JWKS")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return jose.JSONWebKeySet{}, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	var keys jose.JSONWebKeySet
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &keys) != nil || len(keys.Keys) == 0 || len(keys.Keys) > 8 {
		return jose.JSONWebKeySet{}, invalid
	}
	seen := make(map[string]bool)
	for _, key := range keys.Keys {
		public, ok := key.Key.(*rsa.PublicKey)
		if !ok || !key.Valid() || public.N.BitLen() < 2048 || !key.IsPublic() || !validClient(key.KeyID) || seen[key.KeyID] || key.Algorithm != "RS256" || key.Use != "sig" {
			return jose.JSONWebKeySet{}, invalid
		}
		seen[key.KeyID] = true
	}
	return keys, nil
}

type gatewayClaims struct {
	jwt.Claims
	IdentityIssuer string `json:"identity_issuer"`
	ClientID       string `json:"client_id"`
	Scope          string `json:"scope"`
}

// verifyGateway implements MCPGateway's v1 wire contract within this independent
// module. Only /mcp uses it; browser grants keep direct OIDC verification.
func (m *Manager) verifyGateway(raw string) (accessClaims, error) {
	// Compact JWS has protected headers only; reject JSON/unprotected headers.
	if len(raw) == 0 || len(raw) > 32<<10 || strings.Count(raw, ".") != 2 {
		return accessClaims{}, ErrUnauthenticated
	}
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil || len(token.Headers) != 1 || token.Headers[0].ExtraHeaders[jose.HeaderType] != gatewayTokenType || token.Headers[0].KeyID == "" {
		return accessClaims{}, ErrUnauthenticated
	}
	keys := m.gatewayKeys.Key(token.Headers[0].KeyID)
	if len(keys) != 1 {
		return accessClaims{}, ErrUnauthenticated
	}
	var claims gatewayClaims
	if token.Claims(keys[0].Key, &claims) != nil {
		return accessClaims{}, ErrUnauthenticated
	}
	if claims.Issuer != m.cfg.GatewayIssuer || len(claims.Audience) != 1 || claims.Audience[0] != m.cfg.MCPAudience {
		return accessClaims{}, ErrUnauthenticated
	}
	if claims.IdentityIssuer != m.cfg.Issuer || claims.Subject == "" || len(claims.Subject) > 255 ||
		strings.TrimSpace(claims.Subject) != claims.Subject || strings.ContainsRune(claims.Subject, 0) {
		return accessClaims{}, ErrUnauthenticated
	}
	if !slices.Contains(m.cfg.MCPClientIDs, claims.ClientID) || claims.Expiry == nil || claims.IssuedAt == nil || claims.NotBefore == nil {
		return accessClaims{}, ErrUnauthenticated
	}
	now := m.now().Unix()
	expires, issued, notBefore := int64(*claims.Expiry), int64(*claims.IssuedAt), int64(*claims.NotBefore)
	// Check expiry before subtraction to keep extreme signed timestamps safe.
	if expires <= now || expires > now+60 {
		return accessClaims{}, ErrUnauthenticated
	}
	// Permit five seconds of issuer clock skew, but never a lifetime over 60 seconds.
	if issued <= 0 || issued > now+5 || issued < expires-60 || expires <= issued {
		return accessClaims{}, ErrUnauthenticated
	}
	if notBefore <= 0 || notBefore > now || notBefore >= expires {
		return accessClaims{}, ErrUnauthenticated
	}
	for scope := range strings.FieldsSeq(claims.Scope) {
		if !slices.Contains(newsfeedScopes, scope) {
			return accessClaims{}, ErrForbidden
		}
	}
	// Ownership always uses the provider's original issuer with the verified sub.
	// Passing gateway iss into principal would create a different account boundary.
	return accessClaims{Issuer: claims.IdentityIssuer, Subject: claims.Subject, ClientID: claims.ClientID, Scope: claims.Scope}, nil
}
