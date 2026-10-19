// Package sqlite owns durable shopping records. It does not authenticate callers:
// identity adapters must verify credentials before resolving an enrolled owner.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound            = errors.New("record not found")
	ErrOwnerRequired       = errors.New("enrolled owner is required")
	ErrConflict            = errors.New("record revision changed")
	ErrIdempotencyConflict = errors.New("idempotency key was used for another request")
	ErrLimit               = errors.New("supported record limit exceeded")
)

// Store has one write connection and a bounded read-only pool. Neither pool is
// exposed: every personal repository operation must carry an Owner.
type Store struct {
	read  *sql.DB
	write *sql.DB
	// MigrationBackup is the verified pre-upgrade archive, if startup upgraded
	// an existing schema. Operators retain/copy it before deploying again.
	MigrationBackup string
}

func Open(ctx context.Context, path string, busyTimeout time.Duration) (_ *Store, err error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("database must have a persistent file path")
	}
	if busyTimeout < time.Millisecond || busyTimeout > 10*time.Second {
		return nil, fmt.Errorf("database busy timeout must be between 1ms and 10s")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("database file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(fmt.Errorf("database permissions: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("database file: %w", err)
	}
	writer, err := sql.Open("sqlite", dataSource(path, busyTimeout, false))
	if err != nil {
		return nil, err
	}
	store := &Store{write: writer}
	defer func() {
		if err != nil {
			err = errors.Join(err, store.Close())
		}
	}()
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	var journalMode string
	if err := writer.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if journalMode != "wal" {
		return nil, fmt.Errorf("database did not enable WAL")
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return nil, err
	}
	store.MigrationBackup, err = backupBeforeMigration(ctx, path, writer, migrations)
	if err != nil {
		return nil, fmt.Errorf("pre-migration backup: %w", err)
	}
	if err := migrate(ctx, writer, migrations); err != nil {
		return nil, fmt.Errorf("migrate database (verified recovery archive %q): %w", store.MigrationBackup, err)
	}
	reader, err := sql.Open("sqlite", dataSource(path, busyTimeout, true))
	if err != nil {
		return nil, err
	}
	store.read = reader
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	if err := reader.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	return store, nil
}

func dataSource(path string, busyTimeout time.Duration, readOnly bool) string {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	// DSN pragmas run for every new physical connection, including replacements.
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "synchronous(FULL)")
	if readOnly {
		query.Set("mode", "ro")
		query.Set("_query_only", "1")
	} else {
		query.Set("_txlock", "immediate")
	}
	uri.RawQuery = query.Encode()
	return uri.String()
}

func (s *Store) Close() error {
	var readErr, writeErr error
	if s.read != nil {
		readErr = s.read.Close()
	}
	if s.write != nil {
		writeErr = s.write.Close()
	}
	return errors.Join(readErr, writeErr)
}
