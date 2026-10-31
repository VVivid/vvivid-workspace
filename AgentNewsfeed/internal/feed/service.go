// Package feed applies identical owner, scope and validation rules for the web
// feed and MCP tools. Article text is untrusted display data, never instructions.
package feed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/urls"
)

var ErrInvalid = errors.New("invalid article request")

type Service struct {
	store      *sqlite.Store
	thumbnails thumbnail.Policy
}

func New(store *sqlite.Store, policy thumbnail.Policy) *Service {
	return &Service{store: store, thumbnails: policy}
}
func (s *Service) Ready(ctx context.Context) error { return s.store.Ready(ctx) }

type Content struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}
type AddInput struct {
	IdempotencyKey string  `json:"idempotency_key"`
	Article        Content `json:"article"`
}
type UpdateInput struct {
	IdempotencyKey   string  `json:"idempotency_key"`
	ArticleID        string  `json:"article_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	Article          Content `json:"article"`
}
type ListInput struct {
	Limit  int    `json:"limit,omitzero"`
	Cursor string `json:"cursor,omitempty"`
}
type Page struct {
	Articles   []sqlite.Article `json:"articles"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func boundedText(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

// Recheck enrollment even when a previously verified principal is reused.
func (s *Service) authorize(ctx context.Context, p identity.Principal, scope string) error {
	if err := p.Require(scope); err != nil {
		return err
	}
	owner, err := s.store.OwnerForIdentity(ctx, p.Identity())
	if err != nil || owner != p.Owner() {
		return identity.ErrUnauthenticated
	}
	return nil
}

func normalizeContent(c Content, thumbnails thumbnail.Policy) (sqlite.Article, error) {
	canonical, err := urls.Canonical(c.URL)
	if err != nil || !boundedText(c.Title, 300) || !boundedText(c.Summary, 1200) || thumbnails.Validate(c.ThumbnailURL) != nil {
		return sqlite.Article{}, ErrInvalid
	}
	return sqlite.Article{URL: canonical, Title: c.Title, Summary: c.Summary, ThumbnailURL: c.ThumbnailURL}, nil
}

// NormalizeAddInput shares pure input validation with the offline seed command.
// It does not authorize a caller, select an owner or write any records.
func NormalizeAddInput(in AddInput, thumbnails thumbnail.Policy) (sqlite.Article, error) {
	if !boundedText(in.IdempotencyKey, 128) {
		return sqlite.Article{}, ErrInvalid
	}
	return normalizeContent(in.Article, thumbnails)
}

func (s *Service) AddArticle(ctx context.Context, p identity.Principal, in AddInput) (sqlite.MutationResult, error) {
	if err := s.authorize(ctx, p, identity.ArticlesWrite); err != nil {
		return sqlite.MutationResult{}, err
	}
	a, err := NormalizeAddInput(in, s.thumbnails)
	if err != nil {
		return sqlite.MutationResult{}, err
	}
	payload, _ := json.Marshal(a)
	return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, "add_article", payload, a, 0)
}

func (s *Service) UpdateArticle(ctx context.Context, p identity.Principal, in UpdateInput) (sqlite.MutationResult, error) {
	if err := s.authorize(ctx, p, identity.ArticlesWrite); err != nil {
		return sqlite.MutationResult{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) || !boundedText(in.ArticleID, 64) || in.ExpectedRevision < 1 {
		return sqlite.MutationResult{}, ErrInvalid
	}
	a, err := normalizeContent(in.Article, s.thumbnails)
	if err != nil {
		return sqlite.MutationResult{}, err
	}
	a.ID = in.ArticleID
	payload, _ := json.Marshal(struct {
		Article  sqlite.Article
		Expected int64
	}{a, in.ExpectedRevision})
	return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, "update_article", payload, a, in.ExpectedRevision)
}

func (s *Service) ListArticles(ctx context.Context, p identity.Principal, in ListInput) (Page, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return Page{}, err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 50 || len(in.Cursor) > 256 {
		return Page{}, ErrInvalid
	}
	var cursor sqlite.Cursor
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.CreatedAt < 1 || !boundedText(cursor.ID, 64) {
			return Page{}, ErrInvalid
		}
	}
	articles, err := s.store.ListArticles(ctx, p.Owner(), cursor, in.Limit+1)
	if err != nil {
		return Page{}, err
	}
	page := Page{Articles: articles}
	if len(articles) > in.Limit {
		page.Articles = articles[:in.Limit]
		last := page.Articles[len(page.Articles)-1]
		raw, _ := json.Marshal(sqlite.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

func (s *Service) GetArticle(ctx context.Context, p identity.Principal, id string) (sqlite.Article, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.Article{}, err
	}
	if !boundedText(id, 64) {
		return sqlite.Article{}, ErrInvalid
	}
	return s.store.Article(ctx, p.Owner(), id)
}

func SafeError(err error) string {
	switch {
	case errors.Is(err, ErrInvalid):
		return "Invalid article request. Use a public HTTPS source URL, title (1–300 bytes), short introduction (1–1200 bytes), and a permitted thumbnail or omit it."
	case errors.Is(err, identity.ErrUnauthenticated):
		return "Authentication required."
	case errors.Is(err, identity.ErrForbidden), errors.Is(err, sqlite.ErrOwnerRequired):
		return "Permission denied."
	case errors.Is(err, sqlite.ErrNotFound):
		return "Article not found."
	case errors.Is(err, sqlite.ErrConflict):
		return "Article changed or URL already exists. Read the current article before retrying with a new key."
	case errors.Is(err, sqlite.ErrIdempotencyConflict):
		return "Idempotency key already used for different input."
	default:
		return "Newsfeed temporarily unavailable."
	}
}
