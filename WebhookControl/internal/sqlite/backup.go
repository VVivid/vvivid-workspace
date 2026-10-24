package sqlite

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// Backup creates a consistent SQLite snapshot, including committed WAL pages,
// without decrypting payloads or secrets. A destination must not already exist.
// VACUUM INTO uses a database read transaction; copying state.db alone does not.
func (s *Store) Backup(ctx context.Context, destination string) error {
	return snapshotInto(ctx, s.db, destination)
}

func snapshotInto(ctx context.Context, db *sql.DB, destination string) error {
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return ErrInvalid
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("backup destination or SQLite sidecar already exists")
		}
	}
	f, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("backup destination must be a new file")
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			os.Remove(destination)
		}
	}()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return dbError(err)
	}
	// SQLite does not promise an fsync for VACUUM INTO. Flush both contents and
	// the directory entry before reporting a successful administrative backup.
	if err := f.Sync(); err != nil {
		return errors.New("sync backup failed")
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return errors.New("open backup directory failed")
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return errors.New("sync backup directory failed")
	}
	complete = true
	return nil
}

// Restore verifies the supplied key and SQLite integrity before creating a new
// destination from a consistent read snapshot. It never overwrites a live DB.
// The source is opened read-only and is never migrated or decrypted to disk.
func Restore(ctx context.Context, backup, destination string, key []byte) error {
	if len(key) != 32 || !filepath.IsAbs(backup) || filepath.Clean(backup) != backup {
		return ErrInvalid
	}
	u := url.URL{Scheme: "file", Path: backup}
	u.RawQuery = url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return errors.New("open backup failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("backup integrity check failed")
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version < 1 || version > 4 {
		return errors.New("unsupported backup schema")
	}
	var check []byte
	if err := db.QueryRowContext(ctx, "SELECT ciphertext FROM encryption_key WHERE id=1").Scan(&check); err != nil {
		return errors.New("backup key check missing")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ErrInvalid
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, nil, check, []byte("WebhookControl/v1/key/1"))
	if err != nil || string(plain) != "WebhookControl" {
		return ErrCiphertext
	}
	clear(plain)
	return snapshotInto(ctx, db, destination)
}
