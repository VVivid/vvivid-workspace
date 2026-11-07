package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// A probe never loads service identity or opens storage. The ready endpoint in
// the running service owns the bounded, read-only storage check.
func healthcheck(ctx context.Context, kind string, getenv func(string) string, errOut io.Writer) int {
	if kind != "live" && kind != "ready" {
		fmt.Fprintln(errOut, "Healthcheck requires live or ready.")
		return 2
	}
	host, port, err := net.SplitHostPort(cmp.Or(getenv("MEMORY_LISTEN"), "127.0.0.1:8080"))
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		fmt.Fprintln(errOut, "Invalid probe listener.")
		return 1
	}
	loopback := "127.0.0.1"
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		loopback = "::1"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: time.Second}).DialContext,
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
		return 1
	}
	response, err := client.Do(request)
	if err != nil {
		fmt.Fprintln(errOut, "Healthcheck unavailable.")
		return 1
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(errOut, "Healthcheck unavailable.")
		return 1
	}
	return 0
}
