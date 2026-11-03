package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
)

const ThumbnailBudgetBytes = 50 << 20
const MaxThumbnailBytes = 100 << 10

// ThumbnailChange carries prepared bytes only between the fetcher and writer.
// It is never part of an article, MCP result, or durable retry payload.
type ThumbnailChange struct {
	Replace bool
	JPEG    []byte `json:"-"`
	Status  string
}

func replaceThumbnail(ctx context.Context, tx *sql.Tx, owner Owner, a *Article, change ThumbnailChange) error {
	var width, height int
	var hash string
	if len(change.JPEG) != 0 {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(change.JPEG))
		if err != nil || len(change.JPEG) > MaxThumbnailBytes || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 640 || cfg.Height > 640 || a.ThumbnailURL == "" {
			return fmt.Errorf("invalid prepared thumbnail")
		}
		width, height = cfg.Width, cfg.Height
		sum := sha256.Sum256(change.JPEG)
		hash = hex.EncodeToString(sum[:])
	} else if change.Status != "" && change.Status != "unavailable" {
		return fmt.Errorf("invalid thumbnail outcome")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM article_thumbnails WHERE owner_id=? AND article_id=?", owner.id, a.ID); err != nil {
		return err
	}
	// Release the previous payload before accounting for a replacement. Shared
	// references, including Trash and Archive, keep their payload counted.
	if _, err := tx.ExecContext(ctx, `DELETE FROM thumbnail_blobs WHERE owner_id=?
 AND NOT EXISTS(SELECT 1 FROM article_thumbnails t WHERE t.owner_id=thumbnail_blobs.owner_id AND t.hash=thumbnail_blobs.hash)`, owner.id); err != nil {
		return err
	}
	a.ThumbnailHash, a.ThumbnailWidth, a.ThumbnailHeight, a.ThumbnailBytes = "", 0, 0, 0
	a.ThumbnailStatus = change.Status
	if hash != "" {
		var exists bool
		var used int64
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM thumbnail_blobs WHERE owner_id=? AND hash=?), COALESCE(sum(length(jpeg)),0) FROM thumbnail_blobs WHERE owner_id=?", owner.id, hash, owner.id).Scan(&exists, &used); err != nil {
			return err
		}
		if !exists && used+int64(len(change.JPEG)) > ThumbnailBudgetBytes {
			a.ThumbnailStatus = "budget_exceeded"
		} else {
			if _, err := tx.ExecContext(ctx, "INSERT INTO thumbnail_blobs(owner_id,hash,jpeg,width,height) VALUES (?,?,?,?,?) ON CONFLICT(owner_id,hash) DO NOTHING", owner.id, hash, change.JPEG, width, height); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO article_thumbnails(owner_id,article_id,hash) VALUES (?,?,?)", owner.id, a.ID, hash); err != nil {
				return err
			}
			a.ThumbnailHash, a.ThumbnailWidth, a.ThumbnailHeight, a.ThumbnailBytes = hash, width, height, len(change.JPEG)
			a.ThumbnailStatus = "stored"
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE articles SET thumbnail_status=? WHERE owner_id=? AND id=?", a.ThumbnailStatus, owner.id, a.ID)
	return err
}

// Thumbnail selects only the requested owner's saved bytes. Trash must be
// explicitly selected, just like full article reads; there is no URL fallback.
func (s *Store) Thumbnail(ctx context.Context, owner Owner, id string, trashed bool) ([]byte, error) {
	if owner.id == "" {
		return nil, ErrOwnerRequired
	}
	var body []byte
	err := s.read.QueryRowContext(ctx, `SELECT b.jpeg`+articleFrom+`
 WHERE a.owner_id=? AND a.id=? AND (a.trashed_at IS NOT NULL)=?
 AND a.owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, owner.id, id, trashed).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(body) == 0) {
		return nil, ErrNotFound
	}
	return body, err
}
