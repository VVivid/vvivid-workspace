// Package web serves the same private feed as MCP through escaped templates.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	feedmcp "agentnewsfeed/internal/mcp"
	"agentnewsfeed/internal/thumbnail"
)

//go:embed templates/*.gohtml static/*
var assets embed.FS

type handler struct {
	auth      *identity.Manager
	service   *feed.Service
	proxy     *thumbnail.Proxy
	templates *template.Template
}

func New(logger *slog.Logger, auth *identity.Manager, service *feed.Service, proxy *thumbnail.Proxy) (http.Handler, error) {
	templates, err := template.New("feed").Funcs(template.FuncMap{
		"host":      func(raw string) string { u, _ := url.Parse(raw); return u.Hostname() },
		"savedDate": func(ms int64) string { return time.UnixMilli(ms).UTC().Format("Jan 2, 2006") },
	}).ParseFS(assets, "templates/*.gohtml")
	if err != nil {
		return nil, err
	}
	h := &handler{auth: auth, service: service, proxy: proxy, templates: templates}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("live\n"))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if auth == nil || service.Ready(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /static/app.css", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := assets.ReadFile("static/app.css")
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /{$}", h.index)
	if auth != nil {
		auth.Register(mux)
		mux.Handle("GET /api/v1/articles", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.list)))
		mux.Handle("GET /thumbnails/{id}", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.image)))
		mux.Handle("/mcp", auth.BearerMiddleware(auth.MCPAudience(), nil, feedmcp.NewHandler(service, logger)))
	} else {
		mux.HandleFunc("/mcp", unconfigured)
		mux.HandleFunc("/api/", unconfigured)
		mux.HandleFunc("/auth/", unconfigured)
		mux.HandleFunc("/thumbnails/", unconfigured)
	}
	var result http.Handler = mux
	if auth != nil {
		result = auth.RestrictOrigin(result)
	}
	return headers(result, auth), nil
}

func unconfigured(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "Newsfeed identity is not configured.", http.StatusServiceUnavailable)
}

func headers(next http.Handler, auth *identity.Manager) http.Handler {
	formAction := "'self'"
	if auth != nil {
		// Browsers may enforce form-action across the logout POST redirect.
		formAction += " " + auth.IssuerOrigin()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; form-action "+formAction+"; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

type pageData struct {
	Configured, Authenticated bool
	CSRF                      string
	Page                      feed.Page
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := pageData{Configured: h.auth != nil}
	if h.auth == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		p, csrf, err := h.auth.BrowserSession(r)
		if err == nil {
			if p.Require(identity.Read) != nil {
				http.Error(w, "Permission denied.", http.StatusForbidden)
				return
			}
			data.Page, err = h.service.ListArticles(r.Context(), p, feed.ListInput{Cursor: r.URL.Query().Get("cursor")})
			if err != nil {
				writeError(w, err)
				return
			}
			data.Authenticated, data.CSRF = true, csrf
		}
	}
	_ = h.templates.ExecuteTemplate(w, "index", data)
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	in := feed.ListInput{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		in.Limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, feed.ErrInvalid)
			return
		}
	}
	page, err := h.service.ListArticles(r.Context(), p, in)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) image(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	a, err := h.service.GetArticle(r.Context(), p, r.PathValue("id"))
	if err != nil || a.ThumbnailURL == "" {
		http.NotFound(w, r)
		return
	}
	body, err := h.proxy.Fetch(r.Context(), a.ThumbnailURL)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, feed.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, identity.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, identity.ErrUnauthenticated):
		status = http.StatusUnauthorized
	}
	http.Error(w, feed.SafeError(err), status)
}
