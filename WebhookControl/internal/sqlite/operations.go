package sqlite

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
)

// BindDigestKey refuses accidental digest-key rotation, which would make old
// ingress receipts unreachable. The key itself is never stored in SQLite.
func (s *Store) BindDigestKey(ctx context.Context, key []byte) error {
	if len(key) != 32 {
		return ErrInvalid
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("WebhookControl/v1/ingress-digest-key-check"))
	want := mac.Sum(nil)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	var got []byte
	err = tx.QueryRowContext(ctx, "SELECT check_value FROM runtime_keys WHERE purpose='ingress-digest'").Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, "INSERT INTO runtime_keys VALUES ('ingress-digest', ?)", want)
	} else if err == nil && !hmac.Equal(want, got) {
		return errors.New("ingress digest key does not match database")
	}
	if err != nil {
		return dbError(err)
	}
	return dbError(tx.Commit())
}

func (s *Store) Ping(ctx context.Context) error { return dbError(s.db.PingContext(ctx)) }

// SourceMetrics runs inside the same transaction as the caller's grant check.
// Every aggregate predicates on that source; global disk totals are local-only.
func (m *managementTx) SourceMetrics(source domain.ID, now time.Time) (domain.SourceMetrics, error) {
	out := domain.SourceMetrics{SourceID: source, Deliveries: map[domain.DeliveryState]int64{}}
	if !validID(string(source)) || !validTime(now) {
		return out, ErrInvalid
	}
	if err := m.tx.QueryRowContext(m.ctx, "SELECT count(*) FROM events WHERE source_id=?", source).Scan(&out.RetainedEvents); err != nil {
		return out, dbError(err)
	}
	rows, err := m.tx.QueryContext(m.ctx, `
SELECT CASE WHEN expires_at <= ? AND state IN ('pending','leased') THEN 'expired' ELSE state END,
       count(*)
FROM deliveries
WHERE source_id=?
GROUP BY 1`, now.UnixNano(), source)
	if err != nil {
		return out, dbError(err)
	}
	for rows.Next() {
		var state domain.DeliveryState
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return out, dbError(err)
		}
		out.Deliveries[state] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, dbError(err)
	}
	rows.Close()
	var oldest sql.NullInt64
	if err := m.tx.QueryRowContext(m.ctx, `
SELECT min(created_at)
FROM deliveries
WHERE source_id=? AND state IN ('pending','leased') AND (expires_at IS NULL OR expires_at > ?)`,
		source, now.UnixNano()).Scan(&oldest); err != nil {
		return out, dbError(err)
	}
	if oldest.Valid {
		out.OldestQueuedAgeSeconds = max(0, float64(now.UnixNano()-oldest.Int64)/1e9)
	}
	err = m.tx.QueryRowContext(m.ctx, `
SELECT coalesce(sum(max(attempt-1,0)),0),
       count(first_attempt_at),
       coalesce(sum(max(first_attempt_at-created_at,0)/1e9),0),
       coalesce(max(max(first_attempt_at-created_at,0)/1e9),0)
FROM deliveries
WHERE source_id=?`, source).Scan(
		&out.RetryAttempts,
		&out.FirstDispatchCount,
		&out.FirstDispatchSumSeconds,
		&out.FirstDispatchMaxSeconds,
	)
	if err != nil {
		return out, dbError(err)
	}
	err = m.tx.QueryRowContext(m.ctx, `
SELECT (SELECT coalesce(sum(length(o.ciphertext)),0)
        FROM originals o JOIN events e ON e.id=o.event_id
        WHERE e.source_id=?)
     + (SELECT coalesce(sum(length(p.ciphertext)),0)
        FROM delivery_payloads p JOIN deliveries d ON d.id=p.delivery_id
        WHERE d.source_id=?)`, source, source).Scan(&out.CiphertextBytes)
	return out, dbError(err)
}

// StorageUsage is intentionally local-only: physical totals reveal other sources.
type StorageUsage struct {
	AllocatedBytes int64 `json:"allocated_bytes"`
	FreeBytes      int64 `json:"free_bytes"`
	DatabaseBytes  int64 `json:"database_bytes"`
	WALBytes       int64 `json:"wal_bytes"`
}

func (s *Store) StorageUsage(ctx context.Context) (StorageUsage, error) {
	var out StorageUsage
	var pages, pageSize, free int64
	for _, item := range []struct {
		statement string
		value     *int64
	}{
		{"PRAGMA page_count", &pages}, {"PRAGMA page_size", &pageSize}, {"PRAGMA freelist_count", &free},
	} {
		if err := s.db.QueryRowContext(ctx, item.statement).Scan(item.value); err != nil {
			return out, dbError(err)
		}
	}
	out.AllocatedBytes, out.FreeBytes = pages*pageSize, free*pageSize
	for _, item := range []struct {
		path  string
		value *int64
	}{{s.path, &out.DatabaseBytes}, {s.path + "-wal", &out.WALBytes}} {
		info, err := os.Stat(item.path)
		if err == nil {
			*item.value = info.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			return out, errors.New("cannot inspect storage usage")
		}
	}
	return out, nil
}
