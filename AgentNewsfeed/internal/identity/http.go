package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"agentnewsfeed/internal/store/sqlite"
)

// RestrictOrigin uses the configured public origin and Host, never forwarded
// headers supplied by the request. Traefik must preserve the canonical Host.
func (m *Manager) RestrictOrigin(next http.Handler) http.Handler {
	origin, _ := url.Parse(m.cfg.Origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Host != origin.Host {
			http.Error(w, "Unknown newsfeed origin.", http.StatusMisdirectedRequest)
			return
		}
		if origin := r.Header.Values("Origin"); len(origin) > 1 || (len(origin) == 1 && origin[0] != m.cfg.Origin) {
			http.Error(w, "Origin is not allowed.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) BrowserMiddleware(scopes []string, next http.Handler) http.Handler {
	return m.browserMiddleware(scopes, false, next)
}

// BrowserFormMiddleware accepts bounded HTML forms using the same session,
// exact Origin, CSRF hash and explicit permission checks as browser API calls.
func (m *Manager) BrowserFormMiddleware(scopes []string, next http.Handler) http.Handler {
	return m.browserMiddleware(scopes, true, next)
}

func (m *Manager) browserMiddleware(scopes []string, form bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _, principal, err := m.browserSession(r)
		if err != nil {
			setCookie(w, sessionCookie, "", -1)
			http.Error(w, "Sign in to continue.", http.StatusUnauthorized)
			return
		}
		if principal.Require(scopes...) != nil {
			http.Error(w, "Permission denied.", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			token := r.Header.Get("X-CSRF-Token")
			if form {
				r.Body = http.MaxBytesReader(w, r.Body, 4096)
				mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || mediaType != "application/x-www-form-urlencoded" || r.ParseForm() != nil || len(r.PostForm["csrf_token"]) != 1 {
					http.Error(w, "The request could not be verified.", http.StatusForbidden)
					return
				}
				token = r.PostForm.Get("csrf_token")
			}
			if !m.validCSRF(r, session, token) {
				http.Error(w, "The request could not be verified.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func (m *Manager) validCSRF(r *http.Request, session sqlite.Session, token string) bool {
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != m.cfg.Origin || len(token) < 26 || len(token) > 128 {
		return false
	}
	hash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(hash[:], session.CSRFHash[:]) == 1
}

func (m *Manager) BearerMiddleware(audience string, scopes []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		var principal Principal
		err := ErrUnauthenticated
		if len(values) == 1 {
			parts := strings.Fields(values[0])
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				principal, err = m.AuthenticateBearer(r.Context(), parts[1], audience)
			}
		}
		if err == nil {
			err = principal.Require(scopes...)
		}
		if err != nil {
			metadata := m.cfg.Origin + "/.well-known/oauth-protected-resource/mcp"
			if audience == m.cfg.APIAudience {
				metadata = m.cfg.Origin + "/.well-known/oauth-protected-resource/api/v1"
			}
			challenge := `Bearer resource_metadata="` + metadata + `"`
			if len(scopes) > 0 {
				challenge += `, scope="` + strings.Join(scopes, " ") + `"`
			}
			status := http.StatusUnauthorized
			if errors.Is(err, ErrForbidden) {
				status = http.StatusForbidden
				challenge += `, error="insufficient_scope"`
			}
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, http.StatusText(status), status)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

func (m *Manager) resourceMetadata(resource string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			Scopes               []string `json:"scopes_supported"`
			BearerMethods        []string `json:"bearer_methods_supported"`
		}{
			Resource:             resource,
			AuthorizationServers: []string{m.cfg.Issuer},
			Scopes:               resourceScopes(resource, m.cfg.APIAudience),
			BearerMethods:        []string{"header"},
		})
	}
}

func resourceScopes(resource, api string) []string {
	if resource == api {
		return []string{Read, ArticlesWrite}
	}
	return []string{Read, ArticlesWrite}
}
