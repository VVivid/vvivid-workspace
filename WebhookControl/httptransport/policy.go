// Package httptransport implements server-owned destination restrictions and
// single HTTP delivery attempts. Configuration validation performs no network IO.
package httptransport

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

var ErrOptions = errors.New("invalid HTTP transport options")

// OriginRule is server configuration, never an event-supplied permission.
// AllowPrivate explicitly permits HTTP and private/loopback/CGNAT addresses for
// this exact origin. Metadata, link-local and reserved networks remain forbidden.
type OriginRule struct {
	Origin       string `json:"origin"`
	AllowPrivate bool   `json:"allow_private"`
}

// Policy is an immutable exact-origin allowlist. Public destinations require
// HTTPS. Source-specific grants may further narrow this server-owned policy.
type Policy struct{ origins map[string]bool }

var _ ports.DestinationPolicy = (*Policy)(nil)

func NewPolicy(rules []OriginRule) (*Policy, error) {
	p := &Policy{origins: make(map[string]bool, len(rules))}
	for _, rule := range rules {
		d, err := parseDestination(rule.Origin)
		if err != nil {
			return nil, ErrOptions
		}
		if d.url.RawQuery != "" || d.url.ForceQuery || d.url.Path != "" && d.url.Path != "/" {
			return nil, ErrOptions
		}
		if d.url.Scheme != "https" && !rule.AllowPrivate {
			return nil, ErrOptions
		}
		if _, duplicate := p.origins[d.origin]; duplicate {
			return nil, ErrOptions
		}
		if ip, err := netip.ParseAddr(d.hostname); err == nil && !allowedAddress(ip, rule.AllowPrivate) {
			return nil, ErrOptions
		}
		p.origins[d.origin] = rule.AllowPrivate
	}
	return p, nil
}

// CanonicalOrigin returns a normalized scheme, host and nondefault port. It
// rejects credentials, fragments, zones and ambiguous/non-ASCII host syntax.
func CanonicalOrigin(raw string) (string, error) {
	d, err := parseDestination(raw)
	if err != nil {
		return "", ports.ErrPolicyDenied
	}
	return d.origin, nil
}

func (p *Policy) ApproveDestination(ctx context.Context, _ domain.ID, config domain.HTTPConfig) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	d, err := parseDestination(config.URL)
	if err != nil || p == nil {
		return ports.ErrPolicyDenied
	}
	allowPrivate, ok := p.origins[d.origin]
	if !ok || d.url.Scheme != "https" && !allowPrivate || metadataHost(d.hostname) {
		return ports.ErrPolicyDenied
	}
	if ip, err := netip.ParseAddr(d.hostname); err == nil && !allowedAddress(ip, allowPrivate) {
		return ports.ErrPolicyDenied
	}
	return nil
}

type destination struct {
	url      *url.URL
	origin   string
	hostname string
	port     string
}

func parseDestination(raw string) (destination, error) {
	if len(raw) == 0 || len(raw) > 2048 || strings.ContainsAny(raw, "\\#") {
		return destination{}, ports.ErrPolicyDenied
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" || u.Fragment != "" {
		return destination{}, ports.ErrPolicyDenied
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return destination{}, ports.ErrPolicyDenied
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return destination{}, ports.ErrPolicyDenied
		}
		host = ip.Unmap().String()
	} else if !validHostname(host) {
		return destination{}, ports.ErrPolicyDenied
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return destination{}, ports.ErrPolicyDenied
		}
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return destination{}, ports.ErrPolicyDenied
		}
		port = strconv.Itoa(n)
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if u.Scheme == "https" && port != "443" || u.Scheme == "http" && port != "80" {
		u.Host = net.JoinHostPort(host, port)
	}
	return destination{url: u, origin: u.Scheme + "://" + u.Host, hostname: host, port: port}, nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func metadataHost(host string) bool {
	switch host {
	case "metadata", "metadata.google", "metadata.google.internal", "instance-data", "instance-data.ec2.internal":
		return true
	}
	return false
}

var privatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("::1/128"),
}

var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100.100.100.200/32"), netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func allowedAddress(ip netip.Addr, allowPrivate bool) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	for _, prefix := range privatePrefixes {
		if prefix.Contains(ip) {
			return allowPrivate
		}
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	// Other IPv6 spaces include translation, discard and site-local addresses.
	return ip.Is4() || netip.MustParsePrefix("2000::/3").Contains(ip)
}
