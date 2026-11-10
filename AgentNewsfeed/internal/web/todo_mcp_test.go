package web_test

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
	"agentnewsfeed/internal/testfixture"
	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var todoToolNames = []string{"list_todos", "get_todo", "add_todo", "set_todo_completed", "remove_todo", "undo_todo_removal"}

func todoMCPApp(t *testing.T, mode string) (*testfixture.Identity, http.Handler, func(string, string) string) {
	t.Helper()
	if mode == "gateway" {
		f, _, h, token := gatewayApp(t)
		return f, h, token
	}
	f, _, h := app(t)
	return f, h, func(subject, scopes string) string {
		return f.Bearer(subject, "newsfeed-codex", f.Manager.MCPAudience(), scopes)
	}
}

func todoSchemas(t *testing.T, c *sdk.ClientSession) map[string]*jsonschema.Resolved {
	t.Helper()
	init := c.InitializeResult()
	if init == nil || init.ServerInfo.Version != "0.3.0" || !strings.Contains(init.Instructions, "To-do") ||
		!strings.Contains(init.Instructions, "untrusted data") || !strings.Contains(init.Instructions, "historical snapshot") {
		t.Fatal("initialize omitted To-do version, trust or retry contract")
	}
	tools, err := c.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 14 {
		t.Fatalf("expected 14 tools: %v", err)
	}
	out := make(map[string]*jsonschema.Resolved)
	for _, tool := range tools.Tools {
		if !slices.Contains(todoToolNames, tool.Name) {
			continue
		}
		read := tool.Name == "list_todos" || tool.Name == "get_todo"
		a := tool.Annotations
		if a == nil || a.ReadOnlyHint != read || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint == read || a.OpenWorldHint == nil || *a.OpenWorldHint || tool.OutputSchema == nil {
			t.Fatalf("incorrect To-do annotations or missing output schema: %s", tool.Name)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil || wire["additionalProperties"] != false {
			t.Fatalf("%s must reject additionalProperties: %s", tool.Name, raw)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = resolved
	}
	for _, name := range todoToolNames {
		if out[name] == nil {
			t.Fatalf("missing tool %s", name)
		}
	}
	list := out["list_todos"].Schema()
	if *list.Properties["limit"].Minimum != 1 || *list.Properties["limit"].Maximum != 50 ||
		string(list.Properties["limit"].Default) != "20" || *list.Properties["cursor"].MaxLength != feed.MaxTodoCursorBytes ||
		!reflect.DeepEqual(list.Properties["status"].Enum, []any{"open", "completed", "all"}) {
		t.Fatal("discovery omitted list bounds or status enum")
	}
	add := out["add_todo"].Schema()
	estimates, err := json.Marshal(add.Properties["estimate_minutes"].Enum)
	if err != nil {
		t.Fatal(err)
	}
	if *add.Properties["title"].MaxLength != 180 || *add.Properties["tags"].MaxLength != 125 ||
		string(estimates) != "[0,15,30,45,60,90,120,180,240,360,480]" || !slices.Contains(out["set_todo_completed"].Schema().Required, "completed") {
		t.Fatal("discovery omitted task bounds, estimate enum or explicit completion")
	}
	return out
}

func taskFromChecklist(t *testing.T, list *sqlite.TodoList, id string) sqlite.TodoTask {
	t.Helper()
	for _, task := range list.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %s missing from checklist", id)
	return sqlite.TodoTask{}
}

func requireToolError(t *testing.T, result *sdk.CallToolResult) {
	t.Helper()
	if !result.IsError || result.StructuredContent != nil {
		t.Fatalf("expected safe tool failure, got %#v", result)
	}
}

func TestOfficialTodoMCPSharedLifecycleDirectAndGateway(t *testing.T) {
	for _, mode := range []string{"direct", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			f, h, token := todoMCPApp(t, mode)
			server := httptest.NewServer(h)
			defer server.Close()
			alice := client(t, server.URL, token("alice", allScopes))
			bob := client(t, server.URL, token("bob", allScopes))
			readOnly := client(t, server.URL, token("alice", identity.Read))
			writeOnly := client(t, server.URL, token("alice", identity.ArticlesWrite))
			todoSchemas(t, alice)
			cookie := f.CookieWithScopes(t, "alice", allScopes)
			_, csrf := f.BrowserPrincipal(t, cookie)
			post := func(fields url.Values) {
				t.Helper()
				out := browserJSONRequest(h, "/api/v1/todos", cookie, fields, testfixture.Origin)
				if out.Code != 200 {
					t.Fatalf("browser %s failed: %d %s", fields.Get("action"), out.Code, out.Body.String())
				}
			}
			get := func(id string, removed bool) sqlite.TodoTask {
				t.Helper()
				return decode[sqlite.TodoTask](t, call(t, alice, "get_todo", feed.GetTodoInput{TaskID: id, Removed: removed}))
			}
			post(todoAdd(csrf, "browser-task", "  Read the paper  ", "Research, research, Deep  Reading", "120"))
			browser := get("browser-task", false)
			if browser.Revision != 1 || browser.Title != "Read the paper" || !slices.Equal(browser.Tags, []string{"Research", "Deep Reading"}) || browser.EstimateMinutes != 120 {
				t.Fatal("MCP did not see normalized browser creation")
			}
			add := feed.AddTodoInput{IdempotencyKey: "agent-add", Title: "  Prepare notes  ", Tags: "Work, work, Deep  Reading", EstimateMinutes: 120}
			first := decode[sqlite.TodoMutationResult](t, call(t, alice, "add_todo", add))
			if first.Removed || first.Task.ID == "" || first.Task.Revision != 1 || first.Task.Title != "Prepare notes" ||
				!slices.Equal(first.Task.Tags, []string{"Work", "Deep Reading"}) || first.Task.EstimateMinutes != 120 {
				t.Fatal("MCP add lost shared normalization or state")
			}
			separate := add
			separate.IdempotencyKey = "another-add"
			second := decode[sqlite.TodoMutationResult](t, call(t, alice, "add_todo", separate))
			if second.Task.ID == first.Task.ID {
				t.Fatal("distinct intent keys did not create distinct server IDs")
			}
			if task := taskFromChecklist(t, readTodo(t, h, cookie).Todo, first.Task.ID); !reflect.DeepEqual(task, first.Task) {
				t.Fatal("browser did not see MCP creation")
			}
			complete := feed.SetTodoCompletedInput{IdempotencyKey: "complete", TaskID: first.Task.ID, ExpectedRevision: 1, Completed: true}
			completed := decode[sqlite.TodoMutationResult](t, call(t, alice, "set_todo_completed", complete))
			if completed.Task.Revision != 2 || completed.Task.CompletedAt == 0 || taskFromChecklist(t, readTodo(t, h, cookie).Todo, first.Task.ID).CompletedAt != completed.Task.CompletedAt {
				t.Fatal("MCP completion was not visible in browser history")
			}
			reopen := feed.SetTodoCompletedInput{IdempotencyKey: "reopen", TaskID: first.Task.ID, ExpectedRevision: 2, Completed: false}
			reopened := decode[sqlite.TodoMutationResult](t, call(t, alice, "set_todo_completed", reopen))
			if reopened.Task.Revision != 3 || reopened.Task.CompletedAt != 0 || taskFromChecklist(t, readTodo(t, h, cookie).Todo, first.Task.ID).CompletedAt != 0 {
				t.Fatal("explicit completed=false did not reopen in the browser")
			}
			post(todoAction(csrf, first.Task.ID, "complete", reopened.Task.Revision))
			current := get(first.Task.ID, false)
			noop := feed.SetTodoCompletedInput{IdempotencyKey: "complete-noop", TaskID: current.ID, ExpectedRevision: current.Revision, Completed: true}
			unchanged := decode[sqlite.TodoMutationResult](t, call(t, alice, "set_todo_completed", noop))
			if !reflect.DeepEqual(unchanged.Task, current) {
				t.Fatal("no-op completion changed revision or completion time")
			}
			remove := feed.TodoStateInput{IdempotencyKey: "remove", TaskID: current.ID, ExpectedRevision: current.Revision}
			removed := decode[sqlite.TodoMutationResult](t, call(t, alice, "remove_todo", remove))
			if !removed.Removed || removed.Task.Revision != current.Revision+1 || removed.Task.CompletedAt != current.CompletedAt {
				t.Fatal("remove lost completion or revision")
			}
			if list := readTodo(t, h, cookie).Todo; list.Undo == nil || !reflect.DeepEqual(*list.Undo, removed.Task) || len(list.Tasks) != 2 {
				t.Fatal("browser did not see MCP removal and latest Undo")
			}
			requireToolError(t, call(t, alice, "get_todo", feed.GetTodoInput{TaskID: current.ID}))
			if !reflect.DeepEqual(get(current.ID, true), removed.Task) {
				t.Fatal("explicit removed read did not return latest Undo")
			}
			for _, status := range []string{"open", "completed", "all"} {
				page := decode[feed.TodosPage](t, call(t, readOnly, "list_todos", feed.ListTodosInput{Status: status, Limit: 1}))
				if page.Undo == nil || page.Undo.ID != current.ID || len(page.Tasks) > 1 || (status == "completed" && len(page.Tasks) != 0) {
					t.Fatal("list status or independent Undo/limit contract changed")
				}
			}
			post(todoAction(csrf, current.ID, "undo", removed.Task.Revision))
			current = get(current.ID, false)
			post(todoAction(csrf, browser.ID, "remove", browser.Revision))
			browserRemoval := get(browser.ID, true)
			undo := feed.TodoStateInput{IdempotencyKey: "undo-browser-removal", TaskID: browser.ID, ExpectedRevision: browserRemoval.Revision}
			undone := decode[sqlite.TodoMutationResult](t, call(t, alice, "undo_todo_removal", undo))
			if undone.Removed || undone.Task.Revision != browserRemoval.Revision+1 || readTodo(t, h, cookie).Todo.Undo != nil ||
				!reflect.DeepEqual(taskFromChecklist(t, readTodo(t, h, cookie).Todo, browser.ID), undone.Task) {
				t.Fatal("MCP did not Undo the browser's latest removal")
			}
			post(todoAction(csrf, second.Task.ID, "remove", get(second.Task.ID, false).Revision))
			latest := get(second.Task.ID, true)
			beforeRetries := readTodo(t, h, cookie).Todo
			for _, retry := range []struct {
				name string
				in   any
				want sqlite.TodoMutationResult
			}{{"add_todo", add, first}, {"set_todo_completed", complete, completed}, {"set_todo_completed", reopen, reopened}, {"set_todo_completed", noop, unchanged}, {"remove_todo", remove, removed}, {"undo_todo_removal", undo, undone}} {
				for _, limited := range []*sdk.ClientSession{readOnly, writeOnly} {
					requireToolError(t, call(t, limited, retry.name, retry.in))
				}
				if retry.name != "add_todo" {
					requireToolError(t, call(t, bob, retry.name, retry.in))
				}
				if got := decode[sqlite.TodoMutationResult](t, call(t, alice, retry.name, retry.in)); !reflect.DeepEqual(got, retry.want) {
					t.Fatalf("%s did not replay its exact historical snapshot", retry.name)
				}
			}
			if !reflect.DeepEqual(readTodo(t, h, cookie).Todo, beforeRetries) || !reflect.DeepEqual(get(second.Task.ID, true), latest) || !reflect.DeepEqual(get(first.Task.ID, false), current) {
				t.Fatal("historical retries reapplied effects or displaced a later shared Undo")
			}
			for _, in := range []feed.GetTodoInput{{TaskID: first.Task.ID}, {TaskID: second.Task.ID, Removed: true}, {TaskID: "missing"}} {
				requireToolError(t, call(t, bob, "get_todo", in))
				requireToolError(t, call(t, writeOnly, "get_todo", in))
			}
			if page := decode[feed.TodosPage](t, call(t, bob, "list_todos", feed.ListTodosInput{Status: "all"})); len(page.Tasks) != 0 || page.Undo != nil {
				t.Fatal("MCP list crossed owner")
			}
			requireToolError(t, call(t, writeOnly, "list_todos", feed.ListTodosInput{}))
			stale := feed.TodoStateInput{IdempotencyKey: "stale", TaskID: current.ID, ExpectedRevision: 1}
			requireToolError(t, call(t, alice, "remove_todo", stale))
			conflict := add
			conflict.Title = "Different intent"
			requireToolError(t, call(t, alice, "add_todo", conflict))
			if !reflect.DeepEqual(readTodo(t, h, cookie).Todo, beforeRetries) {
				t.Fatal("stale revision or key conflict changed shared state")
			}
			stale.ExpectedRevision = get(current.ID, false).Revision
			if retried := decode[sqlite.TodoMutationResult](t, call(t, alice, "remove_todo", stale)); !retried.Removed {
				t.Fatal("failed revision reserved its key")
			}
			for _, in := range []feed.GetTodoInput{{TaskID: second.Task.ID}, {TaskID: second.Task.ID, Removed: true}} {
				requireToolError(t, call(t, alice, "get_todo", in))
			}
			requireToolError(t, call(t, alice, "undo_todo_removal", feed.TodoStateInput{IdempotencyKey: "superseded-undo", TaskID: second.Task.ID, ExpectedRevision: latest.Revision}))
			// The same SDK session sends each call through authentication again.
			p := f.Principal(t, "alice", allScopes)
			if err := f.Store.DisableAccount(t.Context(), p.Identity()); err != nil {
				t.Fatal(err)
			}
			for _, name := range todoToolNames {
				if result, err := alice.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{}}); err == nil || result != nil {
					t.Fatalf("disabled enrollment reached %s", name)
				}
			}
			for _, subject := range []string{"alice", "unregistered"} {
				if response := request(h, "POST", "/mcp", nil, token(subject, allScopes)); response.Code != 401 {
					t.Fatalf("inactive owner authorized MCP: %d", response.Code)
				}
			}
		})
	}
}

// Snapshot both task rows and durable results to detect effects hidden from a
// visible list, such as a reserved failed key or a displaced pending removal.
func todoStorageSnapshot(t *testing.T, path string) string {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var snapshot [][][]any
	for _, query := range []string{"SELECT * FROM todo_tasks ORDER BY owner_id,id", "SELECT * FROM todo_idempotency_results ORDER BY owner_id,key"} {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range columns {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			table = append(table, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, table)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func rawTodoToolReply(t *testing.T, server *httptest.Server, token, name, arguments string) (int, []byte) {
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
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func rawTodoToolCall(t *testing.T, server *httptest.Server, token, name, arguments string) *sdk.CallToolResult {
	t.Helper()
	status, raw := rawTodoToolReply(t, server, token, name, arguments)
	var envelope struct {
		Result *sdk.CallToolResult `json:"result"`
		Error  json.RawMessage     `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || status != 200 || envelope.Error != nil || envelope.Result == nil {
		t.Fatalf("expected tool result: HTTP %d, %v, protocol error %s", status, err, envelope.Error)
	}
	return envelope.Result
}

func TestTodoMCPStrictSchemasAndRejectedArgumentsHaveNoEffects(t *testing.T) {
	for _, mode := range []string{"direct", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			f, h, token := todoMCPApp(t, mode)
			server := httptest.NewServer(h)
			defer server.Close()
			bearer := token("alice", allScopes)
			alice := client(t, server.URL, bearer)
			schemas := todoSchemas(t, alice)
			first := decode[sqlite.TodoMutationResult](t, call(t, alice, "add_todo", feed.AddTodoInput{IdempotencyKey: "fixture", Title: "Keep this task"}))
			second := decode[sqlite.TodoMutationResult](t, call(t, alice, "add_todo", feed.AddTodoInput{IdempotencyKey: "undo-fixture", Title: "Keep this Undo"}))
			removed := decode[sqlite.TodoMutationResult](t, call(t, alice, "remove_todo", feed.TodoStateInput{IdempotencyKey: "fixture-remove", TaskID: second.Task.ID, ExpectedRevision: 1}))
			original := todoStorageSnapshot(t, f.Path)
			valid := map[string]map[string]any{
				"list_todos":         {"limit": 20, "status": "open", "cursor": ""},
				"get_todo":           {"task_id": first.Task.ID, "removed": false},
				"add_todo":           {"idempotency_key": "rejected", "title": "New task", "tags": "Work", "estimate_minutes": 120},
				"set_todo_completed": {"idempotency_key": "rejected", "task_id": first.Task.ID, "expected_revision": 1, "completed": true},
				"remove_todo":        {"idempotency_key": "rejected", "task_id": first.Task.ID, "expected_revision": 1},
				"undo_todo_removal":  {"idempotency_key": "rejected", "task_id": removed.Task.ID, "expected_revision": removed.Task.Revision},
			}
			check := func(name string, args map[string]any, schemaInvalid bool) {
				t.Helper()
				if schemaInvalid && schemas[name].Validate(args) == nil {
					t.Fatalf("discovered %s schema accepted invalid input: %#v", name, args)
				}
				requireToolError(t, call(t, alice, name, args))
				if actual := todoStorageSnapshot(t, f.Path); actual != original {
					t.Fatalf("rejected %s arguments changed tasks or retry records: %#v", name, args)
				}
			}
			for _, name := range todoToolNames {
				args := valid[name]
				if err := schemas[name].Validate(args); err != nil {
					t.Fatalf("valid %s shape rejected: %v", name, err)
				}
				unknown := maps.Clone(args)
				unknown["owner_id"] = "another-owner"
				check(name, unknown, true)
				for field, value := range args {
					bad := maps.Clone(args)
					bad[field] = nil
					check(name, bad, true)
					if _, stringField := value.(string); stringField {
						bad[field] = 1
					} else {
						bad[field] = "wrong type"
					}
					check(name, bad, true)
				}
				for _, field := range schemas[name].Schema().Required {
					missing := maps.Clone(args)
					delete(missing, field)
					check(name, missing, true)
				}
				for _, raw := range []string{"null", "[]", `"wrong root"`, "42"} {
					requireToolError(t, rawTodoToolCall(t, server, bearer, name, raw))
				}
				// Even equal duplicates and escaped-equivalent names are invalid
				// in the original JSON, before the SDK can collapse them.
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				field := map[string]string{"list_todos": "limit", "get_todo": "task_id", "add_todo": "title", "set_todo_completed": "completed", "remove_todo": "expected_revision", "undo_todo_removal": "expected_revision"}[name]
				value, err := json.Marshal(args[field])
				if err != nil {
					t.Fatal(err)
				}
				escaped := fmt.Sprintf(`"\u%04x%s"`, field[0], field[1:])
				ambiguous := strings.TrimSuffix(string(raw), "}") + "," + escaped + ":" + string(value) + "}"
				requireToolError(t, rawTodoToolCall(t, server, bearer, name, ambiguous))
				if todoStorageSnapshot(t, f.Path) != original {
					t.Fatalf("raw rejected %s arguments changed persistence", name)
				}
			}
			for _, bad := range []struct {
				name, field string
				value       any
			}{
				{"list_todos", "limit", 0}, {"list_todos", "limit", -1}, {"list_todos", "limit", 51}, {"list_todos", "limit", 1.5},
				{"list_todos", "status", ""}, {"list_todos", "status", "archived"}, {"list_todos", "cursor", strings.Repeat("a", feed.MaxTodoCursorBytes+1)},
				{"add_todo", "title", ""}, {"add_todo", "title", strings.Repeat("é", 181)}, {"add_todo", "title", strings.Repeat("é", 180) + " "},
				{"add_todo", "tags", strings.Repeat("é", 126)}, {"add_todo", "estimate_minutes", 31}, {"add_todo", "estimate_minutes", 1.5},
				{"add_todo", "idempotency_key", ""}, {"add_todo", "idempotency_key", strings.Repeat("k", 129)},
				{"get_todo", "task_id", ""}, {"get_todo", "task_id", strings.Repeat("i", 65)},
				{"set_todo_completed", "expected_revision", 0}, {"remove_todo", "expected_revision", 1.5}, {"undo_todo_removal", "expected_revision", -1},
			} {
				args := maps.Clone(valid[bad.name])
				args[bad.field] = bad.value
				check(bad.name, args, true)
			}
			for _, bad := range []struct{ name, field, value string }{
				{"add_todo", "title", "   "}, {"add_todo", "title", "Control\ntext"}, {"add_todo", "tags", "Control\ntext"},
				{"add_todo", "idempotency_key", " key "}, {"get_todo", "task_id", " id "}, {"list_todos", "cursor", "not-a-cursor"},
			} {
				args := maps.Clone(valid[bad.name])
				args[bad.field] = bad.value
				check(bad.name, args, false)
			}
			for _, args := range []string{
				`{"idempotency_key":"rejected","title":"First","title":"Second"}`,
				fmt.Sprintf(`{"idempotency_key":"rejected","task_id":%q,"expected_revision":1,"completed":true,"completed":false}`, first.Task.ID),
				`{"limit":1,"lim\u0069t":2}`, `{"status":"open","status":"all"}`,
			} {
				name := "list_todos"
				if strings.Contains(args, `"title"`) {
					name = "add_todo"
				} else if strings.Contains(args, `"completed"`) {
					name = "set_todo_completed"
				}
				requireToolError(t, rawTodoToolCall(t, server, bearer, name, args))
			}
			for _, malformed := range []string{`{"idempotency_key":"rejected","title":}`, `{"idempotency_key":"rejected","title":"Changed"} {}`, `{"idempotency_key":"rejected","title":"unterminated}`} {
				status, raw := rawTodoToolReply(t, server, bearer, "add_todo", malformed)
				var reply struct {
					Error json.RawMessage `json:"error"`
				}
				if status != http.StatusBadRequest && (status != http.StatusOK || json.Unmarshal(raw, &reply) != nil || len(reply.Error) == 0) {
					t.Fatalf("malformed JSON was not rejected: HTTP %d %s", status, raw)
				}
			}
			requireToolError(t, rawTodoToolCall(t, server, bearer, "add_todo", "{\"idempotency_key\":\"rejected\",\"title\":\"bad"+string([]byte{0xff})+"\"}"))
			if todoStorageSnapshot(t, f.Path) != original {
				t.Fatal("invalid inputs changed a task or reserved a retry key")
			}
			// Schema bounds count supplied runes; storage then applies the same
			// trim/tag normalization as the browser without a smaller tag cap.
			boundary := feed.AddTodoInput{IdempotencyKey: "boundary", Title: " " + strings.Repeat("é", 178) + " ", Tags: strings.Repeat("é", 125), EstimateMinutes: 480}
			out := decode[sqlite.TodoMutationResult](t, call(t, alice, "add_todo", boundary))
			if out.Task.Title != strings.Repeat("é", 178) || !slices.Equal(out.Task.Tags, []string{boundary.Tags}) || out.Task.EstimateMinutes != 480 {
				t.Fatal("exact input boundary or shared normalization changed")
			}
			boundary.IdempotencyKey, boundary.Title, boundary.Tags, boundary.EstimateMinutes = "full-title-boundary", strings.Repeat("é", 180), "", 0
			if out := decode[sqlite.TodoMutationResult](t, call(t, alice, "add_todo", boundary)); out.Task.Title != boundary.Title || out.Task.EstimateMinutes != 0 {
				t.Fatal("180-rune title or explicit zero estimate was rejected")
			}
		})
	}
}

func TestTodoMCPPaginationDefaultsAndCursorIsolation(t *testing.T) {
	for _, mode := range []string{"direct", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			f, h, token := todoMCPApp(t, mode)
			server := httptest.NewServer(h)
			defer server.Close()
			alice := client(t, server.URL, token("alice", allScopes))
			bob := client(t, server.URL, token("bob", allScopes))
			cookie := f.CookieWithScopes(t, "alice", allScopes)
			_, csrf := f.BrowserPrincipal(t, cookie)
			for i := range 23 {
				out := browserJSONRequest(h, "/api/v1/todos", cookie, todoAdd(csrf, fmt.Sprintf("browser-%02d", i), fmt.Sprintf("Task %d", i), "", ""), testfixture.Origin)
				if out.Code != 200 {
					t.Fatal("could not prepare browser tasks")
				}
			}
			page := decode[feed.TodosPage](t, call(t, alice, "list_todos", map[string]any{}))
			if len(page.Tasks) != 20 || page.NextCursor == "" || page.Undo != nil {
				t.Fatal("omitted list limit/status did not default to 20 open tasks")
			}
			last := decode[feed.TodosPage](t, call(t, alice, "list_todos", feed.ListTodosInput{Cursor: page.NextCursor}))
			if len(last.Tasks) != 3 || last.NextCursor != "" {
				t.Fatal("default page cursor did not reach the remaining tasks")
			}
			seen := map[string]bool{}
			for _, task := range append(page.Tasks, last.Tasks...) {
				if seen[task.ID] {
					t.Fatal("pagination repeated a task")
				}
				seen[task.ID] = true
			}
			requireToolError(t, call(t, bob, "list_todos", feed.ListTodosInput{Cursor: page.NextCursor}))
			requireToolError(t, call(t, alice, "list_todos", feed.ListTodosInput{Cursor: page.NextCursor, Status: "completed"}))
			requireToolError(t, call(t, alice, "list_articles", feed.ListInput{Cursor: page.NextCursor}))
			raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
			if err != nil {
				t.Fatal(err)
			}
			var cursor map[string]any
			if err := json.Unmarshal(raw, &cursor); err != nil {
				t.Fatal(err)
			}
			for _, change := range []struct {
				field string
				value any
			}{{"collection", "articles"}, {"owner", "another-owner"}, {"status", "all"}, {"before_created_at", 0}, {"before_task_id", ""}, {"unknown", true}} {
				bad := maps.Clone(cursor)
				bad[change.field] = change.value
				payload, err := json.Marshal(bad)
				if err != nil {
					t.Fatal(err)
				}
				requireToolError(t, call(t, alice, "list_todos", feed.ListTodosInput{Cursor: base64.RawURLEncoding.EncodeToString(payload)}))
			}
			post := browserJSONRequest(h, "/api/v1/todos", cookie, todoAction(csrf, page.Tasks[0].ID, "complete", page.Tasks[0].Revision), testfixture.Origin)
			if post.Code != 200 {
				t.Fatal("could not complete browser task")
			}
			if completed := decode[feed.TodosPage](t, call(t, alice, "list_todos", feed.ListTodosInput{Status: "completed"})); len(completed.Tasks) != 1 || completed.Tasks[0].CompletedAt == 0 {
				t.Fatal("completed view did not see browser history")
			}
			if all := decode[feed.TodosPage](t, call(t, alice, "list_todos", feed.ListTodosInput{Status: "all", Limit: 50})); len(all.Tasks) != 23 {
				t.Fatal("all view omitted open or completed tasks")
			}
		})
	}
}
