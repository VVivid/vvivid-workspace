package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthcheckDistinguishesLiveReadyWithoutIdentityOrProxy(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("healthcheck sent credentials")
		}
		switch r.URL.Path {
		case "/health/live":
			w.WriteHeader(http.StatusOK)
		case "/health/ready":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("probe used ambient proxy") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NEWSFEED_HTTP_ADDR", server.Listener.Addr().String())
	t.Setenv("NEWSFEED_WEB_CLIENT_SECRET_FILE", "/nonexistent/never-read")
	if err := command([]string{"healthcheck", "live"}); err != nil {
		t.Fatal(err)
	}
	if err := command([]string{"healthcheck", "ready"}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unready probe: %v", err)
	}
	// A non-loopback listener setting cannot select a remote probe destination.
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	if err := healthcheck(t.Context(), "203.0.113.1:"+port, "live"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("probe requests %d", requests.Load())
	}
}

func TestHealthcheckRejectsRedirectsAndInvalidArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/followed", http.StatusFound) }))
	defer server.Close()
	if err := healthcheck(t.Context(), server.Listener.Addr().String(), "live"); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect accepted: %v", err)
	}
	for _, address := range []string{"example.com:8091", "127.0.0.1:0", ":65536", "http://127.0.0.1:8091", "127.0.0.1", ":not-a-port"} {
		if err := healthcheck(t.Context(), address, "live"); err == nil {
			t.Fatalf("invalid address accepted: %s", address)
		}
	}
	for _, args := range [][]string{{}, {"healthcheck"}, {"healthcheck", "unknown"}, {"healthcheck", "live", "extra"}} {
		if err := command(args); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}

func TestHealthcheckHonorsCancellationAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := healthcheck(ctx, server.Listener.Addr().String(), "live"); err == nil {
		t.Fatal("cancelled probe succeeded")
	}
	started := time.Now()
	if err := healthcheck(t.Context(), server.Listener.Addr().String(), "ready"); err == nil {
		t.Fatal("stalled probe succeeded")
	}
	if elapsed := time.Since(started); elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("unexpected probe timeout %s", elapsed)
	}
}
