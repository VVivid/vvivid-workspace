package httptransport

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func TestCanonicalOriginsAndExactAllowlist(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"https://EXAMPLE.com.:0443/a?synthetic=yes", "https://example.com"},
		{"http://Example.com:80/", "http://example.com"},
		{"https://[2001:4860:4860::8888]:443/", "https://[2001:4860:4860::8888]"},
		{"https://EXAMPLE.com:8443/a", "https://example.com:8443"},
	} {
		got, err := CanonicalOrigin(test.input)
		if err != nil || got != test.want {
			t.Errorf("canonical origin %q: %q, %v", test.input, got, err)
		}
	}
	p, err := NewPolicy([]OriginRule{{Origin: "https://EXAMPLE.com:443"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ApproveDestination(t.Context(), "source", domain.HTTPConfig{URL: "https://example.com./path?synthetic=1"}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://sub.example.com", "https://example.com.evil.test", "https://example.com:8443", "http://example.com",
		"https://user:synthetic@example.com", "https://example.com/#", "https://example.com/#fragment", "https://example.com:",
		"https://example.com:65536", "https://example.com\\@evil.test", "https://[fe80::1%25eth0]", "https://éxample.test",
		"https://example..com", "https://example.com./../#fragment", "ftp://example.com", "https:example.com",
	} {
		if err := p.ApproveDestination(t.Context(), "source", domain.HTTPConfig{URL: raw}); !errors.Is(err, ports.ErrPolicyDenied) {
			t.Errorf("unsafe/unapproved URL accepted: %q, %v", raw, err)
		}
	}
	for _, rule := range []OriginRule{{Origin: "http://example.com"}, {Origin: "https://example.com/path"}, {Origin: "https://example.com?query"}, {Origin: "https://169.254.169.254", AllowPrivate: true}} {
		if _, err := NewPolicy([]OriginRule{rule}); !errors.Is(err, ErrOptions) {
			t.Errorf("invalid server rule accepted: %+v, %v", rule, err)
		}
	}
	if _, err := NewPolicy([]OriginRule{{Origin: "https://example.com"}, {Origin: "https://EXAMPLE.com:443"}}); !errors.Is(err, ErrOptions) {
		t.Fatalf("canonical duplicate accepted: %v", err)
	}
}

func TestAddressPolicyAlwaysRejectsSpecialNetworks(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "0.1.2.3", "169.254.169.254", "169.254.1.1", "100.100.100.200", "168.63.129.16",
		"192.0.0.1", "192.0.2.1", "198.18.1.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255",
		"::", "fe80::1", "ff02::1", "fec0::1", "fd00:ec2::254", "64:ff9b::a00:1", "2002:a00:1::", "2001:db8::1", "2001::1", "3fff::1",
	} {
		ip := netip.MustParseAddr(raw)
		if allowedAddress(ip, false) || allowedAddress(ip, true) {
			t.Errorf("special address allowed: %s", ip)
		}
	}
	for _, raw := range []string{"10.1.2.3", "172.16.1.1", "192.168.1.1", "127.0.0.1", "100.64.1.1", "::1", "fc00::1", "::ffff:127.0.0.1"} {
		ip := netip.MustParseAddr(raw)
		if allowedAddress(ip, false) || !allowedAddress(ip, true) {
			t.Errorf("private exception incorrect: %s", ip)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !allowedAddress(netip.MustParseAddr(raw), false) {
			t.Errorf("public address denied: %s", raw)
		}
	}
	for _, raw := range []string{"https://metadata.google.internal", "http://metadata", "https://instance-data.ec2.internal"} {
		p, err := NewPolicy([]OriginRule{{Origin: raw, AllowPrivate: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.ApproveDestination(t.Context(), "source", domain.HTTPConfig{URL: raw}); !errors.Is(err, ports.ErrPolicyDenied) {
			t.Errorf("metadata hostname accepted: %s", raw)
		}
	}
}
