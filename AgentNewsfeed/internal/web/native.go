package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

// articleStateChange is one of the revision-checked, idempotent feed operations
// behind the MCP tools trash_article, restore_article and set_article_archived
// (see setArticleArchived). Native apps change article state only through these,
// so an exact retry, a stale revision or a reused key behaves as it does over
// MCP. Browser forms are unchanged: the Trash forms (handler.trash) already call
// these operations, while the archive and restore forms (handler.archive) keep
// the revision-free Service.SetArchived.
type articleStateChange func(context.Context, identity.Principal, feed.StateInput) (sqlite.MutationResult, error)

// nativeAppStateRequest is a native app's complete JSON body. The route path
// names the article.
type nativeAppStateRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

// maxNativeAppStateBody matches the browser forms' bound; a valid body needs
// far less, even with every key byte escaped.
const maxNativeAppStateBody = 4096

// setArticleArchived adapts Service.SetArticleArchived, the MCP
// set_article_archived operation, to the archive (true) and restore (false)
// routes. The route, not the request body, supplies archived.
func (h *handler) setArticleArchived(archived bool) articleStateChange {
	return func(ctx context.Context, p identity.Principal, in feed.StateInput) (sqlite.MutationResult, error) {
		return h.service.SetArticleArchived(ctx, p, feed.ArchiveInput{
			IdempotencyKey: in.IdempotencyKey, ArticleID: in.ArticleID, ExpectedRevision: in.ExpectedRevision, Archived: archived,
		})
	}
}

// nativeAppStateChange applies change for a bearer-authenticated native app and
// answers only with JSON, never a redirect: the resulting article in the shape
// of GET /api/v1/articles/{id}, or {"error": message}. An exact retry returns
// the original article, even after later changes.
func (h *handler) nativeAppStateChange(change articleStateChange) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body nativeAppStateRequest
		if err := readJSONBody(w, r, maxNativeAppStateBody, &body); err != nil {
			writeNativeAppStateError(w, err)
			return
		}
		p, _ := identity.FromContext(r.Context())
		in := feed.StateInput{IdempotencyKey: body.IdempotencyKey, ArticleID: r.PathValue("id"), ExpectedRevision: body.ExpectedRevision}
		out, err := change(r.Context(), p, in)
		if err != nil {
			writeNativeAppStateError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out.Article)
	})
}

// writeNativeAppStateError reports a failed native app state change as JSON,
// with the same errorStatus as the browser routes. The request has only an
// article ID and two body fields, so invalid input and stale revisions get
// advice specific to them instead of feed.SafeError's article content and URL
// hints; other errors keep the feed.SafeError message.
func writeNativeAppStateError(w http.ResponseWriter, err error) {
	message := feed.SafeError(err)
	switch {
	case errors.Is(err, feed.ErrInvalid):
		message = "Invalid article state request. Use an article ID from the API and a JSON object with only expected_revision, the article's current revision, and idempotency_key, 1–128 bytes."
	case errors.Is(err, sqlite.ErrConflict):
		message = "Article changed. Read the current article, then retry with its revision and a new idempotency key."
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errorStatus(err))
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{message})
}
