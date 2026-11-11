package web_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
	"agentnewsfeed/internal/thumbnail"
	"agentnewsfeed/internal/web"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type settingsShell struct {
	Authenticated bool   `json:"authenticated"`
	CanWrite      bool   `json:"can_write"`
	Section       string `json:"section"`
	SignInURL     string `json:"sign_in_url"`
	Settings      *struct {
		Preferences sqlite.PreferencesState           `json:"preferences"`
		Suggestions []sqlite.RecommendationSuggestion `json:"suggestions"`
		SelectedRun *sqlite.DiscoveryRunDetail        `json:"selected_run"`
	} `json:"settings"`
}

func settingsPost(h http.Handler, path string, cookie *http.Cookie, csrf, origin, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", testfixture.Origin+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out
}
func TestRecommendationBrowserAndAllMCPToolsShareOwnerStateDirectAndGateway(t *testing.T) {
	for _, mode := range []string{"direct", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			f, h, token := todoMCPApp(t, mode)
			server := httptest.NewServer(h)
			defer server.Close()
			alice := client(t, server.URL, token("alice", allScopes))
			bob := client(t, server.URL, token("bob", allScopes))
			readOnly := client(t, server.URL, token("alice", identity.Read))
			writeOnly := client(t, server.URL, token("alice", identity.ArticlesWrite))
			if !strings.Contains(alice.InitializeResult().Instructions, "NEVER applies") || !strings.Contains(alice.InitializeResult().Instructions, "America/New_York") {
				t.Fatal("missing settings/discovery instructions")
			}
			initial := decode[sqlite.PreferencesState](t, call(t, alice, "get_recommendation_preferences", feed.EmptyInput{}))
			if len(initial.Preferences.Interests) != 0 || initial.Preferences.Discovery.Enabled {
				t.Fatal("fake defaults")
			}
			cookie := f.CookieWithScopes(t, "alice", allScopes)
			_, csrf := f.BrowserPrincipal(t, cookie)
			page := readBrowserJSON[settingsShell](t, browserJSONRequest(h, "/settings", cookie, nil, ""))
			if page.Section != "settings" || !page.CanWrite || !reflect.DeepEqual(page.Settings.Preferences, initial) {
				t.Fatal("browser MCP defaults differ")
			}
			update := feed.UpdatePreferencesInput{IdempotencyKey: "browser-pref", ExpectedRevision: 1, Preferences: initial.Preferences}
			update.Preferences.ReadingBrief = "Original technical sources"
			update.Preferences.Discovery.Enabled = true
			raw, _ := json.Marshal(update)
			for _, origin := range []string{"", "https://attacker.example"} {
				if out := settingsPost(h, "/api/v1/settings/preferences", cookie, csrf, origin, string(raw)); out.Code != 403 {
					t.Fatal("unsafe origin", out.Code)
				}
			}
			if out := settingsPost(h, "/api/v1/settings/preferences", cookie, "wrong-csrf", testfixture.Origin, string(raw)); out.Code != 403 {
				t.Fatal("csrf bypass", out.Code)
			}
			if out := settingsPost(h, "/api/v1/settings/preferences", f.Cookie(t, "alice"), csrf, testfixture.Origin, string(raw)); out.Code != 403 {
				t.Fatal("read-only browser wrote", out.Code)
			}
			if out := settingsPost(h, "/api/v1/settings/preferences", cookie, csrf, testfixture.Origin, string(raw)); out.Code != 200 {
				t.Fatal(out.Code, out.Body)
			}
			prefs := decode[sqlite.PreferencesState](t, call(t, alice, "get_recommendation_preferences", feed.EmptyInput{}))
			if prefs.Revision != 2 || prefs.Preferences.ReadingBrief != update.Preferences.ReadingBrief {
				t.Fatal("browser write not shared")
			}
			submit := feed.SubmitSuggestionInput{IdempotencyKey: "proposal", ExpectedPreferencesRevision: 2, Kind: "interest", Name: "Storage engines", Preference: "more", Reason: "Source evidence about practical storage design.", EvidenceURLs: []string{"https://example.com/storage"}}
			proposal := decode[sqlite.RecommendationSuggestion](t, call(t, alice, "submit_recommendation_suggestion", submit))
			requireToolError(t, call(t, bob, "get_recommendation_suggestion", feed.GetSuggestionInput{SuggestionID: proposal.ID}))
			if got := decode[sqlite.RecommendationSuggestion](t, call(t, alice, "get_recommendation_suggestion", feed.GetSuggestionInput{SuggestionID: proposal.ID})); !reflect.DeepEqual(got, proposal) {
				t.Fatal("get differs")
			}
			if got := decode[feed.SuggestionsPage](t, call(t, alice, "list_recommendation_suggestions", feed.ListSuggestionsInput{})); len(got.Suggestions) != 1 {
				t.Fatal(got)
			}
			page = readBrowserJSON[settingsShell](t, browserJSONRequest(h, "/settings", cookie, nil, ""))
			if len(page.Settings.Suggestions) != 1 || len(page.Settings.Preferences.Preferences.Interests) != 0 {
				t.Fatal("proposal self applied")
			}
			decision := feed.DecideSuggestionInput{IdempotencyKey: "browser-accept", SuggestionID: proposal.ID, ExpectedRevision: 1, ExpectedPreferencesRevision: 2}
			body, _ := json.Marshal(decision)
			if out := settingsPost(h, "/api/v1/settings/suggestions/accept", cookie, csrf, testfixture.Origin, string(body)); out.Code != 200 {
				t.Fatal(out.Code, out.Body)
			}
			prefs = decode[sqlite.PreferencesState](t, call(t, alice, "get_recommendation_preferences", feed.EmptyInput{}))
			if prefs.Revision != 3 || len(prefs.Preferences.Interests) != 1 {
				t.Fatal("browser acceptance not shared")
			}
			// Exercise MCP acceptance and dismissal independently after browser parity.
			submit.IdempotencyKey = "second-proposal"
			submit.ExpectedPreferencesRevision = 3
			submit.Kind = "source"
			submit.Name = "example.com"
			submit.Preference = "prefer"
			second := decode[sqlite.RecommendationSuggestion](t, call(t, alice, "submit_recommendation_suggestion", submit))
			accepted := decode[sqlite.SuggestionDecision](t, call(t, alice, "accept_recommendation_suggestion", feed.DecideSuggestionInput{IdempotencyKey: "mcp-accept", SuggestionID: second.ID, ExpectedRevision: 1, ExpectedPreferencesRevision: 3}))
			if accepted.Preferences.Revision != 4 {
				t.Fatal(accepted)
			}
			submit.IdempotencyKey = "third-proposal"
			submit.ExpectedPreferencesRevision = 4
			submit.Name = "other.example.com"
			third := decode[sqlite.RecommendationSuggestion](t, call(t, alice, "submit_recommendation_suggestion", submit))
			dismissed := decode[sqlite.SuggestionDecision](t, call(t, alice, "dismiss_recommendation_suggestion", feed.DecideSuggestionInput{IdempotencyKey: "mcp-dismiss", SuggestionID: third.ID, ExpectedRevision: 1, ExpectedPreferencesRevision: 4}))
			if dismissed.Preferences.Revision != 4 || dismissed.Suggestion.Status != "dismissed" {
				t.Fatal(dismissed)
			}
			update.IdempotencyKey = "mcp-pref"
			update.ExpectedRevision = 4
			update.Preferences = dismissed.Preferences.Preferences
			current := decode[sqlite.PreferencesState](t, call(t, alice, "update_recommendation_preferences", update))
			context := decode[sqlite.DiscoveryContext](t, call(t, alice, "get_discovery_context", feed.EmptyInput{}))
			if context.Preferences.Revision != current.Revision || context.DayTimezone != "America/New_York" {
				t.Fatal(context)
			}
			run := decode[sqlite.DiscoveryRun](t, call(t, alice, "start_discovery_run", feed.StartDiscoveryInput{IdempotencyKey: "run", ExpectedPreferencesRevision: current.Revision}))
			ingest := feed.IngestDiscoveryInput{IdempotencyKey: "match", RunID: run.ID, ExpectedPreferencesRevision: current.Revision, Article: feed.Content{URL: "https://example.com/discovery", Title: "Storage design", Summary: "Source-backed introduction."}, WhyMatch: "Detailed storage engine design matches the accepted interest.", PublishedAt: time.Now().UnixMilli()}
			saved := decode[sqlite.MutationResult](t, call(t, alice, "ingest_discovery_article", ingest))
			if !saved.Created {
				t.Fatal(saved)
			}
			for _, c := range []*sdk.ClientSession{readOnly, writeOnly} {
				requireToolError(t, call(t, c, "ingest_discovery_article", ingest))
				requireToolError(t, call(t, c, "update_recommendation_preferences", update))
			}
			requireToolError(t, call(t, bob, "get_discovery_run", feed.GetDiscoveryRunInput{RunID: run.ID}))
			closed := decode[sqlite.DiscoveryRun](t, call(t, alice, "finish_discovery_run", feed.FinishDiscoveryInput{IdempotencyKey: "finish", RunID: run.ID, Status: "completed", Note: "Saved one sourced article."}))
			if closed.SavedCount != 1 {
				t.Fatal(closed)
			}
			detail := decode[sqlite.DiscoveryRunDetail](t, call(t, alice, "get_discovery_run", feed.GetDiscoveryRunInput{RunID: run.ID}))
			if len(detail.Articles) != 1 || detail.Articles[0].WhyMatch != ingest.WhyMatch {
				t.Fatal(detail)
			}
			history := decode[feed.DiscoveryRunsPage](t, call(t, alice, "list_discovery_runs", feed.ListDiscoveryRunsInput{}))
			if len(history.Runs) != 1 || history.Runs[0].Status != "completed" {
				t.Fatal(history)
			}
			selected := readBrowserJSON[settingsShell](t, browserJSONRequest(h, "/settings?run="+run.ID, cookie, nil, ""))
			if selected.Settings.SelectedRun == nil || selected.Settings.SelectedRun.Articles[0].ArticleID != saved.Article.ID {
				t.Fatal("browser history not shared")
			}
			gate := readBrowserJSON[settingsShell](t, browserJSONRequest(h, "/settings", nil, nil, ""))
			if gate.Settings != nil || gate.Authenticated || gate.SignInURL != "/auth/login?return_to=%2Fsettings" {
				t.Fatal("private settings leaked")
			}
			for _, bad := range []string{
				`{"owner_id":"foreign"}`, `{"idempotency_key":"one","idempotency_key":"one"}`, `[]`, `null`,
				`{"idempotency_key":"x","expected_revision":5,"preferences":{"discovery":{"enabled":true,"enabled":false}}}`,
			} {
				if out := settingsPost(h, "/api/v1/settings/preferences", cookie, csrf, testfixture.Origin, bad); out.Code != 400 {
					t.Fatal("bad JSON", out.Code, bad)
				}
			}
			for _, bad := range []string{
				`{"idempotency_key":"bad","expected_revision":5,"preferences":null}`,
				`{"idempotency_key":"bad","expected_revision":5,"preferences":{"interests":[],"reading_brief":"","avoid":"","sources":[],"allow_new_sources":true,"allow_suggestions":true,"discovery":{"enabled":true,"max_per_run":3,"max_per_day":12,"exploration":"balanced","publication_days":7,"include_evergreen":true,"owner_id":"foreign"}}}`,
				`{"idempotency_key":"bad","expected_revision":5,"preferences":{"interests":[{"name":"Topic","preference":"more","owner_id":"foreign"}],"reading_brief":"","avoid":"","sources":[],"allow_new_sources":true,"allow_suggestions":true,"discovery":{"enabled":true,"max_per_run":3,"max_per_day":12,"exploration":"balanced","publication_days":7,"include_evergreen":true}}}`,
				`{"idempotency_key":"bad","expected_revision":5,"preferences":{"interests":null,"reading_brief":"","avoid":"","sources":[],"allow_new_sources":true,"allow_suggestions":true,"discovery":{"enabled":true,"max_per_run":3,"max_per_day":12,"exploration":"balanced","publication_days":7,"include_evergreen":true}}}`,
				`{"idempotency_key":"bad","expected_revision":5,"preferences":{"interests":[],"reading_brief":null,"avoid":"","sources":[],"allow_new_sources":true,"allow_suggestions":true,"discovery":{"enabled":true,"max_per_run":3,"max_per_day":12,"exploration":"balanced","publication_days":7,"include_evergreen":true}}}`,
				`{"idempotency_key":"bad","expected_revision":5,"preferences":{"interests":[],"reading_brief":"","avoid":"","sources":[],"allow_new_sources":true,"allow_suggestions":true,"discovery":{"enabled":true,"enabled":false,"max_per_run":3,"max_per_day":12,"exploration":"balanced","publication_days":7,"include_evergreen":true}}}`,
			} {
				requireToolError(t, rawTodoToolCall(t, server, token("alice", allScopes), "update_recommendation_preferences", bad))
				if out := settingsPost(h, "/api/v1/settings/preferences", cookie, csrf, testfixture.Origin, bad); out.Code != 400 {
					t.Fatal("browser schema divergence", out.Code, bad)
				}
			}
			aliasInput, err := json.Marshal(feed.UpdatePreferencesInput{IdempotencyKey: "case-alias", ExpectedRevision: current.Revision, Preferences: current.Preferences})
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{
				strings.Replace(string(aliasInput), `"allow_new_sources":true`, `"allow_new_sources":true,"ALLOW_NEW_SOURCES":false`, 1),
				strings.Replace(string(aliasInput), `"allow_new_sources":true`, `"allow_new_sources":true,"ALLOW_NEW_SOURCES":null`, 1),
				strings.TrimSuffix(string(aliasInput), "}") + `,"PREFERENCES":null}`,
			} {
				requireToolError(t, rawTodoToolCall(t, server, token("alice", allScopes), "update_recommendation_preferences", bad))
				if out := settingsPost(h, "/api/v1/settings/preferences", cookie, csrf, testfixture.Origin, bad); out.Code != 400 {
					t.Fatal("case alias accepted", out.Code, bad)
				}
			}
			if after := decode[sqlite.PreferencesState](t, call(t, alice, "get_recommendation_preferences", feed.EmptyInput{})); !reflect.DeepEqual(after, current) {
				t.Fatal("rejected input changed preferences")
			}

			for _, name := range []string{"get_recommendation_preferences", "update_recommendation_preferences", "submit_recommendation_suggestion", "list_recommendation_suggestions", "get_recommendation_suggestion", "accept_recommendation_suggestion", "dismiss_recommendation_suggestion", "get_discovery_context", "start_discovery_run", "ingest_discovery_article", "finish_discovery_run", "list_discovery_runs", "get_discovery_run"} {
				requireToolError(t, rawTodoToolCall(t, server, token("alice", allScopes), name, `{"owner_id":"foreign"}`))
				requireToolError(t, rawTodoToolCall(t, server, token("alice", allScopes), name, `{"idempotency_key":"one","idempotency_key":"one"}`))
			}
		})
	}
}

func TestDiscoveryHistoryKeepsOriginalSourceAfterPatchAndRestart(t *testing.T) {
	f, _, h := app(t)
	server := httptest.NewServer(h)
	defer func() { server.Close() }()
	alice := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	preferences := sqlite.DefaultPreferences().Preferences
	preferences.Discovery.Enabled = true
	decode[sqlite.PreferencesState](t, call(t, alice, "update_recommendation_preferences", feed.UpdatePreferencesInput{
		IdempotencyKey: "preferences", ExpectedRevision: 1, Preferences: preferences,
	}))
	run := decode[sqlite.DiscoveryRun](t, call(t, alice, "start_discovery_run", feed.StartDiscoveryInput{IdempotencyKey: "run", ExpectedPreferencesRevision: 2}))
	input := feed.IngestDiscoveryInput{
		IdempotencyKey: "original-source", RunID: run.ID, ExpectedPreferencesRevision: 2,
		Article:  feed.Content{URL: "https://example.com/original?utm_source=discovery#top", Title: "Original discovery title", Summary: "Source-backed introduction."},
		WhyMatch: "Detailed source evidence matches the reading brief.", PublishedAt: time.Now().UnixMilli(),
	}
	saved := decode[sqlite.MutationResult](t, call(t, alice, "ingest_discovery_article", input))
	decode[sqlite.DiscoveryRun](t, call(t, alice, "finish_discovery_run", feed.FinishDiscoveryInput{IdempotencyKey: "finish", RunID: run.ID, Status: "completed", Note: "Saved the original source."}))
	originalHistory := decode[sqlite.DiscoveryRunDetail](t, call(t, alice, "get_discovery_run", feed.GetDiscoveryRunInput{RunID: run.ID}))
	if len(originalHistory.Articles) != 1 || originalHistory.Articles[0].URL != "https://example.com/original" || originalHistory.Articles[0].Title != input.Article.Title {
		t.Fatal("ingestion did not snapshot the canonical original source", originalHistory)
	}
	patched := decode[sqlite.MutationResult](t, call(t, alice, "patch_article", feed.PatchInput{
		IdempotencyKey: "edit-source", ArticleID: saved.Article.ID, ExpectedRevision: saved.Article.Revision,
		Patch: feed.Patch{URL: new("https://example.com/edited"), Title: new("Edited article title")},
	}))
	verify := func() {
		t.Helper()
		history := decode[sqlite.DiscoveryRunDetail](t, call(t, alice, "get_discovery_run", feed.GetDiscoveryRunInput{RunID: run.ID}))
		if !reflect.DeepEqual(history, originalHistory) {
			t.Fatal("MCP history followed the mutable article", history)
		}
		selected := readBrowserJSON[settingsShell](t, browserJSONRequest(h, "/settings?run="+run.ID, cookie, nil, ""))
		if selected.Settings.SelectedRun == nil || !reflect.DeepEqual(*selected.Settings.SelectedRun, originalHistory) {
			t.Fatal("browser history lost the original source snapshot", selected.Settings.SelectedRun)
		}
		current := decode[sqlite.Article](t, call(t, alice, "get_article", feed.GetInput{ArticleID: saved.Article.ID}))
		if current != patched.Article || current.URL == originalHistory.Articles[0].URL || current.Title == originalHistory.Articles[0].Title {
			t.Fatal("current article was not independently editable", current)
		}
		replay := decode[sqlite.MutationResult](t, call(t, alice, "ingest_discovery_article", input))
		if replay != saved {
			t.Fatal("historical ingestion retry changed", replay)
		}
	}
	verify()
	server.Close()
	f.Reopen(t)
	policy, err := thumbnail.NewPolicy("")
	if err != nil {
		t.Fatal(err)
	}
	service := feed.NewWithThumbnailFetcher(f.Store, policy, unavailableThumbnailFetcher{})
	h, err = web.New(slog.New(slog.NewTextHandler(io.Discard, nil)), f.Manager, service, thumbnail.NewProxy(policy))
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(h)
	alice = client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	verify()
}
