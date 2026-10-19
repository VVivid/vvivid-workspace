package sqlite

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func shareFixture(t *testing.T) (*Store, string, Owner, []ShareSelection) {
	t.Helper()
	store, path := newTestStore(t)
	owner := enrollTestOwner(t, store, "alice")
	_, err := store.Write(t.Context(), owner, testMutation("share-fixture"), func(tx *Tx) ([]byte, error) {
		if err := tx.InsertProduct(Product{ID: "private-product", Name: "Selected drink", Category: "drinks"}); err != nil {
			return nil, err
		}
		if err := tx.InsertList(List{ID: "private-list", Title: "Private title"}); err != nil {
			return nil, err
		}
		return []byte(`{}`), tx.InsertListItem(ListItem{ID: "private-item", ListID: "private-list", ProductID: "private-product", Quantity: "2.5", PickedUp: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, path, owner, []ShareSelection{{ItemID: "private-item", ExpectedRevision: 1, ExpectedProductRevision: 1}}
}

func createTestShare(t *testing.T, store *Store, owner Owner, key, code string, items []ShareSelection) CreatedShare {
	t.Helper()
	result, err := store.Write(t.Context(), owner, testMutation(key), func(tx *Tx) ([]byte, error) {
		share, err := tx.CreateShare("private-list", 1, items, func() (string, error) { return code, nil })
		if err != nil {
			return nil, err
		}
		return json.Marshal(share)
	})
	if err != nil {
		t.Fatal(err)
	}
	var share CreatedShare
	if err := json.Unmarshal(result.Data, &share); err != nil {
		t.Fatal(err)
	}
	return share
}

func TestShareExactExpiryReopenReplayAndSeparation(t *testing.T) {
	store, path, owner, items := shareFixture(t)
	created := createTestShare(t, store, owner, "share", "AbCdE", items)
	var started int64
	if err := store.read.QueryRowContext(t.Context(), `SELECT created_at FROM temporary_shares`).Scan(&started); err != nil || created.ExpiresAt-started != ShareLifetime.Milliseconds() {
		t.Fatalf("six-hour lifetime: %d %v", created.ExpiresAt-started, err)
	}
	now := created.ExpiresAt - 2
	store.shareNow = func() time.Time { return time.UnixMilli(now) }
	share, err := store.PublicShare(t.Context(), created.Code)
	if err != nil || len(share.Items) != 1 || share.Items[0].PickedUp || share.Items[0].Quantity != "2.5" {
		t.Fatalf("snapshot/checklist: %#v %v", share, err)
	}
	input := SharedItemInput{Position: 1, PickedUp: true, ExpectedRevision: 1}
	saved, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, "guest", "guest-request", input)
	if err != nil || saved.Replayed {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.shareNow = func() time.Time { return time.UnixMilli(now) }
	reopened, err := store.PublicShare(t.Context(), created.Code)
	if err != nil || !reopened.Items[0].PickedUp || reopened.ExpiresAt != created.ExpiresAt || reopened.WriteToken != share.WriteToken {
		t.Fatalf("reopen changed share: %#v %v", reopened, err)
	}
	replayed, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, "guest", "new-request", input)
	if err != nil || !replayed.Replayed || !bytes.Equal(replayed.Data, saved.Data) {
		t.Fatalf("guest durable replay: %#v %v", replayed, err)
	}
	changed := input
	changed.PickedUp = false
	if _, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, "guest", "changed-request", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed key: %v", err)
	}
	now = created.ExpiresAt - 1
	if _, err := store.PublicShare(t.Context(), created.Code); err != nil {
		t.Fatalf("last live millisecond: %v", err)
	}
	if _, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, "guest", "last-request", input); err != nil {
		t.Fatalf("last live replay: %v", err)
	}
	for _, boundary := range []int64{created.ExpiresAt, created.ExpiresAt + 1} {
		now = boundary
		if data, err := store.PublicShare(t.Context(), created.Code); !errors.Is(err, ErrNotFound) || len(data.Items) != 0 || data.WriteToken != "" {
			t.Fatalf("expired read leaked data: %#v %v", data, err)
		}
		for _, key := range []string{"guest", "new-guest"} {
			if result, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, key, "expired-request", input); !errors.Is(err, ErrNotFound) || len(result.Data) != 0 {
				t.Fatalf("expired write/replay: %#v %v", result, err)
			}
		}
	}
	item, err := store.ListItem(t.Context(), owner, "private-item")
	if err != nil || !item.PickedUp || item.Revision != 1 || item.Quantity != "2.5" {
		t.Fatalf("owner item changed: %#v %v", item, err)
	}
	assertCount(t, store.read, "SELECT count(*) FROM purchases", 0)
	assertCount(t, store.read, "SELECT count(*) FROM shared_item_events", 1)
	assertCount(t, store.read, "SELECT count(*) FROM shared_item_results", 1)
	if _, err := store.write.ExecContext(t.Context(), `UPDATE shared_item_events SET revision=99`); err == nil {
		t.Fatal("guest audit is mutable")
	}
}

func TestShareCollisionNeverReusesExpiredCode(t *testing.T) {
	store, _, owner, items := shareFixture(t)
	first := createTestShare(t, store, owner, "first-share", "AbCdE", items)
	// Advance this record to an expired lifetime without changing its tombstone.
	if _, err := store.write.ExecContext(t.Context(), `UPDATE temporary_shares SET created_at=1,expires_at=?`, 1+ShareLifetime.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	_, err := store.Write(t.Context(), owner, testMutation("colliding-share"), func(tx *Tx) ([]byte, error) {
		share, err := tx.CreateShare("private-list", 1, items, func() (string, error) {
			attempts++
			if attempts == 1 {
				return first.Code, nil
			}
			return "ZyxWV", nil
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(share)
	})
	if err != nil || attempts != 2 {
		t.Fatalf("collision not retried: %d %v", attempts, err)
	}
	if _, err := store.PublicShare(t.Context(), first.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old code became usable: %v", err)
	}
	if _, err := store.PublicShare(t.Context(), "ZyxWV"); err != nil {
		t.Fatal(err)
	}
	attempts = 0
	_, err = store.Write(t.Context(), owner, testMutation("exhausted-share"), func(tx *Tx) ([]byte, error) {
		_, err := tx.CreateShare("private-list", 1, items, func() (string, error) { attempts++; return "AbCdE", nil })
		return []byte(`{}`), err
	})
	if err == nil || attempts != 16 {
		t.Fatalf("collision loop unbounded or succeeded: %d %v", attempts, err)
	}
	assertCount(t, store.read, "SELECT count(*) FROM temporary_shares", 2)
	assertCount(t, store.read, "SELECT count(*) FROM idempotency_results WHERE key='exhausted-share'", 0)
}

func TestShareConcurrentRetriesAndRevisionConflicts(t *testing.T) {
	store, _, owner, items := shareFixture(t)
	created := createTestShare(t, store, owner, "share", "AbCdE", items)
	share, err := store.PublicShare(t.Context(), created.Code)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan MutationResult, 8)
	errorsFound := make(chan error, 8)
	input := SharedItemInput{Position: 1, PickedUp: true, ExpectedRevision: 1}
	for range 8 {
		group.Go(func() {
			result, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, "same", "retry", input)
			results <- result
			errorsFound <- err
		})
	}
	group.Wait()
	for range 8 {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
		if result := <-results; !bytes.Contains(result.Data, []byte(`"revision":2`)) {
			t.Fatalf("retry produced another revision: %s", result.Data)
		}
	}
	input.ExpectedRevision = 2
	input.PickedUp = false
	for i := range 2 {
		group.Go(func() {
			_, err := store.SetSharedItem(t.Context(), created.Code, share.WriteToken, fmt.Sprint("race-", i), "race", input)
			errorsFound <- err
		})
	}
	group.Wait()
	conflicts := 0
	for range 2 {
		err := <-errorsFound
		if errors.Is(err, ErrConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("concurrent stale writes: %d conflicts", conflicts)
	}
	assertCount(t, store.read, "SELECT count(*) FROM shared_item_events", 2)
}
