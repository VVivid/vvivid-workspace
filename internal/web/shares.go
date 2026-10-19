package web

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/shopping"
	"agentshoppinglist/internal/store/sqlite"
)

// The proxy is the only network peer in production. A fixed, app-wide budget
// bounds distributed guessing without trusting attacker-supplied forwarding
// headers, unbounded per-IP maps, or an infrastructure configuration change.
// This personal app allows a burst of 60, then 30 public requests per minute.
type shareBudget struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (b *shareBudget) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		b.tokens, b.last = 60, now
	}
	b.tokens = min(60, b.tokens+max(0, now.Sub(b.last).Seconds())/2)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func registerShares(mux *http.ServeMux, templates *template.Template, auth *identity.Manager, service *shopping.Service) {
	mux.Handle("POST /api/v1/lists/{list_id}/shares", auth.BrowserMiddleware([]string{identity.Read, identity.ListsWrite}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision int64                   `json:"expected_revision"`
			Items            []sqlite.ShareSelection `json:"items"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		p, _ := identity.FromContext(r.Context())
		result, err := service.CreateShare(r.Context(), p, shopping.CreateShareInput{
			WriteMeta:        requestMeta(w, r),
			ListID:           r.PathValue("list_id"),
			ExpectedRevision: input.ExpectedRevision,
			Items:            input.Items,
		})
		respond(w, result, err)
	})))
	public := http.NewServeMux()
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		renderShare(w, templates, sqlite.PublicShare{}, http.StatusNotFound)
	}
	public.HandleFunc("GET /s/{code}", func(w http.ResponseWriter, r *http.Request) {
		share, err := service.PublicShare(r.Context(), r.PathValue("code"))
		if err != nil {
			if errors.Is(err, sqlite.ErrNotFound) {
				unavailable(w, r)
			} else {
				http.Error(w, "The shared list could not be loaded. Try again.", http.StatusServiceUnavailable)
			}
			return
		}
		renderShare(w, templates, share, http.StatusOK)
	})
	origin := strings.TrimSuffix(auth.APIAudience(), "/api/v1")
	public.HandleFunc("PATCH /s/{code}/items/{position}", func(w http.ResponseWriter, r *http.Request) {
		// The link grants reading. A separate unguessable capability issued only
		// with the same-origin page grants writes; no owner session is involved.
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != origin || len(r.Header.Values("X-Share-CSRF")) != 1 || len(r.Header.Get("X-Share-CSRF")) != 26 {
			http.Error(w, "The request could not be verified.", http.StatusForbidden)
			return
		}
		position, err := strconv.Atoi(r.PathValue("position"))
		if err != nil || !sqlite.ValidShareCode(r.PathValue("code")) {
			unavailable(w, r)
			return
		}
		var input struct {
			PickedUp         *bool `json:"picked_up"`
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if input.PickedUp == nil {
			respond(w, nil, &shopping.ValidationError{Message: "Set picked_up to true or false."})
			return
		}
		result, err := service.SetSharedItem(r.Context(), r.PathValue("code"), r.Header.Get("X-Share-CSRF"), requestMeta(w, r), sqlite.SharedItemInput{
			Position:         position,
			PickedUp:         *input.PickedUp,
			ExpectedRevision: input.ExpectedRevision,
		})
		if errors.Is(err, sqlite.ErrNotFound) {
			unavailable(w, r)
			return
		}
		respond(w, result, err)
	})
	public.HandleFunc("/s", unavailable)
	public.HandleFunc("/s/", unavailable)
	budget := &shareBudget{}
	guard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		if !budget.allow(time.Now()) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Too many shared-list requests. Try again shortly.", http.StatusTooManyRequests)
			return
		}
		public.ServeHTTP(w, r)
	})
	mux.Handle("/s", guard)
	mux.Handle("/s/", guard)
}

func renderShare(w http.ResponseWriter, templates *template.Template, share sqlite.PublicShare, status int) {
	var body bytes.Buffer
	if err := templates.ExecuteTemplate(&body, "shared-list", share); err != nil {
		http.Error(w, "The shared list could not be loaded.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

func sharedPack(item sqlite.SharedItem) string {
	return packText(sqlite.ProductSummary{PackageCount: item.PackageCount, PackageSize: item.PackageSize, PackageUnit: item.PackageUnit})
}
