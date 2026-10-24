package sqlite

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func TestImmediateReceiptsPersistNoPayloadAndCannotBeClaimed(t *testing.T) {
	s, clock, _, _ := openTest(t)
	request := requestAt(baseTime, "immediate", 0, 120)
	request.Mode, request.Deliveries = domain.ModeImmediate, nil
	request.ImmediateDeliveryIDs = []domain.ID{"immediate-a", "immediate-b"}
	accepted := mustAccept(t, s, request)
	if accepted.Mode != domain.ModeImmediate || !reflect.DeepEqual(accepted.DeliveryIDs, request.ImmediateDeliveryIDs) {
		t.Fatalf("receipt: %+v", accepted)
	}
	for _, table := range []string{"originals", "delivery_payloads"} {
		if tableCount(t, s, table) != 0 {
			t.Fatalf("immediate acceptance created %s rows", table)
		}
	}
	if _, err := s.Claim(t.Context(), baseTime, time.Minute); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("immediate receipt is claimable: %v", err)
	}
	unknown, err := s.Delivery(t.Context(), "immediate-a")
	if err != nil || unknown.State != domain.DeliveryUnknown || unknown.Attempt != 0 || unknown.TTL != 0 || len(unknown.Snapshot.Body) != 0 {
		t.Fatalf("unknown receipt: %+v, %v", unknown, err)
	}
	failed := ports.ImmediateResult{DeliveryID: "immediate-a", State: domain.DeliveryFailed, StatusCode: 503, ErrorCode: "http_retryable"}
	if err := s.RecordImmediate(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordImmediate(t.Context(), failed); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("outcome overwritten: %v", err)
	}
	metadata, err := s.Delivery(t.Context(), "immediate-a")
	if err != nil || metadata.State != domain.DeliveryFailed || metadata.HTTPStatusCode != 503 || metadata.Attempt != 1 {
		t.Fatalf("failed metadata: %+v, %v", metadata, err)
	}
	request.Mode, request.ImmediateDeliveryIDs = domain.ModeDurable, nil
	duplicate := mustAccept(t, s, request)
	if !duplicate.Duplicate || duplicate.Mode != domain.ModeImmediate || !reflect.DeepEqual(duplicate.DeliveryIDs, accepted.DeliveryIDs) {
		t.Fatalf("duplicate lost original mode: %+v", duplicate)
	}
	receipt, err := s.LookupAcceptance(t.Context(), request.Event.SourceID, request.Dedup.KeyHash, request.Dedup.Fingerprint)
	if err != nil || !receipt.Duplicate || receipt.EventID != accepted.EventID {
		t.Fatalf("lookup: %+v, %v", receipt, err)
	}
	if _, err := s.LookupAcceptance(t.Context(), request.Event.SourceID, request.Dedup.KeyHash, digest("other")); !errors.Is(err, ports.ErrDedupConflict) {
		t.Fatalf("lookup conflict: %v", err)
	}
	clock.Set(baseTime.Add(DefaultRetention))
	if _, err := s.LookupAcceptance(t.Context(), request.Event.SourceID, request.Dedup.KeyHash, request.Dedup.Fingerprint); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expired receipt visible: %v", err)
	}
}

func TestMigrateRealVersionOneDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "version-one.sqlite")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// schema is the original v1 shape: no mode or HTTP-status columns. Populate
	// it without today's acceptance code, which requires the new columns.
	if _, err := db.ExecContext(t.Context(), schema); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db, aead: aead}
	request := requestAt(baseTime, "legacy", 60, 120)
	event, delivery := request.Event, request.Deliveries[0]
	metadataExpiry := baseTime.Add(DefaultRetention).UnixNano()
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO encryption_key(id, ciphertext) VALUES (1, ?)", []any{legacy.encrypt("key", "1", []byte("WebhookControl"))}},
		{"INSERT INTO events VALUES (?, ?, ?, ?, ?, ?, ?)", []any{event.ID, event.SourceID, event.SourceRevision, baseTime.UnixNano(), event.OriginalTTL, event.OriginalExpiresAt.UnixNano(), metadataExpiry}},
		{"INSERT INTO originals VALUES (?, ?)", []any{event.ID, legacy.encrypt("original", string(event.ID), request.Original)}},
		{"INSERT INTO deliveries(id, event_id, source_id, created_at, ttl, expires_at, metadata_expires_at, state, next_attempt_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", []any{delivery.ID, event.ID, event.SourceID, baseTime.UnixNano(), delivery.TTL, delivery.ExpiresAt.UnixNano(), metadataExpiry, domain.DeliveryPending, baseTime.UnixNano()}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(delivery.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO delivery_payloads VALUES (?, ?)", delivery.ID, legacy.encrypt("delivery", string(delivery.ID), payload)); err != nil {
		t.Fatal(err)
	}
	ids, _ := json.Marshal([]domain.ID{delivery.ID})
	if _, err := db.ExecContext(t.Context(), "INSERT INTO deduplication VALUES (?, ?, ?, ?, ?, ?)", event.SourceID, request.Dedup.KeyHash, request.Dedup.Fingerprint, event.ID, string(ids), request.Dedup.ExpiresAt.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path, key, WithClock(func() time.Time { return baseTime }))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("migration version=%d err=%v", version, err)
	}
	receipt, err := migrated.LookupAcceptance(t.Context(), event.SourceID, request.Dedup.KeyHash, request.Dedup.Fingerprint)
	if err != nil || receipt.Mode != domain.ModeDurable || !reflect.DeepEqual(receipt.DeliveryIDs, []domain.ID{delivery.ID}) {
		t.Fatalf("legacy receipt: %+v, %v", receipt, err)
	}
	original, err := migrated.Original(t.Context(), event.ID)
	if err != nil || string(original) != originalJSON {
		t.Fatal("migration lost original ciphertext")
	}
	frozen, err := migrated.Delivery(t.Context(), delivery.ID)
	if err != nil || string(frozen.Snapshot.Body) != renderedJSON || frozen.State != domain.DeliveryPending {
		t.Fatalf("legacy delivery: %+v, %v", frozen, err)
	}
	if err := migrated.Manage(t.Context(), func(tx ports.ManagementTx) error {
		status, err := tx.DeliveryStatus(event.SourceID, delivery.ID)
		if err == nil && (status.Template != delivery.Snapshot.Template || status.Transporter.Revision != 5) {
			t.Fatalf("migration lost frozen refs: %+v", status)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
