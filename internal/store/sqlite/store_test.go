package sqlite

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	driver "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

func TestExplicitIdentityAndPreferences(t *testing.T) {
	store, _ := newTestStore(t)
	identity := ExternalIdentity{Issuer: "https://issuer.example/realm", Subject: "same-subject"}
	preferences := testPreferences()
	owner, err := store.EnrollAccount(t.Context(), identity, preferences)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.OwnerForIdentity(t.Context(), identity)
	if err != nil || resolved != owner {
		t.Fatalf("identity resolution: %#v, %v", resolved, err)
	}
	otherIssuer := identity
	otherIssuer.Issuer = "https://other.example/realm"
	if _, err := store.OwnerForIdentity(t.Context(), otherIssuer); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unenrolled identity resolution: %v", err)
	}
	second, err := store.EnrollAccount(t.Context(), otherIssuer, preferences)
	if err != nil || second == owner {
		t.Fatalf("issuer was not part of identity key: %#v, %v", second, err)
	}
	if _, err := store.EnrollAccount(t.Context(), identity, preferences); err == nil {
		t.Fatal("duplicate identity enrollment accepted")
	}
	assertCount(t, store.read, "SELECT count(*) FROM accounts", 2)
	for name, invalid := range map[string]Preferences{
		"missing timezone": {Currency: "USD", PreferredRetailer: "walmart", WeekStart: "monday"},
		"host timezone":    {Timezone: "Local", Currency: "USD", PreferredRetailer: "walmart", WeekStart: "monday"},
		"invalid timezone": {Timezone: "Invalid/Somewhere", Currency: "USD", PreferredRetailer: "walmart", WeekStart: "monday"},
		"missing currency": {Timezone: "UTC", PreferredRetailer: "walmart", WeekStart: "monday"},
		"invalid currency": {Timezone: "UTC", Currency: "ZZZ", PreferredRetailer: "walmart", WeekStart: "monday"},
		"missing week":     {Timezone: "UTC", Currency: "USD", PreferredRetailer: "walmart"},
		"invalid week":     {Timezone: "UTC", Currency: "USD", PreferredRetailer: "walmart", WeekStart: "tomorrow"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.EnrollAccount(t.Context(), ExternalIdentity{Issuer: identity.Issuer, Subject: name}, invalid); err == nil {
				t.Fatal("invalid preferences accepted")
			}
		})
	}
	if _, err := store.Preferences(t.Context(), Owner{}); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("missing owner: %v", err)
	}
}

func TestIdempotencyPersistsAcrossRestartAndRejectsChangedRequest(t *testing.T) {
	store, path := newTestStore(t)
	owner := enrollTestOwner(t, store, "alice")
	mutation := testMutation("create-product")
	want := []byte(`{"product_id":"polar"}`)
	result, err := store.Write(t.Context(), owner, mutation, func(tx *Tx) ([]byte, error) {
		if err := tx.InsertProduct(Product{ID: "polar", Name: "Polar Black Cherry", Category: "drinks"}); err != nil {
			return nil, err
		}
		if err := tx.InsertList(List{ID: "walmart", Title: "Walmart"}); err != nil {
			return nil, err
		}
		return want, tx.InsertListItem(ListItem{ID: "picked-up", ListID: "walmart", ProductID: "polar", Quantity: "2", PickedUp: true})
	})
	if err != nil || result.Replayed || !bytes.Equal(result.Data, want) {
		t.Fatalf("first write: %#v, %v", result, err)
	}
	neverApply := func(*Tx) ([]byte, error) {
		t.Error("idempotent retry called the database mutation again")
		return nil, errors.New("unexpected callback")
	}
	result, err = store.Write(t.Context(), owner, mutation, neverApply)
	if err != nil || !result.Replayed || !bytes.Equal(result.Data, want) {
		t.Fatalf("replay: %#v, %v", result, err)
	}
	changedPayload := mutation
	changedPayload.Payload = []byte(`{"different":true}`)
	if _, err := store.Write(t.Context(), owner, changedPayload, neverApply); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload: %v", err)
	}
	changedOperation := mutation
	changedOperation.Operation = "another-operation"
	if _, err := store.Write(t.Context(), owner, changedOperation, neverApply); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed operation: %v", err)
	}
	other := enrollTestOwner(t, store, "bob")
	if _, err := store.Write(t.Context(), other, mutation, func(tx *Tx) ([]byte, error) {
		return want, tx.InsertProduct(Product{ID: "polar", Name: "Polar Black Cherry", Category: "drinks"})
	}); err != nil {
		t.Fatalf("another owner's key namespace: %v", err)
	}
	assertCount(t, store.read, "SELECT count(*) FROM audit_events", 2)
	var issuer, subject, client string
	if err := store.read.QueryRowContext(t.Context(), "SELECT actor_issuer, actor_subject, actor_client FROM audit_events WHERE owner_id = ?", owner.id).Scan(&issuer, &subject, &client); err != nil || issuer != "https://issuer.example/realm" || subject != "alice" || client != mutation.ClientID {
		t.Fatalf("audit actor: %q %q %q, %v", issuer, subject, client, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	resolved, err := restarted.OwnerForIdentity(t.Context(), ExternalIdentity{Issuer: "https://issuer.example/realm", Subject: "alice"})
	if err != nil || resolved != owner {
		t.Fatalf("restart owner: %#v, %v", resolved, err)
	}
	result, err = restarted.Write(t.Context(), resolved, mutation, neverApply)
	if err != nil || !result.Replayed || !bytes.Equal(result.Data, want) {
		t.Fatalf("restart replay: %#v, %v", result, err)
	}
	if product, err := restarted.Product(t.Context(), resolved, "polar"); err != nil || product.Revision != 1 {
		t.Fatalf("restart product: %#v, %v", product, err)
	}
	if item, err := restarted.ListItem(t.Context(), resolved, "picked-up"); err != nil || !item.PickedUp || item.Quantity != "2" {
		t.Fatalf("restart picked-up state: %#v, %v", item, err)
	}
	assertCount(t, restarted.read, "SELECT count(*) FROM purchases", 0)
}

func TestWriteRollbackIncludesAuditAndIdempotency(t *testing.T) {
	store, _ := newTestStore(t)
	owner := enrollTestOwner(t, store, "alice")
	mutation := testMutation("atomic-create")
	apply := func(tx *Tx) ([]byte, error) {
		if err := tx.InsertProduct(Product{ID: "product", Name: "Drink", Category: "drinks"}); err != nil {
			return nil, err
		}
		return []byte(`{"item":"item"}`), tx.InsertListItem(ListItem{ID: "item", ListID: "list", ProductID: "product", Quantity: "1"})
	}
	if _, err := store.Write(t.Context(), owner, mutation, apply); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing list must abort: %v", err)
	}
	assertCount(t, store.read, "SELECT count(*) FROM products", 0)
	assertCount(t, store.read, "SELECT count(*) FROM audit_events", 0)
	assertCount(t, store.read, "SELECT count(*) FROM idempotency_results", 0)
	if _, err := store.Write(t.Context(), owner, testMutation("create-list"), func(tx *Tx) ([]byte, error) {
		return []byte(`{}`), tx.InsertList(List{ID: "list", Title: "Walmart"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(t.Context(), owner, mutation, apply); err != nil {
		t.Fatalf("retry after list exists: %v", err)
	}
	if item, err := store.ListItem(t.Context(), owner, "item"); err != nil || item.Quantity != "1" || item.PickedUp {
		t.Fatalf("committed item: %#v, %v", item, err)
	}
	assertCount(t, store.read, "SELECT count(*) FROM purchases", 0)
}

func TestConcurrentSameKeyMutatesOnce(t *testing.T) {
	store, _ := newTestStore(t)
	owner := enrollTestOwner(t, store, "alice")
	var callbacks atomic.Int64
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			_, err := store.Write(t.Context(), owner, testMutation("concurrent"), func(tx *Tx) ([]byte, error) {
				callbacks.Add(1)
				return []byte(`{"id":"only-one"}`), tx.InsertProduct(Product{ID: "only-one", Name: "One product", Category: "drinks"})
			})
			results <- err
		})
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := callbacks.Load(); got != 1 {
		t.Fatalf("same key ran %d callbacks", got)
	}
	assertCount(t, store.read, "SELECT count(*) FROM products", 1)
	assertCount(t, store.read, "SELECT count(*) FROM idempotency_results", 1)
	assertCount(t, store.read, "SELECT count(*) FROM audit_events", 1)
}

func TestOwnerQualifiedReadsWritesAndRelationships(t *testing.T) {
	store, _ := newTestStore(t)
	alice := enrollTestOwner(t, store, "alice")
	bob := enrollTestOwner(t, store, "bob")
	for _, owner := range []Owner{alice, bob} {
		_, err := store.Write(t.Context(), owner, testMutation("setup"), func(tx *Tx) ([]byte, error) {
			if err := tx.InsertProduct(Product{ID: owner.id + "product", Name: "Same product name", Category: "drinks"}); err != nil {
				return nil, err
			}
			if err := tx.InsertList(List{ID: owner.id + "list", Title: "Same list name"}); err != nil {
				return nil, err
			}
			return []byte(`{}`), tx.InsertListItem(ListItem{ID: owner.id + "item", ListID: owner.id + "list", ProductID: owner.id + "product", Quantity: "1.5"})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Product(t.Context(), bob, alice.id+"product"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign product read: %v", err)
	}
	if _, err := store.List(t.Context(), bob, alice.id+"list"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign list read: %v", err)
	}
	if _, err := store.ListItem(t.Context(), bob, alice.id+"item"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign item read: %v", err)
	}
	for name, item := range map[string]ListItem{
		"foreign product": {ID: "foreign-product", ListID: bob.id + "list", ProductID: alice.id + "product", Quantity: "1"},
		"foreign list":    {ID: "foreign-list", ListID: alice.id + "list", ProductID: bob.id + "product", Quantity: "1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Write(t.Context(), bob, testMutation(name), func(tx *Tx) ([]byte, error) {
				return []byte(`{}`), tx.InsertListItem(item)
			}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign relationship write: %v", err)
			}
		})
	}
	// A guessed foreign row ID cannot alter another account's preferences.
	if _, err := store.Write(t.Context(), bob, testMutation("preferences"), func(tx *Tx) ([]byte, error) {
		preferences := testPreferences()
		preferences.Timezone = "Europe/Paris"
		return []byte(`{}`), tx.SetPreferences(preferences, 1)
	}); err != nil {
		t.Fatal(err)
	}
	preferences, err := store.Preferences(t.Context(), alice)
	if err != nil || preferences.Timezone != "UTC" || preferences.Revision != 1 {
		t.Fatalf("another owner's preferences changed: %#v, %v", preferences, err)
	}
	if _, err := store.Write(t.Context(), bob, testMutation("stale-preferences"), func(tx *Tx) ([]byte, error) {
		return []byte(`{}`), tx.SetPreferences(testPreferences(), 1)
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preference write: %v", err)
	}
	assertCompositeForeignKeys(t, store, alice, bob)
}

func assertCompositeForeignKeys(t *testing.T, store *Store, alice, bob Owner) {
	t.Helper()
	for _, owner := range []Owner{alice, bob} {
		if _, err := store.write.ExecContext(t.Context(), `INSERT INTO purchases(owner_id, id, retailer, source_kind, created_at, updated_at) VALUES (?, ?, 'walmart', 'receipt', 1, 1)`, owner.id, owner.id+"purchase"); err != nil {
			t.Fatal(err)
		}
	}
	for name, statement := range map[string]string{
		"media product":        fmt.Sprintf(`INSERT INTO product_media VALUES ('%s', 'media', '%sproduct', 'https://example/image', 'https://example/source', 1)`, bob.id, alice.id),
		"nutrition product":    fmt.Sprintf(`INSERT INTO product_nutrition VALUES ('%s', '%sproduct', '1', 'can', '{}', 'https://example/source', 1, 'verified')`, bob.id, alice.id),
		"listing product":      fmt.Sprintf(`INSERT INTO retailer_listings(owner_id, id, product_id, retailer, product_url) VALUES ('%s', 'listing', '%sproduct', 'walmart', 'https://example/product')`, bob.id, alice.id),
		"item product":         fmt.Sprintf(`INSERT INTO list_items(owner_id, id, list_id, product_id, quantity, created_at, updated_at, created_by, updated_by) VALUES ('%s', 'foreign', '%slist', '%sproduct', '1', 1, 1, 'bob', 'bob')`, bob.id, bob.id, alice.id),
		"item list":            fmt.Sprintf(`INSERT INTO list_items(owner_id, id, list_id, product_id, quantity, created_at, updated_at, created_by, updated_by) VALUES ('%s', 'foreign', '%slist', '%sproduct', '1', 1, 1, 'bob', 'bob')`, bob.id, alice.id, bob.id),
		"replacement purchase": fmt.Sprintf(`UPDATE purchases SET replaces_id = '%spurchase' WHERE owner_id = '%s'`, alice.id, bob.id),
		"superseding purchase": fmt.Sprintf(`UPDATE purchases SET superseded_by_id = '%spurchase' WHERE owner_id = '%s'`, alice.id, bob.id),
		"line purchase":        fmt.Sprintf(`INSERT INTO purchase_lines(owner_id, id, purchase_id, name_snapshot, category_snapshot, quantity, quantity_unit) VALUES ('%s', 'foreign', '%spurchase', 'Drink', 'drinks', '1', 'pack')`, bob.id, alice.id),
		"line product":         fmt.Sprintf(`INSERT INTO purchase_lines(owner_id, id, purchase_id, product_id, name_snapshot, category_snapshot, quantity, quantity_unit) VALUES ('%s', 'foreign', '%spurchase', '%sproduct', 'Drink', 'drinks', '1', 'pack')`, bob.id, bob.id, alice.id),
		"line source item":     fmt.Sprintf(`INSERT INTO purchase_lines(owner_id, id, purchase_id, source_list_item_id, name_snapshot, category_snapshot, quantity, quantity_unit) VALUES ('%s', 'foreign', '%spurchase', '%sitem', 'Drink', 'drinks', '1', 'pack')`, bob.id, bob.id, alice.id),
	} {
		t.Run("schema rejects "+name, func(t *testing.T) {
			_, err := store.write.ExecContext(t.Context(), statement)
			sqliteErr, ok := errors.AsType[*driver.Error](err)
			if !ok || sqliteErr.Code() != sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY {
				t.Fatalf("expected composite foreign-key rejection, got %v", err)
			}
		})
	}
}

func TestForeignKeysReadOnlyAndBusyTimeoutOnEveryConnection(t *testing.T) {
	store, path := newTestStore(t)
	owner := enrollTestOwner(t, store, "alice")
	var held []*sql.Conn
	for range 4 {
		conn, err := store.read.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		assertConnectionPragmas(t, conn, true)
	}
	for _, conn := range held {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store.write.SetMaxIdleConns(0)
	for range 2 {
		conn, err := store.write.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		assertConnectionPragmas(t, conn, false)
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store.write.SetMaxIdleConns(1)
	blocker := openMigrationDatabase(t, path)
	lock, err := blocker.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := store.Preferences(t.Context(), owner); err != nil {
		t.Fatalf("WAL read while writer is locked: %v", err)
	}
	started := time.Now()
	_, err = store.Write(t.Context(), owner, testMutation("busy"), func(*Tx) ([]byte, error) {
		t.Error("write callback ran without acquiring the write lock")
		return []byte(`{}`), nil
	})
	elapsed := time.Since(started)
	sqliteErr, ok := errors.AsType[*driver.Error](err)
	if !ok || sqliteErr.Code() != sqlitelib.SQLITE_BUSY || elapsed > 2*time.Second {
		t.Fatalf("bounded busy handling: elapsed %s, error %v", elapsed, err)
	}
	assertCount(t, store.read, "SELECT count(*) FROM idempotency_results", 0)
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(t.Context(), owner, testMutation("busy"), func(*Tx) ([]byte, error) { return []byte(`{}`), nil }); err != nil {
		t.Fatalf("write did not recover after lock release: %v", err)
	}
}

func assertConnectionPragmas(t *testing.T, conn *sql.Conn, readOnly bool) {
	t.Helper()
	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 100, "query_only": 0} {
		if pragma == "query_only" && readOnly {
			want = 1
		}
		var got int
		if err := conn.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("connection %s: got %d, want %d, error %v", pragma, got, want, err)
		}
	}
	if readOnly {
		if _, err := conn.ExecContext(t.Context(), "INSERT INTO accounts(id, created_at) VALUES ('read-pool-write', 1)"); err == nil {
			t.Fatal("read pool accepted a mutation")
		}
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shopping.sqlite")
	store, err := Open(t.Context(), path, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertCount(t, store.read, "SELECT count(*) FROM schema_migrations", 7)
	return store, path
}

func testPreferences() Preferences {
	return Preferences{Timezone: "UTC", Currency: "USD", PreferredRetailer: "walmart", WeekStart: "monday"}
}

func enrollTestOwner(t *testing.T, store *Store, subject string) Owner {
	t.Helper()
	owner, err := store.EnrollAccount(t.Context(), ExternalIdentity{Issuer: "https://issuer.example/realm", Subject: subject}, testPreferences())
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func testMutation(key string) Mutation {
	return Mutation{Key: key, Operation: key, Payload: []byte(`{"input":"fixture"}`), RequestID: "request-" + key, ClientID: "test-client"}
}
