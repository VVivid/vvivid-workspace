package collector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

const schemaVersion = 4

type Lock struct{ file *os.File }

// Lock file lifetime follows the process, including crashes; no stale PID
// recovery or deletion is necessary. The lock also protects token rotation.
func AcquireLock(database string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(database), 0700); err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	f, err := os.OpenFile(database+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, &Error{Code: "collector_busy"}
	}
	return &Lock{f}, nil
}

func (l *Lock) Close() error { return l.file.Close() }

type Store struct{ db *sql.DB }

func storageError(err error) error {
	e := &Error{Code: "storage_error"}
	if sqliteError, ok := errors.AsType[*sqlite.Error](err); ok {
		e.SQLiteCode = sqliteError.Code()
	}
	return e
}

// Existing-database status reads must remain available while an import holds
// the writer lock. This path does not initialize or migrate a database.
func OpenReadOnlyStore(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, &Error{Code: "unsafe_database_path"}
	}
	u := url.URL{Scheme: "file", Path: abs}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, &Error{Code: "storage_error"}
	}
	if version != schemaVersion {
		db.Close()
		return nil, &Error{Code: "unsupported_database_schema"}
	}
	return &Store{db}, nil
}

func OpenStore(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, &Error{Code: "unsafe_database_path"}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	f.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	u := url.URL{Scheme: "file", Path: abs}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	if err := s.migrate(ctx, abs); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context, path string) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return &Error{Code: "storage_error"}
	}
	if version > schemaVersion {
		return &Error{Code: "database_newer_than_binary"}
	}
	if version < schemaVersion {
		var tables int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return &Error{Code: "storage_error"}
		}
		if version == 0 && tables != 0 {
			return &Error{Code: "unrecognized_database"}
		}
		// VACUUM INTO is a consistent SQLite snapshot, including committed WAL
		// content. Every future schema migration must retain this gate.
		info, err := os.Stat(path)
		if err != nil {
			return &Error{Code: "storage_error"}
		}
		if info.Size() > 0 {
			backup := fmt.Sprintf("%s.pre-v%d-%s.bak", path, schemaVersion, time.Now().UTC().Format("20060102T150405.000000000Z"))
			if err := s.backup(ctx, backup); err != nil {
				return &Error{Code: "migration_backup_failed"}
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return &Error{Code: "storage_error"}
		}
		defer tx.Rollback()
		if version == 0 {
			if _, err := tx.ExecContext(ctx, schema); err != nil {
				return &Error{Code: "migration_failed"}
			}
		} else {
			if version == 1 {
				if err := migrateV1ToV2(ctx, tx); err != nil {
					return err
				}
			}
			if version <= 2 {
				if err := migrateV2ToV3(ctx, tx); err != nil {
					return err
				}
			}
			if err := migrateV3ToV4(ctx, tx); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return &Error{Code: "migration_failed"}
		}
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;"); err != nil {
		return &Error{Code: "storage_error"}
	}
	return nil
}

const schema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE runs (
 id INTEGER PRIMARY KEY, command TEXT NOT NULL, started_at TEXT NOT NULL,
 finished_at TEXT, status TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '',
 completed_windows INTEGER NOT NULL DEFAULT 0, unavailable_windows INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE windows (
 id TEXT PRIMARY KEY, stream TEXT NOT NULL, since_date TEXT NOT NULL, until_date TEXT NOT NULL,
 purpose TEXT NOT NULL CHECK(purpose IN ('sync','backfill')), daily_date TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, attempt_run INTEGER REFERENCES runs(id), next_page_token TEXT NOT NULL DEFAULT '',
 pages INTEGER NOT NULL DEFAULT 0, success_at TEXT, record_count INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT '', UNIQUE(stream, since_date, until_date, purpose),
 CHECK((purpose='sync' AND daily_date!='') OR (purpose='backfill' AND daily_date=''))
);
CREATE INDEX windows_stream_coverage ON windows(stream, since_date, until_date, state);
CREATE TABLE records (
 stream TEXT NOT NULL, record_key TEXT NOT NULL, data_type TEXT NOT NULL, kind TEXT NOT NULL,
 anchor TEXT NOT NULL, start_time TEXT NOT NULL, end_time TEXT NOT NULL,
 observation_date TEXT NOT NULL, source_json TEXT NOT NULL, source_family TEXT NOT NULL,
 original_json TEXT NOT NULL, has_value INTEGER NOT NULL,
 imported_at TEXT NOT NULL, PRIMARY KEY(stream, record_key)
);
CREATE INDEX records_type_time ON records(data_type, start_time, end_time);
CREATE INDEX records_stream_anchor ON records(stream, anchor);
CREATE INDEX records_date ON records(observation_date, data_type);
CREATE INDEX records_source ON records(source_json, data_type);
CREATE TABLE staged_records (
 window_id TEXT NOT NULL REFERENCES windows(id) ON DELETE CASCADE,
 record_key TEXT NOT NULL, anchor TEXT NOT NULL, start_time TEXT NOT NULL,
 end_time TEXT NOT NULL, observation_date TEXT NOT NULL, source_json TEXT NOT NULL,
 original_json TEXT NOT NULL, has_value INTEGER NOT NULL, PRIMARY KEY(window_id, record_key)
);
CREATE TABLE page_tokens (
 window_id TEXT NOT NULL REFERENCES windows(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL, PRIMARY KEY(window_id, token_hash)
);
CREATE TABLE stream_state (stream TEXT PRIMARY KEY, sync_until TEXT NOT NULL);
PRAGMA user_version=4;
`

// V1 shared window IDs between daily imports and backfills. Preserve the latest
// attempt's owner from runs, remap its staged transfer intact, and keep published
// records unchanged. The backup is taken before this transaction starts.
func migrateV1ToV2(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE windows_v2 (
 id TEXT PRIMARY KEY, stream TEXT NOT NULL, since_date TEXT NOT NULL, until_date TEXT NOT NULL,
 purpose TEXT NOT NULL CHECK(purpose IN ('sync','backfill')), daily_date TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, attempt_run INTEGER REFERENCES runs(id), next_page_token TEXT NOT NULL DEFAULT '',
 pages INTEGER NOT NULL DEFAULT 0, success_at TEXT, record_count INTEGER NOT NULL DEFAULT 0,
 error_code TEXT NOT NULL DEFAULT '', UNIQUE(stream, since_date, until_date, purpose),
 CHECK((purpose='sync' AND daily_date!='') OR (purpose='backfill' AND daily_date=''))
);
CREATE TABLE staged_records_v2 (
 window_id TEXT NOT NULL REFERENCES windows_v2(id) ON DELETE CASCADE,
 record_key TEXT NOT NULL, anchor TEXT NOT NULL, start_time TEXT NOT NULL,
 end_time TEXT NOT NULL, observation_date TEXT NOT NULL, source_json TEXT NOT NULL,
 original_json TEXT NOT NULL, has_value INTEGER NOT NULL, PRIMARY KEY(window_id, record_key)
);
CREATE TABLE page_tokens_v2 (
 window_id TEXT NOT NULL REFERENCES windows_v2(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL, PRIMARY KEY(window_id, token_hash)
);`)
	if err != nil {
		return &Error{Code: "migration_failed"}
	}
	type legacyWindow struct {
		id, stream, since, until         string
		attemptCommand, attemptStartedAt string
	}
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.stream,w.since_date,w.until_date,
 COALESCE(r.command,''),COALESCE(r.started_at,'') FROM windows w LEFT JOIN runs r ON r.id=w.attempt_run`)
	if err != nil {
		return &Error{Code: "migration_failed"}
	}
	var legacyWindows []legacyWindow
	for rows.Next() {
		var w legacyWindow
		if err := rows.Scan(&w.id, &w.stream, &w.since, &w.until, &w.attemptCommand, &w.attemptStartedAt); err != nil {
			rows.Close()
			return &Error{Code: "migration_failed"}
		}
		legacyWindows = append(legacyWindows, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return &Error{Code: "migration_failed"}
	}
	for _, old := range legacyWindows {
		w := Window{Since: old.since, Until: old.until}
		id := old.id
		if old.attemptCommand == "sync" {
			end, err := ParseDate(old.until)
			started, timeErr := time.Parse(time.RFC3339Nano, old.attemptStartedAt)
			if err != nil || timeErr != nil || old.since == "" {
				return &Error{Code: "migration_failed"}
			}
			// V1 did not retain the first attempt's partial date. The earlier
			// of its last included date and last attempt date is conservative.
			w.DailyDate = min(end.AddDate(0, 0, -1).Format(time.DateOnly), started.UTC().Format(time.DateOnly))
			id = windowID(old.stream, w)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO windows_v2
 (id,stream,since_date,until_date,purpose,daily_date,state,attempt_run,next_page_token,pages,success_at,record_count,error_code)
 SELECT ?,stream,since_date,until_date,?,?,state,attempt_run,next_page_token,pages,success_at,record_count,error_code
 FROM windows WHERE id=?`, id, w.purpose(), w.DailyDate, old.id)
		if err != nil {
			return &Error{Code: "migration_failed"}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO staged_records_v2
 (window_id,record_key,anchor,start_time,end_time,observation_date,source_json,original_json,has_value)
 SELECT ?,record_key,anchor,start_time,end_time,observation_date,source_json,original_json,has_value
 FROM staged_records WHERE window_id=?`, id, old.id); err != nil {
			return &Error{Code: "migration_failed"}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO page_tokens_v2 (window_id,token_hash)
 SELECT ?,token_hash FROM page_tokens WHERE window_id=?`, id, old.id); err != nil {
			return &Error{Code: "migration_failed"}
		}
	}
	// Swap the transfer tables and reopen the last partial date together. An
	// earlier unfinished daily range must also remain on the catchup path.
	_, err = tx.ExecContext(ctx, `DROP TABLE page_tokens; DROP TABLE staged_records; DROP TABLE windows;
ALTER TABLE windows_v2 RENAME TO windows;
ALTER TABLE staged_records_v2 RENAME TO staged_records;
ALTER TABLE page_tokens_v2 RENAME TO page_tokens;
CREATE INDEX windows_stream_coverage ON windows(stream,since_date,until_date,state);
UPDATE stream_state SET sync_until=MIN(date(sync_until,'-1 day'),COALESCE(
 (SELECT MIN(since_date) FROM windows WHERE stream=stream_state.stream AND purpose='sync' AND state!='complete'),sync_until));
PRAGMA user_version=2;`)
	if err != nil {
		return &Error{Code: "migration_failed"}
	}
	return nil
}

// V2 used UTC's current date for civil transfers. Reopen one civil date while
// keeping pending request bounds, IDs, staged pages, and cursors unchanged.
// Existing history bounds remain pinned; only new plans use separate clocks.
func migrateV2ToV3(ctx context.Context, tx *sql.Tx) error {
	for _, m := range Metrics() {
		if !m.usesCivilDates() {
			continue
		}
		for _, statement := range []string{
			"UPDATE windows SET daily_date=date(daily_date,'-1 day') WHERE stream=? AND purpose='sync'",
			"UPDATE stream_state SET sync_until=date(sync_until,'-1 day') WHERE stream=?",
		} {
			if _, err := tx.ExecContext(ctx, statement, m.Stream()); err != nil {
				return &Error{Code: "migration_failed"}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value)
 SELECT 'history_civil_until',value FROM meta WHERE key='history_until';
 PRAGMA user_version=3;`); err != nil {
		return &Error{Code: "migration_failed"}
	}
	return nil
}

// Older schemas did not track whether a pending snapshot predates another
// overlapping publication. Restart only those ambiguous transfers; their bounds,
// partial dates, failure evidence, and the published view remain unchanged.
func migrateV3ToV4(ctx context.Context, tx *sql.Tx) error {
	const pendingOverlaps = `SELECT pending.id FROM windows pending
 WHERE pending.state!='complete' AND EXISTS (
 SELECT 1 FROM windows published WHERE published.stream=pending.stream
 AND published.id!=pending.id AND published.success_at IS NOT NULL
 AND pending.since_date<published.until_date
 AND (published.since_date='' OR pending.until_date>published.since_date))`
	if err := resetWindowTransfers(ctx, tx, pendingOverlaps); err != nil {
		return &Error{Code: "migration_failed"}
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
		return &Error{Code: "migration_failed"}
	}
	return nil
}

func (s *Store) backup(ctx context.Context, destination string) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return &Error{Code: "backup_failed"}
	}
	if err := os.Chmod(destination, 0600); err != nil {
		return &Error{Code: "backup_failed"}
	}
	return nil
}

func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", &Error{Code: "storage_error"}
	}
	return value, nil
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	if err != nil {
		return &Error{Code: "storage_error"}
	}
	return nil
}

// SetMetaValues saves related history settings together. A failed update must
// not leave the source and aggregate plans with different selected bounds.
func (s *Store) SetMetaValues(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return storageError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return storageError(err)
	}
	return nil
}

func (s *Store) PinIdentity(ctx context.Context, id string) error {
	previous, err := s.Meta(ctx, "health_user_id")
	if err != nil {
		return err
	}
	if previous != "" && previous != id {
		return &Error{Code: "identity_mismatch"}
	}
	return s.SetMeta(ctx, "health_user_id", id)
}

func (s *Store) StartRun(ctx context.Context, command string) (int64, error) {
	_, err := s.db.ExecContext(ctx, "UPDATE runs SET status='interrupted', finished_at=? WHERE status='running'", timestamp())
	if err != nil {
		return 0, &Error{Code: "storage_error"}
	}
	result, err := s.db.ExecContext(ctx, "INSERT INTO runs(command,started_at,status) VALUES(?,?,'running')", command, timestamp())
	if err != nil {
		return 0, &Error{Code: "storage_error"}
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, &Error{Code: "storage_error"}
	}
	return id, nil
}

func (s *Store) FinishRun(ctx context.Context, id int64, result Result) error {
	_, err := s.db.ExecContext(ctx, "UPDATE runs SET status=?,finished_at=?,error_code=?,completed_windows=?,unavailable_windows=? WHERE id=?", result.Status, timestamp(), result.ErrorCode, result.Completed, result.Unavailable, id)
	if err != nil {
		return &Error{Code: "storage_error"}
	}
	return nil
}

// Transfer identifies the next page of an incomplete window. Done means the
// window was already completed and the caller did not request a refresh.
type Transfer struct {
	Token string
	Done  bool
}

func (s *Store) BeginWindow(ctx context.Context, run int64, m Metric, w Window, force bool) (Transfer, error) {
	id := w.ID(m)
	var state, token string
	err := s.db.QueryRowContext(ctx, "SELECT state,next_page_token FROM windows WHERE id=?", id).Scan(&state, &token)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, &Error{Code: "storage_error"}
	}
	if state == "complete" && !force {
		return Transfer{Done: true}, nil
	}
	if state == "complete" {
		// A refresh clears transfer state while retaining the published records.
		// Incomplete attempts keep staging and cursors unless an overlapping
		// publication invalidated their older snapshot.
		if err := s.ResetWindow(ctx, m, w); err != nil {
			return Transfer{}, err
		}
		token = ""
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO windows(id,stream,since_date,until_date,purpose,daily_date,state,attempt_run)
 VALUES(?,?,?,?,?,?,'in_progress',?) ON CONFLICT(id) DO UPDATE SET state='in_progress',attempt_run=excluded.attempt_run,error_code='',
 daily_date=CASE WHEN windows.state='complete' THEN excluded.daily_date ELSE windows.daily_date END`, id, m.Stream(), w.Since, w.Until, w.purpose(), w.DailyDate, run)
	if err != nil {
		return Transfer{}, &Error{Code: "storage_error"}
	}
	return Transfer{Token: token}, nil
}

func (s *Store) ResetWindow(ctx context.Context, m Metric, w Window) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return &Error{Code: "storage_error"}
	}
	defer tx.Rollback()
	if err := resetWindowTransfers(ctx, tx, "SELECT id FROM windows WHERE id=?", w.ID(m)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return &Error{Code: "storage_error"}
	}
	return nil
}

// windowIDs is an internal SELECT, never user SQL. Reset staging and cursor
// history together so a restart cannot mix pages from different snapshots.
func resetWindowTransfers(ctx context.Context, tx *sql.Tx, windowIDs string, args ...any) error {
	for _, statement := range []string{
		"DELETE FROM staged_records WHERE window_id IN (" + windowIDs + ")",
		"DELETE FROM page_tokens WHERE window_id IN (" + windowIDs + ")",
		"UPDATE windows SET next_page_token='',pages=0 WHERE id IN (" + windowIDs + ")",
	} {
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return storageError(err)
		}
	}
	return nil
}

func (s *Store) MarkWindow(ctx context.Context, run int64, m Metric, w Window, state, code string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO windows(id,stream,since_date,until_date,purpose,daily_date,state,attempt_run,error_code)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,attempt_run=excluded.attempt_run,error_code=excluded.error_code,
 daily_date=CASE WHEN windows.state='complete' THEN excluded.daily_date ELSE windows.daily_date END`, w.ID(m), m.Stream(), w.Since, w.Until, w.purpose(), w.DailyDate, state, run, code)
	if err != nil {
		return &Error{Code: "storage_error"}
	}
	return nil
}

// SavePage durably stages a page together with its next-page cursor. An empty
// cursor publishes all staged pages and the completed coverage in one commit.
func (s *Store) SavePage(ctx context.Context, m Metric, w Window, records []Record, next string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	id := w.ID(m)
	if next != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM page_tokens WHERE window_id=? AND token_hash=?", id, digest(next)).Scan(&exists); err != nil {
			return storageError(err)
		}
		if exists != 0 {
			return &Error{Code: "pagination_cycle"}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO page_tokens(window_id,token_hash) VALUES(?,?)", id, digest(next)); err != nil {
			return storageError(err)
		}
	}
	for _, r := range records {
		_, err := tx.ExecContext(ctx, `INSERT INTO staged_records(window_id,record_key,anchor,start_time,end_time,observation_date,source_json,original_json,has_value)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(window_id,record_key) DO UPDATE SET anchor=excluded.anchor,start_time=excluded.start_time,end_time=excluded.end_time,
 observation_date=excluded.observation_date,source_json=excluded.source_json,original_json=excluded.original_json,has_value=excluded.has_value`, id, r.Key, r.Anchor, r.Start, r.End, r.Date, r.Source, r.Original, r.HasValue)
		if err != nil {
			return storageError(err)
		}
	}
	if next != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE windows SET next_page_token=?,pages=pages+1 WHERE id=?", next, id); err != nil {
			return storageError(err)
		}
	} else {
		// Both deletion of superseded records and the successful coverage marker
		// belong to this same transaction. An empty final result is meaningful.
		if _, err := tx.ExecContext(ctx, "DELETE FROM records WHERE stream=? AND (?='' OR anchor>=?) AND anchor<?", m.Stream(), w.Since, w.Since, w.Until); err != nil {
			return storageError(err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO records(stream,record_key,data_type,kind,anchor,start_time,end_time,observation_date,source_json,source_family,original_json,has_value,imported_at)
 SELECT ?,record_key,?,?,anchor,start_time,end_time,observation_date,source_json,'users/me/dataSourceFamilies/google-sources',original_json,has_value,? FROM staged_records WHERE window_id=?
 ON CONFLICT(stream,record_key) DO UPDATE SET anchor=excluded.anchor,start_time=excluded.start_time,end_time=excluded.end_time,
 observation_date=excluded.observation_date,source_json=excluded.source_json,original_json=excluded.original_json,has_value=excluded.has_value,imported_at=excluded.imported_at`, m.Stream(), m.Name, m.Kind, timestamp(), id)
		if err != nil {
			return storageError(err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE windows SET state='complete',next_page_token='',pages=pages+1,success_at=?,
 record_count=(SELECT COUNT(*) FROM staged_records WHERE window_id=? AND has_value=1),error_code='' WHERE id=?`, timestamp(), id, id)
		if err != nil {
			return storageError(err)
		}
		for _, statement := range []string{"DELETE FROM staged_records WHERE window_id=?", "DELETE FROM page_tokens WHERE window_id=?"} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return storageError(err)
			}
		}
		// Writers are serialized for the entire import. Any other pending
		// transfer therefore began before this publication and must refetch
		// its whole snapshot, including deletions, before replacing this view.
		const pendingOverlaps = `SELECT id FROM windows WHERE stream=? AND id!=?
 AND state!='complete' AND since_date<? AND (?='' OR until_date>?)`
		if err := resetWindowTransfers(ctx, tx, pendingOverlaps, m.Stream(), id, w.Until, w.Since, w.Since); err != nil {
			return err
		}
		var dailyDate string
		if err := tx.QueryRowContext(ctx, "SELECT daily_date FROM windows WHERE id=?", id).Scan(&dailyDate); err != nil {
			return storageError(err)
		}
		if dailyDate != "" {
			// A paginated response may have started before midnight or before
			// a long outage. Its original partial date must remain eligible.
			_, err := tx.ExecContext(ctx, `INSERT INTO stream_state(stream,sync_until) VALUES(?,?)
 ON CONFLICT(stream) DO UPDATE SET sync_until=MAX(sync_until,excluded.sync_until)`, m.Stream(), min(w.Until, dailyDate))
			if err != nil {
				return storageError(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return storageError(err)
	}
	return nil
}

func (s *Store) PendingDailyWindows(ctx context.Context, m Metric) ([]Window, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT since_date,until_date,daily_date FROM windows
 WHERE stream=? AND purpose='sync' AND state!='complete' ORDER BY since_date,until_date`, m.Stream())
	if err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	defer rows.Close()
	var pending []Window
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.Since, &w.Until, &w.DailyDate); err != nil {
			return nil, &Error{Code: "storage_error"}
		}
		pending = append(pending, w)
	}
	if err := rows.Err(); err != nil {
		return nil, &Error{Code: "storage_error"}
	}
	return pending, nil
}

func (s *Store) Frontier(ctx context.Context, m Metric) (string, error) {
	var frontier sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT (SELECT sync_until FROM stream_state WHERE stream=?)", m.Stream()).Scan(&frontier)
	if err != nil {
		return "", &Error{Code: "storage_error"}
	}
	return frontier.String, nil
}

func (s *Store) AdvanceFrontier(ctx context.Context, m Metric, until string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO stream_state(stream,sync_until) VALUES(?,?)
 ON CONFLICT(stream) DO UPDATE SET sync_until=MAX(sync_until,excluded.sync_until)`, m.Stream(), until)
	if err != nil {
		return &Error{Code: "storage_error"}
	}
	return nil
}

func (s *Store) EarliestSourceDate(ctx context.Context) (string, error) {
	var value sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT MIN(substr(anchor,1,10)) FROM records WHERE kind!='rollup'").Scan(&value)
	if err != nil {
		return "", &Error{Code: "storage_error"}
	}
	return value.String, nil
}

func (s *Store) WindowComplete(ctx context.Context, m Metric, w Window) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM windows WHERE id=? AND state='complete'", w.ID(m)).Scan(&count)
	if err != nil {
		return false, &Error{Code: "storage_error"}
	}
	return count == 1, nil
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
