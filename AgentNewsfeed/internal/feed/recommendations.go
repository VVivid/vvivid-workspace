package feed

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/urls"
)

type EmptyInput struct{}
type UpdatePreferencesInput struct {
	IdempotencyKey   string                           `json:"idempotency_key"`
	ExpectedRevision int64                            `json:"expected_revision"`
	Preferences      sqlite.RecommendationPreferences `json:"preferences"`
}
type SubmitSuggestionInput struct {
	IdempotencyKey              string   `json:"idempotency_key"`
	ExpectedPreferencesRevision int64    `json:"expected_preferences_revision"`
	Kind                        string   `json:"kind"`
	Name                        string   `json:"name"`
	Preference                  string   `json:"preference"`
	Reason                      string   `json:"reason"`
	EvidenceURLs                []string `json:"evidence_urls"`
}
type ListSuggestionsInput struct {
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitzero"`
}
type SuggestionsPage struct {
	Suggestions []sqlite.RecommendationSuggestion `json:"suggestions"`
}
type GetSuggestionInput struct {
	SuggestionID string `json:"suggestion_id"`
}
type DecideSuggestionInput struct {
	IdempotencyKey              string `json:"idempotency_key"`
	SuggestionID                string `json:"suggestion_id"`
	ExpectedRevision            int64  `json:"expected_revision"`
	ExpectedPreferencesRevision int64  `json:"expected_preferences_revision"`
}
type StartDiscoveryInput struct {
	IdempotencyKey              string `json:"idempotency_key"`
	ExpectedPreferencesRevision int64  `json:"expected_preferences_revision"`
}
type IngestDiscoveryInput struct {
	IdempotencyKey              string  `json:"idempotency_key"`
	RunID                       string  `json:"run_id"`
	ExpectedPreferencesRevision int64   `json:"expected_preferences_revision"`
	Article                     Content `json:"article"`
	WhyMatch                    string  `json:"why_match"`
	PublishedAt                 int64   `json:"published_at"`
	Evergreen                   bool    `json:"evergreen"`
}
type FinishDiscoveryInput struct {
	IdempotencyKey string `json:"idempotency_key"`
	RunID          string `json:"run_id"`
	Status         string `json:"status"`
	Note           string `json:"note"`
}
type ListDiscoveryRunsInput struct {
	Limit  int   `json:"limit,omitzero"`
	Before int64 `json:"before,omitzero"`
}
type DiscoveryRunsPage struct {
	Runs       []sqlite.DiscoveryRun `json:"runs"`
	NextBefore int64                 `json:"next_before,omitzero"`
}
type GetDiscoveryRunInput struct {
	RunID string `json:"run_id"`
}

func normalizePreferences(in sqlite.RecommendationPreferences) (sqlite.RecommendationPreferences, error) {
	if len(in.Interests) > 50 || len(in.Sources) > 50 || len(in.ReadingBrief) > 4000 || len(in.Avoid) > 4000 || !validMarkdown(in.ReadingBrief) || !validMarkdown(in.Avoid) {
		return in, ErrInvalid
	}
	// Return arrays even for a new owner's genuinely empty preferences.
	in.Interests = slices.Clone(in.Interests)
	if in.Interests == nil {
		in.Interests = []sqlite.Interest{}
	}
	in.Sources = slices.Clone(in.Sources)
	if in.Sources == nil {
		in.Sources = []sqlite.SourcePreference{}
	}
	seen := map[string]bool{}
	for i, v := range in.Interests {
		v.Name = strings.Join(strings.Fields(v.Name), " ")
		if !boundedText(v.Name, 320) || utf8.RuneCountInString(v.Name) > 80 || !slices.Contains([]string{"more", "occasionally", "avoid"}, v.Preference) || seen[strings.ToLower(v.Name)] {
			return in, ErrInvalid
		}
		seen[strings.ToLower(v.Name)] = true
		in.Interests[i] = v
	}
	clear(seen)
	for i, v := range in.Sources {
		v.Domain = strings.ToLower(strings.TrimSpace(v.Domain))
		if !urls.PublicHostname(v.Domain) || !slices.Contains([]string{"prefer", "allow", "block"}, v.Preference) || seen[v.Domain] {
			return in, ErrInvalid
		}
		seen[v.Domain] = true
		in.Sources[i] = v
	}
	d := in.Discovery
	if d.MaxPerRun < 1 || d.MaxPerRun > 10 || d.MaxPerDay < 1 || d.MaxPerDay > 50 || !slices.Contains([]string{"familiar", "balanced", "adventurous"}, d.Exploration) || !slices.Contains([]int{0, 1, 7, 30}, d.PublicationDays) {
		return in, ErrInvalid
	}
	return in, nil
}
func (s *Service) GetRecommendationPreferences(ctx context.Context, p identity.Principal, _ EmptyInput) (sqlite.PreferencesState, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.PreferencesState{}, err
	}
	return s.store.RecommendationPreferences(ctx, p.Owner())
}
func (s *Service) UpdateRecommendationPreferences(ctx context.Context, p identity.Principal, in UpdatePreferencesInput) (sqlite.PreferencesState, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.PreferencesState{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) || in.ExpectedRevision < 1 {
		return sqlite.PreferencesState{}, ErrInvalid
	}
	prefs, err := normalizePreferences(in.Preferences)
	if err != nil {
		return sqlite.PreferencesState{}, err
	}
	in.Preferences = prefs
	payload, _ := json.Marshal(in)
	return s.store.UpdateRecommendationPreferences(ctx, p.Owner(), in.IdempotencyKey, payload, in.ExpectedRevision, prefs)
}
func (s *Service) SubmitRecommendationSuggestion(ctx context.Context, p identity.Principal, in SubmitSuggestionInput) (sqlite.RecommendationSuggestion, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.RecommendationSuggestion{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) || in.ExpectedPreferencesRevision < 1 || !boundedText(in.Reason, 1200) || len(in.EvidenceURLs) < 1 || len(in.EvidenceURLs) > 5 {
		return sqlite.RecommendationSuggestion{}, ErrInvalid
	}
	prefs := sqlite.DefaultPreferences().Preferences
	switch in.Kind {
	case "interest":
		prefs.Interests = []sqlite.Interest{{Name: in.Name, Preference: in.Preference}}
	case "source":
		prefs.Sources = []sqlite.SourcePreference{{Domain: in.Name, Preference: in.Preference}}
	default:
		return sqlite.RecommendationSuggestion{}, ErrInvalid
	}
	prefs, err := normalizePreferences(prefs)
	if err != nil {
		return sqlite.RecommendationSuggestion{}, err
	}
	if in.Kind == "interest" {
		in.Name = prefs.Interests[0].Name
	} else {
		in.Name = prefs.Sources[0].Domain
	}
	seen := map[string]bool{}
	in.EvidenceURLs = slices.Clone(in.EvidenceURLs)
	for i, raw := range in.EvidenceURLs {
		canonical, err := urls.Canonical(raw)
		if err != nil || seen[canonical] {
			return sqlite.RecommendationSuggestion{}, ErrInvalid
		}
		in.EvidenceURLs[i] = canonical
		seen[canonical] = true
	}
	payload, _ := json.Marshal(in)
	suggestion := sqlite.RecommendationSuggestion{Kind: in.Kind, Name: in.Name, Preference: in.Preference, Reason: in.Reason, EvidenceURLs: in.EvidenceURLs, PreferencesRevision: in.ExpectedPreferencesRevision}
	return s.store.SubmitRecommendationSuggestion(ctx, p.Owner(), in.IdempotencyKey, payload, suggestion)
}
func (s *Service) ListRecommendationSuggestions(ctx context.Context, p identity.Principal, in ListSuggestionsInput) (SuggestionsPage, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return SuggestionsPage{}, err
	}
	if in.Status == "" {
		in.Status = "pending"
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !slices.Contains([]string{"pending", "accepted", "dismissed", "all"}, in.Status) || in.Limit < 1 || in.Limit > 50 {
		return SuggestionsPage{}, ErrInvalid
	}
	rows, err := s.store.ListRecommendationSuggestions(ctx, p.Owner(), in.Status, in.Limit)
	return SuggestionsPage{Suggestions: rows}, err
}
func (s *Service) GetRecommendationSuggestion(ctx context.Context, p identity.Principal, in GetSuggestionInput) (sqlite.RecommendationSuggestion, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.RecommendationSuggestion{}, err
	}
	if !boundedText(in.SuggestionID, 64) {
		return sqlite.RecommendationSuggestion{}, ErrInvalid
	}
	return s.store.RecommendationSuggestion(ctx, p.Owner(), in.SuggestionID)
}
func (s *Service) decideRecommendationSuggestion(ctx context.Context, p identity.Principal, in DecideSuggestionInput, accept bool) (sqlite.SuggestionDecision, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.SuggestionDecision{}, err
	}
	if !validMutation(in.IdempotencyKey, in.SuggestionID, in.ExpectedRevision) || in.ExpectedPreferencesRevision < 1 {
		return sqlite.SuggestionDecision{}, ErrInvalid
	}
	payload, _ := json.Marshal(in)
	return s.store.DecideRecommendationSuggestion(ctx, p.Owner(), in.IdempotencyKey, payload, in.SuggestionID, in.ExpectedRevision, in.ExpectedPreferencesRevision, accept)
}
func (s *Service) AcceptRecommendationSuggestion(ctx context.Context, p identity.Principal, in DecideSuggestionInput) (sqlite.SuggestionDecision, error) {
	return s.decideRecommendationSuggestion(ctx, p, in, true)
}
func (s *Service) DismissRecommendationSuggestion(ctx context.Context, p identity.Principal, in DecideSuggestionInput) (sqlite.SuggestionDecision, error) {
	return s.decideRecommendationSuggestion(ctx, p, in, false)
}
func (s *Service) GetDiscoveryContext(ctx context.Context, p identity.Principal, _ EmptyInput) (sqlite.DiscoveryContext, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.DiscoveryContext{}, err
	}
	return s.store.DiscoveryContext(ctx, p.Owner())
}
func (s *Service) StartDiscoveryRun(ctx context.Context, p identity.Principal, in StartDiscoveryInput) (sqlite.DiscoveryRun, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.DiscoveryRun{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) || in.ExpectedPreferencesRevision < 1 {
		return sqlite.DiscoveryRun{}, ErrInvalid
	}
	payload, _ := json.Marshal(in)
	return s.store.StartDiscoveryRun(ctx, p.Owner(), in.IdempotencyKey, payload, in.ExpectedPreferencesRevision)
}
func (s *Service) IngestDiscoveryArticle(ctx context.Context, p identity.Principal, in IngestDiscoveryInput) (sqlite.MutationResult, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.MutationResult{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) || !boundedText(in.RunID, 64) || in.ExpectedPreferencesRevision < 1 || !boundedText(in.WhyMatch, 1200) || in.PublishedAt < 0 {
		return sqlite.MutationResult{}, ErrInvalid
	}
	a, err := normalizeContent(in.Article, s.thumbnails)
	if err != nil {
		return sqlite.MutationResult{}, err
	}
	// Canonical content also makes exact retries independent of tracking parameters.
	in.Article.URL = a.URL
	payload, _ := json.Marshal(in)
	if out, found, err := s.store.ReplayMutation(ctx, p.Owner(), in.IdempotencyKey, "ingest_discovery_article", payload); found || err != nil {
		return out, err
	}
	item := sqlite.DiscoveryItem{WhyMatch: in.WhyMatch, PublishedAt: in.PublishedAt, Evergreen: in.Evergreen}
	if err := s.store.CheckDiscoveryArticle(ctx, p.Owner(), in.RunID, in.ExpectedPreferencesRevision, a, item); err != nil {
		return sqlite.MutationResult{}, err
	}
	change := s.prepareThumbnail(ctx, a.ThumbnailURL)
	return s.store.IngestDiscoveryArticle(ctx, p.Owner(), in.IdempotencyKey, payload, in.RunID, in.ExpectedPreferencesRevision, a, item, change)
}
func (s *Service) FinishDiscoveryRun(ctx context.Context, p identity.Principal, in FinishDiscoveryInput) (sqlite.DiscoveryRun, error) {
	if err := s.authorize(ctx, p, identity.Read, identity.ArticlesWrite); err != nil {
		return sqlite.DiscoveryRun{}, err
	}
	if !boundedText(in.IdempotencyKey, 128) || !boundedText(in.RunID, 64) || !slices.Contains([]string{"completed", "failed"}, in.Status) || len(in.Note) > 1000 || !validMarkdown(in.Note) {
		return sqlite.DiscoveryRun{}, ErrInvalid
	}
	payload, _ := json.Marshal(in)
	return s.store.FinishDiscoveryRun(ctx, p.Owner(), in.IdempotencyKey, payload, in.RunID, in.Status, in.Note)
}
func (s *Service) ListDiscoveryRuns(ctx context.Context, p identity.Principal, in ListDiscoveryRunsInput) (DiscoveryRunsPage, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return DiscoveryRunsPage{}, err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 50 || in.Before < 0 {
		return DiscoveryRunsPage{}, ErrInvalid
	}
	rows, err := s.store.ListDiscoveryRuns(ctx, p.Owner(), in.Limit, in.Before)
	out := DiscoveryRunsPage{Runs: rows}
	if len(rows) == in.Limit {
		out.NextBefore = rows[len(rows)-1].StartedAt
	}
	return out, err
}
func (s *Service) GetDiscoveryRun(ctx context.Context, p identity.Principal, in GetDiscoveryRunInput) (sqlite.DiscoveryRunDetail, error) {
	if err := s.authorize(ctx, p, identity.Read); err != nil {
		return sqlite.DiscoveryRunDetail{}, err
	}
	if !boundedText(in.RunID, 64) {
		return sqlite.DiscoveryRunDetail{}, ErrInvalid
	}
	return s.store.DiscoveryRun(ctx, p.Owner(), in.RunID)
}
func RecommendationSafeError(err error) string {
	switch {
	case errors.Is(err, ErrInvalid):
		return "Invalid settings request. Check bounded preferences, allowed choices, revision and retry fields, or source evidence URLs."
	case errors.Is(err, sqlite.ErrNotFound):
		return "Suggestion or discovery run not found."
	case errors.Is(err, sqlite.ErrConflict):
		return "Preferences, suggestion or run changed, or this URL already exists. Read current state before a new intent."
	case errors.Is(err, sqlite.ErrLimit):
		return "Discovery or proposals are disabled, a source/publication rule excludes this article, or an hourly, daily, run or pending-proposal limit was reached."
	default:
		return SafeError(err)
	}
}
