package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Backup creates a consistent SQLite snapshot, including committed WAL pages,
// while the one service writer may remain open. It never creates the source.
func Backup(ctx context.Context, source, destination string) error {
	return snapshot(ctx, source, destination)
}

// Restore verifies a snapshot and creates a NEW isolated journal. It cannot
// replace an active database. Cutover is an explicit, quiesced operator action.
func Restore(ctx context.Context, source, destination string) error {
	return snapshot(ctx, source, destination)
}

func diskPath(path string) (string, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") || strings.ContainsRune(path, 0) {
		return "", errors.New("a disk path is required")
	}
	return filepath.Abs(path)
}

func privateFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("snapshot source must be a private regular file")
	}
	return info, nil
}

func snapshot(ctx context.Context, source, destination string) (err error) {
	source, err = diskPath(source)
	if err != nil {
		return err
	}
	destination, err = diskPath(destination)
	if err != nil {
		return err
	}
	if _, err := privateFile(source); err != nil {
		return err
	}
	// O_EXCL below rejects aliases/hardlinks to the source as well as every
	// existing destination. Refuse orphan sidecars rather than adopting them.
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("snapshot destination already exists or cannot be inspected")
		}
	}
	destinationDir := filepath.Dir(destination)
	if err := os.MkdirAll(destinationDir, 0700); err != nil {
		return err
	}
	info, err := os.Stat(destinationDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("snapshot destination requires a private directory (0700)")
	}
	sourceDB, err := openSnapshotSource(source)
	if err != nil {
		return err
	}
	defer sourceDB.Close()
	if err := verifySnapshot(ctx, sourceDB); err != nil {
		return fmt.Errorf("invalid source journal: %w", err)
	}
	// VACUUM INTO accepts an existing empty file. Reserve it exclusively and
	// keep the handle so cleanup and fsync refer to the output inode we own.
	outputFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer outputFile.Close()
	reservedOutput, err := outputFile.Stat()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Remove only the output inode this operation reserved, never an
			// existing database or a path replaced by another operator.
			if current, statErr := os.Lstat(destination); statErr == nil && os.SameFile(reservedOutput, current) {
				os.Remove(destination)
			}
		}
	}()
	// SQLite's VACUUM INTO copies one consistent read snapshot, including WAL,
	// FTS shadow tables and row IDs; copying only the live main file cannot.
	if _, err := sourceDB.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return err
	}
	outputDB, err := openSnapshotSource(destination)
	if err != nil {
		return err
	}
	err = verifySnapshot(ctx, outputDB)
	err = errors.Join(err, outputDB.Close())
	if err != nil {
		return err
	}
	if current, err := privateFile(destination); err != nil || !os.SameFile(reservedOutput, current) {
		return errors.New("snapshot output changed during creation")
	}
	// VACUUM INTO does not promise an fsync of its output. Make both content
	// and the new directory entry durable before reporting success.
	if err := outputFile.Sync(); err != nil {
		return err
	}
	directory, err := os.Open(destinationDir)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func openSnapshotSource(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "temp_store(MEMORY)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Recovery accepts the current application/schema only; opening it must not
// initialize a blank database or silently migrate an unrelated/older backup.
func verifySnapshot(ctx context.Context, db *sql.DB) error {
	var appID, version, keySize int
	if err := db.QueryRowContext(ctx, "SELECT (SELECT application_id FROM pragma_application_id), (SELECT user_version FROM pragma_user_version), length(cursor_key) FROM settings WHERE id=1").Scan(&appID, &version, &keySize); err != nil {
		return err
	}
	if appID != 0x414d454d || version != 2 || keySize != 32 {
		return errors.New("unsupported journal application/schema/settings")
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("snapshot integrity check failed")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	broken := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil || broken {
		return errors.New("snapshot foreign key check failed")
	}
	// Prepare all shared contracts as well as each generated area table. This
	// catches missing schema even in an empty journal, where FK checks cannot.
	queries := []string{
		"SELECT id,account_id,name,table_name,created_at FROM spaces LIMIT 0",
		"SELECT seq,span_id,account_id,space_id,topic_id,parent_id,kind,external_ref,occurred_at,recorded_at FROM nodes LIMIT 0",
		"SELECT account_id,span_id,tag FROM tags LIMIT 0",
		"SELECT account_id,source_id,type,target_id FROM links LIMIT 0",
		"SELECT account_id,request_key,canonical_request,result FROM idempotency LIMIT 0",
		"SELECT id,created_at FROM accounts LIMIT 0",
		"SELECT issuer,subject,account_id,active FROM enrolled_identities LIMIT 0",
		"SELECT span_id,account_id,title,context,reasoning,external_ref FROM search LIMIT 0",
	}
	rows, err = db.QueryContext(ctx, "SELECT table_name FROM spaces")
	if err != nil {
		return err
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return err
		}
		quoted, err := quotedTable(table)
		if err != nil {
			rows.Close()
			return err
		}
		queries = append(queries, "SELECT seq,span_id,topic_id,parent_id,kind,schema_version,title,context,external_ref,payload,occurred_at,recorded_at,historical,time_unknown,provenance,account_id,client_id,request_key FROM "+quoted+" LIMIT 0")
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
