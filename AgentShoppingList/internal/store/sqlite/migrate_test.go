package sqlite

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	assertCount(t, store.write, "SELECT count(*) FROM schema_migrations", len(migrations))
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
	path := filepath.Join(t.TempDir(), "migration.sqlite")
	db := openMigrationDatabase(t, path)
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
	if err := migrate(t.Context(), db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO accounts(id, created_at) VALUES ('preserved', 1)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations); err == nil {
		t.Fatal("broken migration succeeded")
	}
	assertCount(t, db, "SELECT count(*) FROM schema_migrations", 1)
	assertCount(t, db, "SELECT count(*) FROM accounts WHERE id='preserved'", 1)
	assertCount(t, db, "SELECT count(*) FROM accounts WHERE id='should-rollback'", 0)
	assertCount(t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'should_rollback'", 0)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO accounts(id, created_at) VALUES ('still-usable', 2)"); err != nil {
		t.Fatalf("previous schema is not usable: %v", err)
	}
	assertOnlyDatabaseFiles(t, path)
}

func TestOpenFreshAndExistingSchemasWithoutBackupArtifacts(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{0, 4, len(migrations)} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "shopping.sqlite")
			mutation := testMutation("preserved")
			outcome := []byte(`{"saved":"original"}`)
			if version > 0 {
				db := openMigrationDatabase(t, path)
				if err := migrate(t.Context(), db, migrations[:version]); err != nil {
					t.Fatal(err)
				}
				for _, statement := range []string{
					`INSERT INTO accounts(id,created_at) VALUES ('preserved',1)`,
					`INSERT INTO identities(issuer,subject,owner_id) VALUES ('https://issuer.example/realm','preserved','preserved')`,
					`INSERT INTO preferences(owner_id,timezone,currency,preferred_retailer,week_start) VALUES ('preserved','America/New_York','USD','Walmart','monday')`,
					`INSERT INTO products(owner_id,id,name,category,created_at,updated_at) VALUES ('preserved','polar','Polar','drinks',1,1)`,
					`INSERT INTO shopping_lists(owner_id,id,title,created_at,updated_at) VALUES ('preserved','list','Walmart',1,1)`,
					`INSERT INTO list_items(owner_id,id,list_id,product_id,quantity,picked_up,created_at,updated_at,created_by,updated_by) VALUES ('preserved','item','list','polar','2',1,1,1,'preserved','preserved')`,
					`INSERT INTO purchases(owner_id,id,retailer,purchased_at,currency,source_kind,created_at,updated_at) VALUES ('preserved','receipt','Walmart',1,'USD','receipt',1,1)`,
					`INSERT INTO purchase_lines(owner_id,id,purchase_id,name_snapshot,category_snapshot,quantity,quantity_unit,line_total_minor) VALUES ('preserved','line','receipt','Polar','drinks','1','pack',599)`,
					`UPDATE purchases SET status='confirmed' WHERE id='receipt'`,
					`INSERT INTO web_sessions(token_hash,owner_id,client_id,encrypted_state,csrf_hash,created_at,expires_at) VALUES (zeroblob(32),'preserved','shopping-web',X'010203',zeroblob(32),1,9999999999999)`,
					`INSERT INTO audit_events(owner_id,id,actor_issuer,actor_subject,actor_client,operation,affected_records_json,request_id,occurred_at) VALUES ('preserved','audit','https://issuer.example/realm','preserved','test-client','preserved','["polar"]','request-preserved',1)`,
				} {
					if _, err := db.ExecContext(t.Context(), statement); err != nil {
						t.Fatal(err)
					}
				}
				hash := sha256.Sum256(mutation.Payload)
				if _, err := db.ExecContext(t.Context(), `INSERT INTO idempotency_results(owner_id,key,operation,payload_hash,result,created_at) VALUES ('preserved',?,?,?,?,1)`, mutation.Key, mutation.Operation, hash[:], outcome); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			store, err := Open(t.Context(), path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.Ready(t.Context()); err != nil {
				t.Fatalf("initialized/upgraded schema is not ready: %v", err)
			}
			if version > 0 {
				owner, err := store.OwnerForIdentity(t.Context(), ExternalIdentity{Issuer: "https://issuer.example/realm", Subject: "preserved"})
				if err != nil {
					t.Fatal(err)
				}
				preferences, err := store.Preferences(t.Context(), owner)
				if err != nil || preferences.Timezone != "America/New_York" || preferences.PreferredRetailer != "Walmart" {
					t.Fatalf("upgrade lost preferences: %#v %v", preferences, err)
				}
				item, err := store.ListItem(t.Context(), owner, "item")
				if err != nil || !item.PickedUp || item.Quantity != "2" {
					t.Fatalf("upgrade lost checked state: %#v %v", item, err)
				}
				var ciphertext []byte
				if err := store.read.QueryRowContext(t.Context(), "SELECT encrypted_state FROM web_sessions").Scan(&ciphertext); err != nil || !bytes.Equal(ciphertext, []byte{1, 2, 3}) {
					t.Fatalf("upgrade changed session ciphertext: %x %v", ciphertext, err)
				}
				assertCount(t, store.read, `SELECT count(*) FROM audit_events WHERE id='audit' AND affected_records_json='["polar"]'`, 1)
				assertCount(t, store.read, `SELECT count(*) FROM purchase_lines l JOIN purchases p ON p.owner_id=l.owner_id AND p.id=l.purchase_id WHERE p.status='confirmed' AND l.line_total_minor=599`, 1)
				replay, err := store.Write(t.Context(), owner, mutation, func(*Tx) ([]byte, error) {
					t.Fatal("upgrade lost the original idempotency result")
					return nil, nil
				})
				if err != nil || !replay.Replayed || !bytes.Equal(replay.Data, outcome) {
					t.Fatalf("upgrade changed replay: %#v %v", replay, err)
				}
			}
			assertOnlyDatabaseFiles(t, path)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			assertOnlyDatabaseFiles(t, path)
		})
	}
}

func TestOpenRejectsNonemptySchemaWithoutMigrationHistory(t *testing.T) {
	for name, schema := range map[string]string{
		"application table": `CREATE TABLE accounts(id TEXT PRIMARY KEY,created_at INTEGER); INSERT INTO accounts VALUES ('preserved',1)`,
		"unrelated table":   `CREATE TABLE notes(value TEXT); INSERT INTO notes VALUES ('preserved')`,
		"unrelated view":    `CREATE VIEW notes AS SELECT 'preserved' AS value`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.sqlite")
			db := openMigrationDatabase(t, path)
			if _, err := db.ExecContext(t.Context(), schema); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if store, err := Open(t.Context(), path, time.Second); err == nil {
				_ = store.Close()
				t.Fatal("initialized application alongside a schema without history")
			} else if !strings.Contains(err.Error(), "no application migration history") {
				t.Fatalf("unexpected startup refusal: %v", err)
			}
			db = openMigrationDatabase(t, path)
			assertCount(t, db, "SELECT count(*) FROM sqlite_schema WHERE name='schema_migrations'", 0)
			if name == "application table" {
				assertCount(t, db, "SELECT count(*) FROM accounts WHERE id='preserved'", 1)
			} else {
				assertCount(t, db, "SELECT count(*) FROM notes WHERE value='preserved'", 1)
				assertCount(t, db, "SELECT count(*) FROM sqlite_schema WHERE name='accounts'", 0)
			}
			assertOnlyDatabaseFiles(t, path)
		})
	}
}

func TestReadinessRejectsIncompleteHistoryAndClosedDatabase(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(t.Context(), "DELETE FROM schema_migrations WHERE version=6"); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(t.Context()); err == nil {
		t.Fatal("readiness accepted incomplete migrations")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(t.Context()); err == nil {
		t.Fatal("closed persistence remained ready")
	}
}

func assertOnlyDatabaseFiles(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	for _, entry := range entries {
		if !slices.Contains([]string{name, name + "-wal", name + "-shm"}, entry.Name()) || entry.IsDir() {
			t.Fatalf("startup/migration created an unexpected file or directory: %s", entry.Name())
		}
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

func TestCatalogMigrationPreservesVersionFourShoppingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-four.sqlite")
	db := openMigrationDatabase(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations[:4]); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO accounts(id,created_at) VALUES ('legacy',1)`,
		`INSERT INTO identities(issuer,subject,owner_id) VALUES ('https://issuer.example','legacy-user','legacy')`,
		`INSERT INTO products(owner_id,id,name,category,created_at,updated_at) VALUES ('legacy','drink','Drink','drinks',1,1)`,
		`INSERT INTO shopping_lists(owner_id,id,title,created_at,updated_at) VALUES ('legacy','list','Existing',1,1)`,
		`INSERT INTO list_items(owner_id,id,list_id,product_id,quantity,picked_up,created_at,updated_at,created_by,updated_by) VALUES ('legacy','item','list','drink','2.5',1,1,1,'legacy-user','legacy-user')`,
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
	owner, err := store.OwnerForIdentity(t.Context(), ExternalIdentity{Issuer: "https://issuer.example", Subject: "legacy-user"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.ListView(t.Context(), owner, "list", "", false)
	if err != nil || len(view.Items) != 1 || !view.Items[0].PickedUp || view.Items[0].Quantity != "2.5" || view.SelectionRevision != 0 || view.ActiveListID != nil {
		t.Fatalf("legacy shopping record lost or auto-selected: %#v %v", view, err)
	}
}
