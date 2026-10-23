package collector

import (
	"maps"
	"testing"
	"time"
)

const selectedHistorySince = "2026-06-14"

func savedMetadata(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "SELECT key,value FROM meta")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestSelectedHistoryPersistsAcrossBudgetPauseAndPreservesExistingData(t *testing.T) {
	e, a := fixture(t)
	e.Metrics = Metrics()
	e.OAuth.Tokens.PublishingStatus = "production"
	m := metric("steps", "interval", "activity_and_fitness")
	if err := e.Store.SetMetaValues(t.Context(), map[string]string{
		"history_until": "2026-10-21", "history_civil_until": "2026-10-22",
		"aggregate_floor": "2018-10-03", "aggregate_floor_provenance": "inferred_from_source_records",
	}); err != nil {
		t.Fatal(err)
	}
	oldWindow := Window{Until: "2025-10-21"}
	oldJSON := steps("retained-before-owner-floor", "2018-10-03", 12)
	run := startRun(t, e.Store)
	if _, err := e.Store.BeginWindow(t.Context(), run, m, oldWindow, false); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SavePage(t.Context(), m, oldWindow, []Record{record(t, m, oldWindow, oldJSON)}, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.MarkWindow(t.Context(), run, m, oldWindow, "failed", "api_unavailable"); err != nil {
		t.Fatal(err)
	}
	oldDaily := Window{Since: "2026-06-01", Until: selectedHistorySince, DailyDate: "2026-06-13"}
	if err := e.Store.MarkWindow(t.Context(), run, m, oldDaily, "failed", "network_error"); err != nil {
		t.Fatal(err)
	}
	// This is a pre-existing default-plan window, with its first page staged.
	// Selecting the floor must preserve its exact ID and saved next-page cursor.
	saved := Window{Since: "2026-09-16", Until: "2026-10-16"}
	inScopeJSON := steps("preserved-staging", "2026-09-30", 19)
	if _, err := e.Store.BeginWindow(t.Context(), run, m, saved, false); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SavePage(t.Context(), m, saved, []Record{record(t, m, saved, inScopeJSON)}, "saved-in-scope-cursor"); err != nil {
		t.Fatal(err)
	}
	resumed := false
	a.page = func(metric Metric, w Window, token string) (Page, error) {
		if w.Since < selectedHistorySince || w.Since >= w.Until {
			t.Fatalf("request escaped selected history: %s %+v", metric.Name, w)
		}
		if metric.Stream() == m.Stream() && w == saved {
			if token != "saved-in-scope-cursor" {
				t.Fatal("existing in-scope cursor was discarded")
			}
			resumed = true
		}
		return Page{}, nil
	}
	result, err := e.Backfill(t.Context(), Options{HistorySince: selectedHistorySince, MaxPages: 1})
	if err != nil || result.Status != "paused" || resumed {
		t.Fatalf("selection did not pause before the saved window: %+v %v", result, err)
	}
	assertSavedTransfer(t, e.Store, m, saved, "saved-in-scope-cursor", 1)
	metadata := savedMetadata(t, e.Store)
	if metadata["history_since"] != selectedHistorySince || metadata["aggregate_floor"] != selectedHistorySince || metadata["aggregate_floor_provenance"] != "explicit" {
		t.Fatal("source and aggregate selection was not durable before the page-budget pause")
	}
	if metadata["history_until"] != "2026-10-21" || metadata["history_civil_until"] != "2026-10-22" {
		t.Fatal("selecting a floor changed the pinned ends")
	}
	status, err := e.Status(t.Context(), false)
	if err != nil || status.HistoryScope != "selected_range" || status.HistorySince != selectedHistorySince || status.SourceHistoryComplete || status.AggregateHistoryComplete || status.UnattendedReady {
		t.Fatalf("paused selected history was misrepresented: %+v %v", status, err)
	}
	assertStoredJSON(t, e.Store, m, oldWindow, oldJSON)
	reopenEngineStore(t, e)
	if result, err = e.Backfill(t.Context(), Options{}); err != nil || result.Status != "complete" || !resumed {
		t.Fatalf("flag-free restart did not resume selected history: %+v %v", result, err)
	}
	assertStoredJSON(t, e.Store, m, saved, inScopeJSON)
	assertStoredJSON(t, e.Store, m, oldWindow, oldJSON)
	if _, err := e.Sync(t.Context(), Options{}); err != nil {
		t.Fatal(err)
	}
	currentSyncTimestamp(t, e)
	var sequence, version int
	var name, path string
	if err := e.Store.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("history scope unexpectedly changed the schema")
	}
	reader, err := OpenReadOnlyStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readEngine := *e
	readEngine.Store = reader
	status, err = readEngine.Status(t.Context(), false)
	if err != nil || !status.SourceHistoryComplete || !status.AggregateHistoryComplete || status.OlderAggregateUnverified || !status.UnattendedReady || status.HistoryScope != "selected_range" {
		t.Fatalf("completed selected history did not become ready through read-only status: %+v %v", status, err)
	}
	for _, stream := range status.Streams {
		if stream.DataType == m.Name && (stream.Failed != 2 || stream.DailyPending != 0) {
			t.Fatal("older failure evidence was lost or blocked selected-range readiness")
		}
	}
	assertStoredJSON(t, e.Store, m, oldWindow, oldJSON)
	if recordsCount(t, e.Store) != 2 {
		t.Fatal("selection deleted older records or lost the resumed staged record")
	}
}

func TestSelectedHistoryEmptyAndUnavailableCoverage(t *testing.T) {
	for _, unavailable := range []string{"", "steps", "total-calories"} {
		name := unavailable
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			e, a := fixture(t)
			e.Metrics = append(e.Metrics, civilMetrics()[2])
			a.page = func(m Metric, w Window, _ string) (Page, error) {
				if w.Since < selectedHistorySince {
					t.Fatal("selected request read before the floor")
				}
				if m.Name == unavailable {
					return Page{}, &Error{Code: "resource_unavailable"}
				}
				return Page{}, nil
			}
			result, err := e.Backfill(t.Context(), Options{HistorySince: selectedHistorySince})
			if unavailable == "" && (err != nil || result.Status != "complete") {
				t.Fatalf("empty selected history was not completed: %+v %v", result, err)
			}
			if unavailable != "" && (Code(err) != "data_unavailable" || result.Unavailable != 1) {
				t.Fatalf("unavailable history became successful empty coverage: %+v %v", result, err)
			}
			status, err := e.Status(t.Context(), false)
			if err != nil || status.SourceHistoryComplete != (unavailable != "steps") || status.AggregateHistoryComplete != (unavailable != "total-calories") || recordsCount(t, e.Store) != 0 {
				t.Fatalf("empty/unavailable status was dishonest: %+v %v", status, err)
			}
		})
	}
}

func TestInvalidHistorySelectionDoesNotMutateMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options Options
		code    string
	}{
		{"invalid_date", Options{HistorySince: "2026-06-31"}, "invalid_date"},
		{"at_pinned_end", Options{HistorySince: "2026-10-21"}, "invalid_range"},
		{"after_pinned_ends", Options{HistorySince: "2027-01-01"}, "invalid_range"},
		{"invalid_until", Options{HistorySince: selectedHistorySince, Until: "invalid"}, "invalid_date"},
		{"different_pinned_end", Options{HistorySince: selectedHistorySince, Until: "2026-10-22"}, "history_plan_already_exists"},
		{"different_aggregate_floor", Options{HistorySince: selectedHistorySince, AggregateSince: "2018-10-03"}, "conflicting_history_options"},
		{"repair_and_selection", Options{HistorySince: selectedHistorySince, Since: "2026-07-01"}, "conflicting_history_options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, a := fixture(t)
			if err := e.Store.SetMetaValues(t.Context(), map[string]string{
				"history_until": "2026-10-21", "history_civil_until": "2026-10-22",
				"aggregate_floor": "2018-10-03", "aggregate_floor_provenance": "inferred_from_source_records",
			}); err != nil {
				t.Fatal(err)
			}
			before := savedMetadata(t, e.Store)
			a.page = func(Metric, Window, string) (Page, error) {
				t.Fatal("invalid selection reached the API")
				return Page{}, nil
			}
			if _, err := e.Backfill(t.Context(), tc.options); Code(err) != tc.code {
				t.Fatalf("wrong validation result: %v", err)
			}
			if !maps.Equal(before, savedMetadata(t, e.Store)) {
				t.Fatal("invalid selection changed metadata")
			}
		})
	}
}

func TestHistorySelectionMetadataUpdateRollsBackTogether(t *testing.T) {
	e, a := fixture(t)
	if err := e.Store.SetMetaValues(t.Context(), map[string]string{
		"health_user_id": "test-owner", "sync_origin": "2026-10-14",
		"history_until": "2026-10-21", "history_civil_until": "2026-10-22", "history_since": "2026-06-01",
		"aggregate_floor": "2026-06-01", "aggregate_floor_provenance": "explicit",
	}); err != nil {
		t.Fatal(err)
	}
	before := savedMetadata(t, e.Store)
	if _, err := e.Store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_selection BEFORE UPDATE ON meta
 WHEN NEW.key='aggregate_floor_provenance' BEGIN SELECT RAISE(FAIL,'synthetic update failure'); END`); err != nil {
		t.Fatal(err)
	}
	a.page = func(Metric, Window, string) (Page, error) {
		t.Fatal("failed configuration reached the API")
		return Page{}, nil
	}
	if _, err := e.Backfill(t.Context(), Options{HistorySince: selectedHistorySince}); Code(err) != "storage_error" {
		t.Fatalf("expected metadata transaction failure: %v", err)
	}
	if !maps.Equal(before, savedMetadata(t, e.Store)) {
		t.Fatal("failed configuration left different source/aggregate bounds")
	}
}

func TestDailySyncAppliesSelectedFloorToSavedAndCurrentWindows(t *testing.T) {
	e, a := fixture(t)
	e.Metrics = append(e.Metrics, civilMetrics()[2])
	e.Now = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }
	if err := e.Store.SetMetaValues(t.Context(), map[string]string{"history_since": selectedHistorySince, "sync_origin": "2018-10-03"}); err != nil {
		t.Fatal(err)
	}
	run := startRun(t, e.Store)
	outside := Window{Since: "2026-06-10", Until: selectedHistorySince, DailyDate: "2026-06-13"}
	crossing := Window{Since: "2026-06-12", Until: "2026-06-15", DailyDate: selectedHistorySince}
	inside := Window{Since: "2026-06-15", Until: "2026-06-16", DailyDate: "2026-06-16"}
	for _, m := range e.Metrics {
		if err := e.Store.MarkWindow(t.Context(), run, m, outside, "failed", "network_error"); err != nil {
			t.Fatal(err)
		}
		for _, w := range []Window{crossing, inside} {
			if _, err := e.Store.BeginWindow(t.Context(), run, m, w, false); err != nil {
				t.Fatal(err)
			}
			if err := e.Store.SavePage(t.Context(), m, w, nil, "saved-cursor"); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, err := e.Status(t.Context(), false)
	if err != nil || status.Streams[0].DailyPending != 2 || status.Streams[1].DailyPending != 2 {
		t.Fatal("unfinished in-scope daily work was skipped")
	}
	calls, insideResumes := 0, 0
	a.page = func(_ Metric, w Window, token string) (Page, error) {
		calls++
		if w.Since < selectedHistorySince {
			t.Fatal("daily request escaped the selected floor")
		}
		if w.Until == crossing.Until && token != "" {
			t.Fatal("clipped daily range reused a cursor with different bounds")
		}
		if w == inside {
			if token != "saved-cursor" {
				t.Fatal("wholly in-scope daily cursor was lost")
			}
			insideResumes++
		}
		return Page{}, nil
	}
	if _, err := e.Sync(t.Context(), Options{}); err != nil || insideResumes != 2 {
		t.Fatalf("selected daily recovery failed: %v", err)
	}
	status, err = e.Status(t.Context(), false)
	if err != nil || status.Streams[0].DailyPending != 0 || status.Streams[1].DailyPending != 0 {
		t.Fatal("retained outside/crossing audit rows blocked completed selected daily work")
	}
	calls = 0
	if _, err := e.Sync(t.Context(), Options{}); err != nil || calls != 2 {
		t.Fatal("completed clipped recovery was repeatedly forced by its retained audit row")
	}
}

func TestBoundedRepairLeavesSavedHistoryScopeUnchanged(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_selection", true: "selected_range"}[selected], func(t *testing.T) {
			e, _ := fixture(t)
			if selected {
				if _, err := e.Backfill(t.Context(), Options{HistorySince: selectedHistorySince}); err != nil {
					t.Fatal(err)
				}
			}
			// Identity and daily origin are ordinary run preparation, not scope.
			if err := e.prepare(t.Context()); err != nil {
				t.Fatal(err)
			}
			before := savedMetadata(t, e.Store)
			if _, err := e.Backfill(t.Context(), Options{Since: "2026-07-01", Until: "2026-07-03"}); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(before, savedMetadata(t, e.Store)) {
				t.Fatal("one-off repair changed the saved history scope")
			}
		})
	}
}
