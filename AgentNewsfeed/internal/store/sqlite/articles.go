package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Article struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	Revision     int64  `json:"revision"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type MutationResult struct {
	Article Article `json:"article"`
	Created bool    `json:"created"`
}

type Cursor struct {
	CreatedAt int64  `json:"created_at"`
	ID        string `json:"id"`
}

const articleColumns = "id, canonical_url, title, summary, thumbnail_url, revision, created_at, updated_at"

type scanner interface{ Scan(...any) error }

func scanArticle(row scanner) (Article, error) {
	var a Article
	err := row.Scan(&a.ID, &a.URL, &a.Title, &a.Summary, &a.ThumbnailURL, &a.Revision, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	return a, err
}

func (s *Store) Article(ctx context.Context, owner Owner, id string) (Article, error) {
	if owner.id == "" {
		return Article{}, ErrOwnerRequired
	}
	return scanArticle(s.read.QueryRowContext(ctx, "SELECT "+articleColumns+` FROM articles
  WHERE owner_id = ? AND id = ? AND owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, owner.id, id))
}

func (s *Store) ListArticles(ctx context.Context, owner Owner, after Cursor, limit int) ([]Article, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	if limit < 1 || limit > 51 {
		return nil, ErrLimit
	}
	rows, err := s.read.QueryContext(ctx, "SELECT "+articleColumns+` FROM articles
  WHERE owner_id = ? AND owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)
  AND (? = 0 OR created_at < ? OR (created_at = ? AND id < ?))
  ORDER BY created_at DESC, id DESC LIMIT ?`, owner.id, after.CreatedAt, after.CreatedAt, after.CreatedAt, after.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	articles := []Article{}
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

// MutateArticle commits the article and its durable retry result together.
// Add deduplicates by canonical URL without replacing a previous introduction.
// Update is a full replacement and must compare the current revision.
func (s *Store) MutateArticle(ctx context.Context, owner Owner, key, operation string, payload []byte, a Article, expected int64) (MutationResult, error) {
	if owner.id == "" {
		return MutationResult{}, ErrOwnerRequired
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, err
	}
	defer tx.Rollback()
	var active bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM accounts WHERE id = ? AND disabled_at IS NULL)", owner.id).Scan(&active); err != nil {
		return MutationResult{}, err
	}
	if !active {
		return MutationResult{}, ErrOwnerRequired
	}
	hash := sha256.Sum256(payload)
	var storedOperation string
	var storedHash, result []byte
	err = tx.QueryRowContext(ctx, "SELECT operation, payload_hash, result FROM idempotency_results WHERE owner_id = ? AND key = ?", owner.id, key).Scan(&storedOperation, &storedHash, &result)
	if err == nil {
		if storedOperation != operation || string(storedHash) != string(hash[:]) {
			return MutationResult{}, ErrIdempotencyConflict
		}
		var out MutationResult
		if err := json.Unmarshal(result, &out); err != nil {
			return MutationResult{}, err
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MutationResult{}, err
	}
	now := time.Now().UnixMilli()
	created := false
	switch operation {
	case "add_article":
		existing, err := scanArticle(tx.QueryRowContext(ctx, "SELECT "+articleColumns+" FROM articles WHERE owner_id = ? AND canonical_url = ?", owner.id, a.URL))
		if err == nil {
			a = existing
		} else if errors.Is(err, ErrNotFound) {
			a.ID, a.Revision, a.CreatedAt, a.UpdatedAt = rand.Text(), 1, now, now
			if _, err := tx.ExecContext(ctx, `INSERT INTO articles(owner_id,id,canonical_url,title,summary,thumbnail_url,revision,created_at,updated_at)
    VALUES (?,?,?,?,?,?,?,?,?)`, owner.id, a.ID, a.URL, a.Title, a.Summary, a.ThumbnailURL, a.Revision, a.CreatedAt, a.UpdatedAt); err != nil {
				return MutationResult{}, err
			}
			created = true
		} else {
			return MutationResult{}, err
		}
	case "update_article":
		// An owner predicate also prevents cross-account existence disclosure.
		current, err := scanArticle(tx.QueryRowContext(ctx, "SELECT "+articleColumns+" FROM articles WHERE owner_id = ? AND id = ?", owner.id, a.ID))
		if err != nil {
			return MutationResult{}, err
		}
		if expected != current.Revision {
			return MutationResult{}, ErrConflict
		}
		var duplicate bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM articles WHERE owner_id = ? AND canonical_url = ? AND id != ?)", owner.id, a.URL, a.ID).Scan(&duplicate); err != nil {
			return MutationResult{}, err
		}
		if duplicate {
			return MutationResult{}, ErrConflict
		}
		a.Revision, a.CreatedAt, a.UpdatedAt = current.Revision+1, current.CreatedAt, now
		if _, err := tx.ExecContext(ctx, `UPDATE articles SET canonical_url=?,title=?,summary=?,thumbnail_url=?,revision=?,updated_at=?
   WHERE owner_id=? AND id=? AND revision=?`, a.URL, a.Title, a.Summary, a.ThumbnailURL, a.Revision, a.UpdatedAt, owner.id, a.ID, expected); err != nil {
			return MutationResult{}, err
		}
	default:
		return MutationResult{}, ErrConflict
	}
	out := MutationResult{Article: a, Created: created}
	result, err = json.Marshal(out)
	if err != nil {
		return MutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_results(owner_id,key,operation,payload_hash,result,created_at)
  VALUES (?,?,?,?,?,?)`, owner.id, key, operation, hash[:], result, now); err != nil {
		return MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult{}, err
	}
	return out, nil
}
