package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

func (s *Store) Accept(ctx context.Context, request ports.AcceptRequest) (ports.Acceptance, error) {
	if request.Mode == "" {
		request.Mode = domain.ModeDurable
	}
	now := s.now()
	if err := s.validateAcceptance(request, now); err != nil {
		return ports.Acceptance{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	defer tx.Rollback()
	dedup := request.Dedup
	if _, err := tx.ExecContext(ctx, "DELETE FROM deduplication WHERE source_id = ? AND key_hash = ? AND expires_at <= ?", dedup.SourceID, dedup.KeyHash, now.UnixNano()); err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	var existing ports.Acceptance
	var fingerprint string
	var ids []byte
	err = tx.QueryRowContext(ctx, "SELECT event_id, delivery_ids, fingerprint, mode FROM deduplication WHERE source_id = ? AND key_hash = ?", dedup.SourceID, dedup.KeyHash).Scan(&existing.EventID, &ids, &fingerprint, &existing.Mode)
	if err == nil {
		if fingerprint != dedup.Fingerprint {
			return ports.Acceptance{}, ports.ErrDedupConflict
		}
		if err := json.Unmarshal(ids, &existing.DeliveryIDs); err != nil {
			return ports.Acceptance{}, dbError(err)
		}
		existing.Duplicate = true
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ports.Acceptance{}, dbError(err)
	}
	event := request.Event
	metadataExpiry, _ := addTime(event.AcceptedAt, s.metadataRetention)
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id, source_id, source_revision, accepted_at, original_ttl, original_expires_at, metadata_expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.SourceID, event.SourceRevision, event.AcceptedAt.UnixNano(), event.OriginalTTL, timeValue(event.OriginalExpiresAt), metadataExpiry.UnixNano())
	if err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	// TTL 0 never enters the encryption or INSERT path, even transiently.
	if event.OriginalTTL != domain.NeverPersist {
		_, err = tx.ExecContext(ctx, "INSERT INTO originals(event_id, ciphertext) VALUES (?, ?)", event.ID, s.encrypt("original", string(event.ID), request.Original))
		if err != nil {
			return ports.Acceptance{}, dbError(err)
		}
	}
	accepted := ports.Acceptance{EventID: event.ID, DeliveryIDs: make([]domain.ID, 0, len(request.Deliveries)+len(request.ImmediateDeliveryIDs)), Mode: request.Mode}
	for _, delivery := range request.Deliveries {
		refs, _ := json.Marshal(frozenStatus(delivery.Snapshot))
		_, err = tx.ExecContext(ctx, `INSERT INTO deliveries(id, event_id, source_id, created_at, ttl, expires_at, metadata_expires_at, state, next_attempt_at, frozen_refs) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			delivery.ID, event.ID, event.SourceID, delivery.CreatedAt.UnixNano(), delivery.TTL, timeValue(delivery.ExpiresAt), metadataExpiry.UnixNano(), domain.DeliveryPending, delivery.NextAttemptAt.UnixNano(), string(refs))
		if err != nil {
			return ports.Acceptance{}, dbError(err)
		}
		plaintext, err := json.Marshal(delivery.Snapshot)
		if err != nil {
			return ports.Acceptance{}, invalid("frozen delivery")
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO delivery_payloads(delivery_id, ciphertext) VALUES (?, ?)", delivery.ID, s.encrypt("delivery", string(delivery.ID), plaintext))
		if err != nil {
			return ports.Acceptance{}, dbError(err)
		}
		accepted.DeliveryIDs = append(accepted.DeliveryIDs, delivery.ID)
	}
	for _, id := range request.ImmediateDeliveryIDs {
		_, err = tx.ExecContext(ctx, `INSERT INTO deliveries(id, event_id, source_id, created_at, ttl, metadata_expires_at, state, next_attempt_at) VALUES (?, ?, ?, ?, 0, ?, 'unknown', ?)`, id, event.ID, event.SourceID, event.AcceptedAt.UnixNano(), metadataExpiry.UnixNano(), event.AcceptedAt.UnixNano())
		if err != nil {
			return ports.Acceptance{}, dbError(err)
		}
		accepted.DeliveryIDs = append(accepted.DeliveryIDs, id)
	}
	ids, _ = json.Marshal(accepted.DeliveryIDs)
	_, err = tx.ExecContext(ctx, "INSERT INTO deduplication(source_id, key_hash, fingerprint, event_id, delivery_ids, expires_at, mode) VALUES (?, ?, ?, ?, ?, ?, ?)", dedup.SourceID, dedup.KeyHash, dedup.Fingerprint, event.ID, string(ids), dedup.ExpiresAt.UnixNano(), request.Mode)
	if err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	if err := tx.Commit(); err != nil {
		return ports.Acceptance{}, dbError(err)
	}
	if len(request.Deliveries) > 0 {
		s.signal()
	}
	return accepted, nil
}

func (s *Store) validateAcceptance(request ports.AcceptRequest, now time.Time) error {
	event, dedup := request.Event, request.Dedup
	if request.Mode != domain.ModeDurable && request.Mode != domain.ModeImmediate || request.Mode == domain.ModeDurable && len(request.ImmediateDeliveryIDs) != 0 || request.Mode == domain.ModeImmediate && len(request.Deliveries) != 0 {
		return invalid("delivery mode")
	}
	for _, id := range request.ImmediateDeliveryIDs {
		if !validID(string(id)) {
			return invalid("immediate identity")
		}
	}
	if !validTime(now) || !validTime(event.AcceptedAt) || event.AcceptedAt.After(now) {
		return invalid("acceptance time")
	}
	if !validID(string(event.ID)) || !validID(string(event.SourceID)) || event.SourceRevision == 0 || event.SourceRevision > math.MaxInt64 {
		return invalid("event identity")
	}
	expiry, err := expiresAt(event.AcceptedAt, int64(event.OriginalTTL))
	if err != nil || !sameExpiry(event.OriginalExpiresAt, expiry) {
		return invalid("original retention")
	}
	if expiry != nil && !now.Before(*expiry) {
		return invalid("expired original")
	}
	if event.OriginalTTL == domain.NeverPersist && request.Original != nil {
		return invalid("original TTL zero requires nil bytes")
	}
	if event.OriginalTTL != domain.NeverPersist && !json.Valid(request.Original) {
		return invalid("original JSON")
	}
	if _, err := addTime(event.AcceptedAt, s.metadataRetention); err != nil {
		return invalid("metadata retention")
	}
	maxDedup, err := addTime(event.AcceptedAt, s.dedupRetention)
	if err != nil {
		return invalid("deduplication")
	}
	if dedup.SourceID != event.SourceID || !validDigest(dedup.KeyHash) || !validDigest(dedup.Fingerprint) {
		return invalid("deduplication")
	}
	if !validTime(dedup.ExpiresAt) || !dedup.ExpiresAt.After(now) || dedup.ExpiresAt.After(maxDedup) {
		return invalid("deduplication")
	}
	for _, delivery := range request.Deliveries {
		if !validID(string(delivery.ID)) || delivery.EventID != event.ID || delivery.SourceID != event.SourceID {
			return invalid("delivery identity")
		}
		if !delivery.CreatedAt.Equal(event.AcceptedAt) || delivery.TTL == domain.NeverPersist {
			return invalid("delivery retention")
		}
		expiry, err := expiresAt(event.AcceptedAt, int64(delivery.TTL))
		if err != nil || !sameExpiry(delivery.ExpiresAt, expiry) {
			return invalid("delivery retention")
		}
		if delivery.State != domain.DeliveryPending || delivery.Attempt != 0 || delivery.LastErrorCode != "" || delivery.HTTPStatusCode != 0 || !validTime(delivery.NextAttemptAt) || delivery.NextAttemptAt.Before(event.AcceptedAt) {
			return invalid("initial delivery state")
		}
		if expiry != nil && (!now.Before(*expiry) || !delivery.NextAttemptAt.Before(*expiry)) {
			return invalid("expired delivery")
		}
		if err := validateSnapshot(delivery.Snapshot, event); err != nil {
			return err
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateSnapshot(snapshot domain.FrozenDelivery, event domain.Event) error {
	if snapshot.Source.ID != event.SourceID || snapshot.Source.Revision != event.SourceRevision {
		return invalid("frozen delivery")
	}
	transporter := snapshot.Transporter
	transporterRef := domain.RevisionRef{ID: transporter.ID, Revision: transporter.Revision}
	if !validReference(snapshot.Route) || !validReference(snapshot.Template) || !validReference(transporterRef) {
		return invalid("frozen delivery")
	}
	if transporter.SourceID != event.SourceID || transporter.Kind != domain.TransporterHTTP || !json.Valid(snapshot.Body) {
		return invalid("frozen delivery")
	}
	for _, rule := range snapshot.Rules {
		if !validReference(rule) {
			return invalid("frozen rule")
		}
	}
	return nil
}

func validReference(ref domain.RevisionRef) bool {
	return validID(string(ref.ID)) && ref.Revision > 0 && ref.Revision <= math.MaxInt64
}

func (s *Store) Event(ctx context.Context, id domain.ID) (domain.Event, error) {
	var event domain.Event
	var accepted int64
	var expiry sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT id, source_id, source_revision, accepted_at, original_ttl, original_expires_at, replay_of FROM events WHERE id = ?", id).Scan(&event.ID, &event.SourceID, &event.SourceRevision, &accepted, &event.OriginalTTL, &expiry, &event.ReplayOf)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Event{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.Event{}, dbError(err)
	}
	event.AcceptedAt, event.OriginalExpiresAt = time.Unix(0, accepted).UTC(), optionalTime(expiry)
	return event, nil
}

func (s *Store) Original(ctx context.Context, id domain.ID) ([]byte, error) {
	now := s.now()
	if !validTime(now) {
		return nil, ErrInvalid
	}
	var ciphertext []byte
	err := s.db.QueryRowContext(ctx, `SELECT o.ciphertext FROM originals o JOIN events e ON e.id = o.event_id WHERE e.id = ? AND (e.original_expires_at IS NULL OR e.original_expires_at > ?)`, id, now.UnixNano()).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, dbError(err)
	}
	return s.decrypt("original", string(id), ciphertext)
}

func (s *Store) Delivery(ctx context.Context, id domain.ID) (domain.Delivery, error) {
	now := s.now()
	if !validTime(now) {
		return domain.Delivery{}, ErrInvalid
	}
	return s.readDelivery(ctx, s.db, id, now)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) readDelivery(ctx context.Context, q queryer, id domain.ID, now time.Time) (domain.Delivery, error) {
	var delivery domain.Delivery
	var created, next int64
	var expiry sql.NullInt64
	var ciphertext []byte
	err := q.QueryRowContext(ctx, `SELECT d.id, d.event_id, d.source_id, d.created_at, d.ttl, d.expires_at, d.state, d.attempt, d.next_attempt_at, d.last_error_code, d.http_status_code, p.ciphertext, d.retry_of FROM deliveries d LEFT JOIN delivery_payloads p ON p.delivery_id = d.id WHERE d.id = ?`, id).Scan(
		&delivery.ID, &delivery.EventID, &delivery.SourceID, &created, &delivery.TTL, &expiry, &delivery.State, &delivery.Attempt, &next, &delivery.LastErrorCode, &delivery.HTTPStatusCode, &ciphertext, &delivery.RetryOf)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Delivery{}, ports.ErrNotFound
	}
	if err != nil {
		return domain.Delivery{}, dbError(err)
	}
	delivery.CreatedAt, delivery.NextAttemptAt, delivery.ExpiresAt = time.Unix(0, created).UTC(), time.Unix(0, next).UTC(), optionalTime(expiry)
	if delivery.TTL == domain.NeverPersist {
		return delivery, nil
	}
	if delivery.ExpiresAt != nil && !now.Before(*delivery.ExpiresAt) {
		if delivery.State == domain.DeliveryPending || delivery.State == domain.DeliveryLeased {
			delivery.State = domain.DeliveryExpired
		}
		return delivery, nil
	}
	if ciphertext == nil {
		return domain.Delivery{}, ErrCiphertext
	}
	plaintext, err := s.decrypt("delivery", string(id), ciphertext)
	if err != nil {
		return domain.Delivery{}, err
	}
	if err := json.Unmarshal(plaintext, &delivery.Snapshot); err != nil {
		return domain.Delivery{}, ErrCiphertext
	}
	return delivery, nil
}
