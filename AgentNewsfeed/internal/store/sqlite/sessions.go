package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session carries encrypted OAuth state. Only the identity adapter knows its
// contents; the database stores hashes of the browser credential and CSRF token.
type Session struct {
	TokenHash      [32]byte
	Owner          Owner
	ClientID       string
	EncryptedState []byte
	CSRFHash       [32]byte
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

func (s *Store) SaveLoginAttempt(ctx context.Context, tokenHash [32]byte, encrypted []byte, expires time.Time) error {
	if len(encrypted) == 0 || len(encrypted) > 64<<10 || !expires.After(time.Now()) || expires.After(time.Now().Add(11*time.Minute)) {
		return fmt.Errorf("login attempt requires bounded encrypted state and a short expiry")
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM login_attempts WHERE expires_at <= ?", time.Now().UnixMilli()); err != nil {
		return err
	}
	var attempts int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM login_attempts").Scan(&attempts); err != nil {
		return err
	}
	if attempts >= 1024 {
		return fmt.Errorf("too many outstanding sign-in attempts")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO login_attempts(token_hash, encrypted_state, expires_at) VALUES (?, ?, ?)", tokenHash[:], encrypted, expires.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// ConsumeLoginAttempt deletes and returns one unexpired attempt atomically.
// No provider request runs while the SQLite statement or transaction is open.
func (s *Store) ConsumeLoginAttempt(ctx context.Context, tokenHash [32]byte, now time.Time) ([]byte, error) {
	var encrypted []byte
	err := s.write.QueryRowContext(ctx, "DELETE FROM login_attempts WHERE token_hash = ? AND expires_at > ? RETURNING encrypted_state", tokenHash[:], now.UnixMilli()).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return encrypted, err
}

func (s *Store) CreateSession(ctx context.Context, session Session) error {
	if session.Owner.id == "" {
		return ErrOwnerRequired
	}
	if session.ClientID == "" || len(session.ClientID) > 256 || len(session.EncryptedState) == 0 || len(session.EncryptedState) > 64<<10 || !session.ExpiresAt.After(session.CreatedAt) {
		return fmt.Errorf("session requires a client, bounded encrypted state and expiry")
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM web_sessions WHERE expires_at <= ? OR revoked_at IS NOT NULL", time.Now().UnixMilli()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO web_sessions(token_hash, owner_id, client_id, encrypted_state, csrf_hash, created_at, expires_at)
		SELECT ?, id, ?, ?, ?, ?, ? FROM accounts WHERE id = ? AND disabled_at IS NULL`, session.TokenHash[:], session.ClientID, session.EncryptedState, session.CSRFHash[:], session.CreatedAt.UnixMilli(), session.ExpiresAt.UnixMilli(), session.Owner.id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrOwnerRequired
	}
	return tx.Commit()
}

func (s *Store) Session(ctx context.Context, tokenHash [32]byte, now time.Time) (Session, error) {
	session := Session{TokenHash: tokenHash}
	var csrfHash []byte
	var created, expires int64
	err := s.read.QueryRowContext(ctx, `SELECT s.owner_id, s.client_id, s.encrypted_state, s.csrf_hash, s.created_at, s.expires_at
		FROM web_sessions s JOIN accounts a ON a.id = s.owner_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ? AND a.disabled_at IS NULL`, tokenHash[:], now.UnixMilli()).
		Scan(&session.Owner.id, &session.ClientID, &session.EncryptedState, &csrfHash, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	copy(session.CSRFHash[:], csrfHash)
	session.CreatedAt, session.ExpiresAt = time.UnixMilli(created), time.UnixMilli(expires)
	return session, nil
}

func (s *Store) UpdateSession(ctx context.Context, tokenHash [32]byte, encrypted []byte, now time.Time) error {
	if len(encrypted) == 0 || len(encrypted) > 64<<10 {
		return fmt.Errorf("session state must be bounded ciphertext")
	}
	result, err := s.write.ExecContext(ctx, `UPDATE web_sessions SET encrypted_state = ?
		WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?
		AND owner_id IN (SELECT id FROM accounts WHERE disabled_at IS NULL)`, encrypted, tokenHash[:], now.UnixMilli())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed != 1 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash [32]byte, now time.Time) error {
	_, err := s.write.ExecContext(ctx, "UPDATE web_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE token_hash = ?", now.UnixMilli(), tokenHash[:])
	return err
}

// DisableAccount is a local operator action. Tokens stop resolving immediately;
// sessions are revoked in the same transaction. Domain records stay intact.
func (s *Store) DisableAccount(ctx context.Context, identity ExternalIdentity) error {
	if err := identity.validate(); err != nil {
		return err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err := tx.QueryRowContext(ctx, "SELECT owner_id FROM identities WHERE issuer = ? AND subject = ?", identity.Issuer, identity.Subject).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, "UPDATE accounts SET disabled_at = COALESCE(disabled_at, ?) WHERE id = ?", now, owner); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE web_sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE owner_id = ?", now, owner); err != nil {
		return err
	}
	return tx.Commit()
}
