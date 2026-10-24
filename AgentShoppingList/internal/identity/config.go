// Package identity authenticates enrolled users and supplies a verified actor to
// both HTTP adapters. It never provisions an account during authentication.
package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"

	"agentshoppinglist/internal/gatewaytrust"
)

const (
	Read             = "shopping:read"
	ProductsWrite    = "shopping:products:write"
	ListsWrite       = "shopping:lists:write"
	PurchasesWrite   = "shopping:purchases:write"
	PreferencesWrite = "shopping:preferences:write"
)

var shoppingScopes = []string{Read, ProductsWrite, ListsWrite, PurchasesWrite, PreferencesWrite}

type Config struct {
	Gateway         *gatewaytrust.Config
	Origin          string
	Issuer          string
	WebClientID     string
	WebClientSecret string
	APIAudience     string
	MCPAudience     string
	MCPClientIDs    []string
	Keys            *Keyring
}

// LoadConfig leaves the public shell available when the entire identity bundle
// is absent. A partial bundle is an error, never a weaker authentication mode.
func LoadConfig(lookup func(string) (string, bool)) (*Config, error) {
	names := []string{
		"SHOPPING_ORIGIN",
		"SHOPPING_OIDC_ISSUER",
		"SHOPPING_WEB_CLIENT_ID",
		"SHOPPING_WEB_CLIENT_SECRET_FILE",
		"SHOPPING_API_AUDIENCE",
		"SHOPPING_MCP_AUDIENCE",
		"SHOPPING_MCP_CLIENT_IDS",
		"SHOPPING_SESSION_KEYS_FILE",
	}
	values := make(map[string]string)
	configured := false
	for _, name := range names {
		value, present := lookup(name)
		configured = configured || present
		values[name] = value
	}
	gatewayIssuer, gi := lookup("SHOPPING_GATEWAY_ISSUER")
	gatewayAudience, ga := lookup("SHOPPING_GATEWAY_AUDIENCE")
	gatewayKeys, gk := lookup("SHOPPING_GATEWAY_JWKS_FILE")
	if (gi || ga || gk) && (!configured || gatewayIssuer == "" || gatewayAudience == "" || gatewayKeys == "") {
		return nil, fmt.Errorf("gateway trust requires complete shopping identity and gateway issuer, audience and JWKS")
	}
	if !configured {
		return nil, nil
	}
	for _, name := range names {
		if values[name] == "" {
			return nil, fmt.Errorf("%s is required when identity is configured", name)
		}
	}
	cfg := &Config{
		Origin:       values["SHOPPING_ORIGIN"],
		Issuer:       values["SHOPPING_OIDC_ISSUER"],
		WebClientID:  values["SHOPPING_WEB_CLIENT_ID"],
		APIAudience:  values["SHOPPING_API_AUDIENCE"],
		MCPAudience:  values["SHOPPING_MCP_AUDIENCE"],
		MCPClientIDs: strings.Split(values["SHOPPING_MCP_CLIENT_IDS"], ","),
	}
	if gi || ga || gk {
		cfg.Gateway = &gatewaytrust.Config{Issuer: gatewayIssuer, Audience: gatewayAudience, JWKSFile: gatewayKeys}
	}
	secret, err := readPrivateFile(values["SHOPPING_WEB_CLIENT_SECRET_FILE"])
	if err != nil {
		return nil, fmt.Errorf("SHOPPING_WEB_CLIENT_SECRET_FILE: %w", err)
	}
	cfg.WebClientSecret = strings.TrimSpace(string(secret))
	keys, err := readPrivateFile(values["SHOPPING_SESSION_KEYS_FILE"])
	if err != nil {
		return nil, fmt.Errorf("SHOPPING_SESSION_KEYS_FILE: %w", err)
	}
	cfg.Keys, err = parseKeys(keys)
	if err != nil {
		return nil, err
	}
	return cfg, cfg.validate()
}

func (cfg *Config) validate() error {
	origin, err := secureURL(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Path != "" || origin.RawPath != "" {
		return fmt.Errorf("SHOPPING_ORIGIN must be an exact HTTPS origin without a trailing slash or path")
	}
	if _, err := secureURL(cfg.Issuer); err != nil {
		return fmt.Errorf("SHOPPING_OIDC_ISSUER must be an exact HTTPS issuer (HTTP loopback is allowed for local issuers)")
	}
	if cfg.APIAudience != cfg.Origin+"/api/v1" || cfg.MCPAudience != cfg.Origin+"/mcp" {
		return fmt.Errorf("API and MCP audiences must be the exact origin plus /api/v1 and /mcp respectively")
	}
	if !validClient(cfg.WebClientID) || cfg.WebClientSecret == "" || len(cfg.WebClientSecret) > 4096 || cfg.Keys == nil {
		return fmt.Errorf("identity requires a web client, private client secret and session keyring")
	}
	if len(cfg.MCPClientIDs) == 0 || len(cfg.MCPClientIDs) > 16 {
		return fmt.Errorf("one to sixteen explicit shopping MCP clients are required")
	}
	for i, client := range cfg.MCPClientIDs {
		if !validClient(client) || client == cfg.WebClientID || slices.Contains(cfg.MCPClientIDs[:i], client) {
			return fmt.Errorf("shopping client IDs must be distinct bounded identifiers")
		}
	}
	return nil
}

func validClient(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, " ,\t\r\n")
}

func secureURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("invalid absolute HTTPS URL")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, fmt.Errorf("HTTPS is required")
	}
	return u, nil
}

func readPrivateFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open private file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 16<<10 {
		return nil, fmt.Errorf("secret file must be a private regular file (0400 or 0600), at most 16 KiB")
	}
	return io.ReadAll(io.LimitReader(file, (16<<10)+1))
}

// Keyring retains old decrypt-only keys during a deliberate key rotation. The
// active key encrypts all new login attempts and refreshed sessions.
type Keyring struct {
	active string
	keys   map[string]cipher.AEAD
}

func parseKeys(raw []byte) (*Keyring, error) {
	var document struct {
		Active string            `json:"active"`
		Keys   map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || len(document.Keys) == 0 || len(document.Keys) > 3 {
		return nil, fmt.Errorf("session key file must contain active and one to three base64 AES-256 keys")
	}
	ring := &Keyring{active: document.Active, keys: make(map[string]cipher.AEAD)}
	for name, encoded := range document.Keys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if !validClient(name) || strings.Contains(name, ":") || len(name) > 64 || err != nil || len(key) != 32 {
			return nil, fmt.Errorf("session keys require bounded names and 32 random bytes encoded as standard base64")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("invalid session key")
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		ring.keys[name] = aead
	}
	if ring.keys[ring.active] == nil {
		return nil, fmt.Errorf("session active key must name a key in the file")
	}
	return ring, nil
}
