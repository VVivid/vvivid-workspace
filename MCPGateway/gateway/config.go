package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"unicode"
)

type Config struct {
	Listen         string  `json:"listen"`
	Origin         string  `json:"origin"`
	Issuer         string  `json:"issuer"`
	SigningKeyFile string  `json:"signing_key_file"`
	Routes         []Route `json:"routes"`
}

type Route struct {
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	Upstream       string   `json:"upstream"`
	UpstreamHost   string   `json:"upstream_host,omitempty"`
	Audience       string   `json:"audience"`
	RequiredScopes []string `json:"required_scopes"`
	ForwardScopes  []string `json:"forward_scopes"`
	ClientIDs      []string `json:"client_ids"`
}

func Load(file string) (Config, error) {
	f, err := os.Open(file)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return Config{}, errors.New("configuration must be a regular file at most 1 MiB")
	}
	var c Config
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("configuration must contain one JSON object")
	}
	return c, c.Validate()
}

func secureURL(raw string, internal bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return nil, errors.New("invalid URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (internal || u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, errors.New("HTTPS required except internal upstreams and loopback")
	}
	return u, nil
}

func (c Config) Validate() error {
	origin, err := secureURL(c.Origin, false)
	if err != nil || origin.Path != "" {
		return errors.New("origin must be an exact HTTPS origin (HTTP loopback allowed)")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return errors.New("listen must include host and port")
	}
	if _, err := secureURL(c.Issuer, false); err != nil {
		return errors.New("invalid issuer")
	}
	if c.SigningKeyFile == "" || len(c.Routes) == 0 || len(c.Routes) > 64 {
		return errors.New("signing key and 1..64 routes required")
	}
	paths, names, audiences := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range c.Routes {
		if r.Name == "" || names[r.Name] || paths[r.Path] || audiences[r.Audience] {
			return errors.New("routes require distinct names, audiences and canonical /.../mcp paths")
		}
		// Paths are literal ServeMux routes, with discovery and health reserved.
		if !strings.HasPrefix(r.Path, "/") || !strings.HasSuffix(r.Path, "/mcp") || path.Clean(r.Path) != r.Path ||
			strings.ContainsAny(r.Path, "%?#\\ {}") || strings.ContainsFunc(r.Path, unicode.IsControl) ||
			strings.HasPrefix(r.Path, "/.well-known/") || strings.HasPrefix(r.Path, "/health/") {
			return errors.New("routes require distinct names, audiences and canonical /.../mcp paths")
		}
		if _, err := secureURL(r.Upstream, true); err != nil {
			return errors.New("invalid fixed upstream URL")
		}
		if strings.ContainsAny(r.UpstreamHost, "/\\ \t\r\n") {
			return errors.New("invalid upstream Host override")
		}
		if r.Audience == "" || len(r.RequiredScopes) == 0 || len(r.ForwardScopes) == 0 || len(r.ClientIDs) == 0 {
			return errors.New("route requires backend audience, required/forward scopes and allowed client IDs")
		}
		for _, values := range [][]string{r.RequiredScopes, r.ForwardScopes, r.ClientIDs} {
			seen := map[string]bool{}
			for _, value := range values {
				if value == "" || len(value) > 256 || strings.ContainsAny(value, "\"\\ \t\r\n\x00") || seen[value] {
					return errors.New("permissions and clients must be distinct bounded identifiers")
				}
				seen[value] = true
			}
		}
		paths[r.Path], names[r.Name], audiences[r.Audience] = true, true, true
	}
	return nil
}
