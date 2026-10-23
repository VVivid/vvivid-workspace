package collector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeAPI struct {
	id       string
	identity error
	page     func(Metric, Window, string) (Page, error)
}

func (a *fakeAPI) Identity(context.Context) (string, error) {
	if a.id == "" {
		return "test-owner", a.identity
	}
	return a.id, a.identity
}
func (a *fakeAPI) Page(_ context.Context, m Metric, w Window, token string, _ int) (Page, error) {
	if a.page == nil {
		return Page{}, nil
	}
	return a.page(m, w, token)
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.Context(), filepath.Join(t.TempDir(), "health.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func fixture(t *testing.T) (*Engine, *fakeAPI) {
	t.Helper()
	o := &OAuth{TokenDir: filepath.Join(t.TempDir(), "tokens"), HTTP: NewHTTP(), Tokens: Tokens{
		Access: "synthetic-access-secret", Refresh: "synthetic-refresh-secret", Expires: time.Now().Add(time.Hour),
		Scopes: slices.Clone(RequiredScopes), HealthUserID: "test-owner", Verified: true, PublishingStatus: "testing",
	}}
	if err := o.Save(); err != nil {
		t.Fatal(err)
	}
	a := &fakeAPI{}
	e := NewEngine(testStore(t), o, a)
	e.Metrics = []Metric{metric("steps", "interval", "activity_and_fitness")}
	e.Now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	return e, a
}

func steps(id, date string, count int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"dataPointName":"users/test-owner/dataTypes/steps/dataPoints/%s","steps":{"interval":{"startTime":"%sT10:00:00Z","endTime":"%sT10:01:00Z"},"count":"%d"}}`, id, date, date, count))
}

func recordsCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func startRun(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.StartRun(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func record(t *testing.T, m Metric, w Window, raw json.RawMessage) Record {
	t.Helper()
	r, err := DecodeRecord(m, raw, w)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFiltersAndRecordAnchors(t *testing.T) {
	w := Window{Since: "2026-03-07", Until: "2026-03-09"}
	for _, tc := range []struct{ name, kind, field, literal string }{
		{"steps", "interval", "steps.interval.start_time", "2026-03-07T00:00:00Z"},
		{"weight", "sample", "weight.sample_time.physical_time", "2026-03-07T00:00:00Z"},
		{"sleep", "sleep", "sleep.interval.end_time", "2026-03-07T00:00:00Z"},
		{"exercise", "exercise", "exercise.interval.civil_start_time", "2026-03-07T00:00:00"},
		{"daily-heart-rate-variability", "daily", "daily_heart_rate_variability.date", "2026-03-07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := metric(tc.name, tc.kind, "sleep")
			if !strings.Contains(w.Filter(m), tc.field+` >= "`+tc.literal+`" AND `) {
				t.Fatalf("wrong filter: %s", w.Filter(m))
			}
			if strings.Contains((Window{Until: w.Until}).Filter(m), ">=") {
				t.Fatal("historical tail unexpectedly has a lower cutoff")
			}
		})
	}
	sleep := metric("sleep", "sleep", "sleep")
	r := record(t, sleep, w, json.RawMessage(`{"sleep":{"interval":{"startTime":"2026-03-06T23:00:00Z","endTime":"2026-03-07T06:00:00Z"},"stages":[]}}`))
	if r.Anchor != "2026-03-07T06:00:00.000000000Z" || r.Start[:10] != "2026-03-06" {
		t.Fatal("sleep must be selected by end time and retain its original start")
	}
	exercise := metric("exercise", "exercise", "activity_and_fitness")
	r = record(t, exercise, w, json.RawMessage(`{"exercise":{"interval":{"startTime":"2026-03-07T02:00:00Z","endTime":"2026-03-07T03:00:00Z","startUtcOffset":"-18000s","civilStartTime":{"date":{"year":2026,"month":3,"day":7},"time":{"hours":1}}}}}`))
	if r.Anchor != "2026-03-07T01:00:00" {
		t.Fatal("exercise filter and index must use the recorded civil clock")
	}
	if _, err := DecodeRecord(sleep, json.RawMessage(`{"sleep":{"interval":{"startTime":"2026-03-07T10:00:00Z","endTime":"2026-03-06T10:00:00Z"}}}`), w); Code(err) != "invalid_record_time" {
		t.Fatal("reversed interval accepted")
	}
}

func TestAtomicReplacementResumeCorrectionAndLateUpload(t *testing.T) {
	s := testStore(t)
	m := metric("steps", "interval", "activity_and_fitness")
	w := Window{Since: "2026-10-01", Until: "2026-10-03"}
	run := startRun(t, s)
	old := record(t, m, w, steps("old", "2026-10-01", 3))
	if _, err := s.BeginWindow(t.Context(), run, m, w, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{old}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginWindow(t.Context(), run, m, w, true); err != nil {
		t.Fatal(err)
	}
	corrected := record(t, m, w, steps("old", "2026-10-01", 8))
	if err := s.SavePage(t.Context(), m, w, []Record{corrected}, "next-secret-cursor"); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := s.db.QueryRowContext(t.Context(), "SELECT original_json FROM records").Scan(&original); err != nil || original != old.Original {
		t.Fatal("partial page replaced the completed view")
	}
	transfer, err := s.BeginWindow(t.Context(), run, m, w, true)
	if err != nil || transfer.Token != "next-secret-cursor" {
		t.Fatal("partial window cursor was not resumed")
	}
	late := record(t, m, w, steps("late-upload", "2026-10-02", 13))
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER force_failure BEFORE INSERT ON records BEGIN SELECT RAISE(FAIL,'synthetic private failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{late}, ""); Code(err) != "storage_error" {
		t.Fatal("expected final commit failure")
	}
	if recordsCount(t, s) != 1 {
		t.Fatal("failed final commit deleted the old view")
	}
	complete, _ := s.WindowComplete(t.Context(), m, w)
	if complete {
		t.Fatal("partial transfer checkpoint was marked complete")
	}
	if _, err := s.db.ExecContext(t.Context(), "DROP TRIGGER force_failure"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{late}, ""); err != nil {
		t.Fatal(err)
	}
	if recordsCount(t, s) != 2 {
		t.Fatal("correction and late upload missing")
	}
	if _, err := s.BeginWindow(t.Context(), run, m, w, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{corrected, late}, ""); err != nil || recordsCount(t, s) != 2 {
		t.Fatal("repeated replacement was not idempotent")
	}
	if _, err := s.BeginWindow(t.Context(), run, m, w, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{late}, ""); err != nil || recordsCount(t, s) != 1 {
		t.Fatal("a source deletion left a stale record")
	}
}

func TestEmptyRollupPreservesJSONWithoutInventingZero(t *testing.T) {
	s := testStore(t)
	m := metric("total-calories", "rollup", "activity_and_fitness")
	m.Method = "dailyRollUp"
	w := Window{Since: "2026-10-01", Until: "2026-10-02"}
	raw := json.RawMessage(`{"civilStartTime":{"date":{"year":2026,"month":10,"day":1}},"civilEndTime":{"date":{"year":2026,"month":10,"day":2}}}`)
	r := record(t, m, w, raw)
	if r.HasValue {
		t.Fatal("absence bucket became a measured zero")
	}
	if _, err := s.BeginWindow(t.Context(), startRun(t, s), m, w, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{r}, ""); err != nil {
		t.Fatal(err)
	}
	var count int
	var family, original string
	if err := s.db.QueryRowContext(t.Context(), "SELECT record_count FROM windows").Scan(&count); err != nil || count != 0 {
		t.Fatal("empty rollup coverage is not empty")
	}
	if err := s.db.QueryRowContext(t.Context(), "SELECT source_family,original_json FROM records").Scan(&family, &original); err != nil || original != string(raw) || family != "users/me/dataSourceFamilies/google-sources" {
		t.Fatal("original absence JSON or requested provenance lost")
	}
}

func TestBackfillPageBudgetResumeAndExpiredCursor(t *testing.T) {
	e, a := fixture(t)
	var tokens []string
	a.page = func(_ Metric, w Window, token string) (Page, error) {
		tokens = append(tokens, token)
		if len(tokens) == 1 {
			return Page{Data: []json.RawMessage{steps("one", "2026-10-01", 1)}, Next: "expired-cursor"}, nil
		}
		if token == "expired-cursor" {
			return Page{}, &Error{Code: "invalid_page_token", Status: 400}
		}
		return Page{Data: []json.RawMessage{steps("replacement", "2026-10-02", 2)}}, nil
	}
	options := Options{Since: "2026-10-01", Until: "2026-10-03", MaxPages: 1}
	result, err := e.Backfill(t.Context(), options)
	if err != nil || result.Status != "paused" || recordsCount(t, e.Store) != 0 {
		t.Fatalf("partial page was published: %v %+v", err, result)
	}
	result, err = e.Backfill(t.Context(), options)
	if err != nil || result.Status != "complete" || recordsCount(t, e.Store) != 1 {
		t.Fatalf("resume failed: %v %+v", err, result)
	}
	if !slices.Equal(tokens, []string{"", "expired-cursor", ""}) {
		t.Fatal("expired cursor did not restart exactly its incomplete window")
	}
	result, err = e.Backfill(t.Context(), options)
	if err != nil || result.Skipped != 1 || len(tokens) != 3 {
		t.Fatal("completed backfill repeated a download")
	}
}

func TestPaginationCycleDoesNotComplete(t *testing.T) {
	e, a := fixture(t)
	a.page = func(Metric, Window, string) (Page, error) { return Page{Next: "cycle"}, nil }
	result, err := e.Backfill(t.Context(), Options{Since: "2026-10-01", Until: "2026-10-02"})
	if Code(err) != "pagination_cycle" || result.Status != "failed" || recordsCount(t, e.Store) != 0 {
		t.Fatal("pagination cycle became completed coverage")
	}
}

func TestDailyDowntimeUsesContiguousWatermarkAndRefreshesSevenDays(t *testing.T) {
	e, a := fixture(t)
	m := e.Metrics[0]
	if err := e.Store.AdvanceFrontier(t.Context(), m, "2026-09-01"); err != nil {
		t.Fatal(err)
	}
	var queried []Window
	a.page = func(_ Metric, w Window, _ string) (Page, error) {
		queried = append(queried, w)
		return Page{}, nil
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(queried) < 7 || queried[0].Since != "2026-09-01" || queried[len(queried)-1].Until != "2026-10-21" {
		t.Fatal("downtime was truncated to seven days")
	}
	queried = nil
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(queried) != 1 || queried[0] != (Window{Since: "2026-10-14", Until: "2026-10-21", DailyDate: "2026-10-20"}) {
		t.Fatal("successful sync did not refresh the seven-day correction window")
	}
}

func TestHistoricalSuccessCannotJumpDailyGap(t *testing.T) {
	e, a := fixture(t)
	m := e.Metrics[0]
	e.Store.SetMeta(t.Context(), "sync_origin", "2026-09-01")
	e.Store.AdvanceFrontier(t.Context(), m, "2026-09-01")
	// An independent later historical repair must not move the daily watermark.
	if _, err := e.Backfill(t.Context(), Options{Since: "2026-10-01", Until: "2026-10-03"}); err != nil {
		t.Fatal(err)
	}
	frontier, _ := e.Store.Frontier(t.Context(), m)
	if frontier != "2026-09-01" {
		t.Fatal("historical repair advanced the daily frontier")
	}
	var first string
	a.page = func(_ Metric, w Window, _ string) (Page, error) {
		first = w.Since
		return Page{}, &Error{Code: "network_error"}
	}
	if _, err := e.Sync(t.Context(), Options{}); Code(err) != "network_error" || first != "2026-09-01" {
		t.Fatal("daily sync skipped the failed historical gap")
	}
	frontier, _ = e.Store.Frontier(t.Context(), m)
	if frontier != "2026-09-01" {
		t.Fatal("failed daily window advanced its watermark")
	}
}

func TestDSTAndHistoryTail(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []time.Time{time.Date(2026, 3, 8, 12, 0, 0, 0, zone), time.Date(2026, 11, 1, 12, 0, 0, 0, zone)} {
		e, a := fixture(t)
		e.Now = func() time.Time { return day }
		var fetched Window
		a.page = func(_ Metric, w Window, _ string) (Page, error) { fetched = w; return Page{}, nil }
		if _, err := e.Sync(t.Context(), Options{}); err != nil {
			t.Fatal(err)
		}
		from, _ := ParseDate(fetched.Since)
		to, _ := ParseDate(fetched.Until)
		if to.Sub(from) != 7*24*time.Hour || fetched.Until != day.UTC().AddDate(0, 0, 1).Format(time.DateOnly) {
			t.Fatal("DST changed the number of imported UTC dates")
		}
		windows, _, err := e.HistoryPlan(t.Context(), Options{}, e.Metrics[0])
		if err != nil || windows[len(windows)-1].Since != "" {
			t.Fatal("all-history plan is missing its unbounded tail")
		}
	}
}

func TestPartialScopesEmptyCoverageAndRevokedIdentity(t *testing.T) {
	e, a := fixture(t)
	e.Metrics = append(e.Metrics, metric("sleep", "sleep", "sleep"))
	e.OAuth.Tokens.Scopes = []string{RequiredScopes[0]}
	result, err := e.Sync(t.Context(), Options{})
	if Code(err) != "data_unavailable" || result.Unavailable != 1 || result.Completed != 1 {
		t.Fatal("partial permission did not preserve the allowed stream")
	}
	status, err := e.Status(t.Context(), false)
	if err != nil || status.Streams[0].Empty != 1 || status.Streams[1].Unavailable != 1 || status.Streams[1].Empty != 0 || status.UnattendedReady {
		t.Fatal("unavailable scope was represented as empty successful data")
	}
	a.identity = &Error{Code: "reauthorization_required"}
	if _, err := e.Sync(t.Context(), Options{}); Code(err) != "reauthorization_required" || e.OAuth.Tokens.Verified {
		t.Fatal("revoked authorization remained verified")
	}
	status, _ = e.Status(t.Context(), false)
	encoded, _ := json.Marshal(status)
	for _, secret := range []string{"synthetic-access-secret", "synthetic-refresh-secret", "test-owner"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("status leaked private state")
		}
	}
}

func TestVerifyAuthorizationRequiresIdentityAndRealSample(t *testing.T) {
	e, a := fixture(t)
	if err := e.VerifyAuthorization(t.Context()); Code(err) != "no_health_sample" || e.OAuth.Tokens.Verified {
		t.Fatal("empty response marked account authorized")
	}
	a.page = func(_ Metric, w Window, _ string) (Page, error) {
		if w.Since != "" {
			t.Fatal("authorization sample arbitrarily restricted recent history")
		}
		return Page{Data: []json.RawMessage{steps("real-sample", "2014-01-01", 42)}}, nil
	}
	if err := e.VerifyAuthorization(t.Context()); err != nil || !e.OAuth.Tokens.Verified {
		t.Fatal("valid actual historical sample not accepted")
	}
	a.id = "different-owner"
	if err := e.VerifyAuthorization(t.Context()); Code(err) != "identity_mismatch" || e.OAuth.Tokens.Verified {
		t.Fatal("authorization silently switched an existing datastore owner")
	}
}

func TestMigrationSafetyBackupWALAndExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "health.sqlite3")
	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); Code(err) != "collector_busy" {
		t.Fatal("overlapping writer acquired lock")
	}
	lock.Close()
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.ExecContext(t.Context(), "PRAGMA wal_autocheckpoint=0; INSERT INTO meta VALUES('wal-proof','committed-in-wal')"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("test did not create uncheckpointed WAL content")
	}
	backup := filepath.Join(dir, "backup.sqlite3")
	if err := s.backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	copy, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var value string
	if err := copy.QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key='wal-proof'").Scan(&value); err != nil || value != "committed-in-wal" {
		t.Fatal("backup omitted committed WAL data")
	}
	if info, _ := os.Stat(backup); info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions expose health data")
	}
	for _, tc := range []struct{ statement, code string }{
		{"CREATE TABLE unrelated(private TEXT)", "unrecognized_database"},
		{"PRAGMA user_version=99", "database_newer_than_binary"},
	} {
		other := filepath.Join(t.TempDir(), "unknown.sqlite3")
		db, err := sql.Open("sqlite", other)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), tc.statement); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err := OpenStore(t.Context(), other); Code(err) != tc.code {
			t.Fatalf("unsafe migration accepted: %v", err)
		}
	}
}

func TestPendingPagePersistsAcrossDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.sqlite3")
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	m := metric("steps", "interval", "activity_and_fitness")
	w := Window{Since: "2026-10-01", Until: "2026-10-03"}
	run := startRun(t, s)
	if _, err := s.BeginWindow(t.Context(), run, m, w, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePage(t.Context(), m, w, []Record{record(t, m, w, steps("first", "2026-10-01", 2))}, "persisted-cursor"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	transfer, err := s.BeginWindow(t.Context(), startRun(t, s), m, w, false)
	if err != nil || transfer.Token != "persisted-cursor" || recordsCount(t, s) != 0 {
		t.Fatal("recreated importer lost its incomplete window")
	}
	if err := s.SavePage(t.Context(), m, w, []Record{record(t, m, w, steps("second", "2026-10-02", 5))}, ""); err != nil || recordsCount(t, s) != 2 {
		t.Fatal("resumed final page did not atomically publish all persisted pages")
	}
}

func TestReadOnlyStatusStoreAndInitialMigrationBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA application_id=101"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups, err := filepath.Glob(path + ".pre-v4-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatal("existing initialized SQLite was migrated without its backup")
	}
	if err := s.SetMeta(t.Context(), "reader-proof", "visible-through-WAL"); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnlyStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if value, err := reader.Meta(t.Context(), "reader-proof"); err != nil || value != "visible-through-WAL" {
		t.Fatal("read-only status did not see committed WAL state")
	}
	if err := reader.SetMeta(t.Context(), "reader-proof", "mutation"); Code(err) != "storage_error" {
		t.Fatal("status connection unexpectedly permits writes")
	}
}

func TestReadinessNeedsExplicitHistoryProductionAndLatestSuccessfulSync(t *testing.T) {
	e, a := fixture(t)
	e.Now = time.Now
	m := metric("total-calories", "rollup", "activity_and_fitness")
	m.Method = "dailyRollUp"
	e.Metrics = append(e.Metrics, m)
	observed := time.Now().UTC().AddDate(0, 0, -2).Format(time.DateOnly)
	a.page = func(m Metric, w Window, _ string) (Page, error) {
		if m.Kind != "rollup" && (w.Since == "" || w.Since <= observed) && observed < w.Until {
			return Page{Data: []json.RawMessage{steps("sample", observed, 7)}}, nil
		}
		return Page{}, nil
	}
	if _, err := e.Backfill(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	e.OAuth.Tokens.PublishingStatus = "production"
	status, err := e.Status(t.Context(), false)
	if err != nil || !status.SourceHistoryComplete || !status.AggregateHistoryComplete || !status.OlderAggregateUnverified || status.UnattendedReady {
		t.Fatal("inferred aggregate floor was represented as all-history readiness")
	}
	if _, err := e.Backfill(t.Context(), Options{AggregateSince: observed}); err != nil {
		t.Fatal(err)
	}
	status, err = e.Status(t.Context(), false)
	if err != nil || !status.UnattendedReady {
		t.Fatal("verified explicit completed history and recent sync did not become ready")
	}
	a.page = func(Metric, Window, string) (Page, error) { return Page{}, &Error{Code: "network_error"} }
	if _, err := e.Sync(t.Context(), Options{}); Code(err) != "network_error" {
		t.Fatal("test did not fail the newest daily run")
	}
	status, err = e.Status(t.Context(), false)
	if err != nil || status.UnattendedReady || status.LastSyncStatus != "failed" {
		t.Fatal("old watermarks concealed a newer failed daily run")
	}
	e.OAuth.Tokens.RefreshExpires = time.Now().Add(-time.Minute)
	status, _ = e.Status(t.Context(), false)
	if status.CredentialsUsable || status.UnattendedReady {
		t.Fatal("expired refresh credentials were marked usable")
	}
}
