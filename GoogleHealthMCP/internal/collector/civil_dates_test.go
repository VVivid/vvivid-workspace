package collector

import (
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

func civilMetrics() []Metric {
	rollup := metric("total-calories", "rollup", "activity_and_fitness")
	rollup.Method = "dailyRollUp"
	return []Metric{
		metric("exercise", "exercise", "activity_and_fitness"),
		metric("daily-resting-heart-rate", "daily", "health_metrics_and_measurements"),
		rollup,
	}
}

// Revision is synthetic metadata used to distinguish replacement JSON. The
// indexed fields use the returned record's civil clock, including its offset.
func civilPoint(t *testing.T, m Metric, id string, at time.Time, revision int) json.RawMessage {
	t.Helper()
	date := map[string]int{"year": at.Year(), "month": int(at.Month()), "day": at.Day()}
	point := map[string]any{"dataPointName": "synthetic-" + id, "syntheticRevision": revision}
	switch m.Kind {
	case "exercise":
		_, offset := at.Zone()
		point[m.JSONField()] = map[string]any{"interval": map[string]any{
			"startTime": at.UTC().Format(time.RFC3339), "endTime": at.Add(time.Minute).UTC().Format(time.RFC3339),
			"startUtcOffset": fmt.Sprintf("%ds", offset),
		}}
	case "daily":
		point[m.JSONField()] = map[string]any{"date": date}
	case "rollup":
		point["civilStartTime"] = map[string]any{"date": date}
		point[m.JSONField()] = map[string]any{}
	default:
		t.Fatal("civil fixture used for a physical metric")
	}
	raw, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func containsDate(w Window, date string) bool {
	return (w.Since == "" || w.Since <= date) && date < w.Until
}

func currentSyncTimestamp(t *testing.T, e *Engine) {
	t.Helper()
	if _, err := e.Store.db.ExecContext(t.Context(), "UPDATE runs SET finished_at=? WHERE id=(SELECT MAX(id) FROM runs WHERE command='sync')", e.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func TestMetricDateBoundsCoverAllCivilClocksAndKeepUTCBehavior(t *testing.T) {
	for _, tc := range []struct{ now, partial, until string }{
		{"2026-10-21T00:00:00Z", "2026-10-20", "2026-10-22"},
		{"2026-10-21T01:00:00Z", "2026-10-20", "2026-10-22"},
		{"2026-10-21T11:00:00Z", "2026-10-20", "2026-10-23"},
		{"2026-10-21T12:00:00Z", "2026-10-21", "2026-10-23"},
		{"2026-03-08T06:59:59Z", "2026-03-07", "2026-03-09"},
		{"2026-03-08T07:00:00Z", "2026-03-07", "2026-03-09"},
		{"2026-11-01T05:59:59Z", "2026-10-31", "2026-11-02"},
		{"2026-11-01T06:00:00Z", "2026-10-31", "2026-11-02"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range Metrics() {
				for _, zone := range []*time.Location{time.UTC, time.FixedZone("west", -12*3600), time.FixedZone("east", 14*3600)} {
					partial, until := m.dateBounds(now.In(zone))
					wantPartial, wantUntil := now.Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly)
					if m.Kind == "daily" || m.Kind == "exercise" || m.Kind == "rollup" {
						wantPartial, wantUntil = tc.partial, tc.until
					}
					if partial != wantPartial || until != wantUntil {
						t.Fatalf("%s in host zone %s: got %s..%s, want %s..%s", m.Name, zone, partial, until, wantPartial, wantUntil)
					}
				}
			}
		})
	}
}

func TestCivilPartialDatesSurviveNineDayOutageAcrossOffsetsAndDST(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, now, date, partial string
		zone                     *time.Location
	}{
		{"west_extreme", "2026-10-21T01:00:00Z", "2026-10-20", "2026-10-20", time.FixedZone("UTC-12", -12*3600)},
		{"east_extreme", "2026-10-21T11:00:00Z", "2026-10-22", "2026-10-20", time.FixedZone("UTC+14", 14*3600)},
		{"new_york_utc_midnight", "2026-10-21T00:00:00Z", "2026-10-20", "2026-10-20", newYork},
		{"spring_before", "2026-03-08T06:59:00Z", "2026-03-08", "2026-03-07", newYork},
		{"spring_after", "2026-03-08T07:01:00Z", "2026-03-08", "2026-03-07", newYork},
		{"fall_before", "2026-11-01T05:59:00Z", "2026-11-01", "2026-10-31", newYork},
		{"fall_after", "2026-11-01T06:01:00Z", "2026-11-01", "2026-10-31", newYork},
	} {
		for _, m := range civilMetrics() {
			t.Run(tc.name+"/"+m.Name, func(t *testing.T) {
				e, a := fixture(t)
				e.Metrics = []Metric{m}
				now, err := time.Parse(time.RFC3339, tc.now)
				if err != nil {
					t.Fatal(err)
				}
				e.Now = func() time.Time { return now }
				at := now.In(tc.zone)
				if at.Format(time.DateOnly) != tc.date {
					t.Fatal("test record does not use the expected civil date")
				}
				early := civilPoint(t, m, "existing", at, 1)
				a.page = func(_ Metric, w Window, _ string) (Page, error) {
					if containsDate(w, tc.date) {
						return Page{Data: []json.RawMessage{early}}, nil
					}
					return Page{}, nil
				}
				if _, err := e.Sync(t.Context(), Options{}); err != nil {
					t.Fatal(err)
				}
				if recordsCount(t, e.Store) != 1 {
					t.Fatal("initial sync excluded the current civil date")
				}
				if frontier, err := e.Store.Frontier(t.Context(), m); err != nil || frontier != tc.partial {
					t.Fatalf("possibly open civil date was settled: %s %v", frontier, err)
				}
				reopenEngineStore(t, e)
				e.Now = func() time.Time { return now.AddDate(0, 0, 9) }
				corrected, late := civilPoint(t, m, "existing", at, 2), civilPoint(t, m, "late", at, 3)
				var fetched []Window
				a.page = func(_ Metric, w Window, token string) (Page, error) {
					fetched = append(fetched, w)
					if token != "" {
						t.Fatal("completed civil transfer retained a cursor")
					}
					if containsDate(w, tc.date) {
						return Page{Data: []json.RawMessage{corrected, late}}, nil
					}
					return Page{}, nil
				}
				if _, err := e.Sync(t.Context(), Options{}); err != nil {
					t.Fatal(err)
				}
				if len(fetched) < 2 || fetched[0].Since != tc.partial || recordsCount(t, e.Store) != 2 {
					t.Fatal("persisted civil frontier skipped a late upload after nine days")
				}
				bounds := Window{Until: fetched[len(fetched)-1].Until}
				assertStoredJSON(t, e.Store, m, bounds, corrected)
				assertStoredJSON(t, e.Store, m, bounds, late)
			})
		}
	}
}

func TestCivilCursorRetainsOriginalBoundsAndPartialDateAfterOutage(t *testing.T) {
	for _, m := range civilMetrics() {
		t.Run(m.Name, func(t *testing.T) {
			e, a := fixture(t)
			e.Metrics = []Metric{m}
			e.OAuth.Tokens.PublishingStatus = "production"
			now := time.Date(2026, 10, 21, 0, 5, 0, 0, time.UTC)
			e.Now = func() time.Time { return now }
			at := now.In(time.FixedZone("UTC-12", -12*3600))
			date := at.Format(time.DateOnly)
			old, corrected := civilPoint(t, m, "existing", at, 1), civilPoint(t, m, "existing", at, 2)
			a.page = func(_ Metric, w Window, _ string) (Page, error) {
				if containsDate(w, date) {
					return Page{Data: []json.RawMessage{old}}, nil
				}
				return Page{}, nil
			}
			if _, err := e.Backfill(t.Context(), Options{AggregateSince: date}); err != nil {
				t.Fatal(err)
			}
			var original Window
			a.page = func(_ Metric, w Window, token string) (Page, error) {
				original = w
				if token != "" {
					t.Fatal("new transfer unexpectedly had a cursor")
				}
				return Page{Data: []json.RawMessage{corrected}, Next: "civil-persisted-cursor"}, nil
			}
			if result, err := e.Sync(t.Context(), Options{MaxPages: 1}); err != nil || result.Status != "paused" {
				t.Fatalf("civil page budget did not pause: %+v %v", result, err)
			}
			if original != (Window{Since: "2026-10-14", Until: "2026-10-21", DailyDate: "2026-10-20"}) {
				t.Fatalf("unexpected initial civil request: %+v", original)
			}
			assertStoredJSON(t, e.Store, m, original, old)
			reopenEngineStore(t, e)
			pending, err := e.Store.PendingDailyWindows(t.Context(), m)
			if err != nil || !slices.Equal(pending, []Window{original}) {
				t.Fatal("reopened civil cursor changed its saved request")
			}
			if status, err := e.Status(t.Context(), false); err != nil || status.UnattendedReady || status.Streams[0].DailyPending != 1 {
				t.Fatal("unfinished civil cursor was represented as ready")
			}
			e.Now = func() time.Time { return now.AddDate(0, 0, 9) }
			tail, late := civilPoint(t, m, "tail", at, 3), civilPoint(t, m, "late", at, 4)
			calls := 0
			a.page = func(_ Metric, w Window, token string) (Page, error) {
				calls++
				if calls == 1 {
					if w != original || token != "civil-persisted-cursor" {
						t.Fatal("civil retry abandoned its saved boundaries, partial date, or cursor")
					}
					return Page{Data: []json.RawMessage{tail}}, nil
				}
				if token != "" {
					t.Fatal("fresh civil request reused an old cursor")
				}
				if calls == 2 && w.Since != "2026-10-20" {
					t.Fatal("resumed snapshot prematurely settled its original open date")
				}
				if containsDate(w, date) {
					return Page{Data: []json.RawMessage{corrected, tail, late}}, nil
				}
				return Page{}, nil
			}
			if _, err := e.Sync(t.Context(), Options{}); err != nil {
				t.Fatal(err)
			}
			if calls < 3 || recordsCount(t, e.Store) != 3 {
				t.Fatal("civil cursor recovery lost staging or late records")
			}
			for _, raw := range []json.RawMessage{corrected, tail, late} {
				assertStoredJSON(t, e.Store, m, original, raw)
			}
			currentSyncTimestamp(t, e)
			if status, err := e.Status(t.Context(), false); err != nil || !status.UnattendedReady || status.Streams[0].DailyPending != 0 {
				t.Fatalf("civil retry did not recover readiness: %+v %v", status, err)
			}
		})
	}
}

func TestCivilAuthorizationAndHistoryIncludeTimezoneAheadCurrentRecords(t *testing.T) {
	e, a := fixture(t)
	e.Metrics = append(e.Metrics, civilMetrics()...)
	now := time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	at := now.In(time.FixedZone("UTC+14", 14*3600))
	a.page = func(m Metric, w Window, _ string) (Page, error) {
		if w.Since != "" {
			t.Fatal("authorization sample has a lower history cutoff")
		}
		if !m.usesCivilDates() {
			if w.Until != "2026-10-22" {
				t.Fatal("physical authorization bound changed")
			}
			return Page{}, nil
		}
		if w.Until != "2026-10-23" {
			t.Fatal("civil authorization bound cuts off UTC+14's current date")
		}
		return Page{Data: []json.RawMessage{civilPoint(t, m, "sample", at, 1)}}, nil
	}
	if err := e.VerifyAuthorization(t.Context()); err != nil {
		t.Fatal(err)
	}
	var queried []Window
	a.page = func(m Metric, w Window, _ string) (Page, error) {
		queried = append(queried, w)
		if m.usesCivilDates() && containsDate(w, "2026-10-22") {
			return Page{Data: []json.RawMessage{civilPoint(t, m, m.Name, at, 1)}}, nil
		}
		if !m.usesCivilDates() && containsDate(w, "2026-10-21") {
			return Page{Data: []json.RawMessage{steps("physical", "2026-10-21", 1)}}, nil
		}
		return Page{}, nil
	}
	if _, err := e.Backfill(t.Context(), Options{AggregateSince: "2026-10-20"}); err != nil || recordsCount(t, e.Store) != 4 {
		t.Fatalf("default history excluded current records: %v", err)
	}
	reopenEngineStore(t, e)
	e.Now = func() time.Time { return now.AddDate(0, 0, 9) }
	queried = nil
	if _, err := e.Backfill(t.Context(), Options{}); err != nil || len(queried) != 0 {
		t.Fatal("resumed default history changed its pinned requests")
	}
	for _, m := range e.Metrics {
		windows, until, err := e.HistoryPlan(t.Context(), Options{}, m)
		want := "2026-10-22"
		if m.usesCivilDates() {
			want = "2026-10-23"
		}
		if err != nil || until != want || windows[0].Until != want || windows[len(windows)-1].Since != "" {
			t.Fatal("clock-specific history end or unbounded source tail changed")
		}
		windows, until, err = e.HistoryPlan(t.Context(), Options{Since: "2026-10-20", Until: "2026-10-22"}, m)
		if err != nil || until != "2026-10-22" || windows[0].Until != until {
			t.Fatal("explicit history upper bound was expanded")
		}
	}
}

func TestReadinessUsesEachMetricsEarliestOpenDate(t *testing.T) {
	e, _ := fixture(t)
	e.Metrics = append(e.Metrics, civilMetrics()...)
	e.OAuth.Tokens.PublishingStatus = "production"
	now := time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	if _, err := e.Backfill(t.Context(), Options{AggregateSince: "2026-10-10"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	currentSyncTimestamp(t, e)
	if status, err := e.Status(t.Context(), false); err != nil || !status.UnattendedReady {
		t.Fatal("complete civil sync was required to settle a still-open date")
	}
	for _, m := range e.Metrics {
		partial, _ := m.dateBounds(now)
		day, _ := ParseDate(partial)
		if _, err := e.Store.db.ExecContext(t.Context(), "UPDATE stream_state SET sync_until=? WHERE stream=?", day.AddDate(0, 0, -1).Format(time.DateOnly), m.Stream()); err != nil {
			t.Fatal(err)
		}
		if status, err := e.Status(t.Context(), false); err != nil || status.UnattendedReady {
			t.Fatal("readiness ignored a stale metric frontier")
		}
		if err := e.Store.AdvanceFrontier(t.Context(), m, partial); err != nil {
			t.Fatal(err)
		}
	}
	e.Now = func() time.Time { return now.Add(11 * time.Hour) }
	if status, err := e.Status(t.Context(), false); err != nil || status.UnattendedReady {
		t.Fatal("readiness ignored the civil settlement boundary at UTC noon")
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	currentSyncTimestamp(t, e)
	if status, err := e.Status(t.Context(), false); err != nil || !status.UnattendedReady {
		t.Fatal("fresh clock-specific frontiers did not recover readiness")
	}
}

func openV2CivilFixture(t *testing.T, invalidDate bool) (string, *sql.DB, []Metric, Window) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "health.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(t.Context(), strings.Replace(schema, "PRAGMA user_version=4", "PRAGMA user_version=2", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
 INSERT INTO runs(id,command,started_at,status) VALUES(1,'sync','2026-10-21T01:00:00Z','failed');
 INSERT INTO meta VALUES('history_until','2026-10-22');`); err != nil {
		t.Fatal(err)
	}
	metrics := append([]Metric{metric("steps", "interval", "activity_and_fitness")}, civilMetrics()...)
	w := Window{Since: "2026-10-15", Until: "2026-10-22", DailyDate: "2026-10-21"}
	for _, m := range metrics {
		partial := w.DailyDate
		if invalidDate && m.Kind == "exercise" {
			partial = "invalid-civil-date"
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO windows
 (id,stream,since_date,until_date,purpose,daily_date,state,attempt_run,next_page_token,pages,error_code)
 VALUES(?,?,?,?,'sync',?,'failed',1,'saved-v2-cursor',1,'network_error')`, w.ID(m), m.Stream(), w.Since, w.Until, partial); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), "INSERT INTO stream_state VALUES(?,'2026-10-21')", m.Stream()); err != nil {
			t.Fatal(err)
		}
		raw := steps("published", "2026-10-20", 1)
		if m.usesCivilDates() {
			at := time.Date(2026, 10, 20, 21, 0, 0, 0, time.FixedZone("west", -4*3600))
			raw = civilPoint(t, m, "published", at, 1)
		}
		r := record(t, m, w, raw)
		if _, err := db.ExecContext(t.Context(), "INSERT INTO records VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
			m.Stream(), r.Key, m.Name, m.Kind, r.Anchor, r.Start, r.End, r.Date, r.Source, "users/me/dataSourceFamilies/google-sources", r.Original, r.HasValue, "2026-10-21T01:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), "INSERT INTO staged_records VALUES(?,?,?,?,?,?,?,?,?)",
			w.ID(m), r.Key, r.Anchor, r.Start, r.End, r.Date, r.Source, r.Original, r.HasValue); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), "INSERT INTO page_tokens VALUES(?,?)", w.ID(m), digest("saved-v2-cursor")); err != nil {
			t.Fatal(err)
		}
	}
	return path, db, metrics, w
}

func TestV2MigrationReopensCivilDatesAndPreservesSavedTransfers(t *testing.T) {
	path, _, metrics, original := openV2CivilFixture(t, false)
	if _, err := OpenReadOnlyStore(t.Context(), path); Code(err) != "unsupported_database_schema" {
		t.Fatal("read-only status silently accepted an unmigrated database")
	}
	s, err := OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups, err := filepath.Glob(path + ".pre-v4-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatal("civil clock migration did not retain one backup")
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("migration backup is not private")
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version, records, staged int
	if err := backup.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("backup does not retain schema 2")
	}
	if err := backup.QueryRowContext(t.Context(), "SELECT (SELECT COUNT(*) FROM records),(SELECT COUNT(*) FROM staged_records)").Scan(&records, &staged); err != nil || records != len(metrics) || staged != len(metrics) {
		t.Fatal("backup omitted WAL-committed data or staging")
	}
	for _, m := range metrics {
		partial := "2026-10-21"
		if m.usesCivilDates() {
			partial = "2026-10-20"
		}
		if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != partial {
			t.Fatalf("migration changed the wrong clock's frontier: %s %s %v", m.Name, frontier, err)
		}
		pending, err := s.PendingDailyWindows(t.Context(), m)
		want := original
		want.DailyDate = partial
		if err != nil || !slices.Equal(pending, []Window{want}) || want.ID(m) != original.ID(m) {
			t.Fatal("migration changed a pending request's bounds or ID")
		}
		var cursor string
		var tokens int
		if err := s.db.QueryRowContext(t.Context(), `SELECT next_page_token,
 (SELECT COUNT(*) FROM staged_records WHERE window_id=windows.id),
 (SELECT COUNT(*) FROM page_tokens WHERE window_id=windows.id AND token_hash=?) FROM windows WHERE id=?`, digest("saved-v2-cursor"), want.ID(m)).Scan(&cursor, &staged, &tokens); err != nil || cursor != "saved-v2-cursor" || staged != 1 || tokens != 1 {
			t.Fatal("migration lost a pending cursor, staged page, or token history")
		}
		if transfer, err := s.BeginWindow(t.Context(), startRun(t, s), m, want, true); err != nil || transfer.Token != cursor {
			t.Fatal("migrated request cannot resume its original cursor")
		}
		if err := s.SavePage(t.Context(), m, want, nil, ""); err != nil {
			t.Fatal(err)
		}
		if frontier, err := s.Frontier(t.Context(), m); err != nil || frontier != partial {
			t.Fatal("migrated final page advanced past its corrected partial date")
		}
	}
	if recordsCount(t, s) != len(metrics) {
		t.Fatal("migration or resumed publication lost published records")
	}
	if until, err := s.Meta(t.Context(), "history_civil_until"); err != nil || until != "2026-10-22" {
		t.Fatal("migration shifted the existing civil history plan")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if frontier, err := s.Frontier(t.Context(), metrics[1]); err != nil || frontier != "2026-10-20" {
		t.Fatal("reopening repeated the civil clock rewind")
	}
	if backups, err := filepath.Glob(path + ".pre-v4-*.bak"); err != nil || len(backups) != 1 {
		t.Fatal("reopening repeated the schema migration")
	}
}

func TestV2CivilMigrationFailureRollsBackDatesAndSavedTransfers(t *testing.T) {
	path, db, metrics, original := openV2CivilFixture(t, true)
	if _, err := OpenStore(t.Context(), path); Code(err) != "migration_failed" {
		t.Fatalf("invalid civil migration metadata was accepted: %v", err)
	}
	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("failed migration changed the original version")
	}
	for _, m := range metrics {
		var frontier, partial, cursor string
		if err := db.QueryRowContext(t.Context(), `SELECT sync_until,daily_date,next_page_token FROM stream_state JOIN windows USING(stream) WHERE windows.id=?`, original.ID(m)).Scan(&frontier, &partial, &cursor); err != nil || frontier != "2026-10-21" || cursor != "saved-v2-cursor" {
			t.Fatal("failed migration changed a frontier or cursor")
		}
		want := original.DailyDate
		if m.Kind == "exercise" {
			want = "invalid-civil-date"
		}
		if partial != want {
			t.Fatal("failed migration changed an original partial date")
		}
	}
	var civilEndCount, staged int
	if err := db.QueryRowContext(t.Context(), "SELECT (SELECT COUNT(*) FROM meta WHERE key='history_civil_until'),(SELECT COUNT(*) FROM staged_records)").Scan(&civilEndCount, &staged); err != nil || civilEndCount != 0 || staged != len(metrics) {
		t.Fatal("failed migration changed history metadata or staging")
	}
	if backups, err := filepath.Glob(path + ".pre-v4-*.bak"); err != nil || len(backups) != 1 {
		t.Fatal("failed migration did not retain its original backup")
	}
}
