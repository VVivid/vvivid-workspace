package collector

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type HealthAPI interface {
	Identity(context.Context) (string, error)
	Page(context.Context, Metric, Window, string, int) (Page, error)
}

type Engine struct {
	Store    *Store
	OAuth    *OAuth
	API      HealthAPI
	Metrics  []Metric
	Now      func() time.Time
	Progress func(Result)
}

func NewEngine(store *Store, oauth *OAuth, api HealthAPI) *Engine {
	return &Engine{Store: store, OAuth: oauth, API: api, Metrics: Metrics(), Now: time.Now}
}

type Result struct {
	Command     string `json:"command"`
	Status      string `json:"status"`
	ErrorCode   string `json:"error_code,omitempty"`
	HTTPStatus  int    `json:"http_status,omitzero"`
	SQLiteCode  int    `json:"sqlite_code,omitzero"`
	Completed   int    `json:"completed_windows"`
	Unavailable int    `json:"unavailable_windows"`
	Skipped     int    `json:"skipped_windows"`
	Pages       int    `json:"pages"`
}

type Options struct {
	Since          string
	HistorySince   string
	Until          string
	AggregateSince string
	MaxPages       int // zero is unlimited; a reached budget is a clean resumable pause
	Force          bool
}

func (e *Engine) CheckIdentity(ctx context.Context) error {
	id, err := e.API.Identity(ctx)
	if err != nil {
		if slices.Contains([]string{"account_not_linked", "reauthorization_required"}, Code(err)) {
			e.OAuth.Tokens.Verified, e.OAuth.Tokens.Problem = false, Code(err)
			if saveErr := e.OAuth.Save(); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if e.OAuth.Tokens.HealthUserID != "" && e.OAuth.Tokens.HealthUserID != id {
		e.OAuth.Tokens.Verified, e.OAuth.Tokens.Problem = false, "identity_mismatch"
		if err := e.OAuth.Save(); err != nil {
			return err
		}
		return &Error{Code: "identity_mismatch"}
	}
	if err := e.Store.PinIdentity(ctx, id); err != nil {
		e.OAuth.Tokens.Verified, e.OAuth.Tokens.Problem = false, Code(err)
		if saveErr := e.OAuth.Save(); saveErr != nil {
			return saveErr
		}
		return err
	}
	e.OAuth.Tokens.HealthUserID = id
	return nil
}

// VerifyAuthorization does not mark an empty account authorized. One actual
// schema-valid health record and a pinned identity are required first.
func (e *Engine) VerifyAuthorization(ctx context.Context) error {
	e.OAuth.Tokens.Verified = false
	if !e.OAuth.HasAnyScope() {
		return &Error{Code: "missing_consent"}
	}
	if err := e.CheckIdentity(ctx); err != nil {
		return err
	}
	now := e.Now()
	for _, m := range e.Metrics {
		if m.Kind == "rollup" || !slices.Contains(e.OAuth.Tokens.Scopes, m.Scope) {
			continue
		}
		_, until := m.dateBounds(now)
		w := Window{Until: until}
		page, err := e.API.Page(ctx, m, w, "", 1)
		if err != nil {
			if slices.Contains([]string{"missing_scope", "resource_unavailable"}, Code(err)) {
				continue
			}
			return err
		}
		if len(page.Data) == 0 {
			continue
		}
		if _, err := DecodeRecord(m, page.Data[0], w); err != nil {
			return err
		}
		e.OAuth.Tokens.Verified, e.OAuth.Tokens.VerifiedAt, e.OAuth.Tokens.Problem = true, timestamp(), ""
		return e.OAuth.Save()
	}
	e.OAuth.Tokens.Problem = "no_health_sample"
	if err := e.OAuth.Save(); err != nil {
		return err
	}
	return &Error{Code: "no_health_sample"}
}

func (e *Engine) prepare(ctx context.Context) error {
	if !e.OAuth.Tokens.Verified || !e.OAuth.HasAnyScope() || e.OAuth.Tokens.Refresh == "" {
		return &Error{Code: "authorization_not_verified"}
	}
	if err := e.CheckIdentity(ctx); err != nil {
		return err
	}
	origin, err := e.Store.Meta(ctx, "sync_origin")
	if err != nil {
		return err
	}
	if origin == "" {
		return e.Store.SetMeta(ctx, "sync_origin", e.Now().UTC().AddDate(0, 0, -6).Format(time.DateOnly))
	}
	return nil
}

func (e *Engine) run(ctx context.Context, command string, work func(int64, *Result) error) (Result, error) {
	result := Result{Command: command, Status: "complete"}
	id, err := e.Store.StartRun(ctx, command)
	if err != nil {
		return result, err
	}
	err = e.prepare(ctx)
	if err == nil {
		err = work(id, &result)
	}
	if err != nil {
		result.ErrorCode = Code(err)
		result.HTTPStatus, result.SQLiteCode = DiagnosticCodes(err)
		if Code(err) == "budget_exhausted" {
			result.Status, result.ErrorCode = "paused", ""
			err = nil
		} else {
			result.Status = "failed"
		}
	} else if result.Unavailable > 0 {
		result.Status, result.ErrorCode = "partial", "data_unavailable"
		err = &Error{Code: "data_unavailable"}
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if finishErr := e.Store.FinishRun(finishContext, id, result); finishErr != nil {
		return result, finishErr
	}
	return result, err
}

func (e *Engine) Sync(ctx context.Context, options Options) (Result, error) {
	return e.run(ctx, "sync", func(run int64, result *Result) error {
		now := e.Now()
		historySince, err := e.Store.Meta(ctx, "history_since")
		if err != nil {
			return err
		}
		origin, err := e.Store.Meta(ctx, "sync_origin")
		if err != nil {
			return err
		}
		var firstFailure error
		for _, m := range e.Metrics {
			partial, until := m.dateBounds(now)
			partialDay, _ := ParseDate(partial)
			recent := partialDay.AddDate(0, 0, -6).Format(time.DateOnly)
			pending, err := e.pendingDailyWindows(ctx, m, historySince)
			if err != nil {
				return err
			}
			// These saved boundaries and partial dates belong to the staged
			// cursor. Finish them before building requests for the current date.
			resumeFailed := false
			for _, w := range pending {
				if err := e.importWindow(ctx, run, m, w, true, options.MaxPages, result); err != nil {
					if stopsRun(err) {
						return err
					}
					if Code(err) != "data_unavailable" && firstFailure == nil {
						firstFailure = err
					}
					resumeFailed = true
					break
				}
			}
			if resumeFailed {
				continue
			}
			frontier, err := e.Store.Frontier(ctx, m)
			if err != nil {
				return err
			}
			from := recent
			if frontier == "" {
				from = min(from, origin)
			} else {
				from = min(from, frontier)
			}
			from = max(from, historySince)
			if from >= until {
				continue
			}
			windows, err := Windows(from, until, 7)
			if err != nil {
				return err
			}
			for _, w := range windows {
				w.DailyDate = partial
				// SavePage publishes daily coverage and its settled frontier in
				// one transaction, capped before this attempt's partial date.
				err := e.importWindow(ctx, run, m, w, true, options.MaxPages, result)
				if err != nil {
					if stopsRun(err) {
						return err
					}
					if Code(err) != "data_unavailable" && firstFailure == nil {
						firstFailure = err
					}
					break // Never advance this stream across an unavailable or failed gap.
				}
			}
		}
		return firstFailure
	})
}

// A changed history floor can exclude or clip a saved daily transfer. Keep its
// audit row, but resume only the selected range; a clipped range has a new ID
// and starts a fresh snapshot. Existing wholly in-scope cursors keep their IDs.
func (e *Engine) pendingDailyWindows(ctx context.Context, m Metric, historySince string) ([]Window, error) {
	pending, err := e.Store.PendingDailyWindows(ctx, m)
	if err != nil {
		return nil, err
	}
	var selected []Window
	seen := map[string]bool{}
	for _, w := range pending {
		if w.Until <= historySince {
			continue
		}
		if w.Since < historySince {
			w.Since = historySince
			complete, err := e.Store.WindowComplete(ctx, m, w)
			if err != nil {
				return nil, err
			}
			if complete {
				continue
			}
		}
		if id := w.ID(m); !seen[id] {
			selected = append(selected, w)
			seen[id] = true
		}
	}
	return selected, nil
}

// Other failures stop only the affected stream, allowing independent streams
// to make progress. These failures require stopping the entire import run.
func stopsRun(err error) bool {
	return slices.Contains([]string{"budget_exhausted", "reauthorization_required", "identity_mismatch", "interrupted", "storage_error", "token_write_failed"}, Code(err))
}

// historyUntil reads the clock-specific pinned end without mutating metadata.
// Backfill saves both ends atomically after validating the entire configuration.
// An explicit Since builds a repair plan without changing the saved defaults.
func (e *Engine) historyUntil(ctx context.Context, options Options, m Metric) (string, error) {
	until, since := options.Until, options.Since
	if until == "" && since != "" {
		_, until = m.dateBounds(e.Now())
	}
	if since == "" {
		var err error
		until, err = e.Store.Meta(ctx, m.historyKey())
		if err != nil {
			return "", err
		}
		if until == "" {
			until = options.Until
			if until == "" {
				_, until = m.dateBounds(e.Now())
			}
		} else if options.Until != "" && options.Until != until {
			return "", &Error{Code: "history_plan_already_exists"}
		}
	}
	if _, err := ParseDate(until); err != nil {
		return "", err
	}
	return until, nil
}

// HistoryPlan retains window IDs and API filters across resumed runs.
func (e *Engine) HistoryPlan(ctx context.Context, options Options, m Metric) ([]Window, string, error) {
	until, err := e.historyUntil(ctx, options, m)
	if err != nil {
		return nil, "", err
	}
	since := options.Since
	if since == "" {
		end, _ := ParseDate(until)
		since = end.AddDate(-1, 0, 0).Format(time.DateOnly)
	}
	windows, err := Windows(since, until, 30)
	if err != nil {
		return nil, "", err
	}
	slices.Reverse(windows)
	if options.Since == "" {
		floor, err := e.Store.Meta(ctx, "history_since")
		if err != nil {
			return nil, "", err
		}
		if floor == "" {
			windows = append(windows, Window{Until: since})
		} else {
			if _, err := ParseDate(floor); err != nil {
				return nil, "", err
			}
			if floor >= until {
				return nil, "", &Error{Code: "invalid_range"}
			}
			// Retain the original end-anchored windows and their cursors. Only
			// a boundary crossing the selected floor needs different bounds.
			selected := make([]Window, 0, len(windows))
			for _, w := range windows {
				if w.Until > floor {
					w.Since = max(w.Since, floor)
					selected = append(selected, w)
				}
			}
			if floor < since {
				older, err := Windows(floor, since, 30)
				if err != nil {
					return nil, "", err
				}
				slices.Reverse(older)
				selected = append(selected, older...)
			}
			windows = selected
		}
	}
	return windows, until, nil
}

// backfillMetadata validates every default bound before any metadata changes.
// A repair may use different bounds, but cannot change the saved history scope.
func (e *Engine) backfillMetadata(ctx context.Context, options Options) (map[string]string, error) {
	for _, date := range []string{options.Since, options.HistorySince, options.Until, options.AggregateSince} {
		if date != "" {
			if _, err := ParseDate(date); err != nil {
				return nil, err
			}
		}
	}
	if options.HistorySince != "" && options.Since != "" {
		return nil, &Error{Code: "conflicting_history_options"}
	}
	values := map[string]string{}
	for _, m := range []Metric{{Kind: "sample"}, {Kind: "daily"}} {
		until, err := e.historyUntil(ctx, options, m)
		if err != nil {
			return nil, err
		}
		values[m.historyKey()] = until
	}
	if options.Since != "" {
		floor := options.Since
		if options.AggregateSince != "" {
			floor = options.AggregateSince
		}
		if options.Since >= values["history_until"] || options.Since >= values["history_civil_until"] || floor >= values["history_civil_until"] {
			return nil, &Error{Code: "invalid_range"}
		}
		return nil, nil
	}
	floor := options.HistorySince
	if floor == "" {
		var err error
		floor, err = e.Store.Meta(ctx, "history_since")
		if err != nil {
			return nil, err
		}
	}
	aggregateFloor, provenance, err := e.aggregateFloor(ctx, options)
	if err != nil {
		return nil, err
	}
	if floor != "" {
		if _, err := ParseDate(floor); err != nil {
			return nil, err
		}
		if floor >= values["history_until"] || floor >= values["history_civil_until"] {
			return nil, &Error{Code: "invalid_range"}
		}
		if options.AggregateSince != "" && options.AggregateSince != floor {
			return nil, &Error{Code: "conflicting_history_options"}
		}
		values["history_since"] = floor
		aggregateFloor, provenance = floor, "explicit"
	}
	if aggregateFloor != "" {
		if _, err := ParseDate(aggregateFloor); err != nil {
			return nil, err
		}
		if aggregateFloor >= values["history_civil_until"] {
			return nil, &Error{Code: "invalid_range"}
		}
		values["aggregate_floor"], values["aggregate_floor_provenance"] = aggregateFloor, provenance
	}
	return values, nil
}

func (e *Engine) Backfill(ctx context.Context, options Options) (Result, error) {
	metadata, err := e.backfillMetadata(ctx, options)
	if err != nil {
		return Result{Command: "backfill", Status: "failed", ErrorCode: Code(err)}, err
	}
	return e.run(ctx, "backfill", func(run int64, result *Result) error {
		if len(metadata) > 0 {
			if err := e.Store.SetMetaValues(ctx, metadata); err != nil {
				return err
			}
		}
		var firstFailure error
		for _, m := range e.Metrics {
			if m.Kind == "rollup" {
				continue
			}
			windows, _, err := e.HistoryPlan(ctx, options, m)
			if err != nil {
				return err
			}
			for _, w := range windows {
				if err := e.importWindow(ctx, run, m, w, options.Force, options.MaxPages, result); err != nil {
					if stopsRun(err) {
						return err
					}
					if Code(err) != "data_unavailable" && firstFailure == nil {
						firstFailure = err
					}
					break
				}
			}
		}
		floor, provenance, err := e.aggregateFloor(ctx, options)
		if err != nil {
			return err
		}
		if floor == "" {
			if firstFailure == nil {
				firstFailure = &Error{Code: "aggregate_floor_unknown"}
			}
			return firstFailure
		}
		if options.Since == "" {
			if err := e.Store.SetMetaValues(ctx, map[string]string{"aggregate_floor": floor, "aggregate_floor_provenance": provenance}); err != nil {
				return err
			}
		}
		for _, m := range e.Metrics {
			if m.Kind != "rollup" {
				continue
			}
			until, err := e.historyUntil(ctx, options, m)
			if err != nil {
				return err
			}
			aggregateWindows, err := Windows(floor, until, 14)
			if err != nil {
				return err
			}
			slices.Reverse(aggregateWindows)
			for _, w := range aggregateWindows {
				if err := e.importWindow(ctx, run, m, w, options.Force, options.MaxPages, result); err != nil {
					if stopsRun(err) {
						return err
					}
					if Code(err) != "data_unavailable" && firstFailure == nil {
						firstFailure = err
					}
					break
				}
			}
		}
		return firstFailure
	})
}

func (e *Engine) aggregateFloor(ctx context.Context, options Options) (string, string, error) {
	if options.AggregateSince != "" {
		_, err := ParseDate(options.AggregateSince)
		return options.AggregateSince, "explicit", err
	}
	if options.Since != "" {
		return options.Since, "explicit_range", nil
	}
	historySince, err := e.Store.Meta(ctx, "history_since")
	if err != nil {
		return "", "", err
	}
	if historySince != "" {
		return historySince, "explicit", nil
	}
	previous, err := e.Store.Meta(ctx, "aggregate_floor")
	if err != nil {
		return "", "", err
	}
	provenance, err := e.Store.Meta(ctx, "aggregate_floor_provenance")
	if err != nil {
		return "", "", err
	}
	if provenance == "explicit" {
		return previous, provenance, nil
	}
	earliest, err := e.Store.EarliestSourceDate(ctx)
	if err != nil {
		return "", "", err
	}
	if previous != "" && (earliest == "" || previous < earliest) {
		earliest = previous
	}
	return earliest, "inferred_from_source_records", nil
}

func (e *Engine) importWindow(ctx context.Context, run int64, m Metric, w Window, force bool, maxPages int, result *Result) error {
	if !slices.Contains(e.OAuth.Tokens.Scopes, m.Scope) {
		if err := e.Store.MarkWindow(ctx, run, m, w, "unavailable", "missing_scope"); err != nil {
			return err
		}
		result.Unavailable++
		return &Error{Code: "data_unavailable"}
	}
	transfer, err := e.Store.BeginWindow(ctx, run, m, w, force)
	if err != nil {
		return err
	}
	if transfer.Done {
		result.Skipped++
		return nil
	}
	restarted := false
	for {
		if maxPages > 0 && result.Pages >= maxPages {
			return &Error{Code: "budget_exhausted"}
		}
		page, err := e.API.Page(ctx, m, w, transfer.Token, 0)
		if err != nil {
			apiError, _ := errors.AsType[*Error](err)
			badCursor := Code(err) == "invalid_page_token" || (apiError != nil && apiError.Status == 400)
			if transfer.Token != "" && badCursor && !restarted {
				if err := e.Store.ResetWindow(ctx, m, w); err != nil {
					return err
				}
				transfer.Token, restarted = "", true
				continue
			}
			state := "failed"
			if Code(err) == "missing_scope" {
				e.OAuth.Tokens.Scopes = slices.DeleteFunc(e.OAuth.Tokens.Scopes, func(scope string) bool { return scope == m.Scope })
				if saveErr := e.OAuth.Save(); saveErr != nil {
					return saveErr
				}
			}
			if slices.Contains([]string{"missing_scope", "resource_unavailable"}, Code(err)) {
				state = "unavailable"
				result.Unavailable++
			}
			if markErr := e.Store.MarkWindow(ctx, run, m, w, state, Code(err)); markErr != nil {
				return markErr
			}
			if state == "unavailable" {
				return &Error{Code: "data_unavailable"}
			}
			return err
		}
		records := make([]Record, 0, len(page.Data))
		for _, raw := range page.Data {
			r, err := DecodeRecord(m, json.RawMessage(raw), w)
			if err != nil {
				if markErr := e.Store.MarkWindow(ctx, run, m, w, "failed", Code(err)); markErr != nil {
					return markErr
				}
				return err
			}
			records = append(records, r)
		}
		if err := e.Store.SavePage(ctx, m, w, records, page.Next); err != nil {
			if markErr := e.Store.MarkWindow(ctx, run, m, w, "failed", Code(err)); markErr != nil {
				return markErr
			}
			return err
		}
		result.Pages++
		if e.Progress != nil && result.Pages%25 == 0 {
			progress := *result
			progress.Status = "progress"
			e.Progress(progress)
		}
		if page.Next == "" {
			result.Completed++
			return nil
		}
		transfer.Token = page.Next
	}
}
