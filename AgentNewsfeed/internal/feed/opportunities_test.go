package feed_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

func opportunityContent() feed.OpportunityContent {
	return feed.OpportunityContent{
		Kind: "internship", URL: "https://jobs.example.com/opening?utm_source=agent#apply", Title: "Software Engineering Intern", Company: "Example Company",
		Location: "Philadelphia, PA", Workplace: "hybrid", EmploymentType: "full_time", EmploymentPeriod: "Summer 2027",
		Compensation: "$30–40/hour, as listed", Deadline: "2027-02-28", Tags: []string{"Go", "Summer 2027"},
		SourceDescription: "# Source description\n\nBuild internal tools.\n", AgentNote: "External agent: relevant to the user's Go experience.",
	}
}

func addOpportunity(t *testing.T, s *feed.Service, p identity.Principal, key string, content feed.OpportunityContent) sqlite.OpportunityMutationResult {
	t.Helper()
	out, err := s.AddOpportunity(t.Context(), p, feed.AddOpportunityInput{IdempotencyKey: key, Opportunity: content})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpportunitiesOwnerIsolationCanonicalDedupAndDurableResults(t *testing.T) {
	f, s, alice, bob := setup(t)
	initial := opportunityContent()
	add := feed.AddOpportunityInput{IdempotencyKey: "save", Opportunity: initial}
	first := addOpportunity(t, s, alice, add.IdempotencyKey, initial)
	if !first.Created || first.Opportunity.Revision != 1 || first.Opportunity.URL != "https://jobs.example.com/opening" {
		t.Fatalf("add/canonical identity: %#v", first)
	}
	foreign := addOpportunity(t, s, bob, add.IdempotencyKey, initial)
	if foreign.Opportunity.ID == first.Opportunity.ID {
		t.Fatal("owners shared an opportunity")
	}
	if _, err := s.GetOpportunity(t.Context(), bob, feed.GetOpportunityInput{OpportunityID: first.Opportunity.ID}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("foreign detail exposed")
	}
	// News and opportunities have independent canonical identities and retry keys.
	news := content()
	news.URL = initial.URL
	article, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: "save", Article: news})
	if err != nil || !article.Created {
		t.Fatal("opportunity changed News deduplication or retry namespace")
	}
	duplicate := initial
	duplicate.Title, duplicate.URL = "Must not replace", "https://JOBS.example.com:443/opening?fbclid=other"
	got := addOpportunity(t, s, alice, "duplicate", duplicate)
	if got.Created || !reflect.DeepEqual(got.Opportunity, first.Opportunity) {
		t.Fatal("canonical duplicate replaced content")
	}

	type retry struct {
		name string
		run  func(identity.Principal) (sqlite.OpportunityMutationResult, error)
		want sqlite.OpportunityMutationResult
	}
	retries := []retry{{"add", func(p identity.Principal) (sqlite.OpportunityMutationResult, error) {
		return s.AddOpportunity(t.Context(), p, add)
	}, first}}
	readOnly := f.Principal(t, "alice", identity.Read)
	writeOnly := f.Principal(t, "alice", identity.ArticlesWrite)
	run := func(name string, call func(identity.Principal) (sqlite.OpportunityMutationResult, error)) sqlite.OpportunityMutationResult {
		t.Helper()
		for _, p := range []identity.Principal{readOnly, writeOnly} {
			if _, err := call(p); !errors.Is(err, identity.ErrForbidden) {
				t.Fatalf("%s allowed insufficient scope: %v", name, err)
			}
		}
		if _, err := call(bob); !errors.Is(err, sqlite.ErrNotFound) {
			t.Fatalf("%s crossed owner: %v", name, err)
		}
		out, err := call(alice)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		retries = append(retries, retry{name, call, out})
		return out
	}
	patch := feed.PatchOpportunityInput{IdempotencyKey: "patch", OpportunityID: first.Opportunity.ID, ExpectedRevision: 1,
		Patch: feed.OpportunityPatch{Title: new("Updated source title"), AgentNote: new("Agent commentary only")}}
	patched := run("patch", func(p identity.Principal) (sqlite.OpportunityMutationResult, error) {
		return s.PatchOpportunity(t.Context(), p, patch)
	})
	if patched.Opportunity.SourceDescription != initial.SourceDescription || patched.Opportunity.AgentNote != *patch.Patch.AgentNote || patched.Opportunity.Revision != 2 {
		t.Fatal("partial patch lost source/agent distinction")
	}
	archive := feed.ArchiveOpportunityInput{IdempotencyKey: "archive", OpportunityID: first.Opportunity.ID, ExpectedRevision: 2, Archived: true}
	archived := run("archive", func(p identity.Principal) (sqlite.OpportunityMutationResult, error) {
		return s.SetOpportunityArchived(t.Context(), p, archive)
	})
	minimal := feed.OpportunityContent{Kind: "job", URL: initial.URL, Title: "Replacement title", Company: "Example Company"}
	update := feed.UpdateOpportunityInput{IdempotencyKey: "update", OpportunityID: first.Opportunity.ID, ExpectedRevision: 3, Opportunity: minimal}
	updated := run("update", func(p identity.Principal) (sqlite.OpportunityMutationResult, error) {
		return s.UpdateOpportunity(t.Context(), p, update)
	})
	if updated.Opportunity.AgentNote != "" || updated.Opportunity.SourceDescription != "" || len(updated.Opportunity.Tags) != 0 || updated.Opportunity.Compensation != "" || updated.Opportunity.ArchivedAt != archived.Opportunity.ArchivedAt {
		t.Fatal("replacement failed to clear unknowns or preserve archive")
	}
	trash := feed.OpportunityStateInput{IdempotencyKey: "trash", OpportunityID: first.Opportunity.ID, ExpectedRevision: 4}
	trashed := run("trash", func(p identity.Principal) (sqlite.OpportunityMutationResult, error) {
		return s.TrashOpportunity(t.Context(), p, trash)
	})
	if trashed.Opportunity.TrashedAt == 0 || trashed.Opportunity.ArchivedAt != archived.Opportunity.ArchivedAt {
		t.Fatal("Trash lost archive state")
	}
	if duplicate := addOpportunity(t, s, alice, "duplicate-trash", initial); duplicate.Created || !reflect.DeepEqual(duplicate.Opportunity, trashed.Opportunity) {
		t.Fatal("canonical duplicate revived Trash")
	}
	if _, err := s.GetOpportunity(t.Context(), alice, feed.GetOpportunityInput{OpportunityID: first.Opportunity.ID}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("ordinary get included Trash")
	}
	if got, err := s.GetOpportunity(t.Context(), alice, feed.GetOpportunityInput{OpportunityID: first.Opportunity.ID, Trashed: true}); err != nil || !reflect.DeepEqual(got, trashed.Opportunity) {
		t.Fatal("explicit Trash get lost listing")
	}
	for _, call := range []func() error{
		func() error {
			_, err := s.UpdateOpportunity(t.Context(), alice, feed.UpdateOpportunityInput{IdempotencyKey: "edit-trash", OpportunityID: first.Opportunity.ID, ExpectedRevision: 5, Opportunity: minimal})
			return err
		},
		func() error {
			_, err := s.PatchOpportunity(t.Context(), alice, feed.PatchOpportunityInput{IdempotencyKey: "patch-trash", OpportunityID: first.Opportunity.ID, ExpectedRevision: 5, Patch: feed.OpportunityPatch{Title: new("Changed")}})
			return err
		},
		func() error {
			_, err := s.SetOpportunityArchived(t.Context(), alice, feed.ArchiveOpportunityInput{IdempotencyKey: "archive-trash", OpportunityID: first.Opportunity.ID, ExpectedRevision: 5, Archived: false})
			return err
		},
	} {
		if err := call(); !errors.Is(err, sqlite.ErrNotFound) {
			t.Fatal("Trash allowed content/archive mutation")
		}
	}
	restore := feed.OpportunityStateInput{IdempotencyKey: "restore", OpportunityID: first.Opportunity.ID, ExpectedRevision: 5}
	restored := run("restore", func(p identity.Principal) (sqlite.OpportunityMutationResult, error) {
		return s.RestoreOpportunity(t.Context(), p, restore)
	})
	if restored.Opportunity.TrashedAt != 0 || restored.Opportunity.ArchivedAt != archived.Opportunity.ArchivedAt || restored.Opportunity.CreatedAt != first.Opportunity.CreatedAt || restored.Opportunity.Revision != 6 {
		t.Fatal("restore lost original state/time")
	}
	noop, err := s.SetOpportunityArchived(t.Context(), alice, feed.ArchiveOpportunityInput{IdempotencyKey: "already-archived", OpportunityID: first.Opportunity.ID, ExpectedRevision: 6, Archived: true})
	if err != nil || !reflect.DeepEqual(noop.Opportunity, restored.Opportunity) {
		t.Fatal("archive no-op changed revision/order")
	}
	later := update
	later.IdempotencyKey, later.ExpectedRevision, later.Opportunity.Title = "later-update", 6, "Latest source title"
	current, err := s.UpdateOpportunity(t.Context(), alice, later)
	if err != nil {
		t.Fatal(err)
	}
	f.Reopen(t)
	policy, _ := thumbnail.NewPolicy("")
	s = feed.New(f.Store, policy)
	for _, tc := range retries {
		for _, p := range []identity.Principal{readOnly, writeOnly} {
			if _, err := tc.run(p); !errors.Is(err, identity.ErrForbidden) {
				t.Fatalf("%s exact retry disclosed content without both scopes: %v", tc.name, err)
			}
		}
		if out, err := tc.run(alice); err != nil || !reflect.DeepEqual(out, tc.want) {
			t.Fatalf("%s durable result changed after later updates/reopen: %#v %v", tc.name, out, err)
		}
		if tc.name != "add" {
			if _, err := tc.run(bob); !errors.Is(err, sqlite.ErrNotFound) {
				t.Fatalf("%s replay crossed owner", tc.name)
			}
		}
	}
	if out, err := s.AddOpportunity(t.Context(), bob, add); err != nil || !reflect.DeepEqual(out, foreign) {
		t.Fatal("shared retry key disclosed Alice instead of Bob's result")
	}
	if got, err := s.GetOpportunity(t.Context(), alice, feed.GetOpportunityInput{OpportunityID: first.Opportunity.ID}); err != nil || !reflect.DeepEqual(got, current.Opportunity) {
		t.Fatal("replay applied old state again")
	}
	changed := add
	changed.Opportunity.Title = "Different input"
	if _, err := s.AddOpportunity(t.Context(), alice, changed); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatal("changed intent reused retry key")
	}
	if _, err := s.TrashOpportunity(t.Context(), alice, feed.OpportunityStateInput{IdempotencyKey: "save", OpportunityID: first.Opportunity.ID, ExpectedRevision: 7}); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatal("another operation reused opportunity retry key")
	}
	if news, err := s.GetArticle(t.Context(), alice, article.Article.ID); err != nil || news != article.Article {
		t.Fatal("opportunity operations changed News")
	}
	if err := f.Store.DisableAccount(t.Context(), alice.Identity()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range retries {
		if _, err := tc.run(alice); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatalf("disabled owner replayed %s", tc.name)
		}
	}
}

func TestOpportunityScopesPrecedeLookupAndEffects(t *testing.T) {
	f, s, _, _ := setup(t)
	readOnly, writeOnly := f.Principal(t, "alice", identity.Read), f.Principal(t, "alice", identity.ArticlesWrite)
	if err := f.Store.Close(); err != nil {
		t.Fatal(err)
	}
	// A closed store makes any accidental enrollment lookup or write observable.
	for _, p := range []identity.Principal{readOnly, writeOnly} {
		for name, call := range map[string]func() error{
			"add":    func() error { _, err := s.AddOpportunity(t.Context(), p, feed.AddOpportunityInput{}); return err },
			"update": func() error { _, err := s.UpdateOpportunity(t.Context(), p, feed.UpdateOpportunityInput{}); return err },
			"patch":  func() error { _, err := s.PatchOpportunity(t.Context(), p, feed.PatchOpportunityInput{}); return err },
			"archive": func() error {
				_, err := s.SetOpportunityArchived(t.Context(), p, feed.ArchiveOpportunityInput{})
				return err
			},
			"trash":   func() error { _, err := s.TrashOpportunity(t.Context(), p, feed.OpportunityStateInput{}); return err },
			"restore": func() error { _, err := s.RestoreOpportunity(t.Context(), p, feed.OpportunityStateInput{}); return err },
		} {
			if err := call(); !errors.Is(err, identity.ErrForbidden) {
				t.Fatalf("%s accessed store before required grants: %v", name, err)
			}
		}
	}
	if _, err := s.GetOpportunity(t.Context(), writeOnly, feed.GetOpportunityInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("write implied read")
	}
	if _, err := s.ListOpportunities(t.Context(), writeOnly, feed.ListOpportunitiesInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("write implied list")
	}
}

func TestOpportunityValidationAndFailedWritesReserveNoKey(t *testing.T) {
	_, s, alice, _ := setup(t)
	for name, change := range map[string]func(*feed.OpportunityContent){
		"kind":               func(c *feed.OpportunityContent) { c.Kind = "fellowship" },
		"URL":                func(c *feed.OpportunityContent) { c.URL = "http://jobs.example.com/apply" },
		"title":              func(c *feed.OpportunityContent) { c.Title = strings.Repeat("é", 151) },
		"title space":        func(c *feed.OpportunityContent) { c.Title = " leading" },
		"company":            func(c *feed.OpportunityContent) { c.Company = "" },
		"company bound":      func(c *feed.OpportunityContent) { c.Company = strings.Repeat("a", 201) },
		"location":           func(c *feed.OpportunityContent) { c.Location = strings.Repeat("a", 201) },
		"workplace":          func(c *feed.OpportunityContent) { c.Workplace = "anywhere" },
		"employment type":    func(c *feed.OpportunityContent) { c.EmploymentType = "guess" },
		"period":             func(c *feed.OpportunityContent) { c.EmploymentPeriod = strings.Repeat("a", 161) },
		"compensation":       func(c *feed.OpportunityContent) { c.Compensation = strings.Repeat("a", 301) },
		"deadline":           func(c *feed.OpportunityContent) { c.Deadline = "2027-02-29" },
		"deadline timestamp": func(c *feed.OpportunityContent) { c.Deadline = "2027-02-28T00:00:00Z" },
		"tags count":         func(c *feed.OpportunityContent) { c.Tags = make([]string, 13) },
		"tags duplicate":     func(c *feed.OpportunityContent) { c.Tags = []string{"Go", "Go"} },
		"tags bound":         func(c *feed.OpportunityContent) { c.Tags = []string{strings.Repeat("a", 41)} },
		"tags empty":         func(c *feed.OpportunityContent) { c.Tags = []string{""} },
		"description": func(c *feed.OpportunityContent) {
			c.SourceDescription = strings.Repeat("a", feed.MaxSourceDescriptionBytes+1)
		},
		"note":    func(c *feed.OpportunityContent) { c.AgentNote = strings.Repeat("a", feed.MaxAgentNoteBytes+1) },
		"control": func(c *feed.OpportunityContent) { c.SourceDescription = "text\x00control" },
		"UTF8":    func(c *feed.OpportunityContent) { c.AgentNote = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			c := opportunityContent()
			change(&c)
			if _, err := s.AddOpportunity(t.Context(), alice, feed.AddOpportunityInput{IdempotencyKey: "not-reserved", Opportunity: c}); !errors.Is(err, feed.ErrInvalidOpportunity) {
				t.Fatalf("invalid content accepted: %v", err)
			}
		})
	}
	c := opportunityContent()
	c.SourceDescription, c.AgentNote = strings.Repeat("a", feed.MaxSourceDescriptionBytes-3)+"\n\r\t", strings.Repeat("a", feed.MaxAgentNoteBytes)
	a := addOpportunity(t, s, alice, "not-reserved", c)
	c.URL = "https://jobs.example.com/second"
	b := addOpportunity(t, s, alice, "second", c)
	collision := feed.UpdateOpportunityInput{IdempotencyKey: "failed-update", OpportunityID: b.Opportunity.ID, ExpectedRevision: 1, Opportunity: a.Opportunity.OpportunityContent}
	if _, err := s.UpdateOpportunity(t.Context(), alice, collision); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("canonical collision accepted")
	}
	if got, err := s.GetOpportunity(t.Context(), alice, feed.GetOpportunityInput{OpportunityID: b.Opportunity.ID}); err != nil || !reflect.DeepEqual(got, b.Opportunity) {
		t.Fatal("failed collision partially wrote")
	}
	collision.Opportunity = c
	collision.Opportunity.Title = "Corrected"
	updated, err := s.UpdateOpportunity(t.Context(), alice, collision)
	if err != nil || updated.Opportunity.Revision != 2 {
		t.Fatal("failed collision reserved retry key")
	}
	stale := feed.PatchOpportunityInput{IdempotencyKey: "failed-patch", OpportunityID: b.Opportunity.ID, ExpectedRevision: 1, Patch: feed.OpportunityPatch{Company: new("Corrected company")}}
	if _, err := s.PatchOpportunity(t.Context(), alice, stale); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("stale revision accepted")
	}
	stale.ExpectedRevision = 2
	if out, err := s.PatchOpportunity(t.Context(), alice, stale); err != nil || out.Opportunity.Revision != 3 {
		t.Fatal("failed revision reserved key")
	}
	if message := feed.OpportunitySafeError(errors.New("SQL secret path or content")); message != "Newsfeed temporarily unavailable." {
		t.Fatal("internal error exposed")
	}
}

func TestOpportunityPatchAmbiguityClearsAndIntent(t *testing.T) {
	_, s, alice, _ := setup(t)
	first := addOpportunity(t, s, alice, "first", opportunityContent())
	for _, raw := range []string{
		`{"title":"first","title":"second"}`, `{"tags":[],"tags":null}`, `{"company":null}`, `{"kind":null}`, `{"url":null}`,
		`{"tags":"Go"}`, `{"tags":[1]}`, `{"unknown":"x"}`, `null`, `[]`, `{"title":42}`, `{"title":"x"} {}`,
	} {
		var patch feed.OpportunityPatch
		if err := json.Unmarshal([]byte(raw), &patch); err == nil {
			t.Fatalf("ambiguous/invalid patch accepted: %s", raw)
		}
	}
	for _, patch := range []feed.OpportunityPatch{{}, {Company: new("")}, {Kind: new("other")}, {Tags: new([]string{"Go", "Go"})}, {Deadline: new("tomorrow")}} {
		if _, err := s.PatchOpportunity(t.Context(), alice, feed.PatchOpportunityInput{IdempotencyKey: "clear", OpportunityID: first.Opportunity.ID, ExpectedRevision: 1, Patch: patch}); !errors.Is(err, feed.ErrInvalidOpportunity) {
			t.Fatal("invalid patch accepted or key reserved")
		}
	}
	var patch feed.OpportunityPatch
	if err := json.Unmarshal([]byte(`{"compensation":null,"deadline":null,"tags":null,"agent_note":null}`), &patch); err != nil {
		t.Fatal(err)
	}
	in := feed.PatchOpportunityInput{IdempotencyKey: "clear", OpportunityID: first.Opportunity.ID, ExpectedRevision: 1, Patch: patch}
	cleared, err := s.PatchOpportunity(t.Context(), alice, in)
	if err != nil || cleared.Opportunity.Compensation != "" || cleared.Opportunity.Deadline != "" || len(cleared.Opportunity.Tags) != 0 || cleared.Opportunity.AgentNote != "" || cleared.Opportunity.SourceDescription != first.Opportunity.SourceDescription {
		t.Fatal("explicit clear changed omitted source facts")
	}
	if err := json.Unmarshal([]byte(`{"compensation":"","deadline":"","tags":[],"agent_note":""}`), &in.Patch); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.PatchOpportunity(t.Context(), alice, in); err != nil || !reflect.DeepEqual(replay, cleared) {
		t.Fatal("null/empty clear changed normalized intent")
	}
	in.Patch.AgentNote = nil
	if _, err := s.PatchOpportunity(t.Context(), alice, in); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatal("omission was confused with clear")
	}
}

func TestOpportunityPaginationViewsAndCollectionIsolation(t *testing.T) {
	_, s, alice, bob := setup(t)
	for i := range 15 {
		c := opportunityContent()
		c.URL = fmt.Sprintf("https://jobs.example.com/%d", i)
		out := addOpportunity(t, s, alice, fmt.Sprint(i), c)
		if i >= 5 {
			archived, err := s.SetOpportunityArchived(t.Context(), alice, feed.ArchiveOpportunityInput{IdempotencyKey: fmt.Sprintf("archive-%d", i), OpportunityID: out.Opportunity.ID, ExpectedRevision: 1, Archived: true})
			if err != nil {
				t.Fatal(err)
			}
			out = archived
		}
		if i >= 10 {
			if _, err := s.TrashOpportunity(t.Context(), alice, feed.OpportunityStateInput{IdempotencyKey: fmt.Sprintf("trash-%d", i), OpportunityID: out.Opportunity.ID, ExpectedRevision: 2}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, in := range []feed.ListOpportunitiesInput{{Limit: 2}, {Limit: 2, Archived: true}, {Limit: 2, Trashed: true}} {
		first, err := s.ListOpportunities(t.Context(), alice, in)
		if err != nil || first.NextCursor == "" {
			t.Fatalf("missing page: %v", err)
		}
		otherOwner := in
		otherOwner.Cursor = first.NextCursor
		if _, err := s.ListOpportunities(t.Context(), bob, otherOwner); !errors.Is(err, feed.ErrInvalidOpportunity) {
			t.Fatal("cursor crossed owner")
		}
		for _, other := range []feed.ListOpportunitiesInput{{}, {Archived: true}, {Trashed: true}} {
			if other.Archived == in.Archived && other.Trashed == in.Trashed {
				continue
			}
			other.Cursor = first.NextCursor
			if _, err := s.ListOpportunities(t.Context(), alice, other); !errors.Is(err, feed.ErrInvalidOpportunity) {
				t.Fatal("cursor crossed view")
			}
		}
		if _, err := s.ListArticles(t.Context(), alice, feed.ListInput{Cursor: first.NextCursor, Archived: in.Archived, Trashed: in.Trashed}); !errors.Is(err, feed.ErrInvalid) {
			t.Fatal("opportunity cursor entered News")
		}
		seen := make(map[string]bool)
		var previous sqlite.Opportunity
		for {
			page, err := s.ListOpportunities(t.Context(), alice, in)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range page.Opportunities {
				timestamp := func(o sqlite.Opportunity) int64 {
					if in.Trashed {
						return o.TrashedAt
					}
					if in.Archived {
						return o.ArchivedAt
					}
					return o.CreatedAt
				}
				if seen[o.ID] || (previous.ID != "" && (timestamp(o) > timestamp(previous) || (timestamp(o) == timestamp(previous) && o.ID >= previous.ID))) || o.SourceDescription != "" || o.AgentNote != "" {
					t.Fatal("pagination repeated/reordered records or loaded long detail")
				}
				seen[o.ID], previous = true, o
			}
			in.Cursor = page.NextCursor
			if in.Cursor == "" {
				break
			}
		}
		if len(seen) != 5 {
			t.Fatalf("view lost records: %d", len(seen))
		}
	}
	for i := range 2 {
		c := content()
		c.URL = fmt.Sprintf("https://example.com/news/%d", i)
		if _, err := s.AddArticle(t.Context(), alice, feed.AddInput{IdempotencyKey: fmt.Sprintf("news-%d", i), Article: c}); err != nil {
			t.Fatal(err)
		}
	}
	news, err := s.ListArticles(t.Context(), alice, feed.ListInput{Limit: 1})
	if err != nil || news.NextCursor == "" {
		t.Fatal("News cursor fixture unavailable")
	}
	if _, err := s.ListOpportunities(t.Context(), alice, feed.ListOpportunitiesInput{Cursor: news.NextCursor}); !errors.Is(err, feed.ErrInvalidOpportunity) {
		t.Fatal("News cursor entered opportunities")
	}
	for _, in := range []feed.ListOpportunitiesInput{{Archived: true, Trashed: true}, {Limit: 51}, {Limit: -1}, {Cursor: "garbage"}, {Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"before_time":1,"before_id":"x"}`))}} {
		if _, err := s.ListOpportunities(t.Context(), alice, in); !errors.Is(err, feed.ErrInvalidOpportunity) {
			t.Fatal("invalid pagination accepted")
		}
	}
	if page, err := s.ListOpportunities(t.Context(), bob, feed.ListOpportunitiesInput{}); err != nil || len(page.Opportunities) != 0 {
		t.Fatal("foreign list exposed records")
	}
}

func TestOpportunityConcurrentDedupAndRevisionWinner(t *testing.T) {
	_, s, alice, _ := setup(t)
	results := make(chan sqlite.OpportunityMutationResult, 8)
	failures := make(chan error, 8)
	var group sync.WaitGroup
	for i := range 8 {
		group.Go(func() {
			out, err := s.AddOpportunity(t.Context(), alice, feed.AddOpportunityInput{IdempotencyKey: fmt.Sprintf("add-%d", i), Opportunity: opportunityContent()})
			results <- out
			failures <- err
		})
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	created, id := 0, ""
	for out := range results {
		if out.Created {
			created++
		}
		if id != "" && id != out.Opportunity.ID {
			t.Fatal("concurrent duplicate inserted")
		}
		id = out.Opportunity.ID
	}
	if created != 1 {
		t.Fatalf("inserted %d records", created)
	}
	updates := make(chan error, 8)
	for i := range 8 {
		group.Go(func() {
			_, err := s.PatchOpportunity(t.Context(), alice, feed.PatchOpportunityInput{IdempotencyKey: fmt.Sprintf("patch-%d", i), OpportunityID: id, ExpectedRevision: 1, Patch: feed.OpportunityPatch{Title: new(fmt.Sprintf("Edit %d", i))}})
			updates <- err
		})
	}
	group.Wait()
	close(updates)
	wins, conflicts := 0, 0
	for err := range updates {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, sqlite.ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 7 {
		t.Fatalf("revision winners=%d conflicts=%d", wins, conflicts)
	}
}

func TestOpportunityConcurrentPatchRetries(t *testing.T) {
	for _, differentIntents := range []bool{false, true} {
		t.Run(fmt.Sprintf("different_intents=%t", differentIntents), func(t *testing.T) {
			_, s, alice, _ := setup(t)
			for round := range 16 {
				content := opportunityContent()
				content.URL = fmt.Sprintf("https://jobs.example.com/concurrent-%d", round)
				initial := addOpportunity(t, s, alice, fmt.Sprintf("save-%d", round), content)
				in := feed.PatchOpportunityInput{IdempotencyKey: fmt.Sprintf("patch-%d", round), OpportunityID: initial.Opportunity.ID, ExpectedRevision: 1,
					Patch: feed.OpportunityPatch{Title: new("Concurrent title")}}
				type response struct {
					title string
					out   sqlite.OpportunityMutationResult
					err   error
				}
				responses := make(chan response, 16)
				start := make(chan struct{})
				var group sync.WaitGroup
				for i := range 16 {
					group.Go(func() {
						request := in
						if differentIntents && i%2 != 0 {
							request.Patch.Title = new("Other concurrent title")
						}
						<-start
						out, err := s.PatchOpportunity(t.Context(), alice, request)
						responses <- response{*request.Patch.Title, out, err}
					})
				}
				close(start)
				group.Wait()
				close(responses)

				current, err := s.GetOpportunity(t.Context(), alice, feed.GetOpportunityInput{OpportunityID: initial.Opportunity.ID})
				if err != nil || current.Revision != 2 {
					t.Fatalf("round %d: patch must commit once: %#v %v", round, current, err)
				}
				winner := sqlite.OpportunityMutationResult{Opportunity: current}
				for response := range responses {
					if response.title != current.Title {
						if !errors.Is(response.err, sqlite.ErrIdempotencyConflict) {
							t.Fatalf("round %d: different intent must conflict with the successful key: %v", round, response.err)
						}
					} else if response.err != nil || !reflect.DeepEqual(response.out, winner) {
						t.Fatalf("round %d: identical retry must return its successful result: %#v %v", round, response.out, response.err)
					}
				}

				// Later state cannot replace the result saved for the winning patch.
				if _, err := s.TrashOpportunity(t.Context(), alice, feed.OpportunityStateInput{IdempotencyKey: fmt.Sprintf("trash-%d", round), OpportunityID: current.ID, ExpectedRevision: current.Revision}); err != nil {
					t.Fatal(err)
				}
				in.Patch.Title = new(current.Title)
				if replay, err := s.PatchOpportunity(t.Context(), alice, in); err != nil || !reflect.DeepEqual(replay, winner) {
					t.Fatalf("round %d: retry after Trash: %#v %v", round, replay, err)
				}
				in.Patch.Title = new("Another intent after Trash")
				if _, err := s.PatchOpportunity(t.Context(), alice, in); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
					t.Fatalf("round %d: changed intent after Trash: %v", round, err)
				}
				in.IdempotencyKey = fmt.Sprintf("new-patch-%d", round)
				if _, err := s.PatchOpportunity(t.Context(), alice, in); !errors.Is(err, sqlite.ErrNotFound) {
					t.Fatalf("round %d: new patch must still exclude Trash: %v", round, err)
				}
			}
		})
	}
}
