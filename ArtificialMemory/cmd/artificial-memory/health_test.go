package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHealthcheckOnlyContactsLoopbackWithoutConfiguration(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" || r.Header.Get("Authorization") != "" {
			t.Error("probe used an unexpected path or credentials")
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	// Even a nonlocal configured host and proxy cannot redirect the probe.
	t.Setenv("HTTP_PROXY", "http://unreachable.invalid:1")
	getenv := func(key string) string {
		if key != "MEMORY_LISTEN" {
			t.Fatalf("probe tried to load service/database configuration: %s", key)
		}
		return strings.Replace(strings.TrimPrefix(server.URL, "http://"), "127.0.0.1", "unreachable.invalid", 1)
	}
	if code := healthcheck(t.Context(), "ready", getenv, io.Discard); code != 0 {
		t.Fatal("loopback probe failed")
	}
	status.Store(http.StatusServiceUnavailable)
	if code := healthcheck(t.Context(), "ready", getenv, io.Discard); code != 1 {
		t.Fatal("unready server passed")
	}
	if code := healthcheck(t.Context(), "other", getenv, io.Discard); code != 2 {
		t.Fatal("unsupported probe accepted")
	}
	server.Close()
	if code := healthcheck(t.Context(), "ready", getenv, io.Discard); code != 1 {
		t.Fatal("absent listener passed")
	}
}
