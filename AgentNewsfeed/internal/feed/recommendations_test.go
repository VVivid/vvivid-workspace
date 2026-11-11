package feed_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

func TestRecommendationDefaultsOwnershipProposalsAndDurableRetry(t *testing.T) {
	f, s, alice, bob := setup(t)
	initial, err := s.GetRecommendationPreferences(t.Context(), alice, feed.EmptyInput{})
	if err != nil || initial.Revision != 1 || len(initial.Preferences.Interests) != 0 || len(initial.Preferences.Sources) != 0 || initial.Preferences.Discovery.Enabled || initial.Preferences.ReadingBrief != "" {
		t.Fatalf("first use %#v %v", initial, err)
	}
	submit := feed.SubmitSuggestionInput{IdempotencyKey: "propose", ExpectedPreferencesRevision: 1, Kind: "interest", Name: "Distributed systems", Preference: "more", Reason: "Two source articles explain distributed storage.", EvidenceURLs: []string{"https://example.com/research?utm_source=agent"}}
	proposal, err := s.SubmitRecommendationSuggestion(t.Context(), alice, submit)
	if err != nil || proposal.Status != "pending" {
		t.Fatal(proposal, err)
	}
	unchanged, _ := s.GetRecommendationPreferences(t.Context(), alice, feed.EmptyInput{})
	if !reflect.DeepEqual(initial, unchanged) {
		t.Fatal("proposal self applied")
	}
	if _, err := s.GetRecommendationSuggestion(t.Context(), bob, feed.GetSuggestionInput{SuggestionID: proposal.ID}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("foreign proposal", err)
	}
	decision := feed.DecideSuggestionInput{IdempotencyKey: "accept", SuggestionID: proposal.ID, ExpectedRevision: 1, ExpectedPreferencesRevision: 1}
	accepted, err := s.AcceptRecommendationSuggestion(t.Context(), alice, decision)
	if err != nil || accepted.Preferences.Revision != 2 || len(accepted.Preferences.Preferences.Interests) != 1 || accepted.Suggestion.Revision != 2 {
		t.Fatal(accepted, err)
	}
	update := feed.UpdatePreferencesInput{IdempotencyKey: "brief", ExpectedRevision: 2, Preferences: accepted.Preferences.Preferences}
	update.Preferences.ReadingBrief = "Detailed engineering sources."
	current, err := s.UpdateRecommendationPreferences(t.Context(), alice, update)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := f.Principal(t, "alice", identity.Read)
	writeOnly := f.Principal(t, "alice", identity.ArticlesWrite)
	for _, p := range []identity.Principal{readOnly, writeOnly} {
		if _, err := s.AcceptRecommendationSuggestion(t.Context(), p, decision); !errors.Is(err, identity.ErrForbidden) {
			t.Fatal("replay bypassed both scopes", err)
		}
	}
	f.Reopen(t)
	policy, _ := thumbnail.NewPolicy("images.example.com")
	s = feed.New(f.Store, policy)
	replay, err := s.AcceptRecommendationSuggestion(t.Context(), alice, decision)
	if err != nil || !reflect.DeepEqual(accepted, replay) {
		t.Fatal("historical retry", replay, err)
	}
	got, _ := s.GetRecommendationPreferences(t.Context(), alice, feed.EmptyInput{})
	if !reflect.DeepEqual(got, current) {
		t.Fatal("retry reapplied old settings")
	}
	other, _ := s.GetRecommendationPreferences(t.Context(), bob, feed.EmptyInput{})
	if !reflect.DeepEqual(other, initial) {
		t.Fatal("cross owner preferences")
	}
	decision.IdempotencyKey = "accept-again"
	if _, err := s.AcceptRecommendationSuggestion(t.Context(), alice, decision); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("stale decision", err)
	}
	if err := f.Store.DisableAccount(t.Context(), alice.Identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptRecommendationSuggestion(t.Context(), alice, feed.DecideSuggestionInput{IdempotencyKey: "accept", SuggestionID: proposal.ID, ExpectedRevision: 1, ExpectedPreferencesRevision: 1}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("disabled replay", err)
	}
}

func TestRecommendationDecisionChecksBothRevisionsAtomically(t *testing.T) {
	_, s, alice, _ := setup(t)
	proposal, err := s.SubmitRecommendationSuggestion(t.Context(), alice, feed.SubmitSuggestionInput{IdempotencyKey: "s", ExpectedPreferencesRevision: 1, Kind: "source", Name: "EXAMPLE.COM", Preference: "prefer", Reason: "Original reporting.", EvidenceURLs: []string{"https://example.com/source"}})
	if err != nil {
		t.Fatal(err)
	}
	prefs := sqlite.DefaultPreferences().Preferences
	prefs.ReadingBrief = "Changed elsewhere"
	if _, err = s.UpdateRecommendationPreferences(t.Context(), alice, feed.UpdatePreferencesInput{IdempotencyKey: "p", ExpectedRevision: 1, Preferences: prefs}); err != nil {
		t.Fatal(err)
	}
	in := feed.DecideSuggestionInput{IdempotencyKey: "decision", SuggestionID: proposal.ID, ExpectedRevision: 1, ExpectedPreferencesRevision: 1}
	if _, err = s.AcceptRecommendationSuggestion(t.Context(), alice, in); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal(err)
	}
	still, _ := s.GetRecommendationSuggestion(t.Context(), alice, feed.GetSuggestionInput{SuggestionID: proposal.ID})
	if still.Status != "pending" || still.Revision != 1 {
		t.Fatal("partial resolve", still)
	}
	in.ExpectedPreferencesRevision = 2
	dismissed, err := s.DismissRecommendationSuggestion(t.Context(), alice, in)
	if err != nil || dismissed.Preferences.Revision != 2 || len(dismissed.Preferences.Preferences.Sources) != 0 || dismissed.Suggestion.Status != "dismissed" {
		t.Fatal(dismissed, err)
	}
}

func TestDiscoveryConcurrencyBudgetsDedupeSourceAgeAndStalePreferences(t *testing.T) {
	f, s, alice, bob := setup(t)
	prefs := sqlite.DefaultPreferences().Preferences
	prefs.Discovery.Enabled = true
	prefs.Discovery.MaxPerRun = 2
	prefs.Discovery.MaxPerDay = 2
	prefs.Discovery.IncludeEvergreen = false
	prefs.AllowNewSources = false
	prefs.Sources = []sqlite.SourcePreference{{Domain: "example.com", Preference: "allow"}, {Domain: "blocked.example.com", Preference: "block"}}
	state, err := s.UpdateRecommendationPreferences(t.Context(), alice, feed.UpdatePreferencesInput{IdempotencyKey: "prefs", ExpectedRevision: 1, Preferences: prefs})
	if err != nil {
		t.Fatal(err)
	}
	start := feed.StartDiscoveryInput{IdempotencyKey: "run", ExpectedPreferencesRevision: state.Revision}
	run, err := s.StartDiscoveryRun(t.Context(), alice, start)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := range 8 {
		wg.Go(func() {
			_, err := s.StartDiscoveryRun(t.Context(), alice, feed.StartDiscoveryInput{IdempotencyKey: fmt.Sprintf("another-%d", i), ExpectedPreferencesRevision: state.Revision})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if !errors.Is(err, sqlite.ErrLimit) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes != 0 {
		t.Fatal("concurrent leases")
	}
	input := func(key, path string) feed.IngestDiscoveryInput {
		return feed.IngestDiscoveryInput{IdempotencyKey: key, RunID: run.ID, ExpectedPreferencesRevision: state.Revision, Article: feed.Content{URL: "https://example.com/" + path, Title: "Original research", Summary: "An introduction backed by the original source."}, WhyMatch: "Explains the chosen technical topic with evidence.", PublishedAt: time.Now().UnixMilli()}
	}
	bad := input("blocked", "story")
	bad.Article.URL = "https://blocked.example.com/story"
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, bad); !errors.Is(err, sqlite.ErrLimit) {
		t.Fatal("blocked", err)
	}
	bad.Article.URL = "https://www.example.com/story"
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, bad); !errors.Is(err, sqlite.ErrLimit) {
		t.Fatal("implicit subdomain allowed", err)
	}
	bad = input("unknown", "undated")
	bad.PublishedAt = 0
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, bad); !errors.Is(err, sqlite.ErrLimit) {
		t.Fatal("unknown date accepted", err)
	}
	bad.PublishedAt = time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, bad); !errors.Is(err, sqlite.ErrLimit) {
		t.Fatal("old date", err)
	}
	if _, err := s.IngestDiscoveryArticle(t.Context(), bob, input("foreign", "foreign")); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("foreign run", err)
	}
	firstInput := input("first", "one?utm_source=agent")
	first, err := s.IngestDiscoveryArticle(t.Context(), alice, firstInput)
	if err != nil || !first.Created {
		t.Fatal(first, err)
	}
	duplicate := input("duplicate", "one#different")
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, duplicate); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("canonical duplicate", err)
	}
	for i := range 10 {
		wg.Go(func() {
			_, err := s.IngestDiscoveryArticle(t.Context(), alice, input(fmt.Sprintf("article-%d", i), fmt.Sprintf("article-%d", i)))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if !errors.Is(err, sqlite.ErrLimit) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("budget oversubscribed: %d", successes)
	}
	detail, err := s.GetDiscoveryRun(t.Context(), alice, feed.GetDiscoveryRunInput{RunID: run.ID})
	if err != nil || detail.Run.SavedCount != 2 || len(detail.Articles) != 2 || detail.Articles[0].WhyMatch == "" {
		t.Fatal(detail, err)
	}
	context, err := s.GetDiscoveryContext(t.Context(), alice, feed.EmptyInput{})
	if err != nil || context.SavedToday != 2 || context.RemainingToday != 0 || context.DayTimezone != "America/New_York" {
		t.Fatal(context, err)
	}
	prefs.Discovery.Enabled = false
	if _, err = s.UpdateRecommendationPreferences(t.Context(), alice, feed.UpdatePreferencesInput{IdempotencyKey: "stop", ExpectedRevision: state.Revision, Preferences: prefs}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.IngestDiscoveryArticle(t.Context(), alice, input("stale", "stale")); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("stale preferences", err)
	}
	f.Reopen(t)
	policy, _ := thumbnail.NewPolicy("images.example.com")
	s = feed.New(f.Store, policy)
	replay, err := s.IngestDiscoveryArticle(t.Context(), alice, firstInput)
	if err != nil || replay != first {
		t.Fatal("ingestion retry after settings/restart", replay, err)
	}
	replayRun, err := s.StartDiscoveryRun(t.Context(), alice, start)
	if err != nil || replayRun != run {
		t.Fatal("lease retry after restart", replayRun, err)
	}
	finish := feed.FinishDiscoveryInput{IdempotencyKey: "finish", RunID: run.ID, Status: "completed", Note: "Saved two relevant original articles."}
	closed, err := s.FinishDiscoveryRun(t.Context(), alice, finish)
	if err != nil || closed.Status != "completed" {
		t.Fatal(closed, err)
	}
	if _, err = s.FinishDiscoveryRun(t.Context(), alice, feed.FinishDiscoveryInput{IdempotencyKey: "closeagain", RunID: run.ID, Status: "failed"}); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal(err)
	}
}

func TestDiscoveryBlockingWinsWhenNewSourcesAllowedAndManualAddStaysSupported(t *testing.T) {
	_, s, alice, _ := setup(t)
	prefs := sqlite.DefaultPreferences().Preferences
	prefs.Discovery.Enabled = true
	prefs.Discovery.IncludeEvergreen = true
	prefs.Sources = []sqlite.SourcePreference{{Domain: "example.com", Preference: "block"}}
	if _, err := s.UpdateRecommendationPreferences(t.Context(), alice, feed.UpdatePreferencesInput{IdempotencyKey: "p", ExpectedRevision: 1, Preferences: prefs}); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartDiscoveryRun(t.Context(), alice, feed.StartDiscoveryInput{IdempotencyKey: "r", ExpectedPreferencesRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	in := feed.IngestDiscoveryInput{IdempotencyKey: "i", RunID: run.ID, ExpectedPreferencesRevision: 2, Article: content(), WhyMatch: "Useful enduring explanation.", Evergreen: true}
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, in); !errors.Is(err, sqlite.ErrLimit) {
		t.Fatal("block lost to new sources", err)
	}
	manual, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "manual", Article: content()})
	if err != nil || !manual.Created {
		t.Fatal("manual contract changed", err)
	}
	in.Article.URL = "https://other.example.com/evergreen"
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, in); err != nil {
		t.Fatal("truthful evergreen exception", err)
	}
}

type changingDiscoveryImage struct{ change func() }

func (image changingDiscoveryImage) FetchThumbnail(_ context.Context, _ string) ([]byte, error) {
	image.change()
	return nil, errors.New("controlled image failure")
}
func TestDiscoveryRechecksPreferencesAfterImagePreparation(t *testing.T) {
	f, s, alice, _ := setup(t)
	prefs := sqlite.DefaultPreferences().Preferences
	prefs.Discovery.Enabled = true
	if _, err := s.UpdateRecommendationPreferences(t.Context(), alice, feed.UpdatePreferencesInput{IdempotencyKey: "p", ExpectedRevision: 1, Preferences: prefs}); err != nil {
		t.Fatal(err)
	}
	run, err := s.StartDiscoveryRun(t.Context(), alice, feed.StartDiscoveryInput{IdempotencyKey: "r", ExpectedPreferencesRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := thumbnail.NewPolicy("images.example.com")
	fetcher := changingDiscoveryImage{change: func() {
		prefs.Discovery.Enabled = false
		if _, err := s.UpdateRecommendationPreferences(t.Context(), alice, feed.UpdatePreferencesInput{IdempotencyKey: "stop-during-image", ExpectedRevision: 2, Preferences: prefs}); err != nil {
			t.Fatal(err)
		}
	}}
	s = feed.NewWithThumbnailFetcher(f.Store, policy, fetcher)
	in := feed.IngestDiscoveryInput{IdempotencyKey: "i", RunID: run.ID, ExpectedPreferencesRevision: 2, Article: content(), WhyMatch: "Source supports the preferred topic.", PublishedAt: time.Now().UnixMilli()}
	in.Article.ThumbnailURL = "https://images.example.com/source.jpg"
	if _, err := s.IngestDiscoveryArticle(t.Context(), alice, in); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("stale request committed", err)
	}
	page, err := s.ListArticles(t.Context(), alice, feed.ListInput{})
	if err != nil || len(page.Articles) != 0 {
		t.Fatal("partial article", page, err)
	}
	detail, err := s.GetDiscoveryRun(t.Context(), alice, feed.GetDiscoveryRunInput{RunID: run.ID})
	if err != nil || detail.Run.SavedCount != 0 || len(detail.Articles) != 0 {
		t.Fatal("partial run count", detail, err)
	}
}
