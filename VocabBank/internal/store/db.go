package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = file.Chmod(0600); err != nil {
			file.Close()
			return nil, err
		}
		file.Close()
	}
	dsn := "file::memory:?mode=memory&cache=private"
	if path != ":memory:" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		dsn = (&url.URL{Scheme: "file", Path: absolute}).String() + "?"
	}
	separator := "&"
	if path != ":memory:" {
		separator = ""
	}
	dsn += separator + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return s, nil
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Enroll(ctx context.Context, issuer, subject, label string) (Owner, error) {
	if !bounded(issuer, 1, 2048) || !bounded(subject, 1, 255) || !bounded(label, 1, 100) {
		return Owner{}, ErrInvalid
	}
	o := Owner{ID: ID(), Issuer: issuer, Subject: subject, Label: label}
	_, err := s.db.ExecContext(ctx, `INSERT INTO owners(id,issuer,subject,label)VALUES(?,?,?,?) ON CONFLICT(issuer,subject) DO UPDATE SET label=excluded.label,enabled=1`, o.ID, issuer, subject, label)
	if err != nil {
		return Owner{}, err
	}
	return s.Owner(ctx, issuer, subject)
}
func (s *Store) Owner(ctx context.Context, issuer, subject string) (Owner, error) {
	var o Owner
	err := s.db.QueryRowContext(ctx, `SELECT id,issuer,subject,label FROM owners WHERE issuer=? AND subject=? AND enabled=1`, issuer, subject).Scan(&o.ID, &o.Issuer, &o.Subject, &o.Label)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return o, err
}
func (s *Store) SaveSession(ctx context.Context, v Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(hash,owner,host,cipher,expires_at)VALUES(?,?,?,?,?) ON CONFLICT(hash) DO UPDATE SET cipher=excluded.cipher,expires_at=excluded.expires_at`, v.Hash, v.Owner, v.Host, v.Cipher, v.ExpiresAt.Unix())
	return err
}
func (s *Store) Session(ctx context.Context, hash []byte, host string, now time.Time) (Session, error) {
	v := Session{Hash: hash}
	var expiry int64
	err := s.db.QueryRowContext(ctx, `SELECT s.owner,s.host,s.cipher,s.expires_at FROM sessions s JOIN owners o ON o.id=s.owner WHERE s.hash=? AND s.host=? AND s.expires_at>? AND o.enabled=1`, hash, host, now.Unix()).Scan(&v.Owner, &v.Host, &v.Cipher, &expiry)
	v.ExpiresAt = time.Unix(expiry, 0)
	return v, err
}
func (s *Store) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash=?`, hash)
	return err
}
func (s *Store) SaveLogin(ctx context.Context, hash []byte, host string, cipher []byte, expiry time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM logins WHERE expires_at<=?`, s.now().Unix()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO logins(hash,host,cipher,expires_at)VALUES(?,?,?,?)`, hash, host, cipher, expiry.Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ConsumeLogin(ctx context.Context, hash []byte, host string, now time.Time) ([]byte, error) {
	var cipher []byte
	err := s.db.QueryRowContext(ctx, `DELETE FROM logins WHERE hash=? AND host=? AND expires_at>? RETURNING cipher`, hash, host, now.Unix()).Scan(&cipher)
	return cipher, err
}
