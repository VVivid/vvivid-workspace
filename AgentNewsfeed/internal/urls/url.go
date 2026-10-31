// Package urls defines the conservative, deterministic article URL identity.
// Validation never resolves or fetches a source page.
package urls

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Canonical removes only fragments, default ports and known tracking parameters.
// It preserves significant query parameters, path case and encoded slashes.
func Canonical(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\") || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return "", fmt.Errorf("source URL must be a bounded HTTPS URL without whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" {
		return "", fmt.Errorf("source URL requires HTTPS and no credentials")
	}
	host := strings.ToLower(u.Hostname())
	if !PublicHostname(host) || (u.Port() != "" && u.Port() != "443") || strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("source URL requires a public DNS hostname and HTTPS port")
	}
	u.Host = host
	u.Fragment, u.RawFragment, u.ForceQuery = "", "", false
	escaped := canonicalEscapes(u.EscapedPath())
	if escaped == "" {
		escaped = "/"
	}
	for segment := range strings.SplitSeq(escaped, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("source URL cannot contain dot path segments")
		}
	}
	u.Path, err = url.PathUnescape(escaped)
	if err != nil {
		return "", fmt.Errorf("invalid URL path")
	}
	u.RawPath = escaped
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid source query")
	}
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || slices.Contains([]string{"fbclid", "gclid", "dclid", "msclkid", "mc_cid", "mc_eid"}, lower) {
			delete(query, key)
			continue
		}
	}
	u.RawQuery = query.Encode()
	canonical := u.String()
	if len(canonical) > 2048 {
		return "", fmt.Errorf("canonical source URL is too long")
	}
	return canonical, nil
}

// PublicHostname is a syntax boundary, not an SSRF defense. Network callers
// must additionally validate and pin every DNS answer to a public IP address.
func PublicHostname(host string) bool {
	if len(host) > 253 || len(host) < 4 || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home", ".lan", ".invalid", ".test", ".onion"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
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
	// Numeric alternate address encodings must never be treated as DNS names.
	last := host[strings.LastIndexByte(host, '.')+1:]
	return strings.ContainsAny(last, "abcdefghijklmnopqrstuvwxyz")
}

func canonicalEscapes(path string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] != '%' || i+2 >= len(path) {
			out.WriteByte(path[i])
			continue
		}
		bytes, err := url.PathUnescape(path[i : i+3])
		if err != nil {
			return path
		}
		b := bytes[0]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b)) {
			out.WriteByte(b)
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		}
		i += 2
	}
	return out.String()
}
