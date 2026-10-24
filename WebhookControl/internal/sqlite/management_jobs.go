package sqlite

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

type frozenReferences struct {
	Source      domain.RevisionRef   `json:"source"`
	Route       domain.RevisionRef   `json:"route"`
	Rules       []domain.RevisionRef `json:"rules"`
	Template    domain.RevisionRef   `json:"template"`
	Transporter domain.RevisionRef   `json:"transporter"`
}

func frozenStatus(f domain.FrozenDelivery) frozenReferences {
	return frozenReferences{
		Source: f.Source, Route: f.Route, Rules: f.Rules, Template: f.Template,
		Transporter: domain.RevisionRef{ID: f.Transporter.ID, Revision: f.Transporter.Revision},
	}
}

func (m *managementTx) Event(source, id domain.ID) (domain.Event, error) {
	var e domain.Event
	var at int64
	var expiry sql.NullInt64
	err := m.tx.QueryRowContext(m.ctx, "SELECT id, source_id, source_revision, accepted_at, original_ttl, original_expires_at, replay_of FROM events WHERE source_id=? AND id=?", source, id).Scan(&e.ID, &e.SourceID, &e.SourceRevision, &at, &e.OriginalTTL, &expiry, &e.ReplayOf)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ports.ErrNotFound
	}
	if err != nil {
		return e, dbError(err)
	}
	e.AcceptedAt, e.OriginalExpiresAt = time.Unix(0, at).UTC(), optionalTime(expiry)
	return e, nil
}

func (m *managementTx) Original(source, id domain.ID) ([]byte, error) {
	e, err := m.Event(source, id)
	if err != nil {
		return nil, err
	}
	if e.OriginalTTL == 0 || e.OriginalExpiresAt != nil && !m.s.now().Before(*e.OriginalExpiresAt) {
		return nil, ports.ErrPayloadGone
	}
	var ciphertext []byte
	err = m.tx.QueryRowContext(m.ctx, "SELECT ciphertext FROM originals WHERE event_id=?", id).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ports.ErrPayloadGone
	}
	if err != nil {
		return nil, dbError(err)
	}
	return m.s.decrypt("original", string(id), ciphertext)
}

func (m *managementTx) Events(source, after domain.ID, limit int) ([]domain.Event, error) {
	ids, err := m.pageIDs("events", source, "", after, limit)
	if err != nil {
		return nil, err
	}
	out := []domain.Event{}
	for _, id := range ids {
		e, err := m.Event(source, id)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (m *managementTx) DeliveryStatus(source, id domain.ID) (domain.DeliveryStatus, error) {
	var d domain.DeliveryStatus
	var at, next int64
	var expiry sql.NullInt64
	var refs []byte
	err := m.tx.QueryRowContext(m.ctx, "SELECT id, event_id, source_id, created_at, ttl, expires_at, state, attempt, next_attempt_at, last_error_code, http_status_code, retry_of, frozen_refs FROM deliveries WHERE source_id=? AND id=?", source, id).Scan(&d.ID, &d.EventID, &d.SourceID, &at, &d.TTL, &expiry, &d.State, &d.Attempt, &next, &d.LastErrorCode, &d.HTTPStatusCode, &d.RetryOf, &refs)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ports.ErrNotFound
	}
	if err != nil {
		return d, dbError(err)
	}
	var frozen frozenReferences
	if err := json.Unmarshal(refs, &frozen); err != nil {
		return d, dbError(err)
	}
	d.Source = frozen.Source
	d.Route = frozen.Route
	d.Rules = frozen.Rules
	d.Template = frozen.Template
	d.Transporter = frozen.Transporter
	d.CreatedAt = time.Unix(0, at).UTC()
	d.NextAttemptAt = time.Unix(0, next).UTC()
	d.ExpiresAt = optionalTime(expiry)
	if d.ExpiresAt != nil && !m.s.now().Before(*d.ExpiresAt) && (d.State == domain.DeliveryPending || d.State == domain.DeliveryLeased) {
		d.State = domain.DeliveryExpired
	}
	return d, nil
}

func (m *managementTx) Deliveries(source, event, after domain.ID, limit int) ([]domain.DeliveryStatus, error) {
	ids, err := m.pageIDs("deliveries", source, event, after, limit)
	if err != nil {
		return nil, err
	}
	out := []domain.DeliveryStatus{}
	for _, id := range ids {
		d, err := m.DeliveryStatus(source, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (m *managementTx) pageIDs(table string, source, event, after domain.ID, limit int) ([]domain.ID, error) {
	// table is always one of two internal literals; every query predicates source.
	query := "SELECT id FROM " + table + " WHERE source_id=? AND id>?"
	args := []any{source, after}
	if event != "" {
		query += " AND event_id=?"
		args = append(args, event)
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, min(max(limit, 1), 101))
	rows, err := m.tx.QueryContext(m.ctx, query, args...)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []domain.ID{}
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, dbError(err)
		}
		out = append(out, id)
	}
	return out, dbError(rows.Err())
}

func (m *managementTx) Delivery(source, id domain.ID) (domain.Delivery, error) {
	if _, err := m.DeliveryStatus(source, id); err != nil {
		return domain.Delivery{}, err
	}
	d, err := m.s.readDelivery(m.ctx, m.tx, id, m.s.now())
	if err != nil {
		return d, err
	}
	if d.TTL == 0 || len(d.Snapshot.Body) == 0 {
		return d, ports.ErrPayloadGone
	}
	return d, nil
}

func (m *managementTx) Receipt(principal, operation string, source domain.ID, keyHash, fingerprint string) (ports.ManagementReceipt, error) {
	var out ports.ManagementReceipt
	var stored string
	var raw []byte
	keyHash = m.receiptDigest(principal, operation, source, "key", keyHash)
	fingerprint = m.receiptDigest(principal, operation, source, "selection", fingerprint)
	err := m.tx.QueryRowContext(m.ctx, "SELECT fingerprint, result FROM management_receipts WHERE principal_id=? AND operation=? AND source_id=? AND key_hash=?", principal, operation, source, keyHash).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if err != nil {
		return out, dbError(err)
	}
	if stored != fingerprint {
		return out, ports.ErrDedupConflict
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, dbError(err)
	}
	out.Duplicate = true
	return out, nil
}

func (m *managementTx) SaveReceipt(principal, operation string, source domain.ID, keyHash, fingerprint string, out ports.ManagementReceipt) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return ErrInvalid
	}
	keyHash = m.receiptDigest(principal, operation, source, "key", keyHash)
	fingerprint = m.receiptDigest(principal, operation, source, "selection", fingerprint)
	_, err = m.tx.ExecContext(m.ctx, "INSERT INTO management_receipts VALUES (?, ?, ?, ?, ?, ?)", principal, operation, source, keyHash, fingerprint, string(raw))
	return dbError(err)
}

func (m *managementTx) receiptDigest(principal, operation string, source domain.ID, purpose, value string) string {
	raw, _ := json.Marshal([5]string{principal, operation, string(source), purpose, value})
	mac := hmac.New(sha256.New, m.s.managementMAC)
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *managementTx) Replay(r ports.AcceptRequest, parent domain.ID) error {
	if err := m.s.validateAcceptance(r, m.s.now()); err != nil {
		return err
	}
	if _, err := m.Event(r.Event.SourceID, parent); err != nil {
		return err
	}
	e := r.Event
	meta, _ := addTime(e.AcceptedAt, m.s.metadataRetention)
	_, err := m.tx.ExecContext(m.ctx, "INSERT INTO events(id,source_id,source_revision,accepted_at,original_ttl,original_expires_at,metadata_expires_at,replay_of) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", e.ID, e.SourceID, e.SourceRevision, e.AcceptedAt.UnixNano(), e.OriginalTTL, timeValue(e.OriginalExpiresAt), meta.UnixNano(), parent)
	if err != nil {
		return dbError(err)
	}
	if e.OriginalTTL != 0 {
		if _, err := m.tx.ExecContext(m.ctx, "INSERT INTO originals VALUES (?, ?)", e.ID, m.s.encrypt("original", string(e.ID), r.Original)); err != nil {
			return dbError(err)
		}
	}
	for _, d := range r.Deliveries {
		if err := m.insertDelivery(d, meta); err != nil {
			return err
		}
	}
	m.notify = len(r.Deliveries) > 0
	return nil
}

func (m *managementTx) Retry(d domain.Delivery, parent domain.ID) error {
	old, err := m.Delivery(d.SourceID, parent)
	if err != nil {
		return err
	}
	if old.State != domain.DeliveryFailed {
		return ErrInvalid
	}
	// The selected payload and expiry come from storage, never caller-provided
	// replacements. The fresh ID is the only delivery identity that changes.
	old.ID, old.RetryOf, old.CreatedAt = d.ID, parent, m.s.now()
	old.State, old.Attempt, old.LastErrorCode, old.HTTPStatusCode = domain.DeliveryPending, 0, "", 0
	old.NextAttemptAt = m.s.now()
	meta, err := addTime(old.CreatedAt, m.s.metadataRetention)
	if err != nil {
		return err
	}
	if err := m.insertDelivery(old, meta); err != nil {
		return err
	}
	m.notify = true
	return nil
}

func (m *managementTx) insertDelivery(d domain.Delivery, meta time.Time) error {
	refs, _ := json.Marshal(frozenStatus(d.Snapshot))
	_, err := m.tx.ExecContext(m.ctx, "INSERT INTO deliveries(id,event_id,source_id,created_at,ttl,expires_at,metadata_expires_at,state,next_attempt_at,retry_of,frozen_refs) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)", d.ID, d.EventID, d.SourceID, d.CreatedAt.UnixNano(), d.TTL, timeValue(d.ExpiresAt), meta.UnixNano(), d.NextAttemptAt.UnixNano(), d.RetryOf, string(refs))
	if err != nil {
		return dbError(err)
	}
	raw, err := json.Marshal(d.Snapshot)
	if err != nil {
		return ErrInvalid
	}
	_, err = m.tx.ExecContext(m.ctx, "INSERT INTO delivery_payloads VALUES (?, ?)", d.ID, m.s.encrypt("delivery", string(d.ID), raw))
	return dbError(err)
}
