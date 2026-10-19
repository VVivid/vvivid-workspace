package sqlite

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOnlineBackupActiveWALConcurrentWritesAndIsolatedRestore(t *testing.T) {
	store, source := newTestStore(t)
	owner := enrollTestOwner(t, store, "recovery-alice")
	enrollTestOwner(t, store, "recovery-bob")
	if _, err := store.write.ExecContext(t.Context(), "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	mutation := testMutation("recovery-product")
	result, err := store.Write(t.Context(), owner, mutation, func(tx *Tx) ([]byte, error) {
		return []byte(`{"saved":"once"}`), tx.InsertProduct(Product{ID: "polar", Name: "Polar", Category: "drinks"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO purchases(owner_id,id,retailer,purchased_at,currency,source_kind,created_at,updated_at) VALUES (?, 'receipt','Walmart',1,'USD','receipt',1,1)`,
		`INSERT INTO purchase_lines(owner_id,id,purchase_id,name_snapshot,category_snapshot,quantity,quantity_unit,line_total_minor,tax_minor) VALUES (?, 'known','receipt','Polar','drinks','1','pack',599,0)`,
		`INSERT INTO purchase_lines(owner_id,id,purchase_id,name_snapshot,category_snapshot,quantity,quantity_unit) VALUES (?, 'unknown','receipt','Unknown','other','1','pack')`,
		`UPDATE purchases SET status='confirmed' WHERE owner_id=?`,
		`INSERT INTO web_sessions(token_hash,owner_id,client_id,encrypted_state,csrf_hash,created_at,expires_at) VALUES (zeroblob(32),?,'shopping-web',X'01',zeroblob(32),1,9999999999999)`,
	} {
		if _, err := store.write.ExecContext(t.Context(), statement, owner.id); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("test must snapshot committed records still in a live WAL")
	}
	stop := make(chan struct{})
	started := make(chan struct{})
	failures := make(chan error, 1)
	var writes atomic.Int64
	var workers sync.WaitGroup
	workers.Go(func() {
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
			}
			tx, err := store.write.BeginTx(t.Context(), nil)
			if err == nil {
				_, err = tx.ExecContext(t.Context(), "UPDATE products SET revision=revision+1 WHERE owner_id=?", owner.id)
				if err == nil {
					_, err = tx.ExecContext(t.Context(), "UPDATE preferences SET revision=revision+1 WHERE owner_id=?", owner.id)
				}
				if err == nil {
					err = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			}
			if err != nil {
				failures <- err
				return
			}
			writes.Add(1)
		}
	})
	<-started
	defer func() { close(stop); workers.Wait() }()
	directory := t.TempDir()
	archive := filepath.Join(directory, "online.tar")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	manifest, err := CreateBackup(ctx, source, archive)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, archive)
	if err != nil || !reflect.DeepEqual(verified, manifest) {
		t.Fatalf("verify: %#v %v", verified, err)
	}
	if manifest.Report.Schema != 7 || manifest.Report.Tables["accounts"] != 2 || manifest.Report.Tables["web_sessions"] != 1 || manifest.Report.Tables["idempotency_results"] != 1 || manifest.Report.Tables["audit_events"] != 1 {
		t.Fatalf("snapshot omitted durable evidence: %#v", manifest.Report)
	}
	if len(manifest.Report.Confirmed) != 1 || manifest.Report.Confirmed[0].MerchandiseMinor != "599" || manifest.Report.Confirmed[0].MissingPrices != 1 || manifest.Report.Confirmed[0].TaxMinor != "0" || manifest.Report.Confirmed[0].MissingTax != 1 {
		t.Fatalf("confirmed/unknown/zero totals: %#v", manifest.Report.Confirmed)
	}
	restoredPath := filepath.Join(directory, "isolated.sqlite")
	if _, err := RestoreBackup(ctx, archive, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := openExistingReadOnly(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var productRevision, preferencesRevision int64
	if err := restored.QueryRowContext(ctx, `SELECT p.revision,f.revision FROM products p JOIN preferences f ON f.owner_id=p.owner_id WHERE p.owner_id=?`, owner.id).Scan(&productRevision, &preferencesRevision); err != nil || productRevision != preferencesRevision {
		t.Fatalf("snapshot tore a concurrent transaction: %d %d %v", productRevision, preferencesRevision, err)
	}
	var replay []byte
	if err := restored.QueryRowContext(ctx, "SELECT result FROM idempotency_results WHERE owner_id=?", owner.id).Scan(&replay); err != nil || !bytes.Equal(replay, result.Data) {
		t.Fatalf("persisted outcome lost: %s %v", replay, err)
	}
	if writes.Load() == 0 {
		t.Fatal("concurrent writer made no progress")
	}
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	if _, err := CreateBackup(ctx, source, archive); err == nil {
		t.Fatal("backup overwrote an existing artifact")
	}
	if _, err := RestoreBackup(ctx, archive, restoredPath); err == nil {
		t.Fatal("restore overwrote an existing target")
	}
	for _, path := range []string{archive, restoredPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact permissions: %s %v", path, err)
		}
	}
	entries, err := filepath.Glob(filepath.Join(directory, ".shopping-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging files leaked: %v %v", entries, err)
	}
}

func TestRecoveryRejectsCorruptionNewerSchemaForeignKeysAndMissingSource(t *testing.T) {
	store, source := newTestStore(t)
	enrollTestOwner(t, store, "alice")
	directory := t.TempDir()
	archive := filepath.Join(directory, "good.tar")
	if _, err := CreateBackup(t.Context(), source, archive); err != nil {
		t.Fatal(err)
	}
	for _, defect := range []string{"truncated", "changed database", "changed totals", "unsafe entry"} {
		t.Run(defect, func(t *testing.T) {
			raw, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			broken := filepath.Join(t.TempDir(), "broken.tar")
			if defect == "truncated" {
				raw = raw[:len(raw)/2]
			} else {
				var output bytes.Buffer
				writer := tar.NewWriter(&output)
				reader := tar.NewReader(bytes.NewReader(raw))
				for {
					header, err := reader.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					contents, _ := io.ReadAll(reader)
					if header.Name == "database.sqlite" && defect == "changed database" {
						contents[0] ^= 1
					}
					if header.Name == "manifest.json" && defect == "changed totals" {
						var manifest BackupManifest
						_ = json.Unmarshal(contents, &manifest)
						manifest.Report.Tables["accounts"]++
						contents, _ = json.Marshal(manifest)
					}
					if defect == "unsafe entry" {
						header.Name = "../escaped.sqlite"
					}
					header.Size = int64(len(contents))
					_ = writer.WriteHeader(header)
					_, _ = writer.Write(contents)
				}
				_ = writer.Close()
				raw = output.Bytes()
			}
			if err := os.WriteFile(broken, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(broken), "target.sqlite")
			if _, err := RestoreBackup(t.Context(), broken, target); err == nil {
				t.Fatal("corrupt backup accepted")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed restore published a target")
			}
		})
	}
	if _, err := store.write.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(t.Context(), "INSERT INTO identities(issuer,subject,owner_id) VALUES ('https://issuer.example','orphan','missing')"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBackup(t.Context(), source, filepath.Join(directory, "orphan.tar")); err == nil {
		t.Fatal("foreign key corruption accepted")
	}
	if _, err := store.write.ExecContext(t.Context(), "DELETE FROM identities WHERE subject='orphan'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(t.Context(), "INSERT INTO schema_migrations VALUES (99,'0099_future.sql',?,1)", strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBackup(t.Context(), source, filepath.Join(directory, "future.tar")); err == nil {
		t.Fatal("newer schema accepted")
	}
	missing := filepath.Join(directory, "missing.sqlite")
	if _, err := InspectDatabase(t.Context(), missing); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created a database")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CreateBackup(canceled, source, filepath.Join(directory, "canceled.tar")); err == nil {
		t.Fatal("canceled backup succeeded")
	}
}

func TestRecoverySupportsEveryKnownMigrationPrefixWithoutApplyingNewSQL(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for version := 1; version < len(migrations); version++ {
		t.Run(migrations[version-1].name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "old.sqlite")
			db := openMigrationDatabase(t, path)
			if err := migrate(t.Context(), db, migrations[:version]); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, _, err := hashDatabase(path)
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(directory, "old.tar")
			if _, err := CreateBackup(t.Context(), path, archive); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(directory, "restored.sqlite")
			if _, err := RestoreBackup(t.Context(), archive, target); err != nil {
				t.Fatal(err)
			}
			report, err := InspectDatabase(t.Context(), target)
			if err != nil || report.Schema != version {
				t.Fatalf("version changed: %d %v", report.Schema, err)
			}
			after, _, err := hashDatabase(path)
			if err != nil || before != after {
				t.Fatal("recovery changed its source database")
			}
		})
	}
}
