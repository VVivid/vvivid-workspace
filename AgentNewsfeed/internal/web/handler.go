// Package web serves the private React reader and authenticated feed APIs.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	feedmcp "agentnewsfeed/internal/mcp"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

// Assets are built by Vite before Go compilation, then embedded in this binary.
//
//go:embed templates/*.gohtml static
var assets embed.FS

type handler struct {
	auth      *identity.Manager
	service   *feed.Service
	proxy     *thumbnail.Proxy
	templates *template.Template
}

func New(logger *slog.Logger, auth *identity.Manager, service *feed.Service, proxy *thumbnail.Proxy) (http.Handler, error) {
	templates, err := template.ParseFS(assets, "templates/*.gohtml")
	if err != nil {
		return nil, err
	}
	h := &handler{auth: auth, service: service, proxy: proxy, templates: templates}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
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
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /archive", h.index)
	mux.HandleFunc("GET /articles/{id}", h.index)
	if auth != nil {
		auth.Register(mux)
		mux.Handle("GET /api/v1/articles", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.list)))
		mux.Handle("GET /api/v1/articles/{id}", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.detail)))
		mux.Handle("POST /api/v1/articles/{id}/archive", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, h.archive(true)))
		mux.Handle("POST /api/v1/articles/{id}/restore", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, h.archive(false)))
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; form-action "+formAction+"; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Native POST navigations need their same-origin Origin header for CSRF.
		// no-referrer makes Chromium send Origin:null; same-origin still withholds
		// all cross-origin referrers, and source links also use noreferrer.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

type bootstrap struct {
	Configured    bool            `json:"configured"`
	Authenticated bool            `json:"authenticated"`
	CanWrite      bool            `json:"can_write"`
	CSRF          string          `json:"csrf"`
	View          string          `json:"view"`
	FromArchive   bool            `json:"from_archive"`
	SignInURL     string          `json:"sign_in_url"`
	Page          *feed.Page      `json:"page,omitempty"`
	Article       *sqlite.Article `json:"article,omitempty"`
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	data := bootstrap{Configured: h.auth != nil, View: "feed", SignInURL: "/auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())}
	if r.URL.Path == "/archive" {
		data.View = "archive"
	} else if r.PathValue("id") != "" {
		data.View = "reader"
		data.FromArchive = r.URL.Query().Get("from") == "archive"
	}
	status := http.StatusOK
	if h.auth == nil {
		status = http.StatusServiceUnavailable
	} else if p, csrf, err := h.auth.BrowserSession(r); err == nil {
		if err := p.Require(identity.Read); err != nil {
			writeError(w, err)
			return
		}
		if data.View == "reader" {
			a, err := h.service.GetArticle(r.Context(), p, r.PathValue("id"))
			if err != nil {
				writeError(w, err)
				return
			}
			data.Article = &a
		} else {
			page, err := h.service.ListArticles(r.Context(), p, feed.ListInput{Cursor: r.URL.Query().Get("cursor"), Archived: data.View == "archive"})
			if err != nil {
				writeError(w, err)
				return
			}
			data.Page = &page
		}
		data.Authenticated, data.CSRF, data.CanWrite = true, csrf, p.Allows(identity.ArticlesWrite)
	}
	body, err := json.Marshal(data)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// JSON remains ordinary escaped text inside an HTML attribute. There is no
	// executable inline script or trusted HTML from stored article content.
	_ = h.templates.ExecuteTemplate(w, "index", string(body))
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	in := feed.ListInput{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("archived"); raw != "" {
		if raw != "true" && raw != "false" {
			writeError(w, feed.ErrInvalid)
			return
		}
		in.Archived = raw == "true"
	}
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

func (h *handler) detail(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	a, err := h.service.GetArticle(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

func (h *handler) archive(archived bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		a, err := h.service.SetArchived(r.Context(), p, r.PathValue("id"), archived)
		if err != nil {
			writeError(w, err)
			return
		}
		from := "feed"
		if archived {
			from = "archive"
		}
		http.Redirect(w, r, "/articles/"+url.PathEscape(a.ID)+"?from="+from, http.StatusSeeOther)
	})
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
	case errors.Is(err, sqlite.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, sqlite.ErrConflict), errors.Is(err, sqlite.ErrIdempotencyConflict):
		status = http.StatusConflict
	}
	http.Error(w, feed.SafeError(err), status)
}
