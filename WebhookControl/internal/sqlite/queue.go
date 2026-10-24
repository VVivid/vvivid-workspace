package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func (s *Store) Claim(ctx context.Context, now time.Time, leaseFor time.Duration) (ports.Lease, error) {
	until, err := addTime(now, leaseFor)
	if err != nil {
		return ports.Lease{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.Lease{}, dbError(err)
	}
	defer tx.Rollback()
	if err := expire(ctx, tx, now); err != nil {
		return ports.Lease{}, err
	}
	var id domain.ID
	var expiry sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id, expires_at FROM deliveries
 WHERE ((state = 'pending' AND next_attempt_at <= ?) OR (state = 'leased' AND lease_until <= ?))
 AND (expires_at IS NULL OR expires_at > ?) AND attempt < ?
 ORDER BY next_attempt_at, created_at, id LIMIT 1`, now.UnixNano(), now.UnixNano(), now.UnixNano(), int64(math.MaxInt64)).Scan(&id, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return ports.Lease{}, dbError(err)
		}
		return ports.Lease{}, ports.ErrNoReadyDelivery
	}
	if err != nil {
		return ports.Lease{}, dbError(err)
	}
	if expiry.Valid && expiry.Int64 < until.UnixNano() {
		until = time.Unix(0, expiry.Int64).UTC()
	}
	owner := ports.LeaseToken{DeliveryID: id, Token: rand.Text()}
	_, err = tx.ExecContext(ctx, "UPDATE deliveries SET state = 'leased', attempt = attempt + 1, first_attempt_at = coalesce(first_attempt_at, ?), lease_token = ?, lease_until = ? WHERE id = ?", now.UnixNano(), owner.Token, until.UnixNano(), id)
	if err != nil {
		return ports.Lease{}, dbError(err)
	}
	delivery, err := s.readDelivery(ctx, tx, id, now)
	if err != nil {
		return ports.Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.Lease{}, dbError(err)
	}
	return ports.Lease{Delivery: delivery, Owner: owner, Until: until}, nil
}

// Every transition predicates on the token, lease deadline and payload deadline
// in the same SQL statement. A worker cannot update after recovery or expiry.
const ownsLease = `id = ? AND state = 'leased' AND lease_token = ? AND lease_until > ? AND (expires_at IS NULL OR expires_at > ?)`

func (s *Store) Renew(ctx context.Context, owner ports.LeaseToken, now time.Time, leaseFor time.Duration) (time.Time, error) {
	until, err := addTime(now, leaseFor)
	if err != nil {
		return time.Time{}, err
	}
	if !validOwner(owner) {
		return time.Time{}, ports.ErrLeaseLost
	}
	var renewed int64
	err = s.db.QueryRowContext(ctx, `UPDATE deliveries SET lease_until = min(?, coalesce(expires_at, ?)) WHERE `+ownsLease+` RETURNING lease_until`, until.UnixNano(), until.UnixNano(), owner.DeliveryID, owner.Token, now.UnixNano(), now.UnixNano()).Scan(&renewed)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ports.ErrLeaseLost
	}
	if err != nil {
		return time.Time{}, dbError(err)
	}
	return time.Unix(0, renewed).UTC(), nil
}

func (s *Store) Complete(ctx context.Context, owner ports.LeaseToken, now time.Time, statusCode int) error {
	if !validTime(now) || statusCode < 200 || statusCode > 299 {
		return ErrInvalid
	}
	if !validOwner(owner) {
		return ports.ErrLeaseLost
	}
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET state = 'http_accepted', http_status_code = ?, last_error_code = '', lease_token = NULL, lease_until = NULL WHERE `+ownsLease, statusCode, owner.DeliveryID, owner.Token, now.UnixNano(), now.UnixNano())
	return leaseResult(result, err)
}

func (s *Store) Retry(ctx context.Context, owner ports.LeaseToken, now, next time.Time, errorCode string) error {
	if !validTime(now) || !validTime(next) || !next.After(now) || !validErrorCode(errorCode) {
		return ErrInvalid
	}
	if !validOwner(owner) {
		return ports.ErrLeaseLost
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	var expiry sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT expires_at FROM deliveries WHERE "+ownsLease, owner.DeliveryID, owner.Token, now.UnixNano(), now.UnixNano()).Scan(&expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrLeaseLost
	}
	if err != nil {
		return dbError(err)
	}
	if expiry.Valid && next.UnixNano() >= expiry.Int64 {
		return invalid("retry at or after payload expiry")
	}
	_, err = tx.ExecContext(ctx, "UPDATE deliveries SET state = 'pending', next_attempt_at = ?, last_error_code = ?, lease_token = NULL, lease_until = NULL WHERE id = ?", next.UnixNano(), errorCode, owner.DeliveryID)
	if err != nil {
		return dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	s.signal()
	return nil
}

func (s *Store) Terminate(ctx context.Context, owner ports.LeaseToken, now time.Time, errorCode string) error {
	if !validTime(now) || !validErrorCode(errorCode) {
		return ErrInvalid
	}
	if !validOwner(owner) {
		return ports.ErrLeaseLost
	}
	result, err := s.db.ExecContext(ctx, `UPDATE deliveries SET state = 'failed', last_error_code = ?, lease_token = NULL, lease_until = NULL WHERE `+ownsLease, errorCode, owner.DeliveryID, owner.Token, now.UnixNano(), now.UnixNano())
	return leaseResult(result, err)
}

func validOwner(owner ports.LeaseToken) bool {
	return validID(string(owner.DeliveryID)) && owner.Token != ""
}

// Only fixed machine labels can enter metadata. Accepting arbitrary strings,
// even strings without spaces, could persist a receiver body or secret.
func validErrorCode(code string) bool {
	switch code {
	case "network_error", "timeout", "http_retryable", "http_permanent", "policy_denied", "secret_unavailable", "invalid_payload", "retries_exhausted", "retry_window_exhausted", "canceled":
		return true
	default:
		return false
	}
}

func leaseResult(result sql.Result, err error) error {
	if err != nil {
		return dbError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return dbError(err)
	}
	if count != 1 {
		return ports.ErrLeaseLost
	}
	return nil
}
