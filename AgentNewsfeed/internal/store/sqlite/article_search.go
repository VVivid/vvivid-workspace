package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"slices"
	"strings"

	"modernc.org/sqlite"
)

func init() {
	// SQLite's built-in lower() handles ASCII only. Use Go's Unicode mapping
	// for literal matching without persisting search copies or changing schema.
	sqlite.MustRegisterDeterministicScalarFunction("newsfeed_lower", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		value, _ := args[0].(string)
		return strings.ToLower(value), nil
	})
}

type BrowserArticlePage struct {
	Articles          []Article
	Total, Start, End int
}

// BrowseArticles counts and reads one snapshot, so a concurrent mutation cannot
// make the displayed range disagree with the rows. Cursor paging stays keyset based.
func (s *Store) BrowseArticles(ctx context.Context, owner Owner, cursor Cursor, limit int, query string) (BrowserArticlePage, error) {
	if owner.id == "" {
		return BrowserArticlePage{}, ErrOwnerRequired
	}
	if limit < 1 || limit > 500 || (cursor.Archived && cursor.Trashed) {
		return BrowserArticlePage{}, ErrLimit
	}
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BrowserArticlePage{}, err
	}
	defer tx.Rollback()
	page := BrowserArticlePage{}
	filter, order, args := articleSelection(owner, cursor, query)
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM articles a WHERE "+filter, args...).Scan(&page.Total); err != nil {
		return BrowserArticlePage{}, err
	}
	page.Articles, err = listArticles(ctx, tx, owner, cursor, limit, query)
	if err != nil {
		return BrowserArticlePage{}, err
	}
	if cursor.Before {
		slices.Reverse(page.Articles)
	}
	if len(page.Articles) > 0 {
		first := page.Articles[0]
		timestamp := first.CreatedAt
		if cursor.Trashed {
			timestamp = first.TrashedAt
		} else if cursor.Archived {
			timestamp = first.ArchivedAt
		}
		var newer int
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM articles a WHERE "+filter+
			" AND ("+order+", a.id) > (?, ?)", append(args, timestamp, first.ID)...).Scan(&newer)
		if err != nil {
			return BrowserArticlePage{}, err
		}
		page.Start, page.End = newer+1, newer+len(page.Articles)
	}
	return page, tx.Commit()
}
