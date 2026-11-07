// Package service binds the private HTTP endpoint to verified identity and the
// journal. The gateway owns public OAuth discovery and login.
package service

import (
	"cmp"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/internal/identity"
)

type Config struct {
	Listen        string
	Database      string
	BackendOrigin string
	Identity      identity.Config
}

// FromEnv keeps local listening private by default; identity has no development
// defaults. An explicit MEMORY_LISTEN=:8080 supports a private container Service.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:        cmp.Or(getenv("MEMORY_LISTEN"), "127.0.0.1:8080"),
		Database:      getenv("MEMORY_DATABASE"),
		BackendOrigin: getenv("MEMORY_BACKEND_ORIGIN"),
		Identity: identity.Config{
			GatewayIssuer:  getenv("MEMORY_GATEWAY_ISSUER"),
			Audience:       getenv("MEMORY_AUDIENCE"),
			IdentityIssuer: getenv("MEMORY_IDENTITY_ISSUER"),
			JWKSFile:       getenv("MEMORY_GATEWAY_JWKS_FILE"),
			AllowedClients: strings.Split(getenv("MEMORY_ALLOWED_CLIENT_IDS"), ","),
		},
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Database == "" || strings.ContainsRune(c.Database, 0) {
		return errors.New("MEMORY_DATABASE is required")
	}
	if err := c.Identity.Validate(); err != nil {
		return err
	}
	if !validOrigin(c.BackendOrigin) {
		return errors.New("MEMORY_BACKEND_ORIGIN must be an exact HTTP(S) origin with canonical Host")
	}
	_, port, err := net.SplitHostPort(c.Listen)
	n, numberErr := strconv.Atoi(port)
	if err != nil || numberErr != nil || n < 1 || n > 65535 || strings.ContainsAny(c.Listen, " \t\r\n") {
		return errors.New("MEMORY_LISTEN must be a host:port listener")
	}
	return nil
}

func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || len(origin) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host != strings.ToLower(u.Host) || strings.HasSuffix(u.Hostname(), ".") || u.String() != origin {
		return false
	}
	hostname := u.Hostname()
	if net.ParseIP(hostname) == nil {
		for _, label := range strings.Split(hostname, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return false
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					return false
				}
			}
		}
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return false
		}
	}
	return !strings.HasSuffix(u.Host, ":")
}
