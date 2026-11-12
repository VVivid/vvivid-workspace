package identity

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
)

// RestrictOrigin deliberately ignores Forwarded and X-Forwarded-* headers.
// Traefik and the private gateway must preserve an explicitly allowed Host.
func (m *Manager) RestrictOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}
		origin := m.origin(r.Host)
		if origin == "" {
			http.Error(w, "Unknown origin.", 421)
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 1 || (len(origins) == 1 && origins[0] != origin) {
			http.Error(w, "Origin is not allowed.", 403)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (m *Manager) Browser(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, csrf, err := m.browser(w, r)
		if err != nil {
			authError(w, err)
			return
		}
		if p.Actor.Require(scope) != nil {
			http.Error(w, "Permission denied.", 403)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("Origin") != m.origin(r.Host) || len(r.Header.Values("X-CSRF-Token")) != 1 || !equal(r.Header.Get("X-CSRF-Token"), csrf) {
				http.Error(w, "Request could not be verified.", 403)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func (m *Manager) MCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Values("Authorization")
		if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") {
			m.challenge(w, 401)
			return
		}
		token := strings.TrimPrefix(auth[0], "Bearer ")
		if strings.TrimSpace(token) != token {
			m.challenge(w, 401)
			return
		}
		p, err := m.Authenticate(r.Context(), token, true)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				authError(w, err)
				return
			}
			status := 401
			if errors.Is(err, store.ErrForbidden) {
				status = 403
			}
			m.challenge(w, status)
			return
		}
		if p.Actor.Require(store.Read) != nil {
			m.challenge(w, 403)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func (m *Manager) challenge(w http.ResponseWriter, status int) {
	if m.cfg.MCPMode == "direct" {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+m.cfg.Origins[0]+`/.well-known/oauth-protected-resource/mcp"`)
	}
	http.Error(w, "MCP authentication required.", status)
}
