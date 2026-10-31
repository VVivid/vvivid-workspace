// Package thumbnail fetches only explicitly permitted source-provided images.
// It never inspects article pages or returns an external image URL to a browser.
package thumbnail

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"agentnewsfeed/internal/urls"
)

const maxBytes = 2 << 20

type Policy struct{ hosts []string }

// NewPolicy has no permitted hosts by default. Hosts are exact, not suffixes.
func NewPolicy(raw string) (Policy, error) {
	p := Policy{}
	if raw == "" {
		return p, nil
	}
	for host := range strings.SplitSeq(raw, ",") {
		if host != strings.ToLower(host) || !urls.PublicHostname(host) || slices.Contains(p.hosts, host) || len(p.hosts) >= 16 {
			return Policy{}, fmt.Errorf("thumbnail hosts must be one to sixteen distinct exact lowercase public DNS names")
		}
		p.hosts = append(p.hosts, host)
	}
	return p, nil
}

func (p Policy) Validate(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > 2048 {
		return fmt.Errorf("thumbnail URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Host == "" || u.Host != u.Hostname() || !slices.Contains(p.hosts, u.Hostname()) || strings.ContainsAny(raw, "\\ \t\r\n") {
		return fmt.Errorf("thumbnail requires an explicitly permitted HTTPS host without credentials, fragment or port")
	}
	return nil
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type dialFunc func(context.Context, string, string) (net.Conn, error)

type Proxy struct {
	policy   Policy
	resolver resolver
	dial     dialFunc
	roots    *x509.CertPool
	slots    chan struct{}
}

func NewProxy(policy Policy) *Proxy {
	return &Proxy{policy: policy, resolver: net.DefaultResolver, dial: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, slots: make(chan struct{}, 4)}
}

// Fetch constrains the complete operation, including DNS, redirects, TLS, bytes,
// pixels and re-encoding. All answers must be public before one is pinned.
func (p *Proxy) Fetch(ctx context.Context, raw string) ([]byte, error) {
	if raw == "" || p.policy.Validate(raw) != nil {
		return nil, fmt.Errorf("thumbnail unavailable")
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return nil, fmt.Errorf("thumbnail busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.roots},
		TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 16 << 10,
	}
	defer transport.CloseIdleConnections()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" || !slices.Contains(p.policy.hosts, host) {
			return nil, fmt.Errorf("thumbnail destination denied")
		}
		addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 || len(addresses) > 16 {
			return nil, fmt.Errorf("thumbnail DNS denied")
		}
		for _, ip := range addresses {
			if !publicIP(ip) {
				return nil, fmt.Errorf("thumbnail DNS denied")
			}
		}
		return p.dial(ctx, "tcp", net.JoinHostPort(addresses[0].String(), port))
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		r.Header.Del("Referer")
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		if len(via) > 2 || p.policy.Validate(r.URL.String()) != nil {
			return errors.New("thumbnail redirect denied")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("thumbnail unavailable")
	}
	request.Header.Set("Accept", "image/jpeg, image/png")
	request.Header.Set("User-Agent", "AgentNewsfeed/1")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("thumbnail unavailable")
	}
	defer response.Body.Close()
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || response.StatusCode != http.StatusOK || response.ContentLength > maxBytes || (media != "image/jpeg" && media != "image/png") || response.Header.Get("Content-Encoding") != "" {
		return nil, fmt.Errorf("thumbnail content denied")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		return nil, fmt.Errorf("thumbnail too large")
	}
	return sanitize(body)
}

func sanitize(body []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width < 64 || cfg.Height < 64 || cfg.Width > 2048 || cfg.Height > 2048 || cfg.Width*cfg.Height > 1<<20 {
		return nil, fmt.Errorf("thumbnail dimensions denied")
	}
	source, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("thumbnail image denied")
	}
	// Re-encoding strips metadata and guarantees a raster JPEG response.
	target := image.NewRGBA(source.Bounds())
	draw.Draw(target, target.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(target, target.Bounds(), source, source.Bounds().Min, draw.Over)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, target, &jpeg.Options{Quality: 80}); err != nil || output.Len() > maxBytes {
		return nil, fmt.Errorf("thumbnail encoding denied")
	}
	return output.Bytes(), nil
}

var deniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("168.63.129.16/32"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("3ffe::/16"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func publicIP(ip netip.Addr) bool {
	// Mapped IPv4, IPv6 translation/tunnel and non-global prefixes are rejected.
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range deniedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
