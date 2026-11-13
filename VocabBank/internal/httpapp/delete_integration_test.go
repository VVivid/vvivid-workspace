package httpapp_test

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/VVivid/vvivid-workspace/VocabBank/internal/mcptools"
	"github.com/VVivid/vvivid-workspace/VocabBank/internal/store"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func sdkDeleteItem(word store.Word, key string) store.DeleteWordsItem {
	return store.DeleteWordsItem{ID: word.ID, ExpectedRevision: word.Revision, IdempotencyKey: key}
}

func TestOfficialMCPDeleteSchemaAnd100Records(t *testing.T) {
	f := newFixture(t, false)
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var deletion *sdk.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "delete_words" {
			deletion = tool
		}
		if tool.Name == "update_words" {
			t.Fatal("bulk update was added outside the approved scope")
		}
	}
	if deletion == nil {
		t.Fatal("delete_words was not advertised")
	}
	a := deletion.Annotations
	if a == nil || a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Fatalf("delete annotations=%+v", a)
	}
	input := schemaObject(t, deletion.InputSchema)
	properties := schemaObject(t, input["properties"])
	wordArray := schemaObject(t, properties["words"])
	item := schemaObject(t, wordArray["items"])
	fields := schemaObject(t, item["properties"])
	for _, name := range []string{"id", "expected_revision", "idempotency_key"} {
		if fields[name] == nil {
			t.Fatalf("missing delete item field %s", name)
		}
	}
	if len(fields) != 3 || input["additionalProperties"] != false || item["additionalProperties"] != false {
		t.Fatal("delete schema permits selecting an owner or unknown fields")
	}
	required, ok := item["required"].([]any)
	if !ok || len(required) != 3 || !slices.Contains(required, any("id")) || !slices.Contains(required, any("expected_revision")) || !slices.Contains(required, any("idempotency_key")) {
		t.Fatalf("delete required fields=%v", item["required"])
	}
	if deletion.OutputSchema == nil || !strings.Contains(deletion.Description, "1–100") || !strings.Contains(deletion.Description, "deleted_word") {
		t.Fatal("delete result schema or bounds guidance is missing")
	}
	seed := store.AddWordsInput{Words: make([]store.AddWordsItem, 100)}
	for i := range 100 {
		seed.Words[i] = sdkBatchItem(fmt.Sprintf("synthetic deletion %03d", i), fmt.Sprintf("sdk-delete-capture-%03d", i), nil)
		if i%2 == 0 {
			seed.Words[i].Senses = syntheticSenses()
		}
	}
	created := callTool[mcptools.AddWordsOutput](t, session, "add_words", seed)
	in := store.DeleteWordsInput{Words: make([]store.DeleteWordsItem, 100)}
	for i, result := range created.Results {
		if !result.Success || result.Word == nil {
			t.Fatalf("seed item %d=%+v", i, result)
		}
		in.Words[i] = sdkDeleteItem(*result.Word, fmt.Sprintf("sdk-delete-intent-%03d", i))
	}
	out := callTool[mcptools.DeleteWordsOutput](t, session, "delete_words", in)
	if len(out.Results) != 100 {
		t.Fatalf("delete result count=%d", len(out.Results))
	}
	for i, result := range out.Results {
		if result.Index != i || result.IdempotencyKey != in.Words[i].IdempotencyKey || !result.Success || result.DeletedWord == nil || result.Code != "" || result.Error != "" || !reflect.DeepEqual(result.DeletedWord, created.Results[i].Word) {
			t.Fatalf("delete result %d=%+v", i, result)
		}
	}
	if page := callTool[store.Page](t, session, "list_words", store.Filter{}); page.Total != 0 {
		t.Fatal("maximum deletion left words in the current bank")
	}
	cookie, _ := f.login(t)
	missing := decodeResponse[map[string]string](t, f.request(t, "GET", "/api/v1/words/"+in.Words[0].ID, nil, "", cookie, ""), 404)
	if missing["code"] != "not_found" {
		t.Fatal("browser API still exposes the deleted word")
	}
}

func TestOfficialMCPDeletePartialFailuresAndExactRetries(t *testing.T) {
	f := newFixture(t, false)
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	seed := store.AddWordsInput{Words: []store.AddWordsItem{
		sdkBatchItem("first synthetic removal", "sdk-delete-first-capture", syntheticSenses()),
		sdkBatchItem("stale synthetic removal", "sdk-delete-stale-capture", syntheticSenses()),
		sdkBatchItem("last synthetic removal", "sdk-delete-last-capture", nil),
	}}
	created := callTool[mcptools.AddWordsOutput](t, session, "add_words", seed)
	if len(created.Results) != 3 || !created.Results[0].Success || !created.Results[1].Success || !created.Results[2].Success {
		t.Fatalf("seed=%+v", created)
	}
	first, stale, last := *created.Results[0].Word, *created.Results[1].Word, *created.Results[2].Word
	stale = callTool[store.Word](t, session, "update_word", store.UpdateInput{Capture: store.Capture{Term: stale.Term, PersonalNote: "Newer synthetic capture."}, ID: stale.ID, ExpectedRevision: 1, IdempotencyKey: "sdk-delete-newer-capture"})
	if _, err := f.s.Enroll(t.Context(), f.provider.URL(), "other-test-subject", "Other synthetic owner"); err != nil {
		t.Fatal(err)
	}
	other := sdkSession(t, f, "other-test-subject", store.Read+" "+store.Write)
	foreign := callTool[store.Word](t, other, "add_word", store.AddInput{Capture: store.Capture{Term: "foreign synthetic removal"}, IdempotencyKey: "sdk-delete-foreign-capture"})
	in := store.DeleteWordsInput{Words: []store.DeleteWordsItem{
		sdkDeleteItem(first, "sdk-delete-first-intent"),
		{ID: stale.ID, ExpectedRevision: 1, IdempotencyKey: "sdk-delete-stale-intent"},
		{ID: "missing-synthetic-id", ExpectedRevision: 1, IdempotencyKey: "sdk-delete-missing-intent"},
		sdkDeleteItem(foreign, "sdk-delete-foreign-intent"),
		{ID: " ", ExpectedRevision: 1, IdempotencyKey: "sdk-delete-invalid-id"},
		{ID: last.ID, ExpectedRevision: 0, IdempotencyKey: "sdk-delete-invalid-revision"},
		sdkDeleteItem(last, "short"),
		sdkDeleteItem(last, "sdk-delete-last-intent"),
		sdkDeleteItem(first, "sdk-delete-first-intent"),
		{ID: first.ID, ExpectedRevision: 2, IdempotencyKey: "sdk-delete-first-intent"},
	}}
	out := callTool[mcptools.DeleteWordsOutput](t, session, "delete_words", in)
	codes := []string{"", "stale_revision", "not_found", "not_found", "invalid_input", "invalid_input", "invalid_input", "", "", "idempotency_conflict"}
	if len(out.Results) != len(codes) {
		t.Fatalf("delete result count=%d", len(out.Results))
	}
	for i, result := range out.Results {
		if result.Index != i || result.IdempotencyKey != in.Words[i].IdempotencyKey || result.Success != (codes[i] == "") || result.Code != codes[i] || (result.DeletedWord != nil) != result.Success || (result.Error == "") != result.Success {
			t.Fatalf("delete item %d=%+v", i, result)
		}
	}
	if !reflect.DeepEqual(out.Results[0].DeletedWord, &first) || !reflect.DeepEqual(out.Results[8].DeletedWord, &first) || !reflect.DeepEqual(out.Results[7].DeletedWord, &last) {
		t.Fatal("successful results lost their removed snapshots")
	}
	if current := callTool[store.Word](t, session, "get_word", map[string]any{"id": stale.ID}); !reflect.DeepEqual(current, stale) {
		t.Fatal("stale deletion changed the newer word")
	}
	if current := callTool[store.Word](t, other, "get_word", map[string]any{"id": foreign.ID}); !reflect.DeepEqual(current, foreign) {
		t.Fatal("deletion crossed owners")
	}
	if page := callTool[store.Page](t, session, "list_words", store.Filter{}); page.Total != 1 {
		t.Fatal("partial outcomes changed the wrong number of words")
	}
}

func TestOfficialMCPDeleteRejectsMalformedEnvelopeBeforePrefixMutations(t *testing.T) {
	f := newFixture(t, false)
	session := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	word := callTool[store.Word](t, session, "add_word", store.AddInput{Capture: store.Capture{Term: "synthetic must remain saved"}, IdempotencyKey: "sdk-delete-schema-capture"})
	valid := map[string]any{"id": word.ID, "expected_revision": word.Revision, "idempotency_key": "sdk-delete-schema-intent"}
	tooMany := store.DeleteWordsInput{Words: make([]store.DeleteWordsItem, 101)}
	for i := range 101 {
		tooMany.Words[i] = sdkDeleteItem(word, fmt.Sprintf("sdk-delete-excess-%03d", i))
	}
	for name, input := range map[string]any{
		"empty":                map[string]any{"words": []any{}},
		"missing words":        map[string]any{},
		"null words":           map[string]any{"words": nil},
		"101 words":            tooMany,
		"wrong envelope type":  map[string]any{"words": "invalid"},
		"unknown owner":        map[string]any{"words": []any{valid}, "owner": "another-owner"},
		"unknown item owner":   map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": 1, "idempotency_key": "sdk-delete-schema-owner", "owner": "another-owner"}}},
		"unknown item field":   map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": 1, "idempotency_key": "sdk-delete-schema-field", "term": word.Term}}},
		"wrong item type":      map[string]any{"words": []any{valid, "invalid"}},
		"null item":            map[string]any{"words": []any{valid, nil}},
		"missing id":           map[string]any{"words": []any{valid, map[string]any{"expected_revision": 1, "idempotency_key": "sdk-delete-schema-no-id"}}},
		"missing revision":     map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "idempotency_key": "sdk-delete-schema-no-revision"}}},
		"missing retry key":    map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": 1}}},
		"wrong id type":        map[string]any{"words": []any{valid, map[string]any{"id": 1, "expected_revision": 1, "idempotency_key": "sdk-delete-schema-id-type"}}},
		"wrong revision type":  map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": "1", "idempotency_key": "sdk-delete-schema-revision-type"}}},
		"fractional revision":  map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": 1.5, "idempotency_key": "sdk-delete-schema-fraction"}}},
		"null revision":        map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": nil, "idempotency_key": "sdk-delete-schema-null-revision"}}},
		"wrong retry key type": map[string]any{"words": []any{valid, map[string]any{"id": word.ID, "expected_revision": 1, "idempotency_key": 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "delete_words", Arguments: input})
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("invalid delete envelope/schema accepted=%+v %v", result, err)
			}
			if current := callTool[store.Word](t, session, "get_word", map[string]any{"id": word.ID}); !reflect.DeepEqual(current, word) {
				t.Fatal("malformed request deleted its valid prefix")
			}
		})
	}
}

func TestOfficialMCPDeleteScopeOwnershipAndRequestLimit(t *testing.T) {
	f := newFixture(t, false)
	owned := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	readOnly := sdkSession(t, f, "test-subject", store.Read)
	word := callTool[store.Word](t, owned, "add_word", store.AddInput{Capture: store.Capture{Term: "protected synthetic deletion"}, IdempotencyKey: "sdk-delete-protected-capture"})
	in := store.DeleteWordsInput{Words: []store.DeleteWordsItem{sdkDeleteItem(word, "sdk-delete-protected-intent")}}
	denied, err := readOnly.CallTool(t.Context(), &sdk.CallToolParams{Name: "delete_words", Arguments: in})
	if err != nil || denied == nil || !denied.IsError {
		t.Fatal("read-only deletion accepted")
	}
	// Keep a valid first item to prove the byte cap rejects the whole request.
	large := store.DeleteWordsInput{Words: make([]store.DeleteWordsItem, 80)}
	for i := range 80 {
		large.Words[i] = store.DeleteWordsItem{ID: strings.Repeat("x", 4000), ExpectedRevision: 1, IdempotencyKey: fmt.Sprintf("sdk-delete-large-%03d", i)}
	}
	large.Words[0] = in.Words[0]
	denied, err = owned.CallTool(t.Context(), &sdk.CallToolParams{Name: "delete_words", Arguments: large})
	if err == nil && (denied == nil || !denied.IsError) {
		t.Fatal("MCP accepted a deletion request over 256 KiB")
	}
	// The official SDK closes its session after HTTP 413; reconnect to read.
	owned = sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	if current := callTool[store.Word](t, owned, "get_word", map[string]any{"id": word.ID}); !reflect.DeepEqual(current, word) {
		t.Fatal("forbidden or oversized request deleted a word")
	}
	if _, err := f.s.Enroll(t.Context(), f.provider.URL(), "other-test-subject", "Other synthetic owner"); err != nil {
		t.Fatal(err)
	}
	other := sdkSession(t, f, "other-test-subject", store.Read+" "+store.Write)
	if out := callTool[mcptools.DeleteWordsOutput](t, other, "delete_words", in); out.Results[0].Success || out.Results[0].Code != "not_found" || out.Results[0].DeletedWord != nil {
		t.Fatal("another owner deleted or read an unowned word")
	}
	removed := callTool[mcptools.DeleteWordsOutput](t, owned, "delete_words", in)
	if !removed.Results[0].Success || !reflect.DeepEqual(removed.Results[0].DeletedWord, &word) {
		t.Fatal("authorized deletion failed")
	}
	if out := callTool[mcptools.DeleteWordsOutput](t, other, "delete_words", in); out.Results[0].Success || out.Results[0].Code != "not_found" || out.Results[0].DeletedWord != nil {
		t.Fatal("successful retry snapshot crossed owners")
	}
	otherWord := callTool[store.Word](t, other, "add_word", store.AddInput{Capture: store.Capture{Term: word.Term}, IdempotencyKey: "sdk-delete-protected-capture"})
	otherDeletion := store.DeleteWordsInput{Words: []store.DeleteWordsItem{sdkDeleteItem(otherWord, in.Words[0].IdempotencyKey)}}
	if out := callTool[mcptools.DeleteWordsOutput](t, other, "delete_words", otherDeletion); !out.Results[0].Success || !reflect.DeepEqual(out.Results[0].DeletedWord, &otherWord) {
		t.Fatal("retry keys were not scoped to the verified owner")
	}
}

func TestOfficialMCPDeleteResponseLossAndRecreationKeepNewIDsSafe(t *testing.T) {
	f := newFixture(t, false)
	owned := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	seed := store.AddWordsInput{Words: []store.AddWordsItem{
		sdkBatchItem("synthetic lost delete complete", "sdk-lost-delete-capture-001", syntheticSenses()),
		sdkBatchItem("synthetic lost delete draft", "sdk-lost-delete-capture-002", nil),
	}}
	created := callTool[mcptools.AddWordsOutput](t, owned, "add_words", seed)
	in := store.DeleteWordsInput{}
	for i, result := range created.Results {
		if !result.Success || result.Word == nil {
			t.Fatalf("seed item %d=%+v", i, result)
		}
		in.Words = append(in.Words, sdkDeleteItem(*result.Word, fmt.Sprintf("sdk-lost-delete-intent-%03d", i)))
	}
	token := f.provider.Access("test-subject", "vocab-test", f.origin+"/mcp", store.Read+" "+store.Write, 300)
	transport := &loseBatchResponseTransport{base: bearerTransport{token: token}, tool: "delete_words"}
	client := sdk.NewClient(&sdk.Implementation{Name: "vocabbank-delete-response-loss", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: f.origin + "/mcp", HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	transport.lose.Store(true)
	if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "delete_words", Arguments: in}); err == nil {
		t.Fatal("synthetic response loss was not observed")
	}
	fresh := sdkSession(t, f, "test-subject", store.Read+" "+store.Write)
	if page := callTool[store.Page](t, fresh, "list_words", store.Filter{}); page.Total != 0 {
		t.Fatal("lost response did not commit both deletions")
	}
	recreate := store.AddWordsInput{Words: []store.AddWordsItem{
		sdkBatchItem(seed.Words[0].Term, "sdk-delete-recreated-001", syntheticSenses()),
		sdkBatchItem(seed.Words[1].Term, "sdk-delete-recreated-002", syntheticSenses()),
	}}
	recreated := callTool[mcptools.AddWordsOutput](t, fresh, "add_words", recreate)
	replay := callTool[mcptools.DeleteWordsOutput](t, fresh, "delete_words", store.DeleteWordsInput{Words: []store.DeleteWordsItem{in.Words[1], in.Words[0]}})
	for i, index := range []int{1, 0} {
		if !replay.Results[i].Success || !reflect.DeepEqual(replay.Results[i].DeletedWord, created.Results[index].Word) {
			t.Fatalf("lost delete retry changed original snapshot %d", index)
		}
	}
	oldAdds := callTool[mcptools.AddWordsOutput](t, fresh, "add_words", seed)
	for i, result := range recreated.Results {
		if !result.Success || result.Word == nil || result.Word.ID == in.Words[i].ID || result.Word.Revision != 1 || result.Word.Status != store.Complete {
			t.Fatalf("recreated item %d=%+v", i, result)
		}
		if !oldAdds.Results[i].Success || !reflect.DeepEqual(oldAdds.Results[i].Word, created.Results[i].Word) {
			t.Fatal("deletion changed a retained creation snapshot")
		}
		if current := callTool[store.Word](t, fresh, "get_word", map[string]any{"id": result.Word.ID}); !reflect.DeepEqual(current, *result.Word) {
			t.Fatal("old delete retry affected a recreated word")
		}
		missing, err := fresh.CallTool(t.Context(), &sdk.CallToolParams{Name: "get_word", Arguments: map[string]any{"id": in.Words[i].ID}})
		if err != nil || missing == nil || !missing.IsError {
			t.Fatal("old creation retry resurrected the deleted ID")
		}
	}
	if page := callTool[store.Page](t, fresh, "list_words", store.Filter{}); page.Total != 2 {
		t.Fatal("response-loss retries changed the recreated bank")
	}
}
