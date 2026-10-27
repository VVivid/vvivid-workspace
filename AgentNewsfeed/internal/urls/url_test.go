package urls

import "testing"

func TestCanonicalURLIdentity(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://NEWS.Example.com:443/a/%7euser?utm_source=agent&b=2&a=1#section", "https://news.example.com/a/~user?a=1&b=2"},
		{"https://example.com", "https://example.com/"},
		{"https://example.com/Story/?id=2&id=1&fbclid=track", "https://example.com/Story/?id=2&id=1"},
		{"https://example.com/a%2fb?q=a%20b", "https://example.com/a%2Fb?q=a+b"},
	} {
		got, err := Canonical(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("canonical %q = %q, %v; want %q", tc.in, got, err, tc.want)
		}
		again, err := Canonical(got)
		if err != nil || again != got {
			t.Errorf("canonicalization not stable: %q", got)
		}
	}
	for _, pair := range [][2]string{{"https://example.com/story?id=1", "https://example.com/story?id=2"}, {"https://example.com/a%2Fb", "https://example.com/a/b"}, {"https://example.com/Story", "https://example.com/story"}, {"https://example.com/story/", "https://example.com/story"}} {
		a, _ := Canonical(pair[0])
		b, _ := Canonical(pair[1])
		if a == b {
			t.Fatalf("distinct resources merged: %v", pair)
		}
	}
}

func TestRejectUnsafeSourceURLs(t *testing.T) {
	for _, raw := range []string{"", "/story", "javascript:alert(1)", "data:text/html,x", "http://example.com/a", "https://user:pass@example.com/a", "https://localhost/a", "https://localhost.local/a", "https://127.0.0.1/a", "https://169.254.169.254/latest", "https://[::ffff:169.254.169.254]/a", "https://2130706433/a", "https://0177.0.0.1/a", "https://0x7f000001/a", "https://example.com:8443/a", "https://example.com:/a", "https://example.com./a", "https://example.com/a\\b", "https://example.com/a\nb", "https://example.com/a/../b", "https://example.com/%2e%2e/x", "https://example.com/?q=%zz", "https://example.com/?x=1;y=2", "https://exa_mple.com/a", "https://example.internal/a"} {
		t.Run(raw, func(t *testing.T) {
			if got, err := Canonical(raw); err == nil {
				t.Fatalf("unsafe URL accepted: %q", got)
			}
		})
	}
}
