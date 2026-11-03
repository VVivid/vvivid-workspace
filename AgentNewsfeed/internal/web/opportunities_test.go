package web_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpOpportunity() feed.OpportunityContent {
	return feed.OpportunityContent{Kind: "internship", URL: "https://jobs.example.com/opening", Title: "Engineering Intern", Company: "Example Company", Workplace: "remote", SourceDescription: "Original employer description\n\nSource facts.", AgentNote: "Agent's separate match explanation.", Tags: []string{"Go"}}
}

func TestOfficialMCPOpportunitiesInitializeSchemaCallsAndScopes(t *testing.T) {
	f, _, h := app(t)
	server := httptest.NewServer(h)
	defer server.Close()
	alice := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes))
	bob := client(t, server.URL, f.Bearer("bob", "newsfeed-chatgpt", f.Manager.MCPAudience(), allScopes))
	readOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.Read))
	writeOnly := client(t, server.URL, f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.ArticlesWrite))
	if init := alice.InitializeResult(); init == nil || !strings.Contains(init.Instructions, "Jobs & Internships") || !strings.Contains(init.Instructions, "source_description") {
		t.Fatal("MCP initialize omitted opportunity/source attribution instructions")
	}
	tools, err := alice.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]*jsonschema.Resolved)
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("untyped tool: %s", tool.Name)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
	}
	for _, name := range []string{"add_opportunity", "list_opportunities", "get_opportunity", "update_opportunity", "patch_opportunity", "set_opportunity_archived", "trash_opportunity", "restore_opportunity"} {
		if schemas[name] == nil {
			t.Fatalf("MCP did not advertise %s", name)
		}
	}
	if len(schemas) != 16 {
		t.Fatalf("expected News plus eight opportunity tools, got %d", len(schemas))
	}
	// Validate the exact schema delivered by tools/list, as well as actual calls.
	for _, input := range []struct {
		tool string
		args map[string]any
	}{
		{"add_opportunity", map[string]any{"idempotency_key": "bad", "opportunity": map[string]any{"kind": "fellowship", "url": "https://jobs.example.com/a", "title": "Title", "company": "Company"}}},
		{"add_opportunity", map[string]any{"idempotency_key": "bad", "opportunity": map[string]any{"kind": "job", "url": "https://jobs.example.com/a", "title": "Title"}}},
		{"add_opportunity", map[string]any{"idempotency_key": "bad", "opportunity": map[string]any{"kind": "job", "url": "https://jobs.example.com/a", "title": "Title", "company": "Company", "workplace": "anywhere"}}},
		{"patch_opportunity", map[string]any{"idempotency_key": "bad", "opportunity_id": "missing", "expected_revision": 1, "patch": map[string]any{"company": nil}}},
		{"patch_opportunity", map[string]any{"idempotency_key": "bad", "opportunity_id": "missing", "expected_revision": 1, "patch": map[string]any{"tags": []string{"Go", "Go"}}}},
		{"patch_opportunity", map[string]any{"idempotency_key": "bad", "opportunity_id": "missing", "expected_revision": 1, "patch": map[string]any{}}},
		{"patch_opportunity", map[string]any{"idempotency_key": "bad", "opportunity_id": "missing", "expected_revision": 1, "patch": map[string]any{"extra": "unknown"}}},
		{"set_opportunity_archived", map[string]any{"idempotency_key": "bad", "opportunity_id": "missing", "expected_revision": 1}},
	} {
		if err := schemas[input.tool].Validate(input.args); err == nil {
			t.Fatalf("advertised schema accepted invalid %s input: %#v", input.tool, input.args)
		}
		if result := call(t, alice, input.tool, input.args); !result.IsError || result.StructuredContent != nil {
			t.Fatalf("MCP accepted invalid %s", input.tool)
		}
	}
	add := feed.AddOpportunityInput{IdempotencyKey: "add", Opportunity: mcpOpportunity()}
	first := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "add_opportunity", add))
	if !first.Created || first.Opportunity.SourceDescription != add.Opportunity.SourceDescription || first.Opportunity.AgentNote != add.Opportunity.AgentNote {
		t.Fatal("MCP dropped/distorted source facts or agent note")
	}
	get := feed.GetOpportunityInput{OpportunityID: first.Opportunity.ID}
	if got := decode[sqlite.Opportunity](t, call(t, alice, "get_opportunity", get)); !reflect.DeepEqual(got, first.Opportunity) {
		t.Fatal("MCP full read changed state")
	}
	if !call(t, bob, "get_opportunity", get).IsError || !call(t, writeOnly, "get_opportunity", get).IsError {
		t.Fatal("MCP get crossed owner or scope")
	}
	if page := decode[feed.OpportunitiesPage](t, call(t, alice, "list_opportunities", feed.ListOpportunitiesInput{})); len(page.Opportunities) != 1 || page.Opportunities[0].SourceDescription != "" || page.Opportunities[0].AgentNote != "" {
		t.Fatal("MCP list loaded long fields or lost record")
	}
	if page := decode[feed.OpportunitiesPage](t, call(t, bob, "list_opportunities", feed.ListOpportunitiesInput{})); len(page.Opportunities) != 0 {
		t.Fatal("MCP list crossed owner")
	}
	if !call(t, writeOnly, "list_opportunities", feed.ListOpportunitiesInput{}).IsError {
		t.Fatal("MCP write scope implied read")
	}
	patch := feed.PatchOpportunityInput{IdempotencyKey: "patch", OpportunityID: first.Opportunity.ID, ExpectedRevision: 1, Patch: feed.OpportunityPatch{Compensation: new("$35/hour, as listed")}}
	patched := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "patch_opportunity", patch))
	if patched.Opportunity.SourceDescription != first.Opportunity.SourceDescription || patched.Opportunity.Compensation != *patch.Patch.Compensation {
		t.Fatal("MCP patch changed omitted facts")
	}
	update := feed.UpdateOpportunityInput{IdempotencyKey: "update", OpportunityID: first.Opportunity.ID, ExpectedRevision: 2, Opportunity: feed.OpportunityContent{Kind: "job", URL: first.Opportunity.URL, Title: "Engineer", Company: "Example Company"}}
	updated := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "update_opportunity", update))
	if updated.Opportunity.SourceDescription != "" || updated.Opportunity.AgentNote != "" || updated.Opportunity.Compensation != "" {
		t.Fatal("MCP full update did not clear omitted fields")
	}
	archive := feed.ArchiveOpportunityInput{IdempotencyKey: "archive", OpportunityID: first.Opportunity.ID, ExpectedRevision: 3, Archived: true}
	archived := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "set_opportunity_archived", archive))
	trash := feed.OpportunityStateInput{IdempotencyKey: "trash", OpportunityID: first.Opportunity.ID, ExpectedRevision: 4}
	trashed := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "trash_opportunity", trash))
	if trashed.Opportunity.TrashedAt == 0 || trashed.Opportunity.ArchivedAt != archived.Opportunity.ArchivedAt || !call(t, alice, "get_opportunity", get).IsError {
		t.Fatal("MCP Trash lost state or ordinary read included it")
	}
	get.Trashed = true
	if got := decode[sqlite.Opportunity](t, call(t, alice, "get_opportunity", get)); !reflect.DeepEqual(got, trashed.Opportunity) {
		t.Fatal("MCP explicit Trash get failed")
	}
	if page := decode[feed.OpportunitiesPage](t, call(t, alice, "list_opportunities", feed.ListOpportunitiesInput{Trashed: true})); len(page.Opportunities) != 1 {
		t.Fatal("MCP explicit Trash list failed")
	}
	restore := feed.OpportunityStateInput{IdempotencyKey: "restore", OpportunityID: first.Opportunity.ID, ExpectedRevision: 5}
	restored := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "restore_opportunity", restore))
	if restored.Opportunity.TrashedAt != 0 || restored.Opportunity.ArchivedAt != archived.Opportunity.ArchivedAt || restored.Opportunity.Revision != 6 {
		t.Fatal("MCP restore lost saved state")
	}
	for _, mutation := range []struct {
		tool string
		in   any
		want sqlite.OpportunityMutationResult
	}{{"add_opportunity", add, first}, {"patch_opportunity", patch, patched}, {"update_opportunity", update, updated}, {"set_opportunity_archived", archive, archived}, {"trash_opportunity", trash, trashed}, {"restore_opportunity", restore, restored}} {
		for _, limited := range []*sdk.ClientSession{readOnly, writeOnly} {
			if result := call(t, limited, mutation.tool, mutation.in); !result.IsError || result.StructuredContent != nil {
				t.Fatalf("%s exact retry exposed result without both scopes", mutation.tool)
			}
		}
		if mutation.tool != "add_opportunity" && !call(t, bob, mutation.tool, mutation.in).IsError {
			t.Fatalf("%s crossed owner", mutation.tool)
		}
		if got := decode[sqlite.OpportunityMutationResult](t, call(t, alice, mutation.tool, mutation.in)); !reflect.DeepEqual(got, mutation.want) {
			t.Fatalf("%s exact retry changed", mutation.tool)
		}
	}
	unarchive := feed.ArchiveOpportunityInput{IdempotencyKey: "unarchive", OpportunityID: first.Opportunity.ID, ExpectedRevision: 6, Archived: false}
	if out := decode[sqlite.OpportunityMutationResult](t, call(t, alice, "set_opportunity_archived", unarchive)); out.Opportunity.ArchivedAt != 0 || out.Opportunity.CreatedAt != first.Opportunity.CreatedAt {
		t.Fatal("MCP explicit unarchive changed original saved time")
	}
	if page := decode[feed.Page](t, call(t, alice, "list_articles", feed.ListInput{})); len(page.Articles) != 0 {
		t.Fatal("opportunities appeared in News")
	}
}

func TestRawMCPOpportunityAmbiguityAndInvalidFieldsHaveNoEffects(t *testing.T) {
	f, s, h := app(t)
	server := httptest.NewServer(h)
	defer server.Close()
	token := f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), allScopes)
	c := client(t, server.URL, token)
	first := decode[sqlite.OpportunityMutationResult](t, call(t, c, "add_opportunity", feed.AddOpportunityInput{IdempotencyKey: "initial", Opportunity: mcpOpportunity()}))
	p := f.Principal(t, "alice", allScopes)
	rawCall := func(t *testing.T, name, arguments string) *sdk.CallToolResult {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + fmt.Sprintf("%q", name) + `,"arguments":` + arguments + `}}`
		req, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "newsfeed.example"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var envelope struct {
			Result *sdk.CallToolResult `json:"result"`
			Error  json.RawMessage     `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || response.StatusCode != http.StatusOK || envelope.Error != nil || envelope.Result == nil {
			t.Fatalf("expected tool result: HTTP %d, %v", response.StatusCode, err)
		}
		return envelope.Result
	}
	arguments := func(fields string) string {
		return fmt.Sprintf(`{"idempotency_key":"rejected","opportunity_id":%q,"expected_revision":1,%s}`, first.Opportunity.ID, fields)
	}
	uri := url.URL{Scheme: "file", Path: f.Path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	retries := func() string {
		t.Helper()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM opportunity_idempotency_results").Scan(&count); err != nil || count != 1 {
			t.Fatalf("rejected request reserved a retry: %d, %v", count, err)
		}
		var operation, hash, result string
		if err := db.QueryRowContext(t.Context(), "SELECT operation,payload_hash,result FROM opportunity_idempotency_results").Scan(&operation, &hash, &result); err != nil {
			t.Fatal(err)
		}
		return operation + hash + result
	}
	originalRetry := retries()
	for _, fields := range []string{
		`"patch":{"title":"First","title":"Second"}`, `"patch":{"company":"First","comp\u0061ny":"Second"}`,
		`"patch":{"tags":["Go"],"tags":null}`, `"patch":{"agent_note":null,"agent_note":"note"}`,
		`"patch":{"source_description":"source","source_description":null}`, `"patch":{"url":"https://example.com/a","url":"https://example.com/b"}`,
		`"patch":{"title":"First"},"patch":{"title":"Second"}`, `"idempotency_key":"other","patch":{"title":"Changed"}`,
		`"expected_revision":2,"patch":{"title":"Changed"}`, `"patch":{"extra":"unknown"}`, `"extra":"unknown","patch":{"title":"Changed"}`,
		`"patch":{"company":null}`, `"patch":{"kind":null}`, `"patch":{"workplace":"inferred"}`, `"patch":{"tags":["Go","Go"]}`,
		`"patch":{"tags":42}`, `"patch":{"agent_note":42}`, `"patch":{}`, `"patch":null`, `"patch":[]`,
	} {
		if out := rawCall(t, "patch_opportunity", arguments(fields)); !out.IsError || out.StructuredContent != nil {
			t.Fatalf("raw ambiguous/invalid patch succeeded: %s", fields)
		}
		if current, err := s.GetOpportunity(t.Context(), p, feed.GetOpportunityInput{OpportunityID: first.Opportunity.ID}); err != nil || !reflect.DeepEqual(current, first.Opportunity) || retries() != originalRetry {
			t.Fatal("rejected request changed content or retry bytes")
		}
	}
	if out := rawCall(t, "add_opportunity", `{"idempotency_key":"bad-add","opportunity":{"kind":"job","url":"https://jobs.example.com/new","title":"Title","company":"First","company":"Second"}}`); !out.IsError {
		t.Fatal("raw duplicate content survived schema normalization")
	}
	if retries() != originalRetry {
		t.Fatal("rejected add changed retry data")
	}
	accepted := decode[sqlite.OpportunityMutationResult](t, rawCall(t, "patch_opportunity", arguments(`"patch":{"title":"Accepted","agent_note":null,"tags":null}`)))
	if accepted.Opportunity.Title != "Accepted" || accepted.Opportunity.AgentNote != "" || len(accepted.Opportunity.Tags) != 0 || accepted.Opportunity.SourceDescription != first.Opportunity.SourceDescription {
		t.Fatal("raw null clears lost omitted source facts")
	}
	if got := decode[sqlite.OpportunityMutationResult](t, rawCall(t, "patch_opportunity", arguments(`"patch":{"title":"Accepted","agent_note":"","tags":[]}`))); !reflect.DeepEqual(got, accepted) {
		t.Fatal("raw null/empty clears changed retry intent")
	}
	if !slices.Equal(accepted.Opportunity.Tags, nil) {
		t.Fatal("cleared tags did not remain unknown")
	}
}
