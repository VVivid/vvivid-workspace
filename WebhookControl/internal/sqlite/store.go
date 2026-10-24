// Package sqlite implements atomic storage and a durable, fenced delivery queue.
package sqlite

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
	_ "modernc.org/sqlite"
)

const DefaultRetention = 7 * 24 * time.Hour

var (
	ErrInvalid    = errors.New("invalid storage input")
	ErrCiphertext = errors.New("payload authentication failed")
)

type options struct {
	now               func() time.Time
	metadataRetention time.Duration
	dedupRetention    time.Duration
	maxPages          int64
}

type Option func(*options) error

// WithMaxPageCount limits logical database growth without filling the host disk.
// WAL and backup files require additional space outside this SQLite page limit.
func WithMaxPageCount(pages int64) Option {
	return func(o *options) error {
		if pages < 0 || pages > 4294967294 {
			return ErrInvalid
		}
		o.maxPages = pages
		return nil
	}
}

// WithClock supplies the clock used by acceptance and payload reads. Queue
// operations take an explicit time so a worker can use its own injected clock.
func WithClock(now func() time.Time) Option {
	return func(o *options) error {
		if now == nil {
			return ErrInvalid
		}
		o.now = now
		return nil
	}
}

// WithRetention sets independent, finite windows for minimal status metadata
// and deduplication. Payload lifetimes always come from the accepted TTLs.
func WithRetention(metadata, dedup time.Duration) Option {
	return func(o *options) error {
		if metadata <= 0 || dedup <= 0 {
			return ErrInvalid
		}
		o.metadataRetention, o.dedupRetention = metadata, dedup
		return nil
	}
}

type Store struct {
	db   *sql.DB
	path string
	aead cipher.AEAD
	options
	wakeups       chan struct{}
	managementMAC []byte
}

var _ ports.Storage = (*Store)(nil)
var _ ports.Queue = (*Store)(nil)
var _ ports.ReceiptReader = (*Store)(nil)
var _ ports.ImmediateStorage = (*Store)(nil)

// Open opens a disk database with WAL, foreign keys and full synchronous commits.
// The caller owns the 32-byte encryption key and must provide it again on reopen.
// A single connection per Store and BEGIN IMMEDIATE serialize writers, including
// writers in other Store instances. The busy timeout applies to every connection.
func Open(path string, key []byte, opts ...Option) (*Store, error) {
	if path == "" || path == ":memory:" || len(key) != 32 {
		return nil, ErrInvalid
	}
	o := options{now: time.Now, metadataRetention: DefaultRetention, dedupRetention: DefaultRetention}
	for _, option := range opts {
		if option == nil {
			return nil, ErrInvalid
		}
		if err := option(&o); err != nil {
			return nil, err
		}
	}
	if _, err := addTime(o.now(), o.metadataRetention); err != nil {
		return nil, err
	}
	if _, err := addTime(o.now(), o.dedupRetention); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrInvalid
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("resolve database path")
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("open database file")
	}
	if err := file.Close(); err != nil {
		return nil, errors.New("close database file")
	}
	dsn := url.URL{Scheme: "file", Path: abs}
	query := url.Values{"_txlock": {"immediate"}, "_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "synchronous(FULL)"}}
	if o.maxPages != 0 {
		query["_pragma"] = append(query["_pragma"], fmt.Sprintf("max_page_count(%d)", o.maxPages))
	}
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, errors.New("open SQLite")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: abs, aead: aead, options: o, wakeups: make(chan struct{}, 1)}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("WebhookControl/v1/management-receipts"))
	s.managementMAC = mac.Sum(nil)
	if err := s.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if o.maxPages != 0 {
		var actual int64
		if err := db.QueryRow("PRAGMA max_page_count = " + fmt.Sprint(o.maxPages)).Scan(&actual); err != nil || actual > o.maxPages {
			db.Close()
			return nil, errors.New("database exceeds configured page limit")
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DeduplicationWindow() time.Duration { return s.dedupRetention }

func (s *Store) initialize(ctx context.Context) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return errors.New("enable WAL")
	}
	if mode != "wal" {
		return errors.New("WAL unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return dbError(err)
	}
	if version > 4 {
		return errors.New("unsupported database schema")
	}
	if version == 0 {
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return dbError(err)
		}
	}
	if version < 2 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE deduplication ADD COLUMN mode TEXT NOT NULL DEFAULT 'durable';
ALTER TABLE deliveries ADD COLUMN http_status_code INTEGER NOT NULL DEFAULT 0;
PRAGMA user_version = 2;`); err != nil {
			return dbError(err)
		}
	}
	if version < 3 {
		if err := s.migrateManagement(ctx, tx); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE deliveries ADD COLUMN first_attempt_at INTEGER;
CREATE TABLE runtime_keys(purpose TEXT PRIMARY KEY, check_value BLOB NOT NULL);
PRAGMA user_version = 4;`); err != nil {
			return dbError(err)
		}
	}
	var check []byte
	err = tx.QueryRowContext(ctx, "SELECT ciphertext FROM encryption_key WHERE id = 1").Scan(&check)
	if errors.Is(err, sql.ErrNoRows) {
		check = s.encrypt("key", "1", []byte("WebhookControl"))
		_, err = tx.ExecContext(ctx, "INSERT INTO encryption_key(id, ciphertext) VALUES (1, ?)", check)
	} else if err == nil {
		var plaintext []byte
		plaintext, err = s.decrypt("key", "1", check)
		if err == nil && string(plaintext) != "WebhookControl" {
			err = ErrCiphertext
		}
	}
	if err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit())
}

const schema = `
CREATE TABLE encryption_key (id INTEGER PRIMARY KEY CHECK(id = 1), ciphertext BLOB NOT NULL);
CREATE TABLE events (
 id TEXT PRIMARY KEY, source_id TEXT NOT NULL, source_revision INTEGER NOT NULL,
 accepted_at INTEGER NOT NULL, original_ttl INTEGER NOT NULL,
 original_expires_at INTEGER, metadata_expires_at INTEGER NOT NULL
);
CREATE TABLE originals (event_id TEXT PRIMARY KEY REFERENCES events(id), ciphertext BLOB NOT NULL);
CREATE TABLE deduplication (
 source_id TEXT NOT NULL, key_hash TEXT NOT NULL, fingerprint TEXT NOT NULL,
 event_id TEXT NOT NULL, delivery_ids TEXT NOT NULL, expires_at INTEGER NOT NULL,
 PRIMARY KEY(source_id, key_hash)
);
CREATE INDEX deduplication_expiry ON deduplication(expires_at);
CREATE TABLE deliveries (
 id TEXT PRIMARY KEY, event_id TEXT NOT NULL REFERENCES events(id), source_id TEXT NOT NULL,
 created_at INTEGER NOT NULL, ttl INTEGER NOT NULL, expires_at INTEGER,
 metadata_expires_at INTEGER NOT NULL, state TEXT NOT NULL,
 attempt INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL,
 last_error_code TEXT NOT NULL DEFAULT '', lease_token TEXT, lease_until INTEGER
);
CREATE TABLE delivery_payloads (delivery_id TEXT PRIMARY KEY REFERENCES deliveries(id), ciphertext BLOB NOT NULL);
CREATE INDEX deliveries_ready ON deliveries(state, next_attempt_at, lease_until);
CREATE INDEX deliveries_expiry ON deliveries(expires_at);
PRAGMA user_version = 1;
`

func (s *Store) encrypt(kind, id string, plaintext []byte) []byte {
	return s.aead.Seal(nil, nil, plaintext, []byte("WebhookControl/v1/"+kind+"/"+id))
}

func (s *Store) decrypt(kind, id string, ciphertext []byte) ([]byte, error) {
	plaintext, err := s.aead.Open(nil, nil, ciphertext, []byte("WebhookControl/v1/"+kind+"/"+id))
	if err != nil {
		return nil, ErrCiphertext
	}
	return plaintext, nil
}

// Do not return driver errors: constraint failures can contain caller input.
func dbError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCiphertext) {
		return err
	}
	return errors.New("SQLite operation failed")
}

func validTime(t time.Time) bool {
	return !t.IsZero() && !t.Before(time.Unix(0, 0)) && !t.After(time.Unix(0, math.MaxInt64))
}

func addTime(t time.Time, d time.Duration) (time.Time, error) {
	if !validTime(t) || d <= 0 || t.UnixNano() > math.MaxInt64-int64(d) {
		return time.Time{}, ErrInvalid
	}
	return time.Unix(0, t.UnixNano()+int64(d)).UTC(), nil
}

func expiresAt(t time.Time, ttl int64) (*time.Time, error) {
	if !validTime(t) || ttl < -1 || ttl > math.MaxInt64/int64(time.Second) {
		return nil, ErrInvalid
	}
	if ttl <= 0 {
		return nil, nil
	}
	expiry, err := addTime(t, time.Duration(ttl)*time.Second)
	if err != nil {
		return nil, err
	}
	return new(expiry), nil
}

func sameExpiry(got, want *time.Time) bool {
	return got == nil && want == nil || got != nil && want != nil && got.Equal(*want)
}

func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

func optionalTime(t sql.NullInt64) *time.Time {
	if !t.Valid {
		return nil
	}
	return new(time.Unix(0, t.Int64).UTC())
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func (s *Store) signal() {
	select {
	case s.wakeups <- struct{}{}:
	default:
	}
}

func (s *Store) Wakeups() <-chan struct{} { return s.wakeups }

// Cleanup removes expired ciphertext and bounded metadata/dedup records. Reads
// and claims enforce the same payload deadline even if Cleanup has not run.
// Metadata remains while needed by a retained payload or associated delivery.
func (s *Store) Cleanup(ctx context.Context, now time.Time) error {
	if !validTime(now) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	if err := expire(ctx, tx, now); err != nil {
		return err
	}
	for _, statement := range []string{
		"DELETE FROM deduplication WHERE expires_at <= ?",
		"DELETE FROM deliveries WHERE metadata_expires_at <= ? AND NOT EXISTS (SELECT 1 FROM delivery_payloads p WHERE p.delivery_id = deliveries.id)",
		"DELETE FROM events WHERE metadata_expires_at <= ? AND NOT EXISTS (SELECT 1 FROM originals o WHERE o.event_id = events.id) AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.event_id = events.id)",
	} {
		if _, err := tx.ExecContext(ctx, statement, now.UnixNano()); err != nil {
			return dbError(err)
		}
	}
	return dbError(tx.Commit())
}

func expire(ctx context.Context, tx *sql.Tx, now time.Time) error {
	for _, statement := range []string{
		"DELETE FROM originals WHERE event_id IN (SELECT id FROM events WHERE original_expires_at <= ?)",
		"DELETE FROM delivery_payloads WHERE delivery_id IN (SELECT id FROM deliveries WHERE expires_at <= ?)",
		"UPDATE deliveries SET state = 'expired', lease_token = NULL, lease_until = NULL WHERE expires_at <= ? AND state IN ('pending', 'leased')",
	} {
		if _, err := tx.ExecContext(ctx, statement, now.UnixNano()); err != nil {
			return dbError(err)
		}
	}
	return nil
}

func invalid(field string) error { return fmt.Errorf("%w: %s", ErrInvalid, field) }
