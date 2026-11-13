package feed

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
)

const DefaultBrowserPageSize = 10
const MaxBrowserPageSize = 500
const MaxArticleQueryBytes = 300

// Browser browsing has a separate bound and cursor from the public MCP/API list.
type BrowserListInput struct {
	ListInput
	Query string
}

type BrowserArticlesPage struct {
	Page
	PreviousCursor string `json:"previous_cursor,omitempty"`
	Total          int    `json:"total"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
}

type browserArticleCursor struct {
	sqlite.Cursor
	QueryHash string `json:"query_hash"`
	PageSize  int    `json:"page_size"`
}

// NormalizeArticleQuery makes whitespace and case immaterial to matching and
// cursor identity. Terms are literal substrings, not SQL or regular expressions.
func NormalizeArticleQuery(query string) (string, error) {
	if len(query) > MaxArticleQueryBytes || !utf8.ValidString(query) || strings.ContainsFunc(query, func(r rune) bool {
		return unicode.IsControl(r) && !unicode.IsSpace(r)
	}) {
		return "", ErrInvalid
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	// Unicode lowercasing can grow UTF-8; navigation revalidates this output.
	if len(normalized) > MaxArticleQueryBytes {
		return "", ErrInvalid
	}
	return normalized, nil
}

func (in BrowserListInput) articleCursor(owner sqlite.Owner) (sqlite.Cursor, error) {
	query, err := NormalizeArticleQuery(in.Query)
	if err != nil || in.Limit < 1 || in.Limit > MaxBrowserPageSize || len(in.Cursor) > 1024 || (in.Archived && in.Trashed) {
		return sqlite.Cursor{}, ErrInvalid
	}
	if in.Cursor == "" {
		return sqlite.Cursor{Owner: owner.ID(), Archived: in.Archived, Trashed: in.Trashed}, nil
	}
	var cursor browserArticleCursor
	raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Timestamp < 1 || !boundedText(cursor.ID, 64) ||
		cursor.Owner != owner.ID() || cursor.Archived != in.Archived || cursor.Trashed != in.Trashed ||
		cursor.QueryHash != articleQueryHash(query) || cursor.PageSize != in.Limit {
		return sqlite.Cursor{}, ErrInvalid
	}
	return cursor.Cursor, nil
}

func (in BrowserListInput) ValidateCursor(owner sqlite.Owner) error {
	_, err := in.articleCursor(owner)
	return err
}

func articleQueryHash(query string) string {
	hash := sha256.Sum256([]byte(query))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func (s *Service) BrowseArticles(ctx context.Context, p identity.Principal, in BrowserListInput, before bool) (BrowserArticlesPage, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return BrowserArticlesPage{}, err
	}
	if in.Limit == 0 {
		in.Limit = DefaultBrowserPageSize
	}
	query, err := NormalizeArticleQuery(in.Query)
	if err != nil {
		return BrowserArticlesPage{}, err
	}
	cursor, err := in.articleCursor(p.Owner())
	if err != nil {
		return BrowserArticlesPage{}, err
	}
	cursor.Before = before
	stored, err := s.store.BrowseArticles(ctx, p.Owner(), cursor, in.Limit, query)
	if err != nil {
		return BrowserArticlesPage{}, err
	}
	page := BrowserArticlesPage{Page: Page{Articles: stored.Articles}, Total: stored.Total, Start: stored.Start, End: stored.End}
	cursorFor := func(article sqlite.Article) string {
		timestamp := article.CreatedAt
		if in.Trashed {
			timestamp = article.TrashedAt
		} else if in.Archived {
			timestamp = article.ArchivedAt
		}
		return encodeCursor(browserArticleCursor{
			Cursor:    sqlite.Cursor{Timestamp: timestamp, ID: article.ID, Owner: p.Owner().ID(), Archived: in.Archived, Trashed: in.Trashed},
			QueryHash: articleQueryHash(query), PageSize: in.Limit,
		})
	}
	if page.Start > 1 {
		page.PreviousCursor = cursorFor(page.Articles[0])
	}
	if page.End > 0 && page.End < page.Total {
		page.NextCursor = cursorFor(page.Articles[len(page.Articles)-1])
	}
	return page, nil
}
