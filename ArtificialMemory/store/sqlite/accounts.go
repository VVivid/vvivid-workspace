package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/account"
)

const accountSchema = `
CREATE TABLE accounts (id TEXT PRIMARY KEY, created_at TEXT NOT NULL);
CREATE TABLE enrolled_identities (
 issuer TEXT NOT NULL, subject TEXT NOT NULL, account_id TEXT NOT NULL UNIQUE REFERENCES accounts(id),
 active INTEGER NOT NULL CHECK(active IN (0,1)), PRIMARY KEY(issuer,subject)
);
CREATE TRIGGER accounts_no_update BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT,'stable account identity'); END;
CREATE TRIGGER accounts_no_delete BEFORE DELETE ON accounts BEGIN SELECT RAISE(ABORT,'stable account identity'); END;
CREATE TRIGGER enrollment_no_move BEFORE UPDATE ON enrolled_identities
 WHEN NEW.issuer!=OLD.issuer OR NEW.subject!=OLD.subject OR NEW.account_id!=OLD.account_id
 BEGIN SELECT RAISE(ABORT,'stable account identity'); END;
CREATE TRIGGER enrollment_no_delete BEFORE DELETE ON enrolled_identities BEGIN SELECT RAISE(ABORT,'disable enrollment instead'); END;
`

// EnrollAccount explicitly activates an identity. Re-enrollment preserves its ID
// and therefore its journal, even after an operator previously disabled it.
func (s *Store) EnrollAccount(ctx context.Context, id account.Identity) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var accountID string
	err = tx.QueryRowContext(ctx, "SELECT account_id FROM enrolled_identities WHERE issuer=? AND subject=?", id.Issuer, id.Subject).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		accountID = "account_" + newID()
		if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,created_at) VALUES(?,?)", accountID, formatInstant(time.Now())); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO enrolled_identities(issuer,subject,account_id,active) VALUES(?,?,?,1)", id.Issuer, id.Subject, accountID); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if _, err := tx.ExecContext(ctx, "UPDATE enrolled_identities SET active=1 WHERE issuer=? AND subject=?", id.Issuer, id.Subject); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return accountID, nil
}

// DisableAccount revokes the local mapping for subsequent HTTP requests. It
// does not delete memories or cancel a request already authorized in flight.
func (s *Store) DisableAccount(ctx context.Context, id account.Identity) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE enrolled_identities SET active=0 WHERE issuer=? AND subject=?", id.Issuer, id.Subject)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n != 0, err
}

// ResolveAccount never enrolls an unknown identity. Call it for every request,
// rather than caching local authorization in an MCP transport session.
func (s *Store) ResolveAccount(ctx context.Context, id account.Identity) (string, bool, error) {
	if err := id.Validate(); err != nil {
		return "", false, err
	}
	var accountID string
	err := s.db.QueryRowContext(ctx, "SELECT account_id FROM enrolled_identities WHERE issuer=? AND subject=? AND active=1", id.Issuer, id.Subject).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return accountID, err == nil, err
}

// Ready checks migrated storage without appending a probe record.
func (s *Store) Ready(ctx context.Context) error {
	var version, keySize int
	if err := s.db.QueryRowContext(ctx, "SELECT (SELECT user_version FROM pragma_user_version), length(cursor_key) FROM settings WHERE id=1").Scan(&version, &keySize); err != nil {
		return err
	}
	if version != 2 || keySize != 32 {
		return fmt.Errorf("journal not ready")
	}
	return nil
}
