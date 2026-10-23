package collector

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
)

type StreamStatus struct {
	DataType     string `json:"data_type"`
	Method       string `json:"method"`
	ScopeGranted bool   `json:"scope_granted"`
	Records      int    `json:"records"`
	StoredJSON   int    `json:"stored_json_records"`
	Complete     int    `json:"completed_windows"`
	Empty        int    `json:"empty_windows"`
	Unavailable  int    `json:"unavailable_windows"`
	Failed       int    `json:"failed_windows"`
	Pending      int    `json:"pending_windows"`
	DailyPending int    `json:"unfinished_daily_windows"`
	SyncUntil    string `json:"sync_until,omitempty"`
}

type Status struct {
	Status                    string         `json:"status"`
	IdentityAndSampleVerified bool           `json:"identity_and_sample_verified"`
	AuthorizationCheckedLive  bool           `json:"authorization_checked_live"`
	AuthorizationProblem      string         `json:"authorization_problem,omitempty"`
	CredentialsUsable         bool           `json:"credentials_usable_for_refresh"`
	OAuthPublishingStatus     string         `json:"oauth_publishing_status"`
	TestingTokenExpiryRisk    bool           `json:"testing_token_expiry_risk"`
	MissingScopes             []string       `json:"missing_scopes"`
	HistoryScope              string         `json:"history_scope"`
	HistorySince              string         `json:"history_since,omitempty"`
	HistoryUntil              string         `json:"history_until,omitempty"`
	HistoryCivilUntil         string         `json:"history_civil_until,omitempty"`
	SourceHistoryComplete     bool           `json:"source_history_complete"`
	AggregateHistoryComplete  bool           `json:"aggregate_history_complete"`
	AggregateFloor            string         `json:"aggregate_floor,omitempty"`
	AggregateFloorProvenance  string         `json:"aggregate_floor_provenance,omitempty"`
	OlderAggregateUnverified  bool           `json:"older_aggregate_history_unverified"`
	UnattendedReady           bool           `json:"unattended_ready"`
	LastRunStatus             string         `json:"last_run_status,omitempty"`
	LastRunFinished           string         `json:"last_run_finished_at,omitempty"`
	LastSyncStatus            string         `json:"last_sync_status,omitempty"`
	LastSyncFinished          string         `json:"last_sync_finished_at,omitempty"`
	Streams                   []StreamStatus `json:"streams"`
}

func (e *Engine) Status(ctx context.Context, live bool) (Status, error) {
	now := e.Now()
	status := Status{Status: "ok", IdentityAndSampleVerified: e.OAuth.Tokens.Verified,
		AuthorizationProblem: e.OAuth.Tokens.Problem, OAuthPublishingStatus: e.OAuth.Tokens.PublishingStatus,
		TestingTokenExpiryRisk: e.OAuth.Tokens.PublishingStatus != "production", MissingScopes: []string{},
		OlderAggregateUnverified: true, Streams: []StreamStatus{}}
	var err error
	status.HistorySince, err = e.Store.Meta(ctx, "history_since")
	if err != nil {
		return status, err
	}
	status.HistoryScope = "all_source_history"
	if status.HistorySince != "" {
		status.HistoryScope = "selected_range"
	}
	if live {
		if err := e.CheckIdentity(ctx); err != nil {
			return status, err
		}
		status.AuthorizationCheckedLive = true
		status.IdentityAndSampleVerified = e.OAuth.Tokens.Verified
	}
	for _, scope := range RequiredScopes {
		if !slices.Contains(e.OAuth.Tokens.Scopes, scope) {
			status.MissingScopes = append(status.MissingScopes, scope)
		}
	}
	var finished sql.NullString
	err = e.Store.db.QueryRowContext(ctx, "SELECT status,finished_at FROM runs ORDER BY id DESC LIMIT 1").Scan(&status.LastRunStatus, &finished)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return status, &Error{Code: "storage_error"}
	}
	status.LastRunFinished = finished.String
	err = e.Store.db.QueryRowContext(ctx, "SELECT status,finished_at FROM runs WHERE command='sync' ORDER BY id DESC LIMIT 1").Scan(&status.LastSyncStatus, &finished)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return status, &Error{Code: "storage_error"}
	}
	if err == nil {
		status.LastSyncFinished = finished.String
	}
	allRecent := true
	for _, m := range e.Metrics {
		stream := StreamStatus{DataType: m.Name, Method: m.Method, ScopeGranted: slices.Contains(e.OAuth.Tokens.Scopes, m.Scope)}
		err := e.Store.db.QueryRowContext(ctx, `SELECT
 COUNT(CASE WHEN state='complete' THEN 1 END),COUNT(CASE WHEN state='complete' AND record_count=0 THEN 1 END),
 COUNT(CASE WHEN state='unavailable' THEN 1 END),COUNT(CASE WHEN state='failed' THEN 1 END),COUNT(CASE WHEN state='in_progress' THEN 1 END)
 FROM windows WHERE stream=?`, m.Stream()).Scan(&stream.Complete, &stream.Empty, &stream.Unavailable, &stream.Failed, &stream.Pending)
		if err != nil {
			return status, &Error{Code: "storage_error"}
		}
		pending, err := e.pendingDailyWindows(ctx, m, status.HistorySince)
		if err != nil {
			return status, err
		}
		stream.DailyPending = len(pending)
		if err := e.Store.db.QueryRowContext(ctx, "SELECT COUNT(CASE WHEN has_value=1 THEN 1 END),COUNT(*) FROM records WHERE stream=?", m.Stream()).Scan(&stream.Records, &stream.StoredJSON); err != nil {
			return status, &Error{Code: "storage_error"}
		}
		stream.SyncUntil, err = e.Store.Frontier(ctx, m)
		if err != nil {
			return status, err
		}
		partial, _ := m.dateBounds(now)
		if stream.SyncUntil < partial || stream.DailyPending > 0 || !stream.ScopeGranted {
			allRecent = false
		}
		status.Streams = append(status.Streams, stream)
	}
	utcUntil, err := e.Store.Meta(ctx, "history_until")
	if err != nil {
		return status, err
	}
	civilUntil, err := e.Store.Meta(ctx, "history_civil_until")
	if err != nil {
		return status, err
	}
	status.HistoryUntil, status.HistoryCivilUntil = utcUntil, civilUntil
	status.AggregateFloor, err = e.Store.Meta(ctx, "aggregate_floor")
	if err != nil {
		return status, err
	}
	status.AggregateFloorProvenance, err = e.Store.Meta(ctx, "aggregate_floor_provenance")
	if err != nil {
		return status, err
	}
	status.OlderAggregateUnverified = status.AggregateFloorProvenance != "explicit"
	if utcUntil != "" || civilUntil != "" {
		status.SourceHistoryComplete = true
		for _, m := range e.Metrics {
			if m.Kind == "rollup" {
				continue
			}
			until := utcUntil
			if m.usesCivilDates() {
				until = civilUntil
			}
			if until == "" {
				status.SourceHistoryComplete = false
				continue
			}
			windows, _, err := e.HistoryPlan(ctx, Options{}, m)
			if err != nil {
				return status, err
			}
			for _, w := range windows {
				complete, err := e.Store.WindowComplete(ctx, m, w)
				if err != nil {
					return status, err
				}
				if !complete || !slices.Contains(e.OAuth.Tokens.Scopes, m.Scope) {
					status.SourceHistoryComplete = false
				}
			}
		}
		if status.AggregateFloor != "" {
			status.AggregateHistoryComplete = true
			for _, m := range e.Metrics {
				if m.Kind != "rollup" {
					continue
				}
				if civilUntil == "" {
					status.AggregateHistoryComplete = false
					continue
				}
				aggregates, err := Windows(status.AggregateFloor, civilUntil, 14)
				if err != nil {
					return status, err
				}
				for _, w := range aggregates {
					complete, err := e.Store.WindowComplete(ctx, m, w)
					if err != nil {
						return status, err
					}
					if !complete || !slices.Contains(e.OAuth.Tokens.Scopes, m.Scope) {
						status.AggregateHistoryComplete = false
					}
				}
			}
		}
	}
	lastSync, _ := time.Parse(time.RFC3339Nano, status.LastSyncFinished)
	refreshUsable := e.OAuth.Tokens.Refresh != "" && e.OAuth.Tokens.Problem == "" &&
		(e.OAuth.Tokens.RefreshExpires.IsZero() || now.Before(e.OAuth.Tokens.RefreshExpires))
	status.CredentialsUsable = refreshUsable
	status.UnattendedReady = status.IdentityAndSampleVerified && refreshUsable && !status.TestingTokenExpiryRisk && len(status.MissingScopes) == 0 &&
		status.SourceHistoryComplete && status.AggregateHistoryComplete && !status.OlderAggregateUnverified && allRecent &&
		status.LastSyncStatus == "complete" && !lastSync.IsZero() && now.Sub(lastSync) < 48*time.Hour
	return status, nil
}
