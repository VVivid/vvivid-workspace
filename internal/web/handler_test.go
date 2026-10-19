package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFoundationRoutes(t *testing.T) {
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/", "My list", http.StatusOK},
		{"GET", "/products", "Products", http.StatusOK},
		{"GET", "/history", "History", http.StatusOK},
		{"GET", "/insights", "Insights", http.StatusOK},
		{"GET", "/static/app.css", "shopping-app", http.StatusOK},
		{"GET", "/health/live", `"status":"ok"`, http.StatusOK},
		{"GET", "/health/ready", `"status":"not_ready"`, http.StatusServiceUnavailable},
		{"POST", "/", "", http.StatusMethodNotAllowed},
		{"GET", "/api/v1/me", "", http.StatusNotFound},
		{"POST", "/mcp", "", http.StatusNotFound},
		{"GET", "/reference/ui-demo/src/data.ts", "", http.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.body) {
				t.Fatalf("got status %d and body %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing cache policy or request identifier")
			}
		})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	for _, required := range []string{`aria-label="Shopping views"`, `aria-current="page"`, ">Products</a>", ">History</a>", ">Insights</a>", `href="/static/app.css"`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Errorf("mobile shell missing %q", required)
		}
	}
	if strings.Contains(response.Body.String(), "<script") {
		t.Fatal("the foundation shell must not load a JavaScript application")
	}
}
