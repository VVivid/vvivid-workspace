package sqlite

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"agentshoppinglist/internal/buildinfo"
	driver "modernc.org/sqlite"
)

const backupFormat = 1

// RecoveryReport contains counts and confirmed monetary totals, never identities,
// tokens, receipt evidence or session ciphertext. Amounts are exact decimal minor
// units, accumulated without SQLite's SUM integer overflow.
type RecoveryReport struct {
	Schema        int              `json:"schema"`
	SQLiteVersion string           `json:"sqlite_version"`
	Tables        map[string]int64 `json:"table_counts"`
	Confirmed     []RecoverySpend  `json:"confirmed_spend"`
}

type RecoverySpend struct {
	Currency         string `json:"currency"`
	Lines            int64  `json:"lines"`
	MerchandiseMinor string `json:"merchandise_minor"`
	MissingPrices    int64  `json:"missing_prices"`
	TaxMinor         string `json:"tax_minor"`
	MissingTax       int64  `json:"missing_tax"`
}

type BackupManifest struct {
	Format       int            `json:"format"`
	CreatedAt    time.Time      `json:"created_at"`
	Application  buildinfo.Info `json:"application"`
	SHA256       string         `json:"database_sha256"`
	DatabaseSize int64          `json:"database_bytes"`
	Report       RecoveryReport `json:"report"`
	OwnedAssets  []string       `json:"owned_assets"`
}

// InspectDatabase is deliberately separate from Open: recovery verification must
// never create a missing database or apply pending migrations.
func InspectDatabase(ctx context.Context, path string) (RecoveryReport, error) {
	db, err := openExistingReadOnly(ctx, path)
	if err != nil {
		return RecoveryReport{}, err
	}
	defer db.Close()
	return inspectDatabase(ctx, db)
}

func openExistingReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be an existing regular file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dataSource(absolute, 2*time.Second, true))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func inspectDatabase(ctx context.Context, db *sql.DB) (RecoveryReport, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RecoveryReport{}, err
	}
	defer tx.Rollback()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return RecoveryReport{}, err
	}
	version, err := migrationVersion(ctx, tx, migrations)
	if err != nil {
		return RecoveryReport{}, fmt.Errorf("unsupported migration history: %w", err)
	}
	if version == 0 {
		return RecoveryReport{}, fmt.Errorf("backup requires an initialized schema")
	}
	var integrity string
	if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return RecoveryReport{}, fmt.Errorf("database integrity check failed")
	}
	foreign, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return RecoveryReport{}, err
	}
	broken := foreign.Next()
	if err := errors.Join(foreign.Err(), foreign.Close()); err != nil {
		return RecoveryReport{}, err
	}
	if broken {
		return RecoveryReport{}, fmt.Errorf("database foreign key check failed")
	}
	report := RecoveryReport{Schema: version, Tables: map[string]int64{}, Confirmed: []RecoverySpend{}}
	if err := tx.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&report.SQLiteVersion); err != nil {
		return RecoveryReport{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return RecoveryReport{}, err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return RecoveryReport{}, err
		}
		tables = append(tables, table)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return RecoveryReport{}, err
	}
	for _, table := range tables {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`).Scan(&count); err != nil {
			return RecoveryReport{}, err
		}
		report.Tables[table] = count
	}
	if _, exists := report.Tables["purchase_lines"]; exists {
		report.Confirmed, err = confirmedSpend(ctx, tx)
		if err != nil {
			return RecoveryReport{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RecoveryReport{}, err
	}
	return report, nil
}

func confirmedSpend(ctx context.Context, db queryer) ([]RecoverySpend, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.currency, l.line_total_minor, l.tax_minor
		FROM purchases p JOIN purchase_lines l ON l.owner_id=p.owner_id AND l.purchase_id=p.id
		WHERE p.status='confirmed' ORDER BY p.currency`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RecoverySpend{}
	var merchandise, tax big.Int
	for rows.Next() {
		var currency string
		var price, lineTax sql.NullInt64
		if err := rows.Scan(&currency, &price, &lineTax); err != nil {
			return nil, err
		}
		if len(result) == 0 || result[len(result)-1].Currency != currency {
			result = append(result, RecoverySpend{Currency: currency})
			merchandise.SetInt64(0)
			tax.SetInt64(0)
		}
		current := &result[len(result)-1]
		current.Lines++
		if price.Valid {
			merchandise.Add(&merchandise, big.NewInt(price.Int64))
		} else {
			current.MissingPrices++
		}
		if lineTax.Valid {
			tax.Add(&tax, big.NewInt(lineTax.Int64))
		} else {
			current.MissingTax++
		}
		current.MerchandiseMinor, current.TaxMinor = merchandise.String(), tax.String()
	}
	return result, rows.Err()
}

// CreateBackup uses SQLite's online backup API, including committed WAL pages.
// The published tar is one private, fsynced, no-overwrite artifact. A failed or
// canceled snapshot never publishes an archive. Source connections are read-only.
func CreateBackup(ctx context.Context, source, destination string) (manifest BackupManifest, err error) {
	if err := requireAbsent(destination); err != nil {
		return manifest, err
	}
	db, err := openExistingReadOnly(ctx, source)
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	if _, err := inspectDatabase(ctx, db); err != nil {
		return manifest, fmt.Errorf("inspect backup source: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".shopping-backup-")
	if err != nil {
		return manifest, err
	}
	defer os.RemoveAll(stage)
	snapshot := filepath.Join(stage, "database.sqlite")
	file, err := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return manifest, err
	}
	if err := file.Close(); err != nil {
		return manifest, err
	}
	if err := onlineSnapshot(ctx, db, snapshot); err != nil {
		return manifest, fmt.Errorf("online snapshot: %w", err)
	}
	manifest.Report, err = InspectDatabase(ctx, snapshot)
	if err != nil {
		return manifest, fmt.Errorf("verify backup snapshot: %w", err)
	}
	manifest.Format, manifest.CreatedAt, manifest.Application = backupFormat, time.Now().UTC(), buildinfo.Current()
	manifest.OwnedAssets = []string{} // Current products reference remote URLs only.
	manifest.SHA256, manifest.DatabaseSize, err = hashDatabase(snapshot)
	if err != nil {
		return manifest, err
	}
	archive := filepath.Join(stage, "backup.tar")
	if err := writeBackupArchive(archive, snapshot, manifest); err != nil {
		return manifest, err
	}
	if _, err := VerifyBackup(ctx, archive); err != nil {
		return manifest, err
	}
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	if err := publishFile(archive, destination); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func onlineSnapshot(ctx context.Context, db *sql.DB, destination string) (err error) {
	const (
		pagesPerStep          = 64
		retryDelay            = 10 * time.Millisecond
		sqlitePrimaryCodeMask = 0xff
		sqliteBusy            = 5
		sqliteLocked          = 6
	)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(raw any) (err error) {
		backuper, ok := raw.(interface {
			NewBackup(string) (*driver.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backup, err := backuper.NewBackup(dataSource(destination, 2*time.Second, false))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, stepErr := backup.Step(pagesPerStep)
			if stepErr != nil {
				sqliteErr, ok := errors.AsType[*driver.Error](stepErr)
				if !ok {
					return stepErr
				}
				// Extended SQLite error codes retain their primary code in the
				// low byte. Only temporary lock contention should be retried.
				primaryCode := sqliteErr.Code() & sqlitePrimaryCodeMask
				if primaryCode != sqliteBusy && primaryCode != sqliteLocked {
					return stepErr
				}
				timer := time.NewTimer(retryDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
			if !more {
				return nil
			}
		}
	})
}

// VerifyBackup extracts only the two known entries into a private scratch
// directory, then checks hash, schema, integrity, relationships, counts/totals.
func VerifyBackup(ctx context.Context, archive string) (BackupManifest, error) {
	stage, err := os.MkdirTemp(filepath.Dir(archive), ".shopping-verify-")
	if err != nil {
		return BackupManifest{}, err
	}
	defer os.RemoveAll(stage)
	manifest, _, err := extractAndVerify(ctx, archive, stage)
	return manifest, err
}

// RestoreBackup installs a verified isolated snapshot only at a new path. It
// never opens the target through Open, never migrates it and never overwrites.
func RestoreBackup(ctx context.Context, archive, destination string) (BackupManifest, error) {
	if err := requireAbsent(destination); err != nil {
		return BackupManifest{}, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := requireAbsent(destination + suffix); err != nil {
			return BackupManifest{}, fmt.Errorf("restore sidecar: %w", err)
		}
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".shopping-restore-")
	if err != nil {
		return BackupManifest{}, err
	}
	defer os.RemoveAll(stage)
	manifest, snapshot, err := extractAndVerify(ctx, archive, stage)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := ctx.Err(); err != nil {
		return BackupManifest{}, err
	}
	if err := publishFile(snapshot, destination); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

func extractAndVerify(ctx context.Context, archive, stage string) (BackupManifest, string, error) {
	var manifest BackupManifest
	file, err := os.Open(archive)
	if err != nil {
		return manifest, "", err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	seen := map[string]bool{}
	snapshot := filepath.Join(stage, "database.sqlite")
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, "", fmt.Errorf("invalid backup archive")
		}
		if header.Typeflag != tar.TypeReg || seen[header.Name] {
			return manifest, "", fmt.Errorf("backup entries must be unique regular files")
		}
		seen[header.Name] = true
		switch header.Name {
		case "manifest.json":
			if header.Size <= 0 || header.Size > 1<<20 {
				return manifest, "", fmt.Errorf("invalid backup manifest size")
			}
			decoder := json.NewDecoder(reader)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&manifest); err != nil {
				return manifest, "", fmt.Errorf("invalid backup manifest")
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				return manifest, "", fmt.Errorf("invalid trailing manifest data")
			}
		case "database.sqlite":
			if header.Size <= 0 || header.Size > 64<<30 {
				return manifest, "", fmt.Errorf("backup database must be at most 64 GiB")
			}
			output, err := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return manifest, "", err
			}
			_, copyErr := io.Copy(output, reader)
			if err := errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
				return manifest, "", err
			}
		default:
			return manifest, "", fmt.Errorf("unknown backup entry")
		}
	}
	if len(seen) != 2 || manifest.Format != backupFormat || manifest.CreatedAt.IsZero() || len(manifest.OwnedAssets) != 0 {
		return manifest, "", fmt.Errorf("unsupported or incomplete backup manifest")
	}
	hash, size, err := hashDatabase(snapshot)
	if err != nil || hash != manifest.SHA256 || size != manifest.DatabaseSize {
		return manifest, "", fmt.Errorf("backup database checksum/size mismatch")
	}
	report, err := InspectDatabase(ctx, snapshot)
	if err != nil {
		return manifest, "", fmt.Errorf("verify restored snapshot: %w", err)
	}
	if report.Schema != manifest.Report.Schema || !reflect.DeepEqual(report.Tables, manifest.Report.Tables) || !reflect.DeepEqual(report.Confirmed, manifest.Report.Confirmed) {
		return manifest, "", fmt.Errorf("backup counts/totals differ from manifest")
	}
	return manifest, snapshot, nil
}

func hashDatabase(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return hex.EncodeToString(hash.Sum(nil)), size, err
}

func writeBackupArchive(path, snapshot string, manifest BackupManifest) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	writer := tar.NewWriter(file)
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := writer.Write(raw); err != nil {
		return err
	}
	if err := writer.WriteHeader(&tar.Header{Name: "database.sqlite", Mode: 0o600, Size: manifest.DatabaseSize, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	database, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(writer, database)
	if err := errors.Join(copyErr, database.Close(), writer.Close()); err != nil {
		return err
	}
	return file.Sync()
}

func requireAbsent(path string) error {
	if path == "" {
		return fmt.Errorf("explicit destination path is required")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("destination already exists or cannot be checked")
	}
	return nil
}

func publishFile(source, destination string) error {
	// Link is atomic and fails if any destination exists, including a symlink.
	// Staging is on the same filesystem; no rename-overwrite race is possible.
	if err := os.Link(source, destination); err != nil {
		return fmt.Errorf("publish recovery artifact without overwrite: %w", err)
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}
