package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func (s *Store) LookupAcceptance(ctx context.Context, sourceID domain.ID, keyHash, fingerprint string) (ports.Acceptance, error) {
	now := s.now()
	if !validTime(now) || !validID(string(sourceID)) || !validDigest(keyHash) || !validDigest(fingerprint) {
		return ports.Acceptance{}, ErrInvalid
	}
	var accepted ports.Acceptance
	var storedFingerprint string
	var ids []byte
	err := s.db.QueryRowContext(ctx, `SELECT event_id, delivery_ids, fingerprint, mode FROM deduplication WHERE source_id = ? AND key_hash = ? AND expires_at > ?`, sourceID, keyHash, now.UnixNano()).Scan(&accepted.EventID, &ids, &storedFingerprint, &accepted.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Acceptance{}, ports.ErrNotFound
	}
	if err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	if storedFingerprint != fingerprint {
		return ports.Acceptance{}, ports.ErrDedupConflict
	}
	if err := json.Unmarshal(ids, &accepted.DeliveryIDs); err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	accepted.Duplicate = true
	return accepted, nil
}

func (s *Store) RecordImmediate(ctx context.Context, result ports.ImmediateResult) error {
	if !validID(string(result.DeliveryID)) || result.StatusCode != 0 && (result.StatusCode < 100 || result.StatusCode > 599) {
		return ErrInvalid
	}
	if result.State == domain.DeliveryHTTPAccepted {
		if result.StatusCode < 200 || result.StatusCode > 299 || result.ErrorCode != "" {
			return ErrInvalid
		}
	} else if result.State != domain.DeliveryFailed || !validErrorCode(result.ErrorCode) {
		return ErrInvalid
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE deliveries SET state = ?, last_error_code = ?, http_status_code = ?, attempt = 1 WHERE id = ? AND ttl = 0 AND state = 'unknown'`, result.State, result.ErrorCode, result.StatusCode, result.DeliveryID)
	if err != nil {
		return dbError(err)
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return dbError(err)
	}
	if n != 1 {
		return ports.ErrNotFound
	}
	return nil
}
