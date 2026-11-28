package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Article struct {
	ID string `json:"id"`
	// Only an agent-written article may have no source URL. Every request
	// hashed before such articles existed had a URL, so omitting an empty one
	// keeps old idempotency hashes.
	URL          string `json:"url,omitempty"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	Revision     int64  `json:"revision"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	// Keep optional additions at the end and omitted when empty: old durable
	// idempotency hashes include this struct's original JSON field order.
	ContentMarkdown string `json:"content_markdown,omitempty"`
	ArchivedAt      int64  `json:"archived_at,omitzero"`
	TrashedAt       int64  `json:"trashed_at,omitzero"`
	ThumbnailStatus string `json:"thumbnail_status,omitempty"`
	ThumbnailHash   string `json:"thumbnail_hash,omitempty"`
	ThumbnailWidth  int    `json:"thumbnail_width,omitzero"`
	ThumbnailHeight int    `json:"thumbnail_height,omitzero"`
	ThumbnailBytes  int    `json:"thumbnail_bytes,omitzero"`
	// Sorted, distinct tags; nil when the article has none.
	Tags []string `json:"tags,omitempty"`
	// Narration is how the article is read aloud; nil for an article saved
	// before scripts existed. Lists omit it, like the body.
	Narration []NarrationPart `json:"narration,omitempty"`
}

// NarrationPart is one part of an article's narration script, in reading
// order: what the voice says (Say) for a passage of the article's text (Text),
// or for the code block, diagram or table it describes (Describe).
type NarrationPart struct {
	Text     string `json:"text,omitempty"`
	Describe string `json:"describe,omitempty"`
	Say      string `json:"say"`
}

// narrationJSON is the stored form of an article's narration: ” for none.
func narrationJSON(parts []NarrationPart) string {
	if len(parts) == 0 {
		return ""
	}
	raw, _ := json.Marshal(parts)
	return string(raw)
}

type MutationResult struct {
	Article Article `json:"article"`
	Created bool    `json:"created"`
}

type Cursor struct {
	Timestamp int64  `json:"timestamp"`
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	Archived  bool   `json:"archived,omitzero"`
	Trashed   bool   `json:"trashed,omitzero"`
	Before    bool   `json:"-"`
}

// An article without a source URL stores a private placeholder, noURLPrefix
// plus its ID, in canonical_url. The column stays NOT NULL and unique per owner
// without rebuilding the table its thumbnail and discovery records reference.
// Canonical URLs always use https://, so a placeholder never equals one. Reads
// return an empty URL instead, so the placeholder never leaves this package.
const noURLPrefix = "no-url:"

// SQL over the articles alias a: articleHasURL is true when canonical_url holds
// a source URL rather than a's placeholder, and articleURL reads that URL or "".
const articleHasURL = "a.canonical_url != ('" + noURLPrefix + "' || a.id)"
const articleURL = "CASE WHEN " + articleHasURL + " THEN a.canonical_url ELSE '' END"

// storedURL is the canonical_url value written for a.
func storedURL(a Article) string {
	if a.URL == "" {
		return noURLPrefix + a.ID
	}
	return a.URL
}

// The schema allows only slugs, so a comma never occurs inside a tag. One
// comma-joined value per article lets lists read tags without another query.
const articleTags = `COALESCE((SELECT group_concat(g.tag, ',' ORDER BY g.tag) FROM article_tags g
 WHERE g.owner_id=a.owner_id AND g.article_id=a.id), '')`

const articleFrom = ` FROM articles a
 LEFT JOIN article_thumbnails t ON t.owner_id=a.owner_id AND t.article_id=a.id
 LEFT JOIN thumbnail_blobs b ON b.owner_id=t.owner_id AND b.hash=t.hash `
const articleSummaryColumns = `a.id, ` + articleURL + `, a.title, a.summary, a.thumbnail_url, a.revision, a.created_at, a.updated_at,
 a.archived_at, a.trashed_at, a.thumbnail_status, COALESCE(b.hash,''), COALESCE(b.width,0), COALESCE(b.height,0), COALESCE(length(b.jpeg),0), ` + articleTags
const articleColumns = articleSummaryColumns + ", a.content_markdown, a.narration"

type scanner interface{ Scan(...any) error }

func scanArticle(row scanner) (Article, error) {
	var a Article
	var archived, trashed sql.NullInt64
	var tags, narration string
	err := row.Scan(&a.ID, &a.URL, &a.Title, &a.Summary, &a.ThumbnailURL, &a.Revision, &a.CreatedAt, &a.UpdatedAt,
		&archived, &trashed, &a.ThumbnailStatus, &a.ThumbnailHash, &a.ThumbnailWidth, &a.ThumbnailHeight, &a.ThumbnailBytes, &tags, &a.ContentMarkdown, &narration)
	if errors.Is(err, sql.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, err
	}
	a.ArchivedAt, a.TrashedAt = archived.Int64, trashed.Int64
	if tags != "" {
		a.Tags = strings.Split(tags, ",")
	}
	if narration != "" {
		if err := json.Unmarshal([]byte(narration), &a.Narration); err != nil {
			return Article{}, err
		}
	}
	return a, nil
}

func (s *Store) Article(ctx context.Context, owner Owner, id string) (Article, error) {
	return s.ArticleInView(ctx, owner, id, false)
}

// ArticleInView requires an explicit Trash selection; ordinary reads cannot
// accidentally treat a trashed record as an active or archived article.
func (s *Store) ArticleInView(ctx context.Context, owner Owner, id string, trashed bool) (Article, error) {
	if owner.id == "" {
		return Article{}, ErrOwnerRequired
	}
	return scanArticle(s.read.QueryRowContext(ctx, "SELECT "+articleColumns+articleFrom+`
  WHERE a.owner_id = ? AND a.id = ? AND (a.trashed_at IS NOT NULL) = ?
  AND a.owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, owner.id, id, trashed))
}

// ArticleForURL includes retained Trash to keep canonical deduplication from
// recreating or reviving a trashed article. Without a URL, nothing duplicates.
func (s *Store) ArticleForURL(ctx context.Context, owner Owner, canonical string) (Article, error) {
	if owner.id == "" {
		return Article{}, ErrOwnerRequired
	}
	if canonical == "" {
		return Article{}, ErrNotFound
	}
	return scanArticle(s.read.QueryRowContext(ctx, "SELECT "+articleColumns+articleFrom+`
  WHERE a.owner_id=? AND a.canonical_url=? AND a.owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, owner.id, canonical))
}

// ListArticles reads one view in its page order. Given anyTags, it lists only
// articles that have at least one of them.
func (s *Store) ListArticles(ctx context.Context, owner Owner, cursor Cursor, limit int, anyTags ...string) ([]Article, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	if limit < 1 || limit > 51 || (cursor.Archived && cursor.Trashed) {
		return nil, ErrLimit
	}
	return listArticles(ctx, s.read, owner, cursor, limit, "", anyTags)
}

func articleSelection(owner Owner, cursor Cursor, query string, anyTags []string) (filter, order string, args []any) {
	filter = "a.owner_id = ? AND a.owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL) AND "
	args = []any{owner.id}
	view, order := "a.trashed_at IS NULL AND a.archived_at IS NULL", "a.created_at"
	if cursor.Trashed {
		view, order = "a.trashed_at IS NOT NULL", "a.trashed_at"
	} else if cursor.Archived {
		view, order = "a.trashed_at IS NULL AND a.archived_at IS NOT NULL", "a.archived_at"
	}
	filter += view
	for term := range strings.FieldsSeq(query) {
		filter += " AND instr(newsfeed_lower(a.title || ' ' || a.summary || ' ' || " + articleURL + "), ?) > 0"
		args = append(args, term)
	}
	if len(anyTags) > 0 {
		// The owner+tag index finds the articles carrying any requested tag.
		filter += " AND a.id IN (SELECT article_id FROM article_tags WHERE owner_id = ? AND tag IN (?" + strings.Repeat(", ?", len(anyTags)-1) + "))"
		args = append(args, owner.id)
		for _, tag := range anyTags {
			args = append(args, tag)
		}
	}
	return filter, order, args
}

func listArticles(ctx context.Context, db queryer, owner Owner, cursor Cursor, limit int, query string, anyTags []string) ([]Article, error) {
	// The summary selection excludes both Markdown and JPEG payloads.
	filter, order, args := articleSelection(owner, cursor, query, anyTags)
	pageSQL, pageArgs := pageRange(order, "a.id", cursor.Timestamp, cursor.ID, cursor.Before, limit)
	rows, err := db.QueryContext(ctx, "SELECT "+articleSummaryColumns+", '', ''"+articleFrom+
		"WHERE "+filter+pageSQL, append(args, pageArgs...)...)
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

// TagCount is a tag and how many articles in the selected view have it.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// TagCounts lists every tag on the articles of one view, selected as in
// ListArticles: active by default, Archive with archived, Trash with trashed.
// The most used tags come first and equal counts sort by tag, so the order is
// deterministic.
func (s *Store) TagCounts(ctx context.Context, owner Owner, archived, trashed bool) ([]TagCount, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	if archived && trashed {
		return nil, ErrLimit
	}
	query, args := tagCountsQuery(owner, archived, trashed)
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []TagCount{}
	for rows.Next() {
		var tag TagCount
		if err := rows.Scan(&tag.Tag, &tag.Count); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

// tagCountsQuery counts the tags of the view's articles. articleSelection
// applies the owner and enrollment checks of every article read. Naming the
// owner on the tag rows too lets the owner+tag index read them in tag order,
// already grouped, joining each to its article by primary key.
func tagCountsQuery(owner Owner, archived, trashed bool) (string, []any) {
	filter, _, args := articleSelection(owner, Cursor{Archived: archived, Trashed: trashed}, "", nil)
	return `SELECT g.tag, count(*) FROM article_tags g
 JOIN articles a ON a.owner_id = g.owner_id AND a.id = g.article_id
 WHERE g.owner_id = ? AND ` + filter + `
 GROUP BY g.tag ORDER BY count(*) DESC, g.tag`, append([]any{owner.id}, args...)
}

func checkOwner(ctx context.Context, db rowQueryer, owner Owner) error {
	if owner.id == "" {
		return ErrOwnerRequired
	}
	var active bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM accounts WHERE id = ? AND disabled_at IS NULL)", owner.id).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrOwnerRequired
	}
	return nil
}

func replayMutation(ctx context.Context, db rowQueryer, owner Owner, key, operation string, payload []byte) (MutationResult, bool, error) {
	var storedOperation string
	var storedHash, result []byte
	err := db.QueryRowContext(ctx, "SELECT operation, payload_hash, result FROM idempotency_results WHERE owner_id = ? AND key = ?", owner.id, key).Scan(&storedOperation, &storedHash, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return MutationResult{}, false, nil
	}
	if err != nil {
		return MutationResult{}, false, err
	}
	hash := sha256.Sum256(payload)
	if storedOperation != operation || string(storedHash) != string(hash[:]) {
		return MutationResult{}, false, ErrIdempotencyConflict
	}
	var out MutationResult
	err = json.Unmarshal(result, &out)
	return out, true, err
}

// ReplayMutation avoids network work for a durable retry. MutateArticle repeats
// this check under its write transaction after any external fetch completes.
func (s *Store) ReplayMutation(ctx context.Context, owner Owner, key, operation string, payload []byte) (MutationResult, bool, error) {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return MutationResult{}, false, err
	}
	return replayMutation(ctx, s.read, owner, key, operation, payload)
}

// MutateArticle commits content, thumbnail references, reading state and its
// exact retry result together. Prepared images were fetched outside this lock.
// The optional change preserves the offline seed/store call contract.
func (s *Store) MutateArticle(ctx context.Context, owner Owner, key, operation string, payload []byte, a Article, expected int64, thumbnailChanges ...ThumbnailChange) (MutationResult, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return MutationResult{}, err
	}
	if out, found, err := replayMutation(ctx, tx, owner, key, operation, payload); found || err != nil {
		return out, err
	}
	out, err := mutateArticleTx(ctx, tx, owner, key, operation, payload, a, expected, thumbnailChanges...)
	if err != nil {
		return MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult{}, err
	}
	return out, nil
}

// mutateArticleTx shares article and thumbnail writes with controlled discovery.
// The caller owns enrollment, retry checks, additional guards and commit.
func mutateArticleTx(ctx context.Context, tx *sql.Tx, owner Owner, key, operation string, payload []byte, a Article, expected int64, thumbnailChanges ...ThumbnailChange) (MutationResult, error) {
	now := time.Now().UnixMilli()
	created, contentChanged := false, false
	switch operation {
	case "add_article", "ingest_discovery_article":
		// Only a source URL can duplicate a saved article; one without is always new.
		existing, err := Article{}, ErrNotFound
		if a.URL != "" {
			existing, err = scanArticle(tx.QueryRowContext(ctx, "SELECT "+articleColumns+articleFrom+" WHERE a.owner_id = ? AND a.canonical_url = ?", owner.id, a.URL))
		}
		if err == nil {
			a = existing
		} else if errors.Is(err, ErrNotFound) {
			a.ID, a.Revision, a.CreatedAt, a.UpdatedAt = rand.Text(), 1, now, now
			if a.ThumbnailURL != "" {
				a.ThumbnailStatus = "legacy"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO articles(owner_id,id,canonical_url,title,summary,thumbnail_url,revision,created_at,updated_at,content_markdown,thumbnail_status,narration)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, owner.id, a.ID, storedURL(a), a.Title, a.Summary, a.ThumbnailURL, a.Revision, a.CreatedAt, a.UpdatedAt, a.ContentMarkdown, a.ThumbnailStatus, narrationJSON(a.Narration)); err != nil {
				return MutationResult{}, err
			}
			if err := replaceTags(ctx, tx, owner, a); err != nil {
				return MutationResult{}, err
			}
			created, contentChanged = true, true
		} else {
			return MutationResult{}, err
		}
	case "update_article", "patch_article", "trash_article", "restore_article", "set_article_archived":
		current, err := scanArticle(tx.QueryRowContext(ctx, "SELECT "+articleColumns+articleFrom+" WHERE a.owner_id = ? AND a.id = ?", owner.id, a.ID))
		if err != nil {
			return MutationResult{}, err
		}
		if operation != "restore_article" && current.TrashedAt != 0 {
			return MutationResult{}, ErrNotFound
		}
		if expected != current.Revision {
			return MutationResult{}, ErrConflict
		}
		switch operation {
		case "update_article", "patch_article":
			// Audio speaks the article's text, so the text stays as it is until
			// the owner deletes the audio.
			if locked, err := articleHasAudio(ctx, tx, owner.id, a.ID); err != nil {
				return MutationResult{}, err
			} else if locked {
				return MutationResult{}, ErrLocked
			}
			// A placeholder contains its own article ID, so it never duplicates another.
			var duplicate bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM articles WHERE owner_id = ? AND canonical_url = ? AND id != ?)", owner.id, storedURL(a), a.ID).Scan(&duplicate); err != nil {
				return MutationResult{}, err
			}
			if duplicate {
				return MutationResult{}, ErrConflict
			}
			a.Revision, a.CreatedAt, a.UpdatedAt, a.ArchivedAt = current.Revision+1, current.CreatedAt, now, current.ArchivedAt
			a.ThumbnailStatus, a.ThumbnailHash = current.ThumbnailStatus, current.ThumbnailHash
			a.ThumbnailWidth, a.ThumbnailHeight, a.ThumbnailBytes = current.ThumbnailWidth, current.ThumbnailHeight, current.ThumbnailBytes
			if _, err := tx.ExecContext(ctx, `UPDATE articles SET canonical_url=?,title=?,summary=?,thumbnail_url=?,revision=?,updated_at=?,content_markdown=?,narration=?
   WHERE owner_id=? AND id=?`, storedURL(a), a.Title, a.Summary, a.ThumbnailURL, a.Revision, a.UpdatedAt, a.ContentMarkdown, narrationJSON(a.Narration), owner.id, a.ID); err != nil {
				return MutationResult{}, err
			}
			if err := replaceTags(ctx, tx, owner, a); err != nil {
				return MutationResult{}, err
			}
			contentChanged = true
		default:
			archiveTarget := a.ArchivedAt != 0
			a = current
			changed := false
			switch operation {
			case "trash_article":
				// Trash deletes the article's audio; restoring does not bring it back.
				if err := deleteArticleAudioTx(ctx, tx, owner.id, a.ID); err != nil {
					return MutationResult{}, err
				}
				a.TrashedAt, changed = now, true
			case "restore_article":
				changed = a.TrashedAt != 0
				a.TrashedAt = 0
			case "set_article_archived":
				changed = (a.ArchivedAt != 0) != archiveTarget
				a.ArchivedAt = 0
				if archiveTarget {
					a.ArchivedAt = current.ArchivedAt
					if a.ArchivedAt == 0 {
						a.ArchivedAt = now
					}
				}
			}
			if changed {
				a.Revision++
				a.UpdatedAt = now
				if _, err := tx.ExecContext(ctx, `UPDATE articles SET archived_at=?,trashed_at=?,revision=?,updated_at=? WHERE owner_id=? AND id=?`, nullableTime(a.ArchivedAt), nullableTime(a.TrashedAt), a.Revision, a.UpdatedAt, owner.id, a.ID); err != nil {
					return MutationResult{}, err
				}
			}
		}
	default:
		return MutationResult{}, ErrConflict
	}
	if contentChanged && len(thumbnailChanges) == 1 && thumbnailChanges[0].Replace {
		if err := replaceThumbnail(ctx, tx, owner, &a, thumbnailChanges[0]); err != nil {
			return MutationResult{}, err
		}
	}
	out := MutationResult{Article: a, Created: created}
	result, err := json.Marshal(out)
	if err != nil {
		return MutationResult{}, err
	}
	hash := sha256.Sum256(payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_results(owner_id,key,operation,payload_hash,result,created_at)
  VALUES (?,?,?,?,?,?)`, owner.id, key, operation, hash[:], result, now); err != nil {
		return MutationResult{}, err
	}
	return out, nil
}

// replaceTags makes a's tags the article's complete tag set. Callers run it in
// the transaction that writes the article and its retry result.
func replaceTags(ctx context.Context, tx *sql.Tx, owner Owner, a Article) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM article_tags WHERE owner_id=? AND article_id=?", owner.id, a.ID); err != nil {
		return err
	}
	for _, tag := range a.Tags {
		if _, err := tx.ExecContext(ctx, "INSERT INTO article_tags(owner_id,article_id,tag) VALUES (?,?,?)", owner.id, a.ID, tag); err != nil {
			return err
		}
	}
	return nil
}

func nullableTime(timestamp int64) any {
	if timestamp == 0 {
		return nil
	}
	return timestamp
}

// SetArchived retains the browser's existing CSRF form contract and does not
// create retry records. MCP state mutations use an explicit revision and key.
func (s *Store) SetArchived(ctx context.Context, owner Owner, id string, archived bool) (Article, error) {
	if owner.id == "" {
		return Article{}, ErrOwnerRequired
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return Article{}, err
	}
	defer tx.Rollback()
	a, err := scanArticle(tx.QueryRowContext(ctx, "SELECT "+articleColumns+articleFrom+`
 WHERE a.owner_id=? AND a.id=? AND a.trashed_at IS NULL
 AND a.owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, owner.id, id))
	if err != nil {
		return Article{}, err
	}
	if (a.ArchivedAt != 0) == archived {
		return a, nil
	}
	a.UpdatedAt = time.Now().UnixMilli()
	a.Revision++
	a.ArchivedAt = 0
	if archived {
		a.ArchivedAt = a.UpdatedAt
	}
	if _, err := tx.ExecContext(ctx, `UPDATE articles SET archived_at=?, revision=?, updated_at=? WHERE owner_id=? AND id=?`, nullableTime(a.ArchivedAt), a.Revision, a.UpdatedAt, owner.id, id); err != nil {
		return Article{}, err
	}
	if err := tx.Commit(); err != nil {
		return Article{}, err
	}
	return a, nil
}
