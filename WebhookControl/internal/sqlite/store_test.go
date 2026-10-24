package sqlite

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

var baseTime = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

const originalJSON = `{"private":"synthetic-original-never-visible-in-sqlite-pages-or-wal-7db185"}`
const renderedJSON = `{"notice":"synthetic-sanitized-body-encrypted-in-entire-frozen-record-b42510"}`

type testClock struct{ nanos atomic.Int64 }

func (c *testClock) Now() time.Time    { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *testClock) Set(now time.Time) { c.nanos.Store(now.UnixNano()) }

func openTest(t *testing.T, opts ...Option) (*Store, *testClock, string, []byte) {
	t.Helper()
	clock := new(testClock)
	clock.Set(baseTime)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queue?with-escaped-path.sqlite")
	options := append([]Option{WithClock(clock.Now)}, opts...)
	s, err := Open(path, key, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, clock, path, key
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func requestAt(now time.Time, suffix string, originalTTL, deliveryTTL domain.Retention) ports.AcceptRequest {
	originalExpiry, _ := expiresAt(now, int64(originalTTL))
	deliveryExpiry, _ := expiresAt(now, int64(deliveryTTL))
	event := domain.Event{ID: domain.ID("event-" + suffix), SourceID: "source", SourceRevision: 1, AcceptedAt: now, OriginalTTL: originalTTL, OriginalExpiresAt: originalExpiry}
	request := ports.AcceptRequest{
		Event: event,
		Dedup: domain.Deduplication{SourceID: event.SourceID, KeyHash: digest("key-" + suffix), Fingerprint: digest("input-" + suffix), ExpiresAt: now.Add(DefaultRetention)},
		Deliveries: []domain.Delivery{{
			ID: domain.ID("delivery-" + suffix), EventID: event.ID, SourceID: event.SourceID,
			CreatedAt: now, TTL: deliveryTTL, ExpiresAt: deliveryExpiry,
			State: domain.DeliveryPending, NextAttemptAt: now,
			Snapshot: domain.FrozenDelivery{
				Source: domain.RevisionRef{ID: event.SourceID, Revision: 1},
				Route:  domain.RevisionRef{ID: "route", Revision: 2}, Template: domain.RevisionRef{ID: "template", Revision: 3},
				Rules:       []domain.RevisionRef{{ID: "rule", Revision: 4}},
				Transporter: domain.Transporter{ID: "http", Revision: 5, SourceID: event.SourceID, Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: "https://receiver.example.test/hook", TimeoutSeconds: 10, SecretHeaders: map[string]domain.SecretRef{"Authorization": {Name: "receiver", Version: "v1"}}}},
				Body:        json.RawMessage(renderedJSON),
			},
		}},
	}
	if originalTTL != 0 {
		request.Original = []byte(originalJSON)
	}
	return request
}

func mustAccept(t *testing.T, s *Store, request ports.AcceptRequest) ports.Acceptance {
	t.Helper()
	accepted, err := s.Accept(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func tableCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertEmpty(t *testing.T, s *Store) {
	t.Helper()
	for _, table := range []string{"events", "originals", "deliveries", "delivery_payloads", "deduplication"} {
		if count := tableCount(t, s, table); count != 0 {
			t.Errorf("%s has %d rows after rejected transaction", table, count)
		}
	}
}

func assertNoPlaintext(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal"} {
		pages, err := os.ReadFile(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{originalJSON, renderedJSON, "synthetic-original-never-visible", "synthetic-sanitized-body-encrypted", "receiver.example.test"} {
			if bytes.Contains(pages, []byte(marker)) {
				t.Errorf("plaintext marker in database%s", suffix)
			}
		}
	}
}

func TestAcceptanceDeduplicationEncryptionAndReopen(t *testing.T) {
	s, clock, path, key := openTest(t)
	request := requestAt(baseTime, "first", 60, 120)
	second := request.Deliveries[0]
	second.ID = "delivery-fanout"
	request.Deliveries = append(request.Deliveries, second)
	accepted := mustAccept(t, s, request)
	if accepted.Duplicate || !reflect.DeepEqual(accepted.DeliveryIDs, []domain.ID{"delivery-first", "delivery-fanout"}) {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	original, err := s.Original(t.Context(), accepted.EventID)
	if err != nil || string(original) != originalJSON {
		t.Fatalf("original roundtrip: %q, %v", original, err)
	}
	delivery, err := s.Delivery(t.Context(), accepted.DeliveryIDs[0])
	if err != nil || !reflect.DeepEqual(delivery.Snapshot, request.Deliveries[0].Snapshot) {
		t.Fatalf("frozen delivery changed: %+v, %v", delivery, err)
	}
	duplicate := requestAt(baseTime, "duplicate", 60, 120)
	duplicate.Dedup = request.Dedup
	result := mustAccept(t, s, duplicate)
	if !result.Duplicate || result.EventID != accepted.EventID || !reflect.DeepEqual(result.DeliveryIDs, accepted.DeliveryIDs) {
		t.Fatalf("duplicate identities changed: %+v", result)
	}
	duplicate.Dedup.Fingerprint = digest("conflicting-input")
	if _, err := s.Accept(t.Context(), duplicate); !errors.Is(err, ports.ErrDedupConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if count := tableCount(t, s, "events"); count != 1 {
		t.Fatalf("duplicate inserted events: %d", count)
	}
	if count := tableCount(t, s, "deliveries"); count != 2 {
		t.Fatalf("duplicate inserted deliveries: %d", count)
	}
	assertNoPlaintext(t, path)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, key, WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if original, err := reopened.Original(t.Context(), accepted.EventID); err != nil || string(original) != originalJSON {
		t.Fatalf("reopened original: %q, %v", original, err)
	}
	if delivery, err := reopened.Delivery(t.Context(), accepted.DeliveryIDs[0]); err != nil || !reflect.DeepEqual(delivery.Snapshot, request.Deliveries[0].Snapshot) {
		t.Fatalf("reopened delivery changed: %+v, %v", delivery, err)
	}
}

func TestAcceptanceRollbackAndTTLZero(t *testing.T) {
	t.Run("invalid final job", func(t *testing.T) {
		s, _, _, _ := openTest(t)
		request := requestAt(baseTime, "invalid", 60, 60)
		invalid := request.Deliveries[0]
		invalid.ID, invalid.TTL, invalid.ExpiresAt = "delivery-invalid-last", 0, nil
		request.Deliveries = append(request.Deliveries, invalid)
		if _, err := s.Accept(t.Context(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("TTL zero job: %v", err)
		}
		assertEmpty(t, s)
	})
	t.Run("constraint failure rolls back previous inserts", func(t *testing.T) {
		s, _, _, _ := openTest(t)
		request := requestAt(baseTime, "constraint", 60, 60)
		request.Deliveries = append(request.Deliveries, request.Deliveries[0])
		if _, err := s.Accept(t.Context(), request); err == nil {
			t.Fatal("duplicate delivery ID accepted")
		}
		assertEmpty(t, s)
	})
	t.Run("original TTL zero never writes", func(t *testing.T) {
		s, _, path, _ := openTest(t)
		request := requestAt(baseTime, "zero", 0, 60)
		request.Original = []byte(originalJSON)
		if _, err := s.Accept(t.Context(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("original bytes accepted at TTL zero: %v", err)
		}
		assertEmpty(t, s)
		request.Original = nil
		mustAccept(t, s, request)
		if count := tableCount(t, s, "originals"); count != 0 {
			t.Fatalf("original persisted at TTL zero: %d", count)
		}
		if _, err := s.Original(t.Context(), request.Event.ID); !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("TTL zero read: %v", err)
		}
		if _, err := s.Claim(t.Context(), baseTime, time.Second); err != nil {
			t.Fatal(err)
		}
		assertNoPlaintext(t, path)
	})
}

func TestExpiryBeforeCleanupAndIndependentWindows(t *testing.T) {
	s, clock, _, _ := openTest(t, WithRetention(2*time.Hour, time.Hour))
	request := requestAt(baseTime, "expiry", 1, 2)
	request.Dedup.ExpiresAt = baseTime.Add(time.Hour)
	accepted := mustAccept(t, s, request)
	clock.Set(baseTime.Add(time.Second))
	if _, err := s.Original(t.Context(), accepted.EventID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("original readable at expiry: %v", err)
	}
	if tableCount(t, s, "originals") != 1 {
		t.Fatal("test did not cover expiry before physical cleanup")
	}
	if delivery, err := s.Delivery(t.Context(), accepted.DeliveryIDs[0]); err != nil || len(delivery.Snapshot.Body) == 0 {
		t.Fatalf("original expiry removed delivery: %+v, %v", delivery, err)
	}
	clock.Set(baseTime.Add(2 * time.Second))
	delivery, err := s.Delivery(t.Context(), accepted.DeliveryIDs[0])
	if err != nil || delivery.State != domain.DeliveryExpired || delivery.Snapshot.Body != nil {
		t.Fatalf("expired delivery exposed body: %+v, %v", delivery, err)
	}
	if tableCount(t, s, "delivery_payloads") != 1 {
		t.Fatal("test did not cover delivery expiry before cleanup")
	}
	if _, err := s.Claim(t.Context(), clock.Now(), time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("claimed expired delivery: %v", err)
	}
	if err := s.Cleanup(t.Context(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	if tableCount(t, s, "originals") != 0 || tableCount(t, s, "delivery_payloads") != 0 {
		t.Fatal("expired ciphertext survived cleanup")
	}
	duplicate := requestAt(clock.Now(), "duplicate-after-payload-expiry", 0, 2)
	duplicate.Dedup = request.Dedup
	if got := mustAccept(t, s, duplicate); !got.Duplicate || got.EventID != accepted.EventID {
		t.Fatalf("payload expiry removed deduplication: %+v", got)
	}
	if _, err := s.Event(t.Context(), accepted.EventID); err != nil {
		t.Fatalf("minimal metadata prematurely removed: %v", err)
	}
	clock.Set(baseTime.Add(time.Hour))
	if err := s.Cleanup(t.Context(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	if tableCount(t, s, "deduplication") != 0 || tableCount(t, s, "events") != 1 {
		t.Fatal("dedup and metadata windows were not independent")
	}
	reaccept := requestAt(clock.Now(), "reaccepted", 0, 2)
	reaccept.Dedup.KeyHash, reaccept.Dedup.Fingerprint = request.Dedup.KeyHash, request.Dedup.Fingerprint
	reaccept.Dedup.ExpiresAt = clock.Now().Add(time.Hour)
	if got := mustAccept(t, s, reaccept); got.Duplicate {
		t.Fatal("input could not be reaccepted after dedup window")
	}
	clock.Set(baseTime.Add(3 * time.Hour))
	if err := s.Cleanup(t.Context(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	assertEmpty(t, s)
}

func TestIndefinitePayloadsAndCiphertextIdentity(t *testing.T) {
	s, clock, path, key := openTest(t)
	first := requestAt(baseTime, "indefinite-first", -1, -1)
	second := requestAt(baseTime, "indefinite-second", -1, -1)
	mustAccept(t, s, first)
	mustAccept(t, s, second)
	clock.Set(baseTime.Add(30 * 24 * time.Hour))
	if err := s.Cleanup(t.Context(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Original(t.Context(), first.Event.ID); err != nil {
		t.Fatalf("indefinite original expired: %v", err)
	}
	if delivery, err := s.Delivery(t.Context(), first.Deliveries[0].ID); err != nil || string(delivery.Snapshot.Body) != renderedJSON {
		t.Fatalf("indefinite delivery expired: %+v, %v", delivery, err)
	}
	var firstCiphertext, secondCiphertext []byte
	if err := s.db.QueryRowContext(t.Context(), "SELECT ciphertext FROM originals WHERE event_id = ?", first.Event.ID).Scan(&firstCiphertext); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(t.Context(), "SELECT ciphertext FROM originals WHERE event_id = ?", second.Event.ID).Scan(&secondCiphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("identical plaintext reused a nonce")
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE originals SET ciphertext = ? WHERE event_id = ?", firstCiphertext, second.Event.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Original(t.Context(), second.Event.ID); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("ciphertext accepted for wrong record: %v", err)
	}
	if _, err := s.decrypt("delivery", string(first.Event.ID), firstCiphertext); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("ciphertext accepted for wrong kind: %v", err)
	}
	firstCiphertext[len(firstCiphertext)-1] ^= 1
	if _, err := s.decrypt("original", string(first.Event.ID), firstCiphertext); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("tampered ciphertext accepted: %v", err)
	}
	wrongKey := bytes.Clone(key)
	wrongKey[0] ^= 1
	if other, err := Open(path, wrongKey, WithClock(clock.Now)); !errors.Is(err, ErrCiphertext) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("wrong key accepted: %v", err)
	}
}

func TestInvalidAcceptanceInputs(t *testing.T) {
	tests := map[string]func(*ports.AcceptRequest){
		"negative original TTL":           func(r *ports.AcceptRequest) { r.Event.OriginalTTL = -2 },
		"overflow original TTL":           func(r *ports.AcceptRequest) { r.Event.OriginalTTL = domain.Retention(math.MaxInt64) },
		"zero acceptance time":            func(r *ports.AcceptRequest) { r.Event.AcceptedAt = time.Time{} },
		"future acceptance":               func(r *ports.AcceptRequest) { r.Event.AcceptedAt = baseTime.Add(time.Second) },
		"unrepresentable timestamp":       func(r *ports.AcceptRequest) { r.Event.AcceptedAt = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"false expiry":                    func(r *ports.AcceptRequest) { r.Event.OriginalExpiresAt = new(baseTime.Add(500 * time.Second)) },
		"dedup outside configured window": func(r *ports.AcceptRequest) { r.Dedup.ExpiresAt = baseTime.Add(DefaultRetention + time.Second) },
		"expired dedup":                   func(r *ports.AcceptRequest) { r.Dedup.ExpiresAt = baseTime },
		"raw dedup key":                   func(r *ports.AcceptRequest) { r.Dedup.KeyHash = originalJSON },
		"cross-source job":                func(r *ports.AcceptRequest) { r.Deliveries[0].SourceID = "other" },
		"cross-source frozen record":      func(r *ports.AcceptRequest) { r.Deliveries[0].Snapshot.Transporter.SourceID = "other" },
		"invalid rendered JSON":           func(r *ports.AcceptRequest) { r.Deliveries[0].Snapshot.Body = []byte("invalid") },
		"retry at expiry":                 func(r *ports.AcceptRequest) { r.Deliveries[0].NextAttemptAt = *r.Deliveries[0].ExpiresAt },
		"revision overflow":               func(r *ports.AcceptRequest) { r.Event.SourceRevision = domain.Revision(math.MaxUint64) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s, _, _, _ := openTest(t)
			request := requestAt(baseTime, "invalid", 60, 60)
			mutate(&request)
			if _, err := s.Accept(t.Context(), request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid input accepted: %v", err)
			}
			assertEmpty(t, s)
		})
	}
	for _, options := range [][]Option{{WithRetention(0, time.Hour)}, {WithRetention(time.Hour, -1)}, {WithClock(nil)}, {WithClock(func() time.Time { return time.Unix(0, math.MaxInt64) })}} {
		if s, err := Open(filepath.Join(t.TempDir(), "invalid.sqlite"), make([]byte, 32), options...); !errors.Is(err, ErrInvalid) {
			if s != nil {
				s.Close()
			}
			t.Fatalf("invalid Open option accepted: %v", err)
		}
	}
}
