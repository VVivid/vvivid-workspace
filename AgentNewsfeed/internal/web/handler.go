// Package web serves the private React reader and authenticated feed APIs.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
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
	proxy     imageFetcher
	templates *template.Template
}

type imageFetcher interface {
	Fetch(context.Context, string) ([]byte, error)
}

func New(logger *slog.Logger, auth *identity.Manager, service *feed.Service, proxy imageFetcher) (http.Handler, error) {
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
	mux.HandleFunc("GET /trash", h.index)
	mux.HandleFunc("GET /articles/{id}", h.index)
	mux.HandleFunc("GET /todo", h.todo)
	mux.HandleFunc("GET /settings", h.settings)
	if auth != nil {
		auth.Register(mux)
		h.registerSettings(mux)
		mux.Handle("GET /api/v1/articles", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.list)))
		mux.Handle("GET /api/v1/articles/{id}", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.detail)))
		mux.Handle("POST /api/v1/articles/{id}/archive", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, h.archive(true)))
		mux.Handle("POST /api/v1/articles/{id}/restore", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, h.archive(false)))
		mux.Handle("POST /api/v1/articles/{id}/trash", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, h.trash(false)))
		mux.Handle("POST /api/v1/articles/{id}/restore-trash", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, h.trash(true)))
		mux.Handle("POST /api/v1/todos", auth.BrowserFormMiddleware([]string{identity.Read, identity.ArticlesWrite}, http.HandlerFunc(h.todoForm)))
		mux.Handle("GET /thumbnails/{id}", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.image)))
		mux.Handle("GET /thumbnails/{id}/content/{image}", auth.BrowserMiddleware([]string{identity.Read}, http.HandlerFunc(h.image)))
		mux.Handle("/mcp", auth.BearerMiddleware(auth.MCPAudience(), nil, feedmcp.NewHandler(service, logger)))
	} else {
		mux.HandleFunc("/mcp", unconfigured)
		// Limit setup responses to the active API families so retired paths return 404.
		mux.HandleFunc("/api/v1/articles", unconfigured)
		mux.HandleFunc("/api/v1/articles/", unconfigured)
		mux.HandleFunc("/api/v1/todos", unconfigured)
		mux.HandleFunc("/api/v1/settings/", unconfigured)
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

const browserMediaType = "application/vnd.agentnewsfeed.browser+json"

type bootstrap struct {
	Configured    bool                      `json:"configured"`
	Authenticated bool                      `json:"authenticated"`
	CanWrite      bool                      `json:"can_write"`
	CSRF          string                    `json:"csrf"`
	View          string                    `json:"view"`
	FromArchive   bool                      `json:"from_archive"`
	FromTrash     bool                      `json:"from_trash"`
	MutationKey   string                    `json:"mutation_key,omitempty"`
	SignInURL     string                    `json:"sign_in_url"`
	Page          *feed.BrowserArticlesPage `json:"page,omitempty"`
	Article       *sqlite.Article           `json:"article,omitempty"`
	ContentImages map[string]string         `json:"content_images,omitempty"`
	Section       string                    `json:"section"`
	Cursor        string                    `json:"cursor,omitempty"`
	Before        string                    `json:"before,omitempty"`
	PageSize      int                       `json:"page_size,omitzero"`
	Todo          *sqlite.TodoList          `json:"todo,omitempty"`
	Settings      *settingsPage             `json:"settings,omitempty"`
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	data := bootstrap{Configured: h.auth != nil, Section: "news", View: "feed", SignInURL: "/auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	if r.URL.Path == "/archive" {
		data.View = "archive"
	} else if r.URL.Path == "/trash" {
		data.View = "trash"
	} else if r.PathValue("id") != "" {
		data.View = "reader"
		data.FromArchive = query.Get("from") == "archive"
		data.FromTrash = query.Get("from") == "trash"
	}
	status := http.StatusOK
	if h.auth == nil {
		status = http.StatusServiceUnavailable
	} else if p, csrf, err := h.auth.BrowserSession(r); err == nil {
		if err := p.Require(identity.Read); err != nil {
			writeError(w, err)
			return
		}
		if queryErr != nil || data.readPagePosition(query) != nil {
			writeError(w, feed.ErrInvalid)
			return
		}
		cursor, before := data.pageBoundary()
		in := feed.ListInput{Cursor: cursor, Archived: data.View == "archive" || data.FromArchive, Trashed: data.View == "trash" || data.FromTrash}
		if data.View == "reader" {
			if err := in.ValidateCursor(p.Owner()); err != nil {
				writeError(w, err)
				return
			}
			a, err := h.service.GetArticleInView(r.Context(), p, r.PathValue("id"), data.FromTrash)
			if err != nil {
				writeError(w, err)
				return
			}
			data.Article = &a
			data.ContentImages = contentImageRoutes(a)
			if data.FromTrash {
				for destination, route := range data.ContentImages {
					data.ContentImages[destination] = route + "?trashed=true"
				}
			}
			data.MutationKey = rand.Text()
		} else {
			data.PageSize = browserPageSize(r)
			in.Limit = data.PageSize
			page, err := h.service.BrowseArticles(r.Context(), p, in, before)
			if err != nil {
				writeError(w, err)
				return
			}
			data.Page = new(page)
		}
		data.Authenticated, data.CSRF, data.CanWrite = true, csrf, p.Allows(identity.ArticlesWrite)
	}
	h.renderPage(w, r, status, data)
}

func (h *handler) renderPage(w http.ResponseWriter, r *http.Request, status int, data bootstrap) {
	w.Header().Add("Vary", "Accept")
	if r.Header.Get("Accept") == browserMediaType {
		if data.PageSize != 0 {
			w.Header().Add("Vary", pageSizeHeader)
		}
		w.Header().Set("Content-Type", browserMediaType)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(data)
		return
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
	if raw := r.URL.Query().Get("trashed"); raw != "" {
		if raw != "true" && raw != "false" {
			writeError(w, feed.ErrInvalid)
			return
		}
		in.Trashed = raw == "true"
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
	trashed, err := trashSelection(r)
	if err != nil {
		writeError(w, err)
		return
	}
	a, err := h.service.GetArticleInView(r.Context(), p, r.PathValue("id"), trashed)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a)
}

func (h *handler) archive(archived bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		returnTo, err := articleReturnTo(r)
		if err != nil {
			writeError(w, err)
			return
		}
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
		if returnTo == "" {
			returnTo = "/articles/" + url.PathEscape(a.ID) + "?from=" + from
		}
		respondToStateForm(w, r, returnTo)
	})
}

func (h *handler) image(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	trashed, err := trashSelection(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := h.service.GetArticleInView(r.Context(), p, r.PathValue("id"), trashed)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Saved thumbnail payloads never contact their former source. Only legacy
	// URL-only records retain the previous protected proxy until refreshed.
	if r.PathValue("image") == "" && a.ThumbnailStatus != "legacy" {
		body, err := h.service.Thumbnail(r.Context(), p, a.ID, trashed)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
		return
	}
	imageURL := a.ThumbnailURL
	if imageID := r.PathValue("image"); imageID != "" {
		imageURL = ""
		for _, candidate := range thumbnail.MarkdownImages(a.URL, a.ContentMarkdown) {
			if contentImageID(candidate) == imageID {
				imageURL = candidate
				break
			}
		}
	}
	if imageURL == "" {
		http.NotFound(w, r)
		return
	}
	body, err := h.proxy.Fetch(r.Context(), imageURL)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(body)
}

func trashSelection(r *http.Request) (bool, error) {
	values, ok := r.URL.Query()["trashed"]
	if !ok {
		return false, nil
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		return false, feed.ErrInvalid
	}
	return values[0] == "true", nil
}

func (h *handler) trash(restore bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		returnTo, err := articleReturnTo(r)
		if err != nil {
			writeError(w, err)
			return
		}
		p, _ := identity.FromContext(r.Context())
		if len(r.PostForm) != 3 || len(r.PostForm["idempotency_key"]) != 1 || len(r.PostForm["expected_revision"]) != 1 {
			writeError(w, feed.ErrInvalid)
			return
		}
		revision, err := strconv.ParseInt(r.PostForm.Get("expected_revision"), 10, 64)
		if err != nil {
			writeError(w, feed.ErrInvalid)
			return
		}
		in := feed.StateInput{IdempotencyKey: r.PostForm.Get("idempotency_key"), ArticleID: r.PathValue("id"), ExpectedRevision: revision}
		var out sqlite.MutationResult
		if restore {
			out, err = h.service.RestoreArticle(r.Context(), p, in)
		} else {
			out, err = h.service.TrashArticle(r.Context(), p, in)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		from := "trash"
		if restore {
			from = "feed"
			if out.Article.ArchivedAt != 0 {
				from = "archive"
			}
		}
		if returnTo == "" {
			returnTo = "/articles/" + url.PathEscape(out.Article.ID) + "?from=" + from
		}
		respondToStateForm(w, r, returnTo)
	})
}

// Shelf actions return to the current list. Restrict this optional destination
// before mutating so native forms cannot become an open redirect.
func articleReturnTo(r *http.Request) (string, error) {
	query := r.URL.Query()
	if !query.Has("return_to") {
		return "", nil
	}
	if len(query) != 1 || len(query["return_to"]) != 1 {
		return "", feed.ErrInvalid
	}
	destination, err := url.ParseRequestURI(query.Get("return_to"))
	if err != nil || destination.IsAbs() || destination.Host != "" || destination.Fragment != "" ||
		(destination.Path != "/" && destination.Path != "/archive" && destination.Path != "/trash") || destination.RawPath != "" {
		return "", feed.ErrInvalid
	}
	position, err := url.ParseQuery(destination.RawQuery)
	if err != nil || len(position) > 1 {
		return "", feed.ErrInvalid
	}
	for name, values := range position {
		if (name != "cursor" && name != "before") || len(values) != 1 || values[0] == "" || len(values[0]) > 1024 {
			return "", feed.ErrInvalid
		}
	}
	return destination.String(), nil
}

// Return the same destination to native forms and fetch requests. The latter
// load the new page separately, so a failed page read never repeats the write.
func respondToStateForm(w http.ResponseWriter, r *http.Request, location string) {
	w.Header().Add("Vary", "Accept")
	if r.Header.Get("Accept") == browserMediaType {
		w.Header().Set("Content-Type", browserMediaType)
		_ = json.NewEncoder(w).Encode(struct {
			Location string `json:"location"`
		}{Location: location})
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func contentImageID(imageURL string) string {
	sum := sha256.Sum256([]byte(imageURL))
	return hex.EncodeToString(sum[:])
}

func contentImageRoutes(article sqlite.Article) map[string]string {
	routes := make(map[string]string)
	for destination, imageURL := range thumbnail.MarkdownImages(article.URL, article.ContentMarkdown) {
		routes[destination] = "/thumbnails/" + url.PathEscape(article.ID) + "/content/" + contentImageID(imageURL)
	}
	return routes
}

func writeError(w http.ResponseWriter, err error) {
	writeSafeError(w, err, feed.SafeError(err))
}

func writeSafeError(w http.ResponseWriter, err error, message string) {
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
	case errors.Is(err, sqlite.ErrLimit):
		status = http.StatusTooManyRequests
	case errors.Is(err, sqlite.ErrConflict), errors.Is(err, sqlite.ErrIdempotencyConflict):
		status = http.StatusConflict
	}
	http.Error(w, message, status)
}
