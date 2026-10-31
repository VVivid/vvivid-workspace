package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Probes never load identity settings, secrets or the database. They can only
// contact the configured port on loopback, without proxies or redirect following.
func healthcheck(ctx context.Context, address, kind string) error {
	if kind != "live" && kind != "ready" {
		return fmt.Errorf("healthcheck requires live or ready")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
		return fmt.Errorf("invalid healthcheck listener address")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("invalid healthcheck listener port")
	}
	loopback := "127.0.0.1"
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		loopback = ip.String()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	transport := &http.Transport{
		Proxy:             nil,
		DialContext:       (&net.Dialer{Timeout: time.Second}).DialContext,
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(loopback, port)+"/health/"+kind, nil)
	if err != nil {
		return fmt.Errorf("invalid healthcheck request")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("healthcheck unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned %d", response.StatusCode)
	}
	return nil
}
