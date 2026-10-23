package collector

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func assertSavedTransfer(t *testing.T, s *Store, m Metric, w Window, cursor string, staged int) {
	t.Helper()
	var got string
	var pages, records, tokens int
	err := s.db.QueryRowContext(t.Context(), `SELECT next_page_token,pages,
 (SELECT COUNT(*) FROM staged_records WHERE window_id=windows.id),
 (SELECT COUNT(*) FROM page_tokens WHERE window_id=windows.id)
 FROM windows WHERE id=?`, w.ID(m)).Scan(&got, &pages, &records, &tokens)
	wantPages := 0
	if cursor != "" {
		wantPages = 1
	}
	if err != nil || got != cursor || pages != wantPages || records != staged || tokens != wantPages {
		t.Fatalf("saved transfer does not match expected cursor/staging state: %v", err)
	}
}

// Keep the review's nine-day failure sequence: the corrected date eventually
// falls outside automatic refresh, so a later sync cannot hide a stale publish.
func TestReviewHistoricalResumeDoesNotRegressNewerDailyData(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		t.Run(fmt.Sprintf("source_deletion=%t", deletion), func(t *testing.T) {
			e, a := fixture(t)
			m := e.Metrics[0]
			e.OAuth.Tokens.PublishingStatus = "production"
			initial := steps("observed", "2026-10-20", 1)
			removed := steps("removed", "2026-10-20", 2)
			corrected := steps("observed", "2026-10-20", 7)
			late := steps("late", "2026-10-20", 11)
			var history Window
			a.page = func(_ Metric, w Window, token string) (Page, error) {
				if !containsDate(w, "2026-10-20") || token != "" {
					t.Fatal("initial history request changed")
				}
				history = w
				return Page{Data: []json.RawMessage{initial, removed}, Next: "saved-history-cursor"}, nil
			}
			options := Options{MaxPages: 1, AggregateSince: "2026-10-20"}
			if result, err := e.Backfill(t.Context(), options); err != nil || result.Status != "paused" {
				t.Fatalf("history did not pause: %+v %v", result, err)
			}
			reopenEngineStore(t, e)
			fresh := func(_ Metric, w Window, token string) (Page, error) {
				if token != "" {
					t.Fatal("fresh publication resumed an invalidated snapshot")
				}
				if !deletion && containsDate(w, "2026-10-20") {
					return Page{Data: []json.RawMessage{corrected, late}}, nil
				}
				return Page{}, nil
			}
			a.page = fresh
			if _, err := e.Sync(t.Context(), Options{}); err != nil {
				t.Fatal(err)
			}
			e.Now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
			if _, err := e.Sync(t.Context(), Options{}); err != nil {
				t.Fatal(err)
			}
			reopenEngineStore(t, e)
			assertSavedTransfer(t, e.Store, m, history, "", 0)
			currentSyncTimestamp(t, e)
			if status, err := e.Status(t.Context(), false); err != nil || status.SourceHistoryComplete || status.UnattendedReady {
				t.Fatal("invalidated history was represented as complete")
			}
			options.MaxPages = 0
			if _, err := e.Backfill(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			reopenEngineStore(t, e)
			if _, err := e.Sync(t.Context(), Options{}); err != nil {
				t.Fatal(err)
			}
			currentSyncTimestamp(t, e)
			wantRecords := 0
			if !deletion {
				wantRecords = 2
				assertStoredJSON(t, e.Store, m, history, corrected)
				assertStoredJSON(t, e.Store, m, history, late)
			}
			if recordsCount(t, e.Store) != wantRecords {
				t.Fatal("resumed historical publication regressed fresh data or resurrected deleted data")
			}
			status, err := e.Status(t.Context(), false)
			if err != nil || !status.UnattendedReady || status.Streams[0].SyncUntil != "2026-10-29" {
				t.Fatal("freshly refetched history did not recover honest readiness")
			}
		})
	}
}

func TestPausedDailyTransferRestartsAfterNewerManualPublication(t *testing.T) {
	e, a := fixture(t)
	m := e.Metrics[0]
	initial := steps("observed", "2026-10-20", 1)
	removed := steps("removed", "2026-10-20", 2)
	corrected := steps("observed", "2026-10-20", 7)
	late := steps("late", "2026-10-20", 11)
	var original Window
	a.page = func(_ Metric, w Window, _ string) (Page, error) {
		original = w
		return Page{Data: []json.RawMessage{initial, removed}, Next: "saved-daily-cursor"}, nil
	}
	if result, err := e.Sync(t.Context(), Options{MaxPages: 1}); err != nil || result.Status != "paused" {
		t.Fatalf("daily transfer did not pause: %+v %v", result, err)
	}
	reopenEngineStore(t, e)
	a.page = func(_ Metric, w Window, token string) (Page, error) {
		if token != "" {
			t.Fatal("manual publication took over the daily cursor")
		}
		return Page{Data: []json.RawMessage{corrected, late}}, nil
	}
	if _, err := e.Backfill(t.Context(), Options{Since: original.Since, Until: original.Until}); err != nil {
		t.Fatal(err)
	}
	reopenEngineStore(t, e)
	assertSavedTransfer(t, e.Store, m, original, "", 0)
	if pending, err := e.Store.PendingDailyWindows(t.Context(), m); err != nil || len(pending) != 1 || pending[0] != original {
		t.Fatal("overlap invalidation changed the pending daily bounds or original partial date")
	}
	if frontier, err := e.Store.Frontier(t.Context(), m); err != nil || frontier != "" {
		t.Fatal("manual publication settled the daily frontier")
	}
	e.Now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	calls := 0
	a.page = func(_ Metric, w Window, token string) (Page, error) {
		calls++
		if calls == 1 && w != original {
			t.Fatal("daily restart abandoned the original bounds or partial date")
		}
		if token != "" {
			t.Fatal("daily restart reused its stale cursor")
		}
		if containsDate(w, "2026-10-20") {
			return Page{Data: []json.RawMessage{corrected, late}}, nil
		}
		return Page{}, nil
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	assertStoredJSON(t, e.Store, m, original, corrected)
	assertStoredJSON(t, e.Store, m, original, late)
	if calls < 2 || recordsCount(t, e.Store) != 2 {
		t.Fatal("daily recovery lost the newer correction, late upload, or source deletion")
	}
	if frontier, err := e.Store.Frontier(t.Context(), m); err != nil || frontier != "2026-10-29" {
		t.Fatal("restarted daily transfer did not complete contiguous catchup")
	}
}

func TestPublicationInvalidatesOnlyPendingOverlaps(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		pending, publication         Window
		otherStream, complete, reset bool
	}{
		{"partial_overlap", Window{Since: "2026-10-01", Until: "2026-10-04"}, Window{Since: "2026-10-03", Until: "2026-10-06"}, false, false, true},
		{"contained_publication", Window{Since: "2026-10-01", Until: "2026-10-06"}, Window{Since: "2026-10-02", Until: "2026-10-04"}, false, false, true},
		{"unbounded_pending", Window{Until: "2026-10-04"}, Window{Since: "2026-10-02", Until: "2026-10-06"}, false, false, true},
		{"unbounded_publication", Window{Since: "2026-10-01", Until: "2026-10-04"}, Window{Until: "2026-10-03"}, false, false, true},
		{"equal_bounds_other_purpose", Window{Since: "2026-10-01", Until: "2026-10-04"}, Window{Since: "2026-10-01", Until: "2026-10-04", DailyDate: "2026-10-03"}, false, false, true},
		{"adjacent_after", Window{Since: "2026-10-01", Until: "2026-10-03"}, Window{Since: "2026-10-03", Until: "2026-10-06"}, false, false, false},
		{"adjacent_before", Window{Since: "2026-10-03", Until: "2026-10-06"}, Window{Since: "2026-10-01", Until: "2026-10-03"}, false, false, false},
		{"unbounded_adjacent", Window{Until: "2026-10-03"}, Window{Since: "2026-10-03", Until: "2026-10-06"}, false, false, false},
		{"other_stream", Window{Since: "2026-10-01", Until: "2026-10-04"}, Window{Since: "2026-10-01", Until: "2026-10-04"}, true, false, false},
		{"completed_overlap", Window{Since: "2026-10-01", Until: "2026-10-04"}, Window{Since: "2026-10-02", Until: "2026-10-06"}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := fixture(t)
			s, m := e.Store, e.Metrics[0]
			if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, tc.pending, false); err != nil {
				t.Fatal(err)
			}
			next := "preserved-cursor"
			if tc.complete {
				next = ""
			}
			date := tc.pending.Since
			if date == "" {
				date = "2026-10-01"
			}
			if err := s.SavePage(t.Context(), m, tc.pending, []Record{record(t, m, tc.pending, steps("staged", date, 1))}, next); err != nil {
				t.Fatal(err)
			}
			publisher := m
			if tc.otherStream {
				publisher.Method = "list"
			}
			if _, err := s.BeginWindow(t.Context(), startRun(t, s), publisher, tc.publication, false); err != nil {
				t.Fatal(err)
			}
			if err := s.SavePage(t.Context(), publisher, tc.publication, nil, ""); err != nil {
				t.Fatal(err)
			}
			reopenEngineStore(t, e)
			s = e.Store
			transfer, err := s.BeginWindow(t.Context(), startRun(t, s), m, tc.pending, false)
			if tc.complete {
				if err != nil || !transfer.Done {
					t.Fatal("new publication reopened completed coverage")
				}
				return
			}
			staged := 1
			if tc.reset {
				next, staged = "", 0
			}
			if err != nil || transfer.Token != next {
				t.Fatal("pending transfer did not resume or restart as its overlap requires")
			}
			assertSavedTransfer(t, s, m, tc.pending, next, staged)
			if err := s.SavePage(t.Context(), m, tc.pending, nil, ""); err != nil {
				t.Fatal(err)
			}
			if recordsCount(t, s) != staged {
				t.Fatal("resumed publication retained invalid staging or lost an unrelated page")
			}
		})
	}
}

func TestPublicationFailureRollsBackOverlapInvalidation(t *testing.T) {
	e, _ := fixture(t)
	s, m := e.Store, e.Metrics[0]
	history := Window{Since: "2026-10-01", Until: "2026-10-04"}
	daily := Window{Since: "2026-10-02", Until: "2026-10-04", DailyDate: "2026-10-03"}
	old := record(t, m, history, steps("observed", "2026-10-02", 1))
	corrected := record(t, m, daily, steps("observed", "2026-10-02", 7))
	if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, history, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, history, []Record{old}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, history, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, history, []Record{old}, "must-survive-rollback"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceFrontier(t.Context(), m, "2026-10-02"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, daily, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER fail_frontier BEFORE UPDATE ON stream_state BEGIN SELECT RAISE(FAIL,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, daily, []Record{corrected}, ""); Code(err) != "storage_error" {
		t.Fatal("expected atomic publication failure")
	}
	reopenEngineStore(t, e)
	s = e.Store
	assertStoredJSON(t, s, m, history, json.RawMessage(old.Original))
	assertSavedTransfer(t, s, m, history, "must-survive-rollback", 1)
	if complete, err := s.WindowComplete(t.Context(), m, daily); err != nil || complete {
		t.Fatal("failed publication committed coverage")
	}
	if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != "2026-10-02" {
		t.Fatal("failed publication advanced the frontier")
	}
	if _, err := s.db.ExecContext(t.Context(), "DROP TRIGGER fail_frontier"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, daily, []Record{corrected}, ""); err != nil {
		t.Fatal(err)
	}
	assertSavedTransfer(t, s, m, history, "", 0)
	assertStoredJSON(t, s, m, daily, json.RawMessage(corrected.Original))
}

func legacyPublicationFixture(t *testing.T) (*Engine, Metric, Window, Window, json.RawMessage, string) {
	t.Helper()
	e, _ := fixture(t)
	s, m := e.Store, e.Metrics[0]
	stale := Window{Since: "2026-10-01", Until: "2026-10-04", DailyDate: "2026-10-03"}
	unrelated := Window{Until: "2026-10-01"}
	published := Window{Since: "2026-10-02", Until: "2026-10-05"}
	fresh := steps("observed", "2026-10-02", 7)
	for _, w := range []Window{stale, unrelated, published} {
		if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, w, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SavePage(t.Context(), m, published, []Record{record(t, m, published, fresh)}, ""); err != nil {
		t.Fatal(err)
	}
	// A later refresh can be pending while its earlier published view remains
	// canonical. Migration must still recognize that publication as evidence.
	if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, published, true); err != nil {
		t.Fatal(err)
	}
	// Recreate schema 3's retained stale staging after the newer publication.
	if err := s.SavePage(t.Context(), m, stale, []Record{record(t, m, stale, steps("observed", "2026-10-02", 1))}, "legacy-stale-cursor"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkWindow(t.Context(), startRun(t, s), m, stale, "failed", "network_error"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, unrelated, []Record{record(t, m, unrelated, steps("unrelated", "2026-09-30", 1))}, "legacy-unrelated-cursor"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceFrontier(t.Context(), m, "2026-10-03"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `PRAGMA user_version=3; PRAGMA wal_autocheckpoint=0;
 INSERT INTO meta(key,value) VALUES('migration-wal-proof','committed-in-wal');`); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, path string
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	return e, m, stale, unrelated, fresh, path
}

func TestV3MigrationRestartsAmbiguousOverlapsAndPreservesOtherCursors(t *testing.T) {
	e, m, stale, unrelated, fresh, path := legacyPublicationFixture(t)
	if _, err := OpenReadOnlyStore(t.Context(), path); Code(err) != "unsupported_database_schema" {
		t.Fatal("read-only access accepted an unmigrated database")
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("migration fixture has no committed WAL contents")
	}
	// Keep another connection open so closing the importer does not checkpoint
	// away the WAL before the migration takes its backup.
	walReader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer walReader.Close()
	var proof string
	if err := walReader.QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key='migration-wal-proof'").Scan(&proof); err != nil {
		t.Fatal(err)
	}
	reopenEngineStore(t, e)
	s := e.Store
	assertStoredJSON(t, s, m, stale, fresh)
	assertSavedTransfer(t, s, m, stale, "", 0)
	assertSavedTransfer(t, s, m, unrelated, "legacy-unrelated-cursor", 1)
	var state, code, partial string
	if err := s.db.QueryRowContext(t.Context(), "SELECT state,error_code,daily_date FROM windows WHERE id=?", stale.ID(m)).Scan(&state, &code, &partial); err != nil || state != "failed" || code != "network_error" || partial != stale.DailyDate {
		t.Fatal("migration changed failure evidence or the original daily partial date")
	}
	if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != "2026-10-03" {
		t.Fatal("publication migration shifted a daily frontier")
	}
	backups, err := filepath.Glob(path + ".pre-v4-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatal("publication migration did not retain one consistent backup")
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("publication migration backup is not restricted")
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	var cursor string
	if err := backup.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal("backup does not retain the original schema")
	}
	if err := backup.QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key='migration-wal-proof'").Scan(&proof); err != nil || proof != "committed-in-wal" {
		t.Fatal("migration backup omitted committed WAL content")
	}
	if err := backup.QueryRowContext(t.Context(), "SELECT next_page_token FROM windows WHERE id=?", stale.ID(m)).Scan(&cursor); err != nil || cursor != "legacy-stale-cursor" {
		t.Fatal("backup lost the original pending cursor")
	}
	rows, err := s.db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("publication migration broke a transfer reference")
	}
	rows.Close()
	reopenEngineStore(t, e)
	assertSavedTransfer(t, e.Store, m, unrelated, "legacy-unrelated-cursor", 1)
	if backups, err := filepath.Glob(path + ".pre-v4-*.bak"); err != nil || len(backups) != 1 {
		t.Fatal("reopening repeated the migration")
	}
	transfer, err := e.Store.BeginWindow(t.Context(), startRun(t, e.Store), m, unrelated, false)
	if err != nil || transfer.Token != "legacy-unrelated-cursor" {
		t.Fatal("unrelated legacy transfer cannot resume")
	}
	if err := e.Store.SavePage(t.Context(), m, unrelated, nil, ""); err != nil {
		t.Fatal(err)
	}
	assertStoredJSON(t, e.Store, m, unrelated, steps("unrelated", "2026-09-30", 1))
}

func TestV3PublicationMigrationFailureRollsBackTransferReset(t *testing.T) {
	e, m, stale, unrelated, fresh, path := legacyPublicationFixture(t)
	s := e.Store
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER fail_transfer_reset BEFORE UPDATE OF next_page_token ON windows BEGIN SELECT RAISE(FAIL,'synthetic migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(t.Context(), path); Code(err) != "migration_failed" {
		t.Fatal("expected publication migration failure")
	}
	var version int
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal("failed publication migration changed the original version")
	}
	assertSavedTransfer(t, s, m, stale, "legacy-stale-cursor", 1)
	assertSavedTransfer(t, s, m, unrelated, "legacy-unrelated-cursor", 1)
	assertStoredJSON(t, s, m, stale, fresh)
	if backups, err := filepath.Glob(path + ".pre-v4-*.bak"); err != nil || len(backups) != 1 {
		t.Fatal("failed publication migration did not retain its backup")
	}
}
