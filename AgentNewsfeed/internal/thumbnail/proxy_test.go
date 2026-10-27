package thumbnail

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type fakeResolver struct {
	addresses []netip.Addr
	calls     int
}

func (r *fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.addresses, nil
}

func TestThumbnailPolicyAndPublicAddresses(t *testing.T) {
	empty, _ := NewPolicy("")
	if empty.Validate("https://example.com/p.png") == nil {
		t.Fatal("empty allowlist permitted image")
	}
	p, err := NewPolicy("images.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://images.example.com/a", "file:///etc/passwd", "https://user:pass@images.example.com/a", "https://images.example.com:443/a", "https://images.example.com.attacker.com/a", "https://images.example.com/a#fragment", "https://169.254.169.254/a", "https://images.example.com/a\\b"} {
		if p.Validate(raw) == nil {
			t.Fatalf("unsafe thumbnail %q", raw)
		}
	}
	if p.Validate("https://images.example.com/path/photo.png?width=100") != nil {
		t.Fatal("permitted source image rejected")
	}
	for _, raw := range []string{"127.0.0.1", "0.0.0.0", "10.0.0.1", "100.64.0.1", "169.254.169.254", "172.31.255.255", "192.168.0.1", "192.0.0.192", "168.63.129.16", "198.18.0.1", "203.0.113.1", "224.0.0.1", "255.255.255.255", "::1", "::", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "fe80::1", "fd00:ec2::254", "2001:db8::1", "2002:7f00:1::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Fatalf("non-public address allowed %s", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Fatalf("public address denied %s", raw)
		}
	}
}

func TestRejectPrivateOrMixedDNSBeforeDial(t *testing.T) {
	policy, _ := NewPolicy("example.com")
	for _, addresses := range [][]netip.Addr{{netip.MustParseAddr("169.254.169.254")}, {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")}, {netip.MustParseAddr("::ffff:127.0.0.1")}, nil} {
		p := NewProxy(policy)
		p.resolver = &fakeResolver{addresses: addresses}
		p.dial = func(context.Context, string, string) (net.Conn, error) {
			t.Error("unsafe DNS answer reached dial")
			return nil, fmt.Errorf("unexpected")
		}
		if _, err := p.Fetch(t.Context(), "https://example.com/photo.jpg"); err == nil {
			t.Fatal("unsafe DNS accepted")
		}
	}
}

func pngImage(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testProxy(t *testing.T, handler http.Handler) (*Proxy, *fakeResolver, *[]string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	policy, _ := NewPolicy("example.com")
	p := NewProxy(policy)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	p.roots = roots
	resolver := &fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	p.resolver = resolver
	addresses := []string{}
	p.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return p, resolver, &addresses
}

func TestPinnedTLSRasterReencodeAndNoCredentialOrEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	source := append(pngImage(t), []byte("SENSITIVE_TRAILING_METADATA")...)
	p, resolver, dials := testProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.TLS == nil {
			t.Error("TLS hostname lost")
		}
		for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if r.Header.Get(name) != "" {
				t.Errorf("credential/header forwarded %s", name)
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(source)
	}))
	body, err := p.Fetch(t.Context(), "https://example.com/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("response not reencoded JPEG: %v", err)
	}
	if bytes.Contains(body, []byte("SENSITIVE_TRAILING_METADATA")) {
		t.Fatal("metadata retained")
	}
	if resolver.calls != 1 || len(*dials) != 1 || (*dials)[0] != "93.184.216.34:443" {
		t.Fatalf("DNS not pinned calls=%d dials=%v", resolver.calls, *dials)
	}
}

func TestResponseTypeSizeDimensionsRedirectAndCancellation(t *testing.T) {
	imageBody := pngImage(t)
	for _, mode := range []string{"html", "svg", "mismatched", "large", "compressed", "redirect-private", "redirect-outside", "redirect-loop", "bad-status"} {
		t.Run(mode, func(t *testing.T) {
			p, _, _ := testProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "html":
					w.Header().Set("Content-Type", "text/html")
					_, _ = w.Write([]byte("<script>x</script>"))
				case "svg":
					w.Header().Set("Content-Type", "image/svg+xml")
					_, _ = w.Write([]byte("<svg/>"))
				case "mismatched":
					w.Header().Set("Content-Type", "image/jpeg")
					_, _ = w.Write([]byte("<html>not a JPEG</html>"))
				case "large":
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write([]byte(strings.Repeat("x", maxBytes+1)))
				case "compressed":
					w.Header().Set("Content-Type", "image/png")
					w.Header().Set("Content-Encoding", "gzip")
					_, _ = w.Write(imageBody)
				case "redirect-private":
					http.Redirect(w, r, "https://169.254.169.254/latest", 302)
				case "redirect-outside":
					http.Redirect(w, r, "https://other.example.com/image", 302)
				case "redirect-loop":
					http.Redirect(w, r, "https://example.com/again", 302)
				case "bad-status":
					w.WriteHeader(401)
				}
			}))
			if _, err := p.Fetch(t.Context(), "https://example.com/photo"); err == nil {
				t.Fatalf("%s accepted", mode)
			}
		})
	}
	var pixel bytes.Buffer
	if err := png.Encode(&pixel, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := sanitize(pixel.Bytes()); err == nil {
		t.Fatal("tracking pixel accepted")
	}
	var huge bytes.Buffer
	if err := png.Encode(&huge, image.NewGray(image.Rect(0, 0, 2048, 2048))); err != nil {
		t.Fatal(err)
	}
	if _, err := sanitize(huge.Bytes()); err == nil {
		t.Fatal("pixel bomb accepted")
	}
	policy, _ := NewPolicy("example.com")
	p := NewProxy(policy)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p.resolver = &fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}
	start := time.Now()
	if _, err := p.Fetch(ctx, "https://example.com/photo"); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled request exceeded bound")
	}
}
