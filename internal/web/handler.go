// Package web serves compact mobile templates and the authenticated browser API.
package web

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"agentshoppinglist/internal/identity"
	shoppingmcp "agentshoppinglist/internal/mcp"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
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
	List     sqlite.ListView
	Products shopping.ProductPage
	Query    string
	Error    string
}

func NewHandler(logger *slog.Logger, auth *identity.Manager, service *shopping.Service) (http.Handler, error) {
	templates, err := template.New("shopping").Funcs(template.FuncMap{"pack": packText}).ParseFS(webassets.Files, "templates/*.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	mux := http.NewServeMux()
	if auth != nil {
		if service == nil {
			return nil, fmt.Errorf("authenticated shopping requires services")
		}
		auth.Register(mux)
		registerAPI(mux, auth, service)
		mux.Handle("/mcp", auth.BearerMiddleware(auth.MCPAudience(), nil, shoppingmcp.NewHandler(service, logger)))
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
				if principal, csrf, err := auth.BrowserSession(r); err == nil {
					data.SignedIn, data.CSRF = true, csrf
					data.Current.Message = "No confirmed purchases yet."
					if current.Path == "/insights" {
						data.Current.Message = "Insights will appear after confirmed purchases."
					}
					context, loadErr := service.GetContext(r.Context(), principal)
					if loadErr == nil {
						data.List, loadErr = service.GetList(r.Context(), principal, shopping.ListInput{Retailer: context.Preferences.PreferredRetailer})
					}
					if loadErr == nil && current.Path == "/products" {
						data.Query = r.URL.Query().Get("q")
						data.Products, loadErr = service.SearchProducts(r.Context(), principal, shopping.SearchInput{Query: data.Query, Cursor: r.URL.Query().Get("cursor")})
					}
					if loadErr != nil {
						data.Error = shopping.SafeError(loadErr)
					}
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
	mux.HandleFunc("GET /static/app.js", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, webassets.Files, "static/app.js") })
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		// Live identity/client and complete purchase/recovery acceptance remain
		// incomplete. Local service checks must not imply production readiness.
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' https:; base-uri 'none'; form-action "+formAction+"; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		routes.ServeHTTP(w, r)
		logger.Info("http request", "request_id", requestID, "method", r.Method, "route", r.Pattern, "duration", time.Since(start))
	}), nil
}

func packText(p sqlite.ProductSummary) string {
	parts := []string{}
	if p.PackageCount != nil {
		parts = append(parts, fmt.Sprint(*p.PackageCount))
	}
	if p.PackageSize != nil && p.PackageUnit != nil {
		size := *p.PackageSize + " " + *p.PackageUnit
		if len(parts) > 0 {
			parts = append(parts, "×", size)
		} else {
			parts = append(parts, size)
		}
	}
	if len(parts) == 0 {
		return "Pack details unknown"
	}
	return strings.Join(parts, " ")
}
