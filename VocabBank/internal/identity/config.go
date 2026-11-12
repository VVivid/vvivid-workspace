// Package identity verifies browser OIDC grants and private MCP identities.
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
)

type Config struct {
	Origins                                           []string
	Issuer, WebClientID, WebClientSecret, APIAudience string
	MCPMode, MCPResource, MCPAudience, GatewayIssuer  string
	MCPClients                                        []string
	GatewayJWKSFile                                   string
	Keys                                              *Keyring
}

func LoadConfig() (Config, error) {
	c := Config{
		Origins:         strings.Split(os.Getenv("VOCAB_ORIGINS"), ","),
		Issuer:          os.Getenv("VOCAB_OIDC_ISSUER"),
		WebClientID:     os.Getenv("VOCAB_WEB_CLIENT_ID"),
		APIAudience:     os.Getenv("VOCAB_API_AUDIENCE"),
		MCPMode:         os.Getenv("VOCAB_MCP_MODE"),
		MCPResource:     os.Getenv("VOCAB_MCP_RESOURCE"),
		MCPAudience:     os.Getenv("VOCAB_MCP_AUDIENCE"),
		GatewayIssuer:   os.Getenv("VOCAB_GATEWAY_ISSUER"),
		MCPClients:      strings.Split(os.Getenv("VOCAB_MCP_CLIENT_IDS"), ","),
		GatewayJWKSFile: os.Getenv("VOCAB_GATEWAY_JWKS_FILE"),
	}
	raw, err := PrivateFile(os.Getenv("VOCAB_WEB_CLIENT_SECRET_FILE"))
	if err != nil {
		return c, fmt.Errorf("web client secret: %w", err)
	}
	c.WebClientSecret = strings.TrimSpace(string(raw))
	raw, err = PrivateFile(os.Getenv("VOCAB_SESSION_KEYS_FILE"))
	if err != nil {
		return c, fmt.Errorf("session keyring: %w", err)
	}
	c.Keys, err = ParseKeys(raw)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if len(c.Origins) < 1 || len(c.Origins) > 2 {
		return fmt.Errorf("one or two exact origins are required")
	}
	for i, raw := range c.Origins {
		u, err := secureURL(raw)
		if err != nil || u.Path != "" || slices.Contains(c.Origins[:i], raw) {
			return fmt.Errorf("origins must be distinct exact HTTPS origins without paths")
		}
	}
	if _, err := secureURL(c.Issuer); err != nil {
		return fmt.Errorf("exact HTTPS OIDC issuer required")
	}
	if c.APIAudience != c.Origins[0]+"/api/v1" || !identifier(c.WebClientID) ||
		c.WebClientSecret == "" || len(c.WebClientSecret) > 4096 || c.Keys == nil {
		return fmt.Errorf("canonical API audience, client and protected keyring required")
	}
	if len(c.MCPClients) < 1 || len(c.MCPClients) > 16 {
		return fmt.Errorf("explicit MCP clients required")
	}
	for i, v := range c.MCPClients {
		if !identifier(v) || v == c.WebClientID || slices.Contains(c.MCPClients[:i], v) {
			return fmt.Errorf("distinct explicit MCP clients required")
		}
	}
	u, err := secureURL(c.MCPResource)
	if err != nil || !strings.HasSuffix(u.Path, "/mcp") {
		return fmt.Errorf("exact MCP resource URL required")
	}
	switch c.MCPMode {
	case "gateway":
		u, err := secureURL(c.GatewayIssuer)
		if err != nil || u.Path != "" || c.GatewayJWKSFile == "" || c.MCPAudience != "vocabbank-mcp" {
			return fmt.Errorf("gateway requires its exact issuer, public JWKS and vocabbank-mcp audience")
		}
	case "direct":
		if c.MCPAudience != c.MCPResource || c.GatewayIssuer != "" || c.GatewayJWKSFile != "" {
			return fmt.Errorf("direct MCP audience must equal its resource; gateway settings must be absent")
		}
	default:
		return fmt.Errorf("MCP mode must be gateway or direct")
	}
	return nil
}

func identifier(v string) bool {
	return len(v) > 0 && len(v) <= 256 && strings.TrimSpace(v) == v && !strings.ContainsAny(v, " \t\n\r\x00")
}

func secureURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
		return nil, fmt.Errorf("invalid URL")
	}
	loop := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loop = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return nil, fmt.Errorf("HTTPS required")
	}
	return u, nil
}

func PrivateFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open protected file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() ||
		(st.Mode().Perm() != 0400 && st.Mode().Perm() != 0600) || st.Size() > 64<<10 {
		return nil, fmt.Errorf("protected file must be regular, 0400 or 0600, at most 64 KiB")
	}
	return io.ReadAll(io.LimitReader(f, (64<<10)+1))
}

type Keyring struct {
	active string
	keys   map[string]cipher.AEAD
}

func ParseKeys(raw []byte) (*Keyring, error) {
	var doc struct {
		Active string            `json:"active"`
		Keys   map[string]string `json:"keys"`
	}
	if json.Unmarshal(raw, &doc) != nil || len(doc.Keys) < 1 || len(doc.Keys) > 3 {
		return nil, fmt.Errorf("invalid session keyring")
	}
	ring := &Keyring{active: doc.Active, keys: map[string]cipher.AEAD{}}
	for name, encoded := range doc.Keys {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != 32 || !identifier(name) || strings.Contains(name, ":") {
			return nil, fmt.Errorf("session keys require names and base64 32-byte keys")
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		ring.keys[name], err = cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
	}
	if ring.keys[ring.active] == nil {
		return nil, fmt.Errorf("active session key is missing")
	}
	return ring, nil
}
