package web

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

// Jobs use native URLs and forms, so a refresh or a shared private deep link
// selects the same view and list page without keeping browser-only state.
func (h *handler) jobs(w http.ResponseWriter, r *http.Request) {
	data := bootstrap{Configured: h.auth != nil, Section: "jobs", View: "feed", SignInURL: "/auth/login?return_to=" + url.QueryEscape(r.URL.RequestURI())}
	status := http.StatusOK
	if h.auth == nil {
		status = http.StatusServiceUnavailable
	} else if p, csrf, err := h.auth.BrowserSession(r); err == nil {
		if err := p.Require(identity.Read); err != nil {
			writeOpportunityError(w, err)
			return
		}
		query, err := opportunityQuery(r)
		if err != nil {
			writeOpportunityError(w, err)
			return
		}
		id := r.PathValue("id")
		for name := range query {
			if name != "cursor" && (name != "from" || id == "") {
				writeOpportunityError(w, feed.ErrInvalidOpportunity)
				return
			}
		}
		switch r.URL.Path {
		case "/jobs/archive":
			data.View = "archive"
		case "/jobs/trash":
			data.View = "trash"
		default:
			if id != "" {
				switch query.Get("from") {
				case "", "feed":
				case "archive", "trash":
					data.View = query.Get("from")
				default:
					writeOpportunityError(w, feed.ErrInvalidOpportunity)
					return
				}
			}
		}
		data.Cursor = query.Get("cursor")
		page, err := h.service.ListOpportunities(r.Context(), p, feed.ListOpportunitiesInput{
			Cursor: data.Cursor, Archived: data.View == "archive", Trashed: data.View == "trash",
		})
		if err != nil {
			writeOpportunityError(w, err)
			return
		}
		data.OpportunitiesPage = &page
		data.DetailSelected = id != ""
		if id == "" && len(page.Opportunities) != 0 {
			id = page.Opportunities[0].ID
		}
		if id != "" {
			o, err := h.service.GetOpportunity(r.Context(), p, feed.GetOpportunityInput{OpportunityID: id, Trashed: data.View == "trash"})
			if err != nil {
				writeOpportunityError(w, err)
				return
			}
			data.Opportunity = new(o)
			data.MutationKey = rand.Text()
		}
		data.Authenticated, data.CSRF, data.CanWrite = true, csrf, p.Allows(identity.ArticlesWrite)
	}
	h.renderShell(w, status, data)
}

func (h *handler) listOpportunities(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	query, err := opportunityQuery(r)
	if err != nil {
		writeOpportunityError(w, err)
		return
	}
	in := feed.ListOpportunitiesInput{}
	for name, values := range query {
		switch name {
		case "cursor":
			in.Cursor = values[0]
		case "limit":
			limit, err := strconv.Atoi(values[0])
			if err != nil || limit < 1 || limit > 50 {
				writeOpportunityError(w, feed.ErrInvalidOpportunity)
				return
			}
			in.Limit = limit
		case "archived", "trashed":
			if values[0] != "true" && values[0] != "false" {
				writeOpportunityError(w, feed.ErrInvalidOpportunity)
				return
			}
			if name == "archived" {
				in.Archived = values[0] == "true"
			} else {
				in.Trashed = values[0] == "true"
			}
		default:
			writeOpportunityError(w, feed.ErrInvalidOpportunity)
			return
		}
	}
	page, err := h.service.ListOpportunities(r.Context(), p, in)
	if err != nil {
		writeOpportunityError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func (h *handler) opportunityDetail(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	query, err := opportunityQuery(r)
	if err != nil {
		writeOpportunityError(w, err)
		return
	}
	for name, values := range query {
		if name != "trashed" || (values[0] != "true" && values[0] != "false") {
			writeOpportunityError(w, feed.ErrInvalidOpportunity)
			return
		}
	}
	o, err := h.service.GetOpportunity(r.Context(), p, feed.GetOpportunityInput{OpportunityID: r.PathValue("id"), Trashed: query.Get("trashed") == "true"})
	if err != nil {
		writeOpportunityError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(o)
}

func opportunityQuery(r *http.Request) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, feed.ErrInvalidOpportunity
	}
	for _, values := range query {
		if len(values) != 1 {
			return nil, feed.ErrInvalidOpportunity
		}
	}
	return query, nil
}

func (h *handler) opportunityState(action string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		if len(r.PostForm) != 3 || len(r.PostForm["csrf_token"]) != 1 || len(r.PostForm["idempotency_key"]) != 1 || len(r.PostForm["expected_revision"]) != 1 {
			writeOpportunityError(w, feed.ErrInvalidOpportunity)
			return
		}
		revision, err := strconv.ParseInt(r.PostForm.Get("expected_revision"), 10, 64)
		if err != nil {
			writeOpportunityError(w, feed.ErrInvalidOpportunity)
			return
		}
		in := feed.OpportunityStateInput{IdempotencyKey: r.PostForm.Get("idempotency_key"), OpportunityID: r.PathValue("id"), ExpectedRevision: revision}
		switch action {
		case "archive", "unarchive":
			_, err = h.service.SetOpportunityArchived(r.Context(), p, feed.ArchiveOpportunityInput{
				IdempotencyKey: in.IdempotencyKey, OpportunityID: in.OpportunityID, ExpectedRevision: in.ExpectedRevision, Archived: action == "archive",
			})
		case "trash":
			_, err = h.service.TrashOpportunity(r.Context(), p, in)
		case "restore":
			_, err = h.service.RestoreOpportunity(r.Context(), p, in)
		}
		if err != nil {
			writeOpportunityError(w, err)
			return
		}
		// Exact retries return their original result, which may predate a later
		// restore. Redirect to the current state instead of a stale Trash URL.
		o, err := h.service.GetOpportunity(r.Context(), p, feed.GetOpportunityInput{OpportunityID: in.OpportunityID})
		if errors.Is(err, sqlite.ErrNotFound) {
			o, err = h.service.GetOpportunity(r.Context(), p, feed.GetOpportunityInput{OpportunityID: in.OpportunityID, Trashed: true})
		}
		if err != nil {
			writeOpportunityError(w, err)
			return
		}
		view := "feed"
		if o.TrashedAt != 0 {
			view = "trash"
		} else if o.ArchivedAt != 0 {
			view = "archive"
		}
		http.Redirect(w, r, "/jobs/"+url.PathEscape(o.ID)+"?from="+view+"#opportunity-detail", http.StatusSeeOther)
	})
}

func writeOpportunityError(w http.ResponseWriter, err error) {
	message := feed.OpportunitySafeError(err)
	switch {
	case errors.Is(err, feed.ErrInvalidOpportunity), errors.Is(err, feed.ErrInvalid):
		message = "Invalid listing request."
	case errors.Is(err, sqlite.ErrNotFound):
		message = "Listing not found."
	case errors.Is(err, sqlite.ErrConflict), errors.Is(err, sqlite.ErrIdempotencyConflict):
		message = "This listing changed. Reload it before trying again."
	}
	writeSafeError(w, err, message)
}
