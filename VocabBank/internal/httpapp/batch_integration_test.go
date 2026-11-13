package httpapp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/mcptools"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func sdkBatchItem(term, key string, content []store.Sense) store.AddWordsItem {
	return store.AddWordsItem{AddInput: store.AddInput{Capture: store.Capture{Term: term, OriginalSentence: "Synthetic captured sentence.", SourceURL: "https://example.test/synthetic", PersonalNote: "Synthetic personal note."}, IdempotencyKey: key}, Senses: content}
}

func schemaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		t.Fatalf("schema object=%s %v", raw, err)
	}
	return object
}

func TestOfficialMCPBatchSchemaAnd100MixedRecords(t *testing.T) {
	f := newFixture(t, false)
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var batch *sdk.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "add_words" {
			batch = tool
		}
	}
	if batch == nil {
		t.Fatal("add_words was not advertised")
	}
	a := batch.Annotations
	if a == nil || a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Fatalf("batch annotations=%+v", a)
	}
	input := schemaObject(t, batch.InputSchema)
	properties := schemaObject(t, input["properties"])
	wordArray := schemaObject(t, properties["words"])
	item := schemaObject(t, wordArray["items"])
	fields := schemaObject(t, item["properties"])
	for _, name := range []string{"term", "idempotency_key", "original_sentence", "source_url", "personal_note", "senses"} {
		if fields[name] == nil {
			t.Fatalf("missing advertised item field %s", name)
		}
	}
	if fields["owner"] != nil || input["additionalProperties"] != false || item["additionalProperties"] != false {
		t.Fatal("batch schema permits selecting an owner or unknown fields")
	}
	required, ok := item["required"].([]any)
	if !ok || !slices.Contains(required, any("term")) || !slices.Contains(required, any("idempotency_key")) || slices.Contains(required, any("senses")) {
		t.Fatalf("batch required fields=%v", item["required"])
	}
	if batch.OutputSchema == nil || !strings.Contains(batch.Description, "1–100") || !strings.Contains(batch.Description, "preposition") {
		t.Fatal("batch output schema/bounds/POS guidance missing")
	}
	in := store.AddWordsInput{Words: make([]store.AddWordsItem, 100)}
	for i := range 100 {
		in.Words[i] = sdkBatchItem(fmt.Sprintf("synthetic batch %03d", i), fmt.Sprintf("sdk-batch-mixed-%03d", i), nil)
		if i%2 == 0 {
			in.Words[i].Senses = syntheticSenses()
		}
	}
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": sdk.CallToolParams{Name: "add_words", Arguments: in}})
	if err != nil || len(request) >= 256<<10 {
		t.Fatalf("realistic maximum batch bytes=%d err=%v", len(request), err)
	}
	out := callTool[mcptools.AddWordsOutput](t, session, "add_words", in)
	if len(out.Results) != 100 {
		t.Fatalf("batch result count=%d", len(out.Results))
	}
	for i, result := range out.Results {
		if result.Index != i || result.IdempotencyKey != in.Words[i].IdempotencyKey || !result.Success || result.Word == nil || result.Word.Revision != 1 || result.Code != "" || result.Error != "" {
			t.Fatalf("batch result %d=%+v", i, result)
		}
		word := result.Word
		if word.OriginalSentence != in.Words[i].OriginalSentence || word.SourceURL != in.Words[i].SourceURL || word.PersonalNote != in.Words[i].PersonalNote {
			t.Fatalf("lost capture at %d", i)
		}
		if i%2 == 0 {
			if word.Status != store.Complete || !reflect.DeepEqual(word.Senses, in.Words[i].Senses) {
				t.Fatalf("complete item %d=%+v", i, word)
			}
		} else if word.Status != store.NeedsDetails || len(word.Senses) != 0 {
			t.Fatalf("draft item %d=%+v", i, word)
		}
	}
	for _, status := range []string{store.NeedsDetails, store.Complete} {
		page := callTool[store.Page](t, session, "list_words", store.Filter{Status: status, PageSize: 100})
		if page.Total != 50 || len(page.Items) != 50 {
			t.Fatalf("mixed status %s count=%d", status, page.Total)
		}
	}
	cookie, _ := f.login(t)
	word := out.Results[0].Word
	viaHTTP := decodeResponse[store.Word](t, f.request(t, "GET", "/api/v1/words/"+word.ID, nil, "", cookie, ""), 200)
	if !reflect.DeepEqual(viaHTTP, *word) {
		t.Fatal("browser API does not see the batch final state")
	}
}

func TestOfficialMCPBatchPartialFailuresDuplicatesAndRetryEdits(t *testing.T) {
	f := newFixture(t, false)
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	existing := callTool[store.Word](t, session, "add_word", store.AddInput{Capture: store.Capture{Term: "existing synthetic", PersonalNote: "Keep this capture."}, IdempotencyKey: "sdk-existing-001"})
	existing = callTool[store.Word](t, session, "complete_word", store.CompleteInput{ID: existing.ID, ExpectedRevision: 1, IdempotencyKey: "sdk-existing-fill", Senses: syntheticSenses()})
	in := store.AddWordsInput{Words: []store.AddWordsItem{
		sdkBatchItem("synthetic complete", "sdk-batch-complete", syntheticSenses()),
		sdkBatchItem("   ", "sdk-batch-blank", nil),
		sdkBatchItem(" EXISTING SYNTHETIC ", "sdk-batch-existing", syntheticSenses()),
		sdkBatchItem("SYNTHETIC COMPLETE", "sdk-batch-duplicate", nil),
		sdkBatchItem("synthetic invalid learning", "sdk-batch-invalid", []store.Sense{{PartOfSpeech: "unknown"}}),
		sdkBatchItem("synthetic invalid key", "short", nil),
		sdkBatchItem("synthetic draft", "sdk-batch-draft", nil),
	}}
	out := callTool[mcptools.AddWordsOutput](t, session, "add_words", in)
	if len(out.Results) != len(in.Words) {
		t.Fatalf("partial result count=%d", len(out.Results))
	}
	codes := []string{"", "invalid_input", "duplicate_term", "duplicate_term", "invalid_input", "invalid_input", ""}
	for i, item := range out.Results {
		if item.Index != i || item.IdempotencyKey != in.Words[i].IdempotencyKey || item.Success != (codes[i] == "") || item.Code != codes[i] || (item.Word != nil) != item.Success || (item.Error == "") != item.Success {
			t.Fatalf("partial result %d=%+v", i, item)
		}
	}
	if current := callTool[store.Word](t, session, "get_word", map[string]any{"id": existing.ID}); !reflect.DeepEqual(current, existing) {
		t.Fatal("duplicate overwrote existing captures or learning")
	}
	created := *out.Results[0].Word
	if current := callTool[store.Word](t, session, "get_word", map[string]any{"id": created.ID}); !reflect.DeepEqual(current, created) {
		t.Fatal("within-batch duplicate overwrote the first creation")
	}
	edited := callTool[store.Word](t, session, "update_word", store.UpdateInput{Capture: store.Capture{Term: "renamed synthetic complete", PersonalNote: "A later user edit."}, ID: created.ID, ExpectedRevision: 1, IdempotencyKey: "sdk-batch-later-edit"})
	updatedSenses := syntheticSenses()
	updatedSenses[0].Definition = "Later learning content."
	edited = callTool[store.Word](t, session, "complete_word", store.CompleteInput{ID: edited.ID, ExpectedRevision: edited.Revision, IdempotencyKey: "sdk-batch-later-learning", Senses: updatedSenses})
	reordered := callTool[mcptools.AddWordsOutput](t, session, "add_words", store.AddWordsInput{Words: []store.AddWordsItem{in.Words[6], in.Words[0]}})
	if !reflect.DeepEqual(reordered.Results[0].Word, out.Results[6].Word) || !reflect.DeepEqual(reordered.Results[1].Word, out.Results[0].Word) {
		t.Fatal("reordering changed successful retry snapshots")
	}
	split := callTool[mcptools.AddWordsOutput](t, session, "add_words", store.AddWordsInput{Words: []store.AddWordsItem{in.Words[0]}})
	if !reflect.DeepEqual(split.Results[0].Word, out.Results[0].Word) {
		t.Fatal("split retry changed its snapshot")
	}
	changed := in.Words[0]
	changed.PersonalNote = "Different intent."
	conflict := callTool[mcptools.AddWordsOutput](t, session, "add_words", store.AddWordsInput{Words: []store.AddWordsItem{changed}})
	if conflict.Results[0].Success || conflict.Results[0].Code != "idempotency_conflict" {
		t.Fatalf("changed key input=%+v", conflict.Results[0])
	}
	if current := callTool[store.Word](t, session, "get_word", map[string]any{"id": created.ID}); !reflect.DeepEqual(current, edited) {
		t.Fatal("batch retry replaced later edits")
	}
	page := callTool[store.Page](t, session, "list_words", store.Filter{})
	if page.Total != 3 {
		t.Fatal("batch retry duplicated a renamed term or saved failed items")
	}
}

func TestOfficialMCPBatchEmptySensesAndSchemaFailures(t *testing.T) {
	f := newFixture(t, false)
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	for i, senses := range []any{nil, []any{}} {
		out := callTool[mcptools.AddWordsOutput](t, session, "add_words", map[string]any{"words": []any{map[string]any{"term": fmt.Sprintf("synthetic empty %d", i), "idempotency_key": fmt.Sprintf("sdk-empty-senses-%d", i), "senses": senses}}})
		if !out.Results[0].Success || out.Results[0].Word.Status != store.NeedsDetails || out.Results[0].Word.Revision != 1 {
			t.Fatal("null/empty senses did not create a draft")
		}
		// An omitted value is the same capture-only intent, with the same key.
		replay := callTool[mcptools.AddWordsOutput](t, session, "add_words", map[string]any{"words": []any{map[string]any{"term": fmt.Sprintf("synthetic empty %d", i), "idempotency_key": fmt.Sprintf("sdk-empty-senses-%d", i)}}})
		if !reflect.DeepEqual(replay.Results[0].Word, out.Results[0].Word) {
			t.Fatal("empty senses and omitted senses changed retry meaning")
		}
	}
	tooMany := store.AddWordsInput{Words: make([]store.AddWordsItem, 101)}
	for i := range 101 {
		tooMany.Words[i] = sdkBatchItem(fmt.Sprintf("excess synthetic %03d", i), fmt.Sprintf("excess-key-%03d", i), nil)
	}
	valid := map[string]any{"term": "synthetic must stay unsaved", "idempotency_key": "sdk-schema-valid"}
	for name, input := range map[string]any{
		"empty":                map[string]any{"words": []any{}},
		"missing words":        map[string]any{},
		"null words":           map[string]any{"words": nil},
		"101 words":            tooMany,
		"wrong envelope type":  map[string]any{"words": "invalid"},
		"unknown owner":        map[string]any{"words": []any{valid}, "owner": "another-owner"},
		"unknown item owner":   map[string]any{"words": []any{valid, map[string]any{"term": "hidden", "idempotency_key": "sdk-schema-owner", "owner": "another-owner"}}},
		"wrong item type":      map[string]any{"words": []any{valid, "invalid"}},
		"null item":            map[string]any{"words": []any{valid, nil}},
		"missing item key":     map[string]any{"words": []any{valid, map[string]any{"term": "hidden"}}},
		"wrong senses type":    map[string]any{"words": []any{valid, map[string]any{"term": "hidden", "idempotency_key": "sdk-schema-senses", "senses": "invalid"}}},
		"missing sense fields": map[string]any{"words": []any{valid, map[string]any{"term": "hidden", "idempotency_key": "sdk-schema-sense-fields", "senses": []any{map[string]any{"part_of_speech": "noun"}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "add_words", Arguments: input})
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("invalid envelope/schema accepted=%+v %v", result, err)
			}
			page := callTool[store.Page](t, session, "list_words", store.Filter{})
			if page.Total != 2 {
				t.Fatal("schema-invalid batch wrote its valid prefix")
			}
		})
	}
}

func TestOfficialMCPBatchScopeOwnershipAndRequestLimit(t *testing.T) {
	f := newFixture(t, false)
	owned := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	readOnly := sdkSession(t, f, "test-subject", store.Read)
	in := store.AddWordsInput{Words: []store.AddWordsItem{sdkBatchItem("owned synthetic batch", "sdk-owner-batch", syntheticSenses())}}
	denied, err := readOnly.CallTool(t.Context(), &sdk.CallToolParams{Name: "add_words", Arguments: in})
	if err != nil || denied == nil || !denied.IsError {
		t.Fatal("read-only batch write accepted")
	}
	page := callTool[store.Page](t, owned, "list_words", store.Filter{})
	if page.Total != 0 {
		t.Fatal("forbidden batch wrote data")
	}
	large := store.AddWordsInput{Words: make([]store.AddWordsItem, 80)}
	for i := range 80 {
		large.Words[i] = sdkBatchItem(fmt.Sprintf("large synthetic %03d", i), fmt.Sprintf("large-key-%03d", i), nil)
		large.Words[i].PersonalNote = strings.Repeat("x", 4000)
	}
	denied, err = owned.CallTool(t.Context(), &sdk.CallToolParams{Name: "add_words", Arguments: large})
	if err == nil && (denied == nil || !denied.IsError) {
		t.Fatal("MCP accepted a request over 256 KiB")
	}
	// The official SDK closes its session after HTTP 413; reconnect to check the store.
	owned = sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	page = callTool[store.Page](t, owned, "list_words", store.Filter{})
	if page.Total != 0 {
		t.Fatal("oversized batch wrote data")
	}
	first := callTool[mcptools.AddWordsOutput](t, owned, "add_words", in)
	if _, err := f.s.Enroll(t.Context(), f.provider.URL(), "other-test-subject", "Other synthetic owner"); err != nil {
		t.Fatal(err)
	}
	other := sdkSession(t, f, "other-test-subject", store.Read+" "+store.Write)
	second := callTool[mcptools.AddWordsOutput](t, other, "add_words", in)
	if !second.Results[0].Success || second.Results[0].Word.ID == first.Results[0].Word.ID {
		t.Fatal("batch returned another owner's record or retry snapshot")
	}
	for _, session := range []*sdk.ClientSession{owned, other} {
		page := callTool[store.Page](t, session, "list_words", store.Filter{})
		if page.Total != 1 {
			t.Fatal("batch reads crossed owners")
		}
	}
}

// Losing an already committed HTTP response must leave the same item intent
// retryable, even after a different write changes the saved word.
type loseBatchResponseTransport struct {
	base http.RoundTripper
	tool string
	lose atomic.Bool
}

func (transport *loseBatchResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var call struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if request.Body != nil {
		raw, err := io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			return nil, err
		}
		request = request.Clone(request.Context())
		request.Body = io.NopCloser(bytes.NewReader(raw))
		_ = json.Unmarshal(raw, &call)
	}
	response, err := transport.base.RoundTrip(request)
	if err == nil && call.Method == "tools/call" && call.Params.Name == transport.tool && transport.lose.CompareAndSwap(true, false) {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("synthetic loss after committed batch response")
	}
	return response, err
}

func TestOfficialMCPBatchResponseLossRetryReturnsOriginalSnapshot(t *testing.T) {
	f := newFixture(t, false)
	token := f.provider.Access("test-subject", "vocab-test", f.origin+"/mcp", store.Read+" "+store.Write, 300)
	transport := &loseBatchResponseTransport{base: bearerTransport{token: token}, tool: "add_words"}
	client := sdk.NewClient(&sdk.Implementation{Name: "vocabbank-response-loss", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: f.origin + "/mcp", HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	in := store.AddWordsInput{Words: []store.AddWordsItem{sdkBatchItem("lost response synthetic complete", "sdk-response-loss-001", syntheticSenses()), sdkBatchItem("lost response synthetic draft", "sdk-response-loss-002", nil)}}
	transport.lose.Store(true)
	if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "add_words", Arguments: in}); err == nil {
		t.Fatal("synthetic response loss was not observed")
	}
	fresh := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	before := callTool[store.Page](t, fresh, "list_words", store.Filter{})
	if before.Total != 2 {
		t.Fatalf("lost response did not commit both items: %+v", before)
	}
	original := before.Items[0]
	if original.Term != in.Words[0].Term || original.Status != store.Complete || original.Revision != 1 {
		t.Fatal("lost batch did not create the final complete state")
	}
	edited := callTool[store.Word](t, fresh, "update_word", store.UpdateInput{Capture: store.Capture{Term: original.Term, PersonalNote: "Later edit after lost response."}, ID: original.ID, ExpectedRevision: 1, IdempotencyKey: "sdk-loss-later-edit"})
	replay := callTool[mcptools.AddWordsOutput](t, fresh, "add_words", in)
	if !reflect.DeepEqual(replay.Results[0].Word, &original) || !reflect.DeepEqual(replay.Results[1].Word, &before.Items[1]) {
		t.Fatal("response-loss retry did not return original successful snapshots")
	}
	current := callTool[store.Word](t, fresh, "get_word", map[string]any{"id": original.ID})
	if !reflect.DeepEqual(current, edited) {
		t.Fatal("response-loss retry replaced a later edit")
	}
}
