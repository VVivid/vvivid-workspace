// Package httpapp maps authenticated browser requests to the shared data service.
package httpapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/identity"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
)

func New(s *store.Store, auth *identity.Manager, mcp http.Handler, static fs.FS) http.Handler {
	mux := http.NewServeMux()
	auth.Register(mux)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.Ping(r.Context()) != nil {
			http.Error(w, "Unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
	route := func(pattern, scope string, h http.HandlerFunc) { mux.Handle(pattern, auth.Browser(scope, h)) }
	route("GET /api/v1/words", store.Read, func(w http.ResponseWriter, r *http.Request) {
		f, err := parseFilter(r)
		if err != nil {
			fail(w, err)
			return
		}
		p, _ := identity.FromContext(r.Context())
		out, err := s.Words(r.Context(), p.Actor, f)
		respond(w, out, err)
	})
	route("POST /api/v1/words", store.Write, func(w http.ResponseWriter, r *http.Request) {
		var in store.AddInput
		if !decode(w, r, &in) {
			return
		}
		p, _ := identity.FromContext(r.Context())
		out, err := s.AddWord(r.Context(), p.Actor, in)
		respond(w, out, err)
	})
	route("GET /api/v1/words/{id}", store.Read, func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		out, err := s.Word(r.Context(), p.Actor, r.PathValue("id"))
		respond(w, out, err)
	})
	route("PUT /api/v1/words/{id}", store.Write, func(w http.ResponseWriter, r *http.Request) {
		var in store.UpdateInput
		if !decode(w, r, &in) {
			return
		}
		if in.ID != "" && in.ID != r.PathValue("id") {
			fail(w, store.ErrInvalid)
			return
		}
		in.ID = r.PathValue("id")
		p, _ := identity.FromContext(r.Context())
		out, err := s.UpdateWord(r.Context(), p.Actor, in)
		respond(w, out, err)
	})
	route("PUT /api/v1/words/{id}/completion", store.Write, func(w http.ResponseWriter, r *http.Request) {
		var in store.CompleteInput
		if !decode(w, r, &in) {
			return
		}
		if in.ID != "" && in.ID != r.PathValue("id") {
			fail(w, store.ErrInvalid)
			return
		}
		in.ID = r.PathValue("id")
		p, _ := identity.FromContext(r.Context())
		out, err := s.CompleteWord(r.Context(), p.Actor, in)
		respond(w, out, err)
	})
	mux.Handle("/mcp", auth.MCP(mcp))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.Handle("/", staticHandler(static))
	return securityHeaders(auth.RestrictOrigin(mux))
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "JSON content type required.", 415)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(new(any)) != io.EOF {
		fail(w, store.ErrInvalid)
		return false
	}
	return true
}
func parseFilter(r *http.Request) (store.Filter, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return store.Filter{}, store.ErrInvalid
	}
	for key, values := range q {
		if !slices.Contains([]string{"query", "status", "page", "page_size"}, key) || len(values) != 1 {
			return store.Filter{}, store.ErrInvalid
		}
	}
	f := store.Filter{Query: q.Get("query"), Status: q.Get("status")}
	for _, field := range []struct {
		name  string
		value *int
	}{{"page", &f.Page}, {"page_size", &f.PageSize}} {
		if q.Get(field.name) != "" {
			value, err := strconv.Atoi(q.Get(field.name))
			if err != nil {
				return f, store.ErrInvalid
			}
			*field.value = value
		}
	}
	return f, nil
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func SafeError(err error) string {
	switch {
	case errors.Is(err, store.ErrInvalid):
		return err.Error()
	case errors.Is(err, store.ErrNotFound):
		return "Word not found."
	case errors.Is(err, store.ErrDuplicate):
		return "This word or expression is already saved. Search for it to edit its details."
	case errors.Is(err, store.ErrStale):
		return "This word changed elsewhere. Load the latest version before saving again; your input has been kept."
	case errors.Is(err, store.ErrRetryKey):
		return "This retry key was already used with different input. Use a new key for a new intent."
	case errors.Is(err, store.ErrForbidden):
		return "Permission denied."
	default:
		return "The request could not be saved. Please retry."
	}
}
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, store.ErrInvalid):
		return "invalid_input"
	case errors.Is(err, store.ErrNotFound):
		return "not_found"
	case errors.Is(err, store.ErrDuplicate):
		return "duplicate_term"
	case errors.Is(err, store.ErrStale):
		return "stale_revision"
	case errors.Is(err, store.ErrRetryKey):
		return "idempotency_conflict"
	case errors.Is(err, store.ErrForbidden):
		return "forbidden"
	default:
		return "unavailable"
	}
}
func fail(w http.ResponseWriter, err error) {
	status := 500
	switch {
	case errors.Is(err, store.ErrInvalid):
		status = 400
	case errors.Is(err, store.ErrNotFound):
		status = 404
	case errors.Is(err, store.ErrConflict):
		status = 409
	case errors.Is(err, store.ErrForbidden):
		status = 403
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": SafeError(err), "code": ErrorCode(err)})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; worker-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; frame-src 'none'; object-src 'none'; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}
func staticHandler(root fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(405)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if strings.HasPrefix(name, ".") || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(root, name)
		if err != nil {
			if strings.Contains(path.Base(name), ".") {
				http.NotFound(w, r)
				return
			}
			name = strings.TrimSuffix(name, "/") + "/index.html"
			info, err = fs.Stat(root, name)
		} else if info.IsDir() {
			name = path.Join(name, "index.html")
			info, err = fs.Stat(root, name)
		}
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "_next/static/") {
			w.Header().Set("Cache-Control", "public,max-age=31536000,immutable")
		}
		raw, readErr := fs.ReadFile(root, name)
		if readErr != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(raw))
	})
}
func StaticDir(dir string) fs.FS { return os.DirFS(dir) }
