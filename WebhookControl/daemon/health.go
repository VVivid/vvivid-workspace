package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// CheckReady is the container's local probe. It contacts only the configured
// listener and never reads credentials, initializes SQLite or discovers OAuth.
func CheckReady(ctx context.Context, supplied Config) error {
	c, err := supplied.Validated()
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(c.Listen)
	ip, _ := netip.ParseAddr(host)
	if ip.IsUnspecified() {
		if ip.Is6() {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
	if err != nil {
		return errors.New("readiness probe failed")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 2 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("readiness probe failed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 128))
	if err != nil || response.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != `{"status":"ok"}` {
		return errors.New("readiness probe failed")
	}
	return nil
}
