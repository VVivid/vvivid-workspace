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

const MaxContentBytes = 256 << 10

type Service struct {
	store      *sqlite.Store
	thumbnails thumbnail.Policy
	images     ThumbnailFetcher
}

func New(store *sqlite.Store, policy thumbnail.Policy) *Service {
	return NewWithThumbnailFetcher(store, policy, thumbnail.NewProxy(policy))
}

// ThumbnailFetcher prepares a bounded JPEG without holding a database lock.
type ThumbnailFetcher interface {
	FetchThumbnail(context.Context, string) ([]byte, error)
}

func NewWithThumbnailFetcher(store *sqlite.Store, policy thumbnail.Policy, images ThumbnailFetcher) *Service {
	return &Service{store: store, thumbnails: policy, images: images}
}

func (s *Service) Ready(ctx context.Context) error { return s.store.Ready(ctx) }

type Content struct {
	URL             string `json:"url"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	ThumbnailURL    string `json:"thumbnail_url,omitempty"`
	ContentMarkdown string `json:"content_markdown,omitempty"`
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
	Limit    int    `json:"limit,omitzero"`
	Cursor   string `json:"cursor,omitempty"`
	Archived bool   `json:"archived,omitzero"`
	Trashed  bool   `json:"trashed,omitzero"`
}
type Page struct {
	Articles   []sqlite.Article `json:"articles"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func boundedText(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

// Recheck enrollment even when a previously verified principal is reused.
func (s *Service) authorize(ctx context.Context, p identity.Principal, scopes ...string) error {
	if err := p.Require(scopes...); err != nil {
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
	if err != nil || !boundedText(c.Title, 300) || !boundedText(c.Summary, 1200) || thumbnails.Validate(c.ThumbnailURL) != nil || !validMarkdown(c.ContentMarkdown) {
		return sqlite.Article{}, ErrInvalid
	}
	return sqlite.Article{URL: canonical, Title: c.Title, Summary: c.Summary, ThumbnailURL: c.ThumbnailURL, ContentMarkdown: c.ContentMarkdown}, nil
}

// Markdown preserves the source's whitespace. Line breaks and tabs are text;
// other controls and malformed UTF-8 are never valid article content.
func validMarkdown(value string) bool {
	return len(value) <= MaxContentBytes && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, func(r rune) bool {
			return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
		})
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
	if out, found, err := s.store.ReplayMutation(ctx, p.Owner(), in.IdempotencyKey, "add_article", payload); found || err != nil {
		return out, err
	}
	// Canonical duplicates, including retained Trash, do not fetch a new image.
	if _, err := s.store.ArticleForURL(ctx, p.Owner(), a.URL); err == nil {
		return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, "add_article", payload, a, 0)
	} else if !errors.Is(err, sqlite.ErrNotFound) {
		return sqlite.MutationResult{}, err
	}
	change := s.prepareThumbnail(ctx, a.ThumbnailURL)
	return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, "add_article", payload, a, 0, change)
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
	if out, found, err := s.store.ReplayMutation(ctx, p.Owner(), in.IdempotencyKey, "update_article", payload); found || err != nil {
		return out, err
	}
	current, err := s.store.Article(ctx, p.Owner(), a.ID)
	if err != nil {
		return sqlite.MutationResult{}, err
	}
	if current.Revision != in.ExpectedRevision {
		return sqlite.MutationResult{}, sqlite.ErrConflict
	}
	if duplicate, err := s.store.ArticleForURL(ctx, p.Owner(), a.URL); err == nil && duplicate.ID != a.ID {
		return sqlite.MutationResult{}, sqlite.ErrConflict
	} else if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
		return sqlite.MutationResult{}, err
	}
	change := sqlite.ThumbnailChange{}
	if a.ThumbnailURL != current.ThumbnailURL || current.ThumbnailStatus != "stored" {
		change = s.prepareThumbnail(ctx, a.ThumbnailURL)
	}
	return s.store.MutateArticle(ctx, p.Owner(), in.IdempotencyKey, "update_article", payload, a, in.ExpectedRevision, change)
}

func (s *Service) ListArticles(ctx context.Context, p identity.Principal, in ListInput) (Page, error) {
	page, err := s.listArticles(ctx, p, in, forwardOnly)
	return Page{Articles: page.rows, NextCursor: page.next}, err
}

func (s *Service) listArticles(ctx context.Context, p identity.Principal, in ListInput, mode pageMode) (listPage[sqlite.Article], error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return listPage[sqlite.Article]{}, err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 50 {
		return listPage[sqlite.Article]{}, ErrInvalid
	}
	owner := p.Owner()
	cursor, err := in.articleCursor(owner)
	if err != nil {
		return listPage[sqlite.Article]{}, err
	}
	return loadPage(in.Limit, mode, in.Cursor != "", cursor,
		func(boundary sqlite.Cursor, before bool, limit int) ([]sqlite.Article, error) {
			boundary.Before = before
			return s.store.ListArticles(ctx, owner, boundary, limit)
		},
		func(article sqlite.Article) sqlite.Cursor {
			timestamp := article.CreatedAt
			if in.Archived {
				timestamp = article.ArchivedAt
			} else if in.Trashed {
				timestamp = article.TrashedAt
			}
			return sqlite.Cursor{Timestamp: timestamp, ID: article.ID, Owner: owner.ID(), Archived: in.Archived, Trashed: in.Trashed}
		})
}

// ValidateCursor checks a reader's return position without loading its list.
func (in ListInput) ValidateCursor(owner sqlite.Owner) error {
	_, err := in.articleCursor(owner)
	return err
}

func (in ListInput) articleCursor(owner sqlite.Owner) (sqlite.Cursor, error) {
	if len(in.Cursor) > 256 || (in.Archived && in.Trashed) {
		return sqlite.Cursor{}, ErrInvalid
	}
	if in.Cursor == "" {
		return sqlite.Cursor{Owner: owner.ID(), Archived: in.Archived, Trashed: in.Trashed}, nil
	}
	var cursor sqlite.Cursor
	raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err != nil || decoder.Decode(&cursor) != nil || decoder.InputOffset() != int64(len(raw)) || cursor.Timestamp < 1 || !boundedText(cursor.ID, 64) ||
		cursor.Owner != owner.ID() || cursor.Archived != in.Archived || cursor.Trashed != in.Trashed {
		return sqlite.Cursor{}, ErrInvalid
	}
	return cursor, nil
}

func (s *Service) GetArticle(ctx context.Context, p identity.Principal, id string) (sqlite.Article, error) {
	return s.GetArticleInView(ctx, p, id, false)
}

func (s *Service) GetArticleInView(ctx context.Context, p identity.Principal, id string, trashed bool) (sqlite.Article, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.Article{}, err
	}
	if !boundedText(id, 64) {
		return sqlite.Article{}, ErrInvalid
	}
	return s.store.ArticleInView(ctx, p.Owner(), id, trashed)
}

func (s *Service) SetArchived(ctx context.Context, p identity.Principal, id string, archived bool) (sqlite.Article, error) {
	if err := s.authorize(ctx, p, identity.ArticlesWrite); err != nil {
		return sqlite.Article{}, err
	}
	if !boundedText(id, 64) {
		return sqlite.Article{}, ErrInvalid
	}
	return s.store.SetArchived(ctx, p.Owner(), id, archived)
}

func (s *Service) prepareThumbnail(ctx context.Context, raw string) sqlite.ThumbnailChange {
	change := sqlite.ThumbnailChange{Replace: true}
	if raw == "" {
		return change
	}
	body, err := s.images.FetchThumbnail(ctx, raw)
	if err != nil {
		change.Status = "unavailable"
	} else {
		change.JPEG = body
	}
	return change
}

func (s *Service) Thumbnail(ctx context.Context, p identity.Principal, id string, trashed bool) ([]byte, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return nil, err
	}
	if !boundedText(id, 64) {
		return nil, ErrInvalid
	}
	return s.store.Thumbnail(ctx, p.Owner(), id, trashed)
}

func SafeError(err error) string {
	switch {
	case errors.Is(err, ErrInvalid):
		return "Invalid article request. Use a public HTTPS source URL, title (1–300 bytes), short introduction (1–1200 bytes), optional original-source Markdown (up to 256 KiB), and an optional source-provided public HTTPS thumbnail allowed by the image policy."
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
