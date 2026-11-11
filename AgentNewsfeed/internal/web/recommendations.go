package web

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/strictjson"
)

type settingsPage struct {
	Preferences sqlite.PreferencesState           `json:"preferences"`
	Suggestions []sqlite.RecommendationSuggestion `json:"suggestions"`
	Discovery   sqlite.DiscoveryContext           `json:"discovery"`
	Runs        feed.DiscoveryRunsPage            `json:"runs"`
	SelectedRun *sqlite.DiscoveryRunDetail        `json:"selected_run,omitempty"`
}

func (h *handler) settings(w http.ResponseWriter, r *http.Request) {
	data := bootstrap{Configured: h.auth != nil, Section: "settings", View: "feed", SignInURL: "/auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())}
	status := http.StatusOK
	if h.auth == nil {
		status = http.StatusServiceUnavailable
	} else if p, csrf, err := h.auth.BrowserSession(r); err == nil {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 2 {
			writeRecommendationError(w, feed.ErrInvalid)
			return
		}
		for key, values := range query {
			if (key != "run" && key != "before") || len(values) != 1 {
				writeRecommendationError(w, feed.ErrInvalid)
				return
			}
		}
		before := int64(0)
		if query.Has("before") {
			before, err = strconv.ParseInt(query.Get("before"), 10, 64)
			if err != nil || before < 1 {
				writeRecommendationError(w, feed.ErrInvalid)
				return
			}
		}
		prefs, err := h.service.GetRecommendationPreferences(r.Context(), p, feed.EmptyInput{})
		if err != nil {
			writeRecommendationError(w, err)
			return
		}
		proposals, err := h.service.ListRecommendationSuggestions(r.Context(), p, feed.ListSuggestionsInput{Limit: 50})
		if err != nil {
			writeRecommendationError(w, err)
			return
		}
		discovery, err := h.service.GetDiscoveryContext(r.Context(), p, feed.EmptyInput{})
		if err != nil {
			writeRecommendationError(w, err)
			return
		}
		runs, err := h.service.ListDiscoveryRuns(r.Context(), p, feed.ListDiscoveryRunsInput{Limit: 20, Before: before})
		if err != nil {
			writeRecommendationError(w, err)
			return
		}
		settings := settingsPage{Preferences: prefs, Suggestions: proposals.Suggestions, Discovery: discovery, Runs: runs}
		if query.Has("run") {
			run, err := h.service.GetDiscoveryRun(r.Context(), p, feed.GetDiscoveryRunInput{RunID: query.Get("run")})
			if err != nil {
				writeRecommendationError(w, err)
				return
			}
			settings.SelectedRun = new(run)
		}
		data.Settings = &settings
		data.Authenticated, data.CSRF, data.CanWrite = true, csrf, p.Allows(identity.ArticlesWrite)
	}
	h.renderPage(w, r, status, data)
}
func writeRecommendationError(w http.ResponseWriter, err error) {
	writeSafeError(w, err, feed.RecommendationSafeError(err))
}
func recommendationJSON[In, Out any](call func(context.Context, identity.Principal, In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			writeRecommendationError(w, feed.ErrInvalid)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			writeRecommendationError(w, feed.ErrInvalid)
			return
		}
		var input In
		if strictjson.Decode(raw, &input) != nil {
			writeRecommendationError(w, feed.ErrInvalid)
			return
		}
		p, _ := identity.FromContext(r.Context())
		out, err := call(r.Context(), p, input)
		if err != nil {
			writeRecommendationError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}
func (h *handler) registerSettings(mux *http.ServeMux) {
	scopes := []string{identity.Read, identity.ArticlesWrite}
	mux.Handle("POST /api/v1/settings/preferences", h.auth.BrowserMiddleware(scopes, recommendationJSON(h.service.UpdateRecommendationPreferences)))
	mux.Handle("POST /api/v1/settings/suggestions/accept", h.auth.BrowserMiddleware(scopes, recommendationJSON(h.service.AcceptRecommendationSuggestion)))
	mux.Handle("POST /api/v1/settings/suggestions/dismiss", h.auth.BrowserMiddleware(scopes, recommendationJSON(h.service.DismissRecommendationSuggestion)))
}
