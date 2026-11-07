package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/testfixture"
)

type todoShell struct {
	Authenticated bool             `json:"authenticated"`
	CanWrite      bool             `json:"can_write"`
	Section       string           `json:"section"`
	SignInURL     string           `json:"sign_in_url"`
	Todo          *sqlite.TodoList `json:"todo"`
}

func readTodo(t *testing.T, h http.Handler, cookie *http.Cookie) todoShell {
	t.Helper()
	data := readBrowserJSON[todoShell](t, browserJSONRequest(h, "/todo", cookie, nil, ""))
	if data.Section != "todo" || data.Authenticated != (cookie != nil) || (cookie != nil && data.Todo == nil) {
		t.Fatalf("unexpected To-do browser response: %#v", data)
	}
	return data
}

func todoAdd(csrf, id, title, tags, minutes string) url.Values {
	return url.Values{"csrf_token": {csrf}, "action": {"add"}, "task_id": {id},
		"title": {title}, "tags": {tags}, "estimate_minutes": {minutes}}
}

func todoAction(csrf, id, action string, revision int64) url.Values {
	return url.Values{"csrf_token": {csrf}, "action": {action}, "task_id": {id},
		"expected_revision": {strconv.FormatInt(revision, 10)}}
}

func TestTodoLoginReturnsToPrivateChecklist(t *testing.T) {
	_, _, h := app(t)
	data := readTodo(t, h, nil)
	native := request(h, "GET", "/todo", nil, "")
	if data.SignInURL != "/auth/login?return_to=%2Ftodo" || data.Todo != nil || data.CanWrite || native.Code != 200 ||
		!strings.Contains(native.Body.String(), data.SignInURL) {
		t.Fatal("direct anonymous checklist did not preserve its private sign-in return")
	}
	start := request(h, "GET", data.SignInURL, nil, "")
	authURL, err := url.Parse(start.Header().Get("Location"))
	if err != nil || start.Code != http.StatusFound || len(start.Result().Cookies()) != 1 {
		t.Fatalf("could not begin sign-in: %d %v", start.Code, err)
	}
	query := authURL.Query()
	query.Set("test_subject", "alice")
	query.Set("test_scopes", allScopes)
	authURL.RawQuery = query.Encode()
	provider := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := http.NewRequestWithContext(t.Context(), "GET", authURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	signedIn, err := provider.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer signedIn.Body.Close()
	callback, err := url.Parse(signedIn.Header.Get("Location"))
	if err != nil || signedIn.StatusCode != http.StatusFound || callback.Path != "/auth/callback" {
		t.Fatalf("test identity sign-in did not redirect to callback: %d %v", signedIn.StatusCode, err)
	}
	finish := request(h, "GET", callback.RequestURI(), start.Result().Cookies()[0], "")
	if finish.Code != http.StatusSeeOther || finish.Header().Get("Location") != "/todo" {
		t.Fatalf("sign-in did not return to /todo: %d %q", finish.Code, finish.Header().Get("Location"))
	}
	for _, cookie := range finish.Result().Cookies() {
		if cookie.Name == "__Host-newsfeed-session" {
			if page := readTodo(t, h, cookie); !page.CanWrite || len(page.Todo.Tasks) != 0 {
				t.Fatal("signed-in checklist did not load its empty owner data")
			}
			return
		}
	}
	t.Fatal("callback did not set a session")
}

func TestTodoFormsRequireOwnerScopeAndCSRF(t *testing.T) {
	f, _, h := app(t)
	alice := f.CookieWithScopes(t, "alice", allScopes)
	bob := f.CookieWithScopes(t, "bob", allScopes)
	readOnly := f.Cookie(t, "alice")
	_, csrf := f.BrowserPrincipal(t, alice)
	_, bobCSRF := f.BrowserPrincipal(t, bob)
	_, readCSRF := f.BrowserPrincipal(t, readOnly)
	for _, tc := range []struct {
		name         string
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{
		{"anonymous", nil, csrf, testfixture.Origin, 401},
		{"read-only", readOnly, readCSRF, testfixture.Origin, 403},
		{"another session token", bob, csrf, testfixture.Origin, 403},
		{"missing origin", alice, csrf, "", 403},
		{"foreign origin", alice, csrf, "https://foreign.example", 403},
		{"wrong CSRF", alice, "wrong", testfixture.Origin, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := browserJSONRequest(h, "/api/v1/todos", tc.cookie, todoAdd(tc.csrf, "private", "Private task", "", ""), tc.origin)
			if out.Code != tc.want {
				t.Fatalf("got %d, want %d", out.Code, tc.want)
			}
		})
	}
	token := f.Bearer("alice", "newsfeed-codex", f.Manager.MCPAudience(), identity.ArticlesWrite)
	if out := request(h, "POST", "/api/v1/todos", nil, token); out.Code != 401 {
		t.Fatalf("bearer without browser session returned %d", out.Code)
	}
	invalidForms := []url.Values{
		todoAdd(csrf, "invalid", "   ", "", ""),
		todoAdd(csrf, "invalid", strings.Repeat("é", 181), "", ""),
		todoAdd(csrf, "invalid", "Task", strings.Repeat("é", 126), ""),
		todoAdd(csrf, "invalid", "Task", "", "31"),
		todoAdd(csrf, "invalid", "Task", "", "2h"),
		todoAction(csrf, "invalid", "delete", 1),
		todoAction(csrf, "invalid", "complete", 0),
	}
	extraOwner := todoAdd(csrf, "invalid", "Task", "", "")
	extraOwner.Set("owner_id", "bob")
	duplicateAction := todoAdd(csrf, "invalid", "Task", "", "")
	duplicateAction.Add("action", "complete")
	invalidForms = append(invalidForms, extraOwner, duplicateAction)
	for _, fields := range invalidForms {
		if out := browserJSONRequest(h, "/api/v1/todos", alice, fields, testfixture.Origin); out.Code != 400 {
			t.Fatalf("invalid form returned %d", out.Code)
		}
	}
	if list := readTodo(t, h, alice).Todo; len(list.Tasks) != 0 || list.Undo != nil {
		t.Fatal("a rejected form changed the checklist")
	}
	fields := todoAdd(csrf, "private", "<private task>", "", "")
	r := httptest.NewRequestWithContext(t.Context(), "POST", testfixture.Origin+"/api/v1/todos", strings.NewReader(fields.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", testfixture.Origin)
	r.AddCookie(alice)
	native := httptest.NewRecorder()
	h.ServeHTTP(native, r)
	if native.Code != 303 || native.Header().Get("Location") != "/todo" {
		t.Fatalf("native Add did not redirect to the checklist: %d", native.Code)
	}
	if data := readTodo(t, h, readOnly); data.CanWrite || len(data.Todo.Tasks) != 1 {
		t.Fatal("read-only owner did not receive the checklist")
	}
	if out := request(h, "GET", "/todo", alice, ""); strings.Contains(out.Body.String(), "<private task>") {
		t.Fatal("task title escaped the shell's bootstrap encoding")
	}
	if data := readTodo(t, h, bob); len(data.Todo.Tasks) != 0 || data.Todo.Undo != nil {
		t.Fatal("another owner saw the task")
	}
	for _, id := range []string{"private", "unknown"} {
		for _, action := range []string{"complete", "reopen", "remove", "undo"} {
			if out := browserJSONRequest(h, "/api/v1/todos", bob, todoAction(bobCSRF, id, action, 1), testfixture.Origin); out.Code != 404 ||
				out.Body.String() != "Task not found.\n" {
				t.Fatalf("foreign or unknown ID returned %d for %s", out.Code, action)
			}
		}
	}
	for _, route := range []struct {
		path string
		want int
	}{{"/api/v1/todos", 405}, {"/api/v1/todos/x", 404}, {"/todo/history", 404}, {"/todo-evil", 404}} {
		if out := request(h, "GET", route.path, alice, ""); out.Code != route.want {
			t.Fatalf("unexpected browser route %s: %d", route.path, out.Code)
		}
	}
}

func TestTodoWriteLifecycleUsesRevisionAndKeepsOneUndo(t *testing.T) {
	f, _, h := app(t)
	cookie := f.CookieWithScopes(t, "alice", allScopes)
	_, csrf := f.BrowserPrincipal(t, cookie)
	post := func(fields url.Values, want int) {
		t.Helper()
		out := browserJSONRequest(h, "/api/v1/todos", cookie, fields, testfixture.Origin)
		if out.Code != want {
			t.Fatalf("%s returned %d, want %d: %s", fields.Get("action"), out.Code, want, out.Body.String())
		}
		if want == 200 {
			result := readBrowserJSON[struct {
				Location string `json:"location"`
			}](t, out)
			if result.Location != "/todo" {
				t.Fatalf("mutation returned unexpected SPA destination: %q", result.Location)
			}
		}
	}
	first := todoAdd(csrf, "first", "  Review paper  ", "Research, Work, research,  Deep  Reading", "120")
	post(first, 200)
	post(first, 200)
	post(todoAdd(csrf, "first", "Changed payload", "", ""), 409)
	list := readTodo(t, h, cookie).Todo
	if len(list.Tasks) != 1 || list.Tasks[0].Title != "Review paper" || list.Tasks[0].EstimateMinutes != 120 ||
		!reflect.DeepEqual(list.Tasks[0].Tags, []string{"Research", "Work", "Deep Reading"}) {
		t.Fatalf("Add retry or field normalization did not match: %#v", list)
	}
	before := time.Now().UnixMilli()
	post(todoAction(csrf, "first", "complete", 1), 200)
	task := readTodo(t, h, cookie).Todo.Tasks[0]
	if task.CompletedAt < before || task.CompletedAt > time.Now().UnixMilli() || task.Revision != 2 {
		t.Fatalf("complete did not record the current time and advance the revision: %#v", task)
	}
	post(todoAction(csrf, "first", "complete", 1), 409)
	post(todoAction(csrf, "first", "remove", 2), 200)
	list = readTodo(t, h, cookie).Todo
	if len(list.Tasks) != 0 || list.Undo == nil || list.Undo.ID != task.ID || list.Undo.CompletedAt != task.CompletedAt {
		t.Fatal("remove did not preserve the previous completion for Undo")
	}
	post(todoAction(csrf, "first", "undo", 3), 200)
	list = readTodo(t, h, cookie).Todo
	if list.Undo != nil || len(list.Tasks) != 1 || list.Tasks[0].CompletedAt != task.CompletedAt {
		t.Fatal("Undo did not restore the original completion")
	}
	post(todoAction(csrf, "first", "reopen", 4), 200)
	if task := readTodo(t, h, cookie).Todo.Tasks[0]; task.CompletedAt != 0 || task.Revision != 5 {
		t.Fatalf("reopen did not clear the completion: %#v", task)
	}
	post(todoAdd(csrf, "second", "Keep a second task", "", ""), 200)
	post(todoAction(csrf, "first", "remove", 5), 200)
	post(todoAction(csrf, "second", "remove", 0), 400)
	if list := readTodo(t, h, cookie).Todo; list.Undo == nil || list.Undo.ID != "first" {
		t.Fatal("a rejected remove replaced Undo")
	}
	post(todoAction(csrf, "second", "remove", 1), 200)
	post(todoAction(csrf, "first", "undo", 6), 404)
	post(first, 200)
	post(todoAction(csrf, "second", "undo", 2), 200)
	list = readTodo(t, h, cookie).Todo
	if len(list.Tasks) != 1 || list.Tasks[0].ID != "second" || list.Undo != nil {
		t.Fatal("Undo or an Add replay revived a superseded removal")
	}
}
