package collector

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func reopenEngineStore(t *testing.T, e *Engine) {
	t.Helper()
	var sequence int
	var name, path string
	if err := e.Store.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = s
	t.Cleanup(func() { s.Close() })
}

func assertStoredJSON(t *testing.T, s *Store, m Metric, w Window, raw json.RawMessage) {
	t.Helper()
	want := record(t, m, w, raw)
	var got string
	if err := s.db.QueryRowContext(t.Context(), "SELECT original_json FROM records WHERE stream=? AND record_key=?", m.Stream(), want.Key).Scan(&got); err != nil || got != string(raw) {
		t.Fatalf("expected synthetic record was not stored: %v", err)
	}
}

func TestDailyPartialDateRemainsEligibleAfterNineDayOutage(t *testing.T) {
	e, a := fixture(t)
	m := e.Metrics[0]
	early := steps("early", "2026-10-20", 3)
	a.page = func(Metric, Window, string) (Page, error) { return Page{Data: []json.RawMessage{early}}, nil }
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	if frontier, err := e.Store.Frontier(t.Context(), m); err != nil || frontier != "2026-10-20" {
		t.Fatalf("partial date was marked settled: %q %v", frontier, err)
	}
	reopenEngineStore(t, e)
	e.Now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	corrected, late := steps("early", "2026-10-20", 8), steps("late", "2026-10-20", 13)
	var fetched []Window
	a.page = func(_ Metric, w Window, token string) (Page, error) {
		fetched = append(fetched, w)
		if token != "" {
			t.Fatal("completed daily window retained a cursor")
		}
		if w.Since <= "2026-10-20" && "2026-10-20" < w.Until {
			return Page{Data: []json.RawMessage{corrected, late}}, nil
		}
		return Page{}, nil
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(fetched) < 2 || fetched[0].Since != "2026-10-20" {
		t.Fatal("outage recovery skipped the last partially collected date")
	}
	assertStoredJSON(t, e.Store, m, fetched[0], corrected)
	assertStoredJSON(t, e.Store, m, fetched[0], late)
	if recordsCount(t, e.Store) != 2 {
		t.Fatal("late upload was missing or stale data survived replacement")
	}
	if frontier, err := e.Store.Frontier(t.Context(), m); err != nil || frontier != "2026-10-29" {
		t.Fatalf("recovery did not settle through the latest closed date: %q %v", frontier, err)
	}
}

func TestDailyCursorSurvivesMidnightAndOutageWithoutStaleReadiness(t *testing.T) {
	for _, days := range []int{1, 9} {
		for _, interruptedBy := range []string{"page_budget", "network_failure"} {
			t.Run(interruptedBy+"/"+(time.Duration(days)*24*time.Hour).String(), func(t *testing.T) {
				e, a := fixture(t)
				m := e.Metrics[0]
				originalDay := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -days)
				originalDate := originalDay.Format(time.DateOnly)
				observed := originalDay.AddDate(0, 0, -2).Format(time.DateOnly)
				e.Now = func() time.Time { return originalDay.Add(23*time.Hour + 55*time.Minute) }
				e.OAuth.Tokens.PublishingStatus = "production"
				old, corrected := steps("observed", observed, 2), steps("observed", observed, 7)
				a.page = func(_ Metric, w Window, _ string) (Page, error) {
					if (w.Since == "" || w.Since <= observed) && observed < w.Until {
						return Page{Data: []json.RawMessage{old}}, nil
					}
					return Page{}, nil
				}
				if _, err := e.Backfill(t.Context(), Options{AggregateSince: observed}); err != nil {
					t.Fatal(err)
				}
				if _, err := e.Sync(t.Context(), Options{}); err != nil {
					t.Fatal(err)
				}
				var original Window
				a.page = func(_ Metric, w Window, token string) (Page, error) {
					original = w
					if token == "" {
						return Page{Data: []json.RawMessage{corrected}, Next: "persisted-daily-cursor"}, nil
					}
					return Page{}, &Error{Code: "network_error"}
				}
				options := Options{}
				if interruptedBy == "page_budget" {
					options.MaxPages = 1
				}
				result, err := e.Sync(t.Context(), options)
				if interruptedBy == "page_budget" && (err != nil || result.Status != "paused") {
					t.Fatalf("daily page-budget pause failed: %+v %v", result, err)
				}
				if interruptedBy == "network_failure" && (Code(err) != "network_error" || result.Status != "failed") {
					t.Fatalf("daily network failure was not saved: %+v %v", result, err)
				}
				assertStoredJSON(t, e.Store, m, original, old)
				// An equal-range manual repair has its own state and cannot steal
				// the daily cursor or become a permanent daily readiness failure.
				a.page = func(Metric, Window, string) (Page, error) { return Page{}, &Error{Code: "network_error"} }
				if _, err := e.Backfill(t.Context(), Options{Since: original.Since, Until: original.Until}); Code(err) != "network_error" {
					t.Fatal("manual failure was not recorded independently")
				}
				reopenEngineStore(t, e)
				pending, err := e.Store.PendingDailyWindows(t.Context(), m)
				if err != nil || len(pending) != 1 || pending[0] != original {
					t.Fatal("daily request boundaries or original partial date changed on reopen")
				}
				status, err := e.Status(t.Context(), false)
				if err != nil || status.UnattendedReady || status.Streams[0].DailyPending != 1 {
					t.Fatal("unfinished daily transfer was represented as ready")
				}
				e.Now = func() time.Time { return originalDay.AddDate(0, 0, days).Add(5 * time.Minute) }
				tail, late := steps("tail", originalDate, 4), steps("late", originalDate, 11)
				calls, resumed := 0, false
				a.page = func(_ Metric, w Window, token string) (Page, error) {
					calls++
					if calls == 1 {
						if w != original || token != "persisted-daily-cursor" {
							t.Fatal("retry abandoned its original window or persisted cursor")
						}
						resumed = true
						return Page{Data: []json.RawMessage{tail}}, nil
					}
					if token != "" {
						t.Fatal("new daily request reused the old cursor")
					}
					var data []json.RawMessage
					for _, point := range []struct {
						date string
						raw  json.RawMessage
					}{{observed, corrected}, {originalDate, tail}, {originalDate, late}} {
						if w.Since <= point.date && point.date < w.Until {
							data = append(data, point.raw)
						}
					}
					return Page{Data: data}, nil
				}
				if _, err := e.Sync(t.Context(), Options{}); err != nil {
					t.Fatal(err)
				}
				if !resumed || calls < 2 || recordsCount(t, e.Store) != 3 {
					t.Fatal("daily recovery did not preserve staging and collect the late partial-date record")
				}
				assertStoredJSON(t, e.Store, m, original, corrected)
				assertStoredJSON(t, e.Store, m, original, tail)
				assertStoredJSON(t, e.Store, m, original, late)
				status, err = e.Status(t.Context(), false)
				if err != nil || !status.UnattendedReady || status.Streams[0].DailyPending != 0 || status.Streams[0].Failed != 1 {
					t.Fatalf("retry left stale daily readiness or erased manual failure evidence: %+v %v", status, err)
				}
				var staged, cursors int
				if err := e.Store.db.QueryRowContext(t.Context(), "SELECT (SELECT COUNT(*) FROM staged_records),(SELECT COUNT(*) FROM page_tokens)").Scan(&staged, &cursors); err != nil || staged != 0 || cursors != 0 {
					t.Fatal("completed daily transfer left orphan staging or page tokens")
				}
			})
		}
	}
}

func TestDailyFinalTransactionRollsBackPublishedViewAndFrontier(t *testing.T) {
	e, a := fixture(t)
	m := e.Metrics[0]
	old, corrected := steps("existing", "2026-10-20", 3), steps("existing", "2026-10-20", 8)
	late := steps("late", "2026-10-20", 13)
	a.page = func(Metric, Window, string) (Page, error) { return Page{Data: []json.RawMessage{old}}, nil }
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	e.Now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	a.page = func(Metric, Window, string) (Page, error) {
		return Page{Data: []json.RawMessage{corrected, late}}, nil
	}
	if _, err := e.Store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_frontier BEFORE UPDATE ON stream_state BEGIN SELECT RAISE(FAIL,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sync(t.Context(), Options{}); Code(err) != "storage_error" {
		t.Fatal("daily final transaction did not fail")
	}
	w := Window{Since: "2026-10-15", Until: "2026-10-22", DailyDate: "2026-10-21"}
	assertStoredJSON(t, e.Store, m, w, old)
	if recordsCount(t, e.Store) != 1 {
		t.Fatal("failed frontier update published replacement records")
	}
	if complete, err := e.Store.WindowComplete(t.Context(), m, w); err != nil || complete {
		t.Fatal("failed frontier update committed completed coverage")
	}
	if frontier, err := e.Store.Frontier(t.Context(), m); err != nil || frontier != "2026-10-20" {
		t.Fatal("failed final transaction advanced the daily frontier")
	}
	if _, err := e.Store.db.ExecContext(t.Context(), "DROP TRIGGER fail_frontier"); err != nil {
		t.Fatal(err)
	}
	e.Now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	a.page = func(_ Metric, w Window, _ string) (Page, error) {
		if w.Since <= "2026-10-20" && "2026-10-20" < w.Until {
			return Page{Data: []json.RawMessage{corrected, late}}, nil
		}
		return Page{}, nil
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	assertStoredJSON(t, e.Store, m, w, corrected)
	assertStoredJSON(t, e.Store, m, w, late)
}

func openV1Fixture(t *testing.T, invalidDate bool) (string, *sql.DB, Metric, Window, json.RawMessage) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "health.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema, err := os.ReadFile("testdata/schema-v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), string(schema)); err != nil {
		t.Fatal(err)
	}
	m := metric("steps", "interval", "activity_and_fitness")
	w := Window{Since: "2026-10-14", Until: "2026-10-21"}
	started := "2026-10-20T12:00:00Z"
	if invalidDate {
		started = "invalid-saved-timestamp"
	}
	if _, err := db.ExecContext(t.Context(), `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
 INSERT INTO runs(id,command,started_at,status) VALUES(1,'sync',?,'failed');
 INSERT INTO runs(id,command,started_at,status) VALUES(2,'backfill','2026-10-19T12:00:00Z','failed');
 INSERT INTO meta(key,value) VALUES('sync_origin','2026-10-14');`, started); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO windows(id,stream,since_date,until_date,state,attempt_run,next_page_token,pages)
 VALUES(?,?,?,?,'failed',1,'v1-daily-cursor',1)`, w.ID(m), m.Stream(), w.Since, w.Until); err != nil {
		t.Fatal(err)
	}
	staged := record(t, m, w, steps("staged", "2026-10-14", 7))
	if _, err := db.ExecContext(t.Context(), `INSERT INTO staged_records VALUES(?,?,?,?,?,?,?,?,?)`, w.ID(m), staged.Key, staged.Anchor, staged.Start, staged.End, staged.Date, staged.Source, staged.Original, staged.HasValue); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO page_tokens VALUES(?,?)", w.ID(m), digest("v1-daily-cursor")); err != nil {
		t.Fatal(err)
	}
	history := Window{Since: "2026-10-01", Until: "2026-10-03"}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO windows(id,stream,since_date,until_date,state,attempt_run,error_code) VALUES(?,?,?,?,'failed',2,'network_error')`, history.ID(m), m.Stream(), history.Since, history.Until); err != nil {
		t.Fatal(err)
	}
	published := steps("published", "2026-10-20", 3)
	r := record(t, m, w, published)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO records VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.Stream(), r.Key, m.Name, m.Kind, r.Anchor, r.Start, r.End, r.Date, r.Source, "users/me/dataSourceFamilies/google-sources", r.Original, r.HasValue, started); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO stream_state VALUES(?,'2026-10-21')", m.Stream()); err != nil {
		t.Fatal(err)
	}
	return path, db, m, w, published
}

func TestV1MigrationPreservesPublishedDataAndRemapsDailyCursor(t *testing.T) {
	path, _, m, old, published := openV1Fixture(t, false)
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertStoredJSON(t, s, m, old, published)
	backups, err := filepath.Glob(path + ".pre-v4-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatal("schema v1 was changed without one consistent backup")
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("migration backup permissions are not restricted")
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version, staged, tokens int
	if err := backup.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("migration backup is not the original v1 database")
	}
	var cursor, original string
	if err := backup.QueryRowContext(t.Context(), "SELECT next_page_token FROM windows WHERE id=?", old.ID(m)).Scan(&cursor); err != nil || cursor != "v1-daily-cursor" {
		t.Fatal("backup omitted a WAL-committed daily cursor")
	}
	if err := backup.QueryRowContext(t.Context(), "SELECT original_json FROM records").Scan(&original); err != nil || original != string(published) {
		t.Fatal("backup omitted the published view")
	}
	w := old
	w.DailyDate = "2026-10-20"
	if w.ID(m) == old.ID(m) {
		t.Fatal("daily and historical window namespaces still overlap")
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT next_page_token,
 (SELECT COUNT(*) FROM staged_records WHERE window_id=windows.id),
 (SELECT COUNT(*) FROM page_tokens WHERE window_id=windows.id AND token_hash=?) FROM windows WHERE id=?`, digest("v1-daily-cursor"), w.ID(m)).Scan(&cursor, &staged, &tokens); err != nil || cursor != "v1-daily-cursor" || staged != 1 || tokens != 1 {
		t.Fatal("migration lost daily staging, cursor, or cycle detection state")
	}
	if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != "2026-10-14" {
		t.Fatal("migration did not conservatively reopen the unfinished daily range")
	}
	rows, err := s.db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration left broken staged-record or token references")
	}
	rows.Close()
	run, err := s.StartRun(t.Context(), "sync")
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := s.BeginWindow(t.Context(), run, m, w, true)
	if err != nil || transfer.Token != "v1-daily-cursor" {
		t.Fatal("migrated daily window cannot resume its original cursor")
	}
	if err := s.SavePage(t.Context(), m, w, []Record{record(t, m, w, steps("final", "2026-10-20", 9))}, ""); err != nil {
		t.Fatal(err)
	}
	assertStoredJSON(t, s, m, w, steps("staged", "2026-10-14", 7))
	if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != "2026-10-20" {
		t.Fatal("migrated snapshot advanced beyond its original partial date")
	}
	var purpose, state string
	history := Window{Since: "2026-10-01", Until: "2026-10-03"}
	if err := s.db.QueryRowContext(t.Context(), "SELECT purpose,state FROM windows WHERE id=?", history.ID(m)).Scan(&purpose, &state); err != nil || purpose != "backfill" || state != "failed" {
		t.Fatal("migration erased or reclassified historical failure evidence")
	}
}

func TestV1MigrationFailureRollsBackOriginalSchemaAndTransfer(t *testing.T) {
	path, db, m, old, published := openV1Fixture(t, true)
	if _, err := OpenStore(t.Context(), path); Code(err) != "migration_failed" {
		t.Fatalf("invalid daily migration metadata was accepted: %v", err)
	}
	var version, partialTables, stages int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("failed migration changed the original schema version")
	}
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_v2'").Scan(&partialTables); err != nil || partialTables != 0 {
		t.Fatal("failed migration left partial replacement tables")
	}
	var cursor, original string
	if err := db.QueryRowContext(t.Context(), `SELECT next_page_token,(SELECT COUNT(*) FROM staged_records WHERE window_id=windows.id) FROM windows WHERE id=?`, old.ID(m)).Scan(&cursor, &stages); err != nil || cursor != "v1-daily-cursor" || stages != 1 {
		t.Fatal("failed migration changed the original transfer")
	}
	if err := db.QueryRowContext(t.Context(), "SELECT original_json FROM records").Scan(&original); err != nil || original != string(published) {
		t.Fatal("failed migration changed published data")
	}
	backups, err := filepath.Glob(path + ".pre-v4-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatal("failed migration did not retain its pre-migration backup")
	}
}

func TestV1CompletedDailyFrontierReopensItsPartialDate(t *testing.T) {
	path, db, m, old, published := openV1Fixture(t, false)
	if _, err := db.ExecContext(t.Context(), `DELETE FROM staged_records; DELETE FROM page_tokens;
 UPDATE windows SET state='complete',next_page_token='',success_at='2026-10-20T12:05:00Z' WHERE attempt_run=1;`); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertStoredJSON(t, s, m, old, published)
	if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != "2026-10-20" {
		t.Fatal("v1 completed daily frontier still skips the last partial date")
	}
	if pending, err := s.PendingDailyWindows(t.Context(), m); err != nil || len(pending) != 0 {
		t.Fatal("migration turned completed coverage or a historical failure into a daily retry")
	}
}
