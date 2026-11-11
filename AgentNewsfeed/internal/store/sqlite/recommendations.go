package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Interest struct {
	Name       string `json:"name"`
	Preference string `json:"preference"`
}
type SourcePreference struct {
	Domain     string `json:"domain"`
	Preference string `json:"preference"`
}
type DiscoveryPreferences struct {
	Enabled          bool   `json:"enabled"`
	MaxPerRun        int    `json:"max_per_run"`
	MaxPerDay        int    `json:"max_per_day"`
	Exploration      string `json:"exploration"`
	PublicationDays  int    `json:"publication_days"`
	IncludeEvergreen bool   `json:"include_evergreen"`
}
type RecommendationPreferences struct {
	Interests        []Interest           `json:"interests"`
	ReadingBrief     string               `json:"reading_brief"`
	Avoid            string               `json:"avoid"`
	Sources          []SourcePreference   `json:"sources"`
	AllowNewSources  bool                 `json:"allow_new_sources"`
	AllowSuggestions bool                 `json:"allow_suggestions"`
	Discovery        DiscoveryPreferences `json:"discovery"`
}
type PreferencesState struct {
	Revision    int64                     `json:"revision"`
	UpdatedAt   int64                     `json:"updated_at"`
	Preferences RecommendationPreferences `json:"preferences"`
}

func DefaultPreferences() PreferencesState {
	return PreferencesState{Revision: 1, Preferences: RecommendationPreferences{
		Interests: []Interest{}, Sources: []SourcePreference{}, AllowNewSources: true, AllowSuggestions: true,
		Discovery: DiscoveryPreferences{MaxPerRun: 3, MaxPerDay: 12, Exploration: "balanced", PublicationDays: 7, IncludeEvergreen: true},
	}}
}
func preferencesState(ctx context.Context, db rowQueryer, owner Owner) (PreferencesState, error) {
	var out PreferencesState
	var raw []byte
	err := db.QueryRowContext(ctx, "SELECT revision,preferences,updated_at FROM recommendation_preferences WHERE owner_id=?", owner.id).Scan(&out.Revision, &raw, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultPreferences(), nil
	}
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(raw, &out.Preferences)
	return out, err
}
func (s *Store) RecommendationPreferences(ctx context.Context, owner Owner) (PreferencesState, error) {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return PreferencesState{}, err
	}
	return preferencesState(ctx, s.read, owner)
}
func savePreferences(ctx context.Context, tx *sql.Tx, owner Owner, current PreferencesState, preferences RecommendationPreferences) (PreferencesState, error) {
	out := PreferencesState{Revision: current.Revision + 1, UpdatedAt: time.Now().UnixMilli(), Preferences: preferences}
	raw, err := json.Marshal(preferences)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recommendation_preferences(owner_id,revision,preferences,updated_at) VALUES (?,?,?,?)
 ON CONFLICT(owner_id) DO UPDATE SET revision=excluded.revision,preferences=excluded.preferences,updated_at=excluded.updated_at`, owner.id, out.Revision, raw, out.UpdatedAt)
	return out, err
}

// Every recommendation write checks enrollment before the durable retry lookup.
// The result and all domain changes commit under the same immediate transaction.
func recommendationMutation[T any](ctx context.Context, s *Store, owner Owner, key, operation string, payload []byte, change func(*sql.Tx) (T, error)) (T, error) {
	var empty T
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err := checkOwner(ctx, tx, owner); err != nil {
		return empty, err
	}
	var oldOperation string
	var oldHash, raw []byte
	hash := sha256.Sum256(payload)
	err = tx.QueryRowContext(ctx, "SELECT operation,payload_hash,result FROM recommendation_idempotency_results WHERE owner_id=? AND key=?", owner.id, key).Scan(&oldOperation, &oldHash, &raw)
	if err == nil {
		if oldOperation != operation || string(oldHash) != string(hash[:]) {
			return empty, ErrIdempotencyConflict
		}
		var out T
		err = json.Unmarshal(raw, &out)
		return out, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	out, err := change(tx)
	if err != nil {
		return empty, err
	}
	raw, err = json.Marshal(out)
	if err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO recommendation_idempotency_results(owner_id,key,operation,payload_hash,result,created_at) VALUES (?,?,?,?,?,?)", owner.id, key, operation, hash[:], raw, time.Now().UnixMilli())
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return out, nil
}
func (s *Store) UpdateRecommendationPreferences(ctx context.Context, owner Owner, key string, payload []byte, expected int64, preferences RecommendationPreferences) (PreferencesState, error) {
	return recommendationMutation(ctx, s, owner, key, "update_preferences", payload, func(tx *sql.Tx) (PreferencesState, error) {
		current, err := preferencesState(ctx, tx, owner)
		if err != nil {
			return PreferencesState{}, err
		}
		if current.Revision != expected {
			return PreferencesState{}, ErrConflict
		}
		return savePreferences(ctx, tx, owner, current, preferences)
	})
}

type RecommendationSuggestion struct {
	ID                  string   `json:"id"`
	Revision            int64    `json:"revision"`
	Status              string   `json:"status"`
	Kind                string   `json:"kind"`
	Name                string   `json:"name"`
	Preference          string   `json:"preference"`
	Reason              string   `json:"reason"`
	EvidenceURLs        []string `json:"evidence_urls"`
	PreferencesRevision int64    `json:"preferences_revision"`
	CreatedAt           int64    `json:"created_at"`
	ResolvedAt          int64    `json:"resolved_at"`
}
type SuggestionDecision struct {
	Suggestion  RecommendationSuggestion `json:"suggestion"`
	Preferences PreferencesState         `json:"preferences"`
}

func scanSuggestion(row scanner) (RecommendationSuggestion, error) {
	var out RecommendationSuggestion
	var raw []byte
	err := row.Scan(&out.ID, &out.Revision, &out.Status, &raw, &out.CreatedAt, &out.ResolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var content RecommendationSuggestion
	if err = json.Unmarshal(raw, &content); err != nil {
		return out, err
	}
	content.ID, content.Revision, content.Status, content.CreatedAt, content.ResolvedAt = out.ID, out.Revision, out.Status, out.CreatedAt, out.ResolvedAt
	return content, nil
}

const suggestionColumns = "id,revision,status,suggestion,created_at,resolved_at"

func (s *Store) RecommendationSuggestion(ctx context.Context, owner Owner, id string) (RecommendationSuggestion, error) {
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return RecommendationSuggestion{}, err
	}
	return scanSuggestion(s.read.QueryRowContext(ctx, "SELECT "+suggestionColumns+" FROM recommendation_suggestions WHERE owner_id=? AND id=?", owner.id, id))
}
func (s *Store) ListRecommendationSuggestions(ctx context.Context, owner Owner, status string, limit int) ([]RecommendationSuggestion, error) {
	if limit < 1 || limit > 50 {
		return nil, ErrLimit
	}
	if err := checkOwner(ctx, s.read, owner); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, "SELECT "+suggestionColumns+" FROM recommendation_suggestions WHERE owner_id=? AND (?='all' OR status=?) ORDER BY created_at DESC,id DESC LIMIT ?", owner.id, status, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecommendationSuggestion{}
	for rows.Next() {
		v, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SubmitRecommendationSuggestion(ctx context.Context, owner Owner, key string, payload []byte, in RecommendationSuggestion) (RecommendationSuggestion, error) {
	return recommendationMutation(ctx, s, owner, key, "submit_suggestion", payload, func(tx *sql.Tx) (RecommendationSuggestion, error) {
		prefs, err := preferencesState(ctx, tx, owner)
		if err != nil {
			return in, err
		}
		if prefs.Revision != in.PreferencesRevision {
			return in, ErrConflict
		}
		if !prefs.Preferences.AllowSuggestions {
			return in, ErrLimit
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM recommendation_suggestions WHERE owner_id=? AND status='pending'", owner.id).Scan(&count); err != nil {
			return in, err
		}
		if count >= 50 {
			return in, ErrLimit
		}
		in.ID, in.Revision, in.Status, in.CreatedAt = rand.Text(), 1, "pending", time.Now().UnixMilli()
		raw, err := json.Marshal(in)
		if err != nil {
			return in, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO recommendation_suggestions(owner_id,id,revision,status,suggestion,created_at) VALUES (?,?,?,?,?,?)", owner.id, in.ID, in.Revision, in.Status, raw, in.CreatedAt)
		return in, err
	})
}
func (s *Store) DecideRecommendationSuggestion(ctx context.Context, owner Owner, key string, payload []byte, id string, expected, expectedPreferences int64, accept bool) (SuggestionDecision, error) {
	operation := "dismiss_suggestion"
	if accept {
		operation = "accept_suggestion"
	}
	return recommendationMutation(ctx, s, owner, key, operation, payload, func(tx *sql.Tx) (SuggestionDecision, error) {
		out := SuggestionDecision{}
		suggestion, err := scanSuggestion(tx.QueryRowContext(ctx, "SELECT "+suggestionColumns+" FROM recommendation_suggestions WHERE owner_id=? AND id=?", owner.id, id))
		if err != nil {
			return out, err
		}
		prefs, err := preferencesState(ctx, tx, owner)
		if err != nil {
			return out, err
		}
		if suggestion.Revision != expected || suggestion.Status != "pending" || prefs.Revision != expectedPreferences {
			return out, ErrConflict
		}
		suggestion.Status = "dismissed"
		if accept {
			next := prefs.Preferences
			if suggestion.Kind == "interest" {
				found := false
				for i := range next.Interests {
					if strings.EqualFold(next.Interests[i].Name, suggestion.Name) {
						next.Interests[i].Preference = suggestion.Preference
						found = true
						break
					}
				}
				if !found {
					if len(next.Interests) >= 50 {
						return out, ErrLimit
					}
					next.Interests = append(next.Interests, Interest{Name: suggestion.Name, Preference: suggestion.Preference})
				}
			} else {
				found := false
				for i := range next.Sources {
					if next.Sources[i].Domain == suggestion.Name {
						next.Sources[i].Preference = suggestion.Preference
						found = true
						break
					}
				}
				if !found {
					if len(next.Sources) >= 50 {
						return out, ErrLimit
					}
					next.Sources = append(next.Sources, SourcePreference{Domain: suggestion.Name, Preference: suggestion.Preference})
				}
			}
			prefs, err = savePreferences(ctx, tx, owner, prefs, next)
			if err != nil {
				return out, err
			}
			suggestion.Status = "accepted"
		}
		suggestion.Revision++
		suggestion.ResolvedAt = time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, "UPDATE recommendation_suggestions SET status=?,revision=?,resolved_at=? WHERE owner_id=? AND id=?", suggestion.Status, suggestion.Revision, suggestion.ResolvedAt, owner.id, id)
		return SuggestionDecision{Suggestion: suggestion, Preferences: prefs}, err
	})
}
