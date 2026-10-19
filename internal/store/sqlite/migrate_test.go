package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestFreshSchemaAndUpgradePreserveAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shopping?#.sqlite")
	db := openMigrationDatabase(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO accounts(id, created_at) VALUES ('preserved', 1)`,
		`INSERT INTO identities(issuer, subject, owner_id) VALUES ('https://issuer.example', 'existing-user', 'preserved')`,
		`INSERT INTO preferences(owner_id, timezone, currency, preferred_retailer, week_start) VALUES ('preserved', 'Asia/Tokyo', 'JPY', 'walmart', 'sunday')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, err := store.OwnerForIdentity(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "existing-user"})
	if err != nil || owner.id != "preserved" {
		t.Fatalf("upgraded identity: %#v, %v", owner, err)
	}
	preferences, err := store.Preferences(t.Context(), owner)
	if err != nil || preferences.Timezone != "Asia/Tokyo" || preferences.Currency != "JPY" || preferences.WeekStart != "sunday" {
		t.Fatalf("upgraded preferences: %#v, %v", preferences, err)
	}
	if err := migrate(t.Context(), store.write, migrations); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertCount(t, store.write, "SELECT count(*) FROM schema_migrations", 4)
	rows, err := store.read.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("foreign key violation after upgrade: %v", rows.Err())
	}
}

func TestMigrationFailureRollsBackDDLAndData(t *testing.T) {
	db := openMigrationDatabase(t, filepath.Join(t.TempDir(), "migration.sqlite"))
	first, err := migrationFiles.ReadFile("migrations/0001_accounts.sql")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"migrations/0001_accounts.sql": {Data: first},
		"migrations/0002_broken.sql": {Data: []byte(`
			CREATE TABLE should_rollback(id TEXT);
			INSERT INTO accounts(id, created_at) VALUES ('should-rollback', 1);
			INSERT INTO nonexistent_table VALUES (1);`)},
	}
	migrations, err := loadMigrations(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations); err == nil {
		t.Fatal("broken migration succeeded")
	}
	assertCount(t, db, "SELECT count(*) FROM schema_migrations", 1)
	assertCount(t, db, "SELECT count(*) FROM accounts", 0)
	assertCount(t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'should_rollback'", 0)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO accounts(id, created_at) VALUES ('still-usable', 2)"); err != nil {
		t.Fatalf("previous schema is not usable: %v", err)
	}
}

func TestMigrationsRejectDriftNewerAndMissingHistory(t *testing.T) {
	for name, statement := range map[string]string{
		"changed checksum": `UPDATE schema_migrations SET checksum = '` + strings.Repeat("0", 64) + `' WHERE version = 1`,
		"newer schema":     `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (99, '0099_future.sql', '` + strings.Repeat("0", 64) + `', 1)`,
		"missing history":  `DELETE FROM schema_migrations WHERE version = 2`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shopping.sqlite")
			store, err := Open(t.Context(), path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.write.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(t.Context(), path, time.Second); err == nil {
				_ = reopened.Close()
				t.Fatal("incompatible migration history accepted")
			}
		})
	}
}

func openMigrationDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(t.Context(), query).Scan(&got); err != nil || got != want {
		t.Fatalf("%s: got %d, want %d, error %v", query, got, want, err)
	}
}
