package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const ShareLifetime = 6 * time.Hour

var ErrShareLimit = errors.New("temporary sharing limit reached")

// ShareSelection pins both the requested quantity and the catalog facts shown
// during selection. A concurrent product edit need not change the list revision.
type ShareSelection struct {
	ItemID                  string `json:"item_id"`
	ExpectedRevision        int64  `json:"expected_revision"`
	ExpectedProductRevision int64  `json:"expected_product_revision"`
}

type ShareAudit struct {
	Count     int   `json:"item_count"`
	ExpiresAt int64 `json:"expires_at"`
}

type CreatedShare struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expires_at"`
}

// SharedItem deliberately contains no personal product/list IDs, photos, source
// URLs, prices, owner identity or purchase data. Position identifies this copy.
type SharedItem struct {
	Position     int     `json:"position"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	PackageCount *int64  `json:"package_count"`
	PackageSize  *string `json:"package_size"`
	PackageUnit  *string `json:"package_unit"`
	Quantity     string  `json:"quantity"`
	Retailer     *string `json:"retailer"`
	PickedUp     bool    `json:"picked_up"`
	Revision     int64   `json:"revision"`
}

type PublicShare struct {
	Retailer   *string      `json:"retailer"`
	ExpiresAt  int64        `json:"expires_at"`
	Items      []SharedItem `json:"items"`
	WriteToken string       `json:"-"`
}

type SharedItemInput struct {
	Position         int   `json:"position"`
	PickedUp         bool  `json:"picked_up"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func ValidShareCode(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// CreateShare is called only inside the ordinary owner mutation transaction.
// Generate inside that callback so idempotent replays cannot reserve another code.
func (tx *Tx) CreateShare(listID string, expected int64, selected []ShareSelection, generate func() (string, error)) (CreatedShare, error) {
	list, err := readList(tx.ctx, tx.sql, tx.owner, listID)
	if err != nil {
		return CreatedShare{}, err
	}
	if list.Revision != expected || list.Status != "active" {
		return CreatedShare{}, ErrConflict
	}
	// Bound public capability creation as well as the public request surface.
	var recentShares int
	if err := tx.sql.QueryRowContext(tx.ctx, `SELECT count(*) FROM temporary_shares WHERE owner_id=? AND created_at>?`, tx.owner.id, tx.now-time.Hour.Milliseconds()).Scan(&recentShares); err != nil {
		return CreatedShare{}, err
	}
	if recentShares >= 20 {
		return CreatedShare{}, ErrShareLimit
	}
	items := make([]SharedItem, 0, len(selected))
	for i, selection := range selected {
		var item SharedItem
		var itemRevision, productRevision int64
		err := tx.sql.QueryRowContext(tx.ctx, `SELECT p.name,p.category,p.package_count,p.package_size,p.package_unit,i.quantity,i.retailer,i.revision,p.revision
			FROM list_items i JOIN products p ON p.owner_id=i.owner_id AND p.id=i.product_id
			WHERE i.owner_id=? AND i.list_id=? AND i.id=? AND i.removed_at IS NULL`, tx.owner.id, listID, selection.ItemID).
			Scan(&item.Name, &item.Category, &item.PackageCount, &item.PackageSize, &item.PackageUnit, &item.Quantity, &item.Retailer, &itemRevision, &productRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return CreatedShare{}, ErrNotFound
		}
		if err != nil {
			return CreatedShare{}, err
		}
		if itemRevision != selection.ExpectedRevision || productRevision != selection.ExpectedProductRevision {
			return CreatedShare{}, ErrConflict
		}
		item.Position, item.Revision = i+1, 1
		items = append(items, item)
	}
	shareID := rand.Text()
	expiresAt := tx.now + ShareLifetime.Milliseconds()
	for range 16 {
		code, err := generate()
		if err != nil {
			return CreatedShare{}, err
		}
		if !ValidShareCode(code) {
			return CreatedShare{}, fmt.Errorf("invalid generated share code")
		}
		codeHash := sha256.Sum256([]byte(code))
		result, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO temporary_shares(code_hash,owner_id,id,retailer,write_token,created_at,expires_at)
			VALUES (?,?,?,?,?,?,?) ON CONFLICT(code_hash) DO NOTHING`, codeHash[:], tx.owner.id, shareID, list.Retailer, rand.Text(), tx.now, expiresAt)
		if err != nil {
			return CreatedShare{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return CreatedShare{}, err
		}
		if count == 0 {
			continue
		}
		for _, item := range items {
			if _, err := tx.sql.ExecContext(tx.ctx, `INSERT INTO shared_items(share_id,position,name,category,package_count,package_size,package_unit,quantity,retailer)
				VALUES (?,?,?,?,?,?,?,?,?)`, shareID, item.Position, item.Name, item.Category, item.PackageCount, item.PackageSize, item.PackageUnit, item.Quantity, item.Retailer); err != nil {
				return CreatedShare{}, err
			}
		}
		// Keep capability tokens out of activity/audit; only the private share ID,
		// count and lifetime are needed to explain this explicit disclosure.
		audit := ShareAudit{Count: len(items), ExpiresAt: expiresAt}
		if err := tx.RecordChange("temporary_share", shareID, nil, audit); err != nil {
			return CreatedShare{}, err
		}
		return CreatedShare{Code: code, ExpiresAt: expiresAt}, nil
	}
	return CreatedShare{}, fmt.Errorf("could not reserve a temporary share code")
}

// readAvailableShare checks the public code, lifetime and enabled owner before
// returning metadata or the separate guest write capability.
func readAvailableShare(ctx context.Context, q queryer, code string, now int64) (shareID string, share PublicShare, err error) {
	if !ValidShareCode(code) {
		return "", PublicShare{}, ErrNotFound
	}
	codeHash := sha256.Sum256([]byte(code))
	err = q.QueryRowContext(ctx, `SELECT s.id,s.retailer,s.expires_at,s.write_token FROM temporary_shares s JOIN accounts a ON a.id=s.owner_id
		WHERE s.code_hash=? AND s.expires_at>? AND a.disabled_at IS NULL`, codeHash[:], now).
		Scan(&shareID, &share.Retailer, &share.ExpiresAt, &share.WriteToken)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

const sharedItemColumns = `position,name,category,package_count,package_size,package_unit,quantity,retailer,picked_up,revision`

type rowScanner interface{ Scan(...any) error }

func scanSharedItem(row rowScanner) (item SharedItem, err error) {
	err = row.Scan(&item.Position, &item.Name, &item.Category, &item.PackageCount, &item.PackageSize, &item.PackageUnit, &item.Quantity, &item.Retailer, &item.PickedUp, &item.Revision)
	return
}

func (s *Store) PublicShare(ctx context.Context, code string) (PublicShare, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PublicShare{}, err
	}
	defer tx.Rollback()
	shareID, share, err := readAvailableShare(ctx, tx, code, s.shareNow().UnixMilli())
	if err != nil {
		return PublicShare{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+sharedItemColumns+` FROM shared_items WHERE share_id=? ORDER BY position`, shareID)
	if err != nil {
		return PublicShare{}, err
	}
	share.Items = []SharedItem{}
	for rows.Next() {
		item, err := scanSharedItem(rows)
		if err != nil {
			_ = rows.Close()
			return PublicShare{}, err
		}
		share.Items = append(share.Items, item)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return PublicShare{}, err
	}
	if s.shareNow().UnixMilli() >= share.ExpiresAt {
		return PublicShare{}, ErrNotFound
	}
	return share, tx.Commit()
}

// SetSharedItem has a separate capability/replay namespace. Every attempt,
// including a replay, rechecks expiry and enabled-owner status before any data.
func (s *Store) SetSharedItem(ctx context.Context, code, token, key, requestID string, input SharedItemInput) (MutationResult, error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, err
	}
	defer tx.Rollback()
	shareID, share, err := readAvailableShare(ctx, tx, code, s.shareNow().UnixMilli())
	if err != nil {
		return MutationResult{}, err
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(share.WriteToken)) != 1 {
		return MutationResult{}, ErrNotFound
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return MutationResult{}, err
	}
	payloadHash := sha256.Sum256(payload)
	var previousPayloadHash, resultJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,result FROM shared_item_results WHERE share_id=? AND key=?`, shareID, key).Scan(&previousPayloadHash, &resultJSON)
	if err == nil {
		if !bytes.Equal(previousPayloadHash, payloadHash[:]) {
			return MutationResult{}, ErrIdempotencyConflict
		}
		if s.shareNow().UnixMilli() >= share.ExpiresAt {
			return MutationResult{}, ErrNotFound
		}
		return MutationResult{Data: resultJSON, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MutationResult{}, err
	}
	item, err := scanSharedItem(tx.QueryRowContext(ctx, `SELECT `+sharedItemColumns+` FROM shared_items WHERE share_id=? AND position=?`, shareID, input.Position))
	if errors.Is(err, sql.ErrNoRows) {
		return MutationResult{}, ErrNotFound
	}
	if err != nil {
		return MutationResult{}, err
	}
	if item.Revision != input.ExpectedRevision {
		return MutationResult{}, ErrConflict
	}
	beforePickedUp := item.PickedUp
	item.PickedUp, item.Revision = input.PickedUp, item.Revision+1
	if _, err := tx.ExecContext(ctx, `UPDATE shared_items SET picked_up=?,revision=? WHERE share_id=? AND position=?`, item.PickedUp, item.Revision, shareID, item.Position); err != nil {
		return MutationResult{}, err
	}
	// The guest state, exact retry result and anonymous event share one commit.
	resultJSON, err = json.Marshal(item)
	if err != nil {
		return MutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO shared_item_results(share_id,key,payload_hash,result) VALUES (?,?,?,?)`, shareID, key, payloadHash[:], resultJSON); err != nil {
		return MutationResult{}, err
	}
	now := s.shareNow().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO shared_item_events(share_id,id,position,before_picked_up,after_picked_up,revision,request_id,occurred_at) VALUES (?,?,?,?,?,?,?,?)`, shareID, rand.Text(), item.Position, beforePickedUp, item.PickedUp, item.Revision, requestID, now); err != nil {
		return MutationResult{}, err
	}
	if s.shareNow().UnixMilli() >= share.ExpiresAt {
		return MutationResult{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{Data: resultJSON}, nil
}
