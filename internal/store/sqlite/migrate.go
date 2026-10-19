package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func loadMigrations(files fs.FS) ([]migration, error) {
	paths, err := fs.Glob(files, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no embedded migrations")
	}
	migrations := make([]migration, 0, len(paths))
	for i, path := range paths {
		name := strings.TrimPrefix(path, "migrations/")
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || prefix != fmt.Sprintf("%04d", version) || version != i+1 {
			return nil, fmt.Errorf("migration %q must use a consecutive four-digit version", name)
		}
		contents, err := fs.ReadFile(files, path)
		if err != nil {
			return nil, err
		}
		checksum := sha256.Sum256(contents)
		migrations = append(migrations, migration{version: version, name: name, sql: string(contents), checksum: fmt.Sprintf("%x", checksum)})
	}
	return migrations, nil
}

func migrate(ctx context.Context, db *sql.DB, migrations []migration) error {
	var hasHistory bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='schema_migrations' AND type='table')").Scan(&hasHistory); err != nil {
		return err
	}
	if !hasHistory {
		// Only a new schema may initialize migration history. An existing schema
		// without that history cannot be identified as a supported application DB.
		var objects int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			return fmt.Errorf("existing database has no application migration history")
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL CHECK (length(checksum) = 64),
		applied_at INTEGER NOT NULL
	) STRICT`); err != nil {
		return fmt.Errorf("create migration history: %w", err)
	}
	applied, err := migrationVersion(ctx, db, migrations)
	if err != nil {
		return err
	}
	for _, migration := range migrations[applied:] {
		if err := applyMigration(ctx, db, migration); err != nil {
			return fmt.Errorf("migration %s: %w", migration.name, err)
		}
	}
	return nil
}

// migrationVersion checks history without creating, migrating or changing it.
func migrationVersion(ctx context.Context, db queryer, migrations []migration) (int, error) {
	rows, err := db.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, err
	}
	applied := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if version > len(migrations) {
			_ = rows.Close()
			return 0, fmt.Errorf("database schema version %d is newer than supported version %d", version, len(migrations))
		}
		if version != applied+1 {
			_ = rows.Close()
			return 0, fmt.Errorf("migration history is not consecutive at version %d", version)
		}
		expected := migrations[version-1]
		if name != expected.name || checksum != expected.checksum {
			_ = rows.Close()
			return 0, fmt.Errorf("migration %d history differs from embedded SQL", version)
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	return applied, nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
		migration.version, migration.name, migration.checksum, time.Now().UTC().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}
