package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Owner can only be obtained by resolving an explicitly enrolled identity.
// No browser field, MCP argument or trusted-proxy header can construct one.
type Owner struct{ id string }

func (o Owner) ID() string { return o.id }

type ExternalIdentity struct{ Issuer, Subject string }

func (identity ExternalIdentity) validate() error {
	u, err := url.Parse(identity.Issuer)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("identity issuer must be an exact HTTPS issuer")
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return fmt.Errorf("identity issuer requires HTTPS")
	}
	if len(identity.Subject) < 1 || len(identity.Subject) > 255 || strings.TrimSpace(identity.Subject) != identity.Subject || strings.ContainsFunc(identity.Subject, unicode.IsControl) {
		return fmt.Errorf("subject must be an exact bounded identifier")
	}
	return nil
}

// EnrollAccount is an operator-only action. Authentication never calls it.
func (s *Store) EnrollAccount(ctx context.Context, identity ExternalIdentity) (Owner, error) {
	if err := identity.validate(); err != nil {
		return Owner{}, err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return Owner{}, err
	}
	defer tx.Rollback()
	owner := Owner{id: rand.Text()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO accounts(id, created_at) VALUES (?, ?)", owner.id, time.Now().UnixMilli()); err != nil {
		return Owner{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO identities(issuer, subject, owner_id) VALUES (?, ?, ?)", identity.Issuer, identity.Subject, owner.id); err != nil {
		return Owner{}, fmt.Errorf("identity already enrolled or unavailable")
	}
	if err := tx.Commit(); err != nil {
		return Owner{}, err
	}
	return owner, nil
}

func (s *Store) OwnerForIdentity(ctx context.Context, identity ExternalIdentity) (Owner, error) {
	if err := identity.validate(); err != nil {
		return Owner{}, err
	}
	var owner Owner
	err := s.read.QueryRowContext(ctx, `SELECT i.owner_id FROM identities i JOIN accounts a ON a.id = i.owner_id
  WHERE i.issuer = ? AND i.subject = ? AND a.disabled_at IS NULL`, identity.Issuer, identity.Subject).Scan(&owner.id)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	return owner, err
}
