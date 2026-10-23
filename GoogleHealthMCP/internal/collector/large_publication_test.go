package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This opt-in deployment regression uses synthetic data only. Run the same test
// with /tmp constrained to 16 MiB, first with no SQLITE_TMPDIR and then with
// SQLITE_TMPDIR pointing at the large writable database filesystem.
func TestLargeWindowPublication(t *testing.T) {
	parent := os.Getenv("GOOGLE_HEALTH_PUBLICATION_TEST_DIR")
	if parent == "" {
		t.Skip("set GOOGLE_HEALTH_PUBLICATION_TEST_DIR to a large disposable directory")
	}
	directory, err := os.MkdirTemp(parent, "google-health-publication-")
	if err != nil {
		t.Fatal("cannot create synthetic publication directory")
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	s, err := OpenStore(t.Context(), filepath.Join(directory, "synthetic.sqlite3"))
	if err != nil {
		t.Fatal("cannot open synthetic publication database")
	}
	t.Cleanup(func() { s.Close() })
	m := metric("heart-rate", "sample", "health_metrics_and_measurements")
	w := Window{Since: "2026-10-16", Until: "2026-10-21"}
	if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, w, false); err != nil {
		t.Fatal("cannot create synthetic transfer")
	}
	const total = 120000
	raw := `{"synthetic":true,"padding":"` + strings.Repeat("a", 1024) + `"}`
	_, err = s.db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (
 VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?)
 INSERT INTO staged_records(window_id,record_key,anchor,start_time,end_time,observation_date,source_json,original_json,has_value)
 SELECT ?,printf('synthetic-%06d',i),'2026-10-18T12:00:00Z','2026-10-18T12:00:00Z',
 '2026-10-18T12:00:00Z','2026-10-18','{}',?,1 FROM n`, total, w.ID(m), raw)
	if err != nil {
		t.Fatal("cannot seed synthetic staging")
	}
	_, err = s.db.ExecContext(t.Context(), `INSERT INTO records
 (stream,record_key,data_type,kind,anchor,start_time,end_time,observation_date,source_json,source_family,original_json,has_value,imported_at)
 SELECT ?,record_key,?,?,anchor,start_time,end_time,observation_date,source_json,'synthetic',original_json,has_value,?
 FROM staged_records WHERE window_id=? AND record_key<='synthetic-060000'`, m.Stream(), m.Name, m.Kind, timestamp(), w.ID(m))
	if err != nil {
		t.Fatal("cannot seed synthetic canonical overlap")
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE windows SET pages=24,next_page_token='synthetic-resume' WHERE id=?", w.ID(m)); err != nil {
		t.Fatal("cannot seed synthetic cursor")
	}
	if err := s.SavePage(t.Context(), m, w, nil, ""); err != nil {
		// Even exhausted temporary storage must not replace the published view
		// or lose any saved page/cursor from the incomplete transfer.
		if recordsCount(t, s) != 60000 {
			t.Fatal("failed publication changed canonical synthetic rows")
		}
		_, sqliteCode := DiagnosticCodes(err)
		t.Logf("synthetic publication error_code=%s sqlite_code=%d", Code(err), sqliteCode)
		assertLargeTransfer(t, s, m, w, "in_progress", "synthetic-resume", 24, total)
		t.Fatalf("synthetic publication failed: error_code=%s sqlite_code=%d; prior rows and cursor retained", Code(err), sqliteCode)
	}
	if recordsCount(t, s) != total {
		t.Fatal("large publication lost synthetic rows")
	}
	assertLargeTransfer(t, s, m, w, "complete", "", 25, 0)
	var integrity string
	if err := s.db.QueryRowContext(t.Context(), "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("large publication damaged synthetic database integrity")
	}
}

func assertLargeTransfer(t *testing.T, s *Store, m Metric, w Window, state, cursor string, pages, staged int) {
	t.Helper()
	var savedState, savedCursor string
	var savedPages, savedStaged int
	err := s.db.QueryRowContext(t.Context(), `SELECT state,next_page_token,pages,
 (SELECT COUNT(*) FROM staged_records WHERE window_id=windows.id) FROM windows WHERE id=?`, w.ID(m)).Scan(
		&savedState, &savedCursor, &savedPages, &savedStaged)
	if err != nil || savedState != state || savedCursor != cursor || savedPages != pages || savedStaged != staged {
		t.Fatalf("synthetic transfer state mismatch: state=%s pages=%d staged=%d", savedState, savedPages, savedStaged)
	}
}
