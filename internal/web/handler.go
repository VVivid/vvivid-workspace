// Package web serves the mobile interface. Personal shopping routes are added only
// behind verified authentication; the foundation shell contains no account data.
package web

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"agentshoppinglist/internal/identity"
	webassets "agentshoppinglist/web"
)

type page struct {
	Path    string
	Title   string
	Message string
}

var pages = []page{
	{Path: "/", Title: "My list", Message: "Your list will be available after sign-in is set up."},
	{Path: "/products", Title: "Products", Message: "Your saved products will appear here after sign-in."},
	{Path: "/history", Title: "History", Message: "Your confirmed purchases will appear here after sign-in."},
	{Path: "/insights", Title: "Insights", Message: "Purchase insights will appear here after sign-in."},
}

type pageData struct {
	Current  page
	Pages    []page
	SignIn   bool
	SignedIn bool
	CSRF     string
}

func NewHandler(logger *slog.Logger, auth *identity.Manager) (http.Handler, error) {
	templates, err := template.ParseFS(webassets.Files, "templates/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	mux := http.NewServeMux()
	if auth != nil {
		auth.Register(mux)
	}
	for _, current := range pages {
		pattern := "GET " + current.Path
		if current.Path == "/" {
			pattern = "GET /{$}"
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			data := pageData{Current: current, Pages: pages, SignIn: auth != nil}
			if auth != nil {
				data.Current.Message = "Sign in to view your shopping data."
				if _, csrf, err := auth.BrowserSession(r); err == nil {
					data.SignedIn, data.CSRF = true, csrf
					data.Current.Message = "Your shopping data will appear here when it is ready."
				}
			}
			var body bytes.Buffer
			if err := templates.ExecuteTemplate(&body, "shell", data); err != nil {
				logger.Error("render page", "error", err)
				http.Error(w, "This page could not be loaded.", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = body.WriteTo(w)
		})
	}
	mux.HandleFunc("GET /static/app.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, webassets.Files, "static/app.css")
	})
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		// Live identity and domain acceptance remain incomplete. Successful
		// persistence/auth startup must not make this shell production-ready.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("{\"status\":\"not_ready\"}\n"))
	})
	var routes http.Handler = mux
	formAction := "'self'"
	if auth != nil {
		routes = auth.RestrictOrigin(routes)
		// Browsers can enforce form-action across the POST logout redirect.
		formAction += " " + auth.IssuerOrigin()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := rand.Text()
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action "+formAction+"; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		routes.ServeHTTP(w, r)
		logger.Info("http request", "request_id", requestID, "method", r.Method, "route", r.Pattern, "duration", time.Since(start))
	}), nil
}
