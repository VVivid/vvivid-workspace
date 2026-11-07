package feed_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/identity"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

func TestTodoDurableIntentsShareBrowserStateAndReplayOriginalSnapshots(t *testing.T) {
	f, s, alice, bob := setup(t)
	ctx := t.Context()
	add := feed.AddTodoInput{IdempotencyKey: "add", Title: "  Read paper  ", Tags: "Study, study, Deep  Reading", EstimateMinutes: 120}
	first, err := s.AddTodoTask(ctx, alice, add)
	if err != nil || first.Task.ID == "" || first.Task.Revision != 1 || first.Removed || first.Task.Title != "Read paper" ||
		!reflect.DeepEqual(first.Task.Tags, []string{"Study", "Deep Reading"}) || first.Task.EstimateMinutes != 120 {
		t.Fatalf("add: %#v %v", first, err)
	}
	normalized := add
	normalized.Title, normalized.Tags = "Read paper", "Study,Deep Reading"
	if got, err := s.AddTodoTask(ctx, alice, normalized); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("normalized retry changed result: %#v %v", got, err)
	}
	secondIntent := add
	secondIntent.IdempotencyKey = "distinct-add"
	second, err := s.AddTodoTask(ctx, alice, secondIntent)
	if err != nil || second.Task.ID == first.Task.ID {
		t.Fatalf("distinct add intent was content-deduped: %#v %v", second, err)
	}
	other, err := s.AddTodoTask(ctx, bob, add)
	if err != nil || other.Task.ID == first.Task.ID {
		t.Fatalf("another owner's key was not independent: %#v %v", other, err)
	}
	if _, err := s.GetTodo(ctx, bob, feed.GetTodoInput{TaskID: first.Task.ID}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign task read: %v", err)
	}
	if _, err := s.RemoveTodo(ctx, bob, feed.TodoStateInput{IdempotencyKey: "foreign", TaskID: first.Task.ID, ExpectedRevision: 1}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("foreign task mutation: %v", err)
	}

	// A browser write and an agent no-op use the same persisted task/revision.
	if err := s.ChangeTodoState(ctx, alice, first.Task.ID, 1, sqlite.TodoComplete); err != nil {
		t.Fatal(err)
	}
	completed, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: first.Task.ID})
	if err != nil || completed.Revision != 2 || completed.CompletedAt == 0 {
		t.Fatalf("browser completion: %#v %v", completed, err)
	}
	noop := feed.SetTodoCompletedInput{IdempotencyKey: "noop", TaskID: first.Task.ID, ExpectedRevision: 1, Completed: true}
	if _, err := s.SetTodoCompleted(ctx, alice, noop); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("no-op accepted a stale revision: %v", err)
	}
	noop.ExpectedRevision = 2
	nooped, err := s.SetTodoCompleted(ctx, alice, noop)
	if err != nil || !reflect.DeepEqual(nooped.Task, completed) || nooped.Removed {
		t.Fatalf("failed key was reserved or no-op changed completion: %#v %v", nooped, err)
	}
	if err := s.ChangeTodoState(ctx, alice, first.Task.ID, 2, sqlite.TodoReopen); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetTodoCompleted(ctx, alice, noop); err != nil || !reflect.DeepEqual(got, nooped) {
		t.Fatalf("no-op replay lost its original completion: %#v %v", got, err)
	}
	complete := feed.SetTodoCompletedInput{IdempotencyKey: "complete", TaskID: first.Task.ID, ExpectedRevision: 3, Completed: true}
	finished, err := s.SetTodoCompleted(ctx, alice, complete)
	if err != nil || finished.Task.Revision != 4 || finished.Task.CompletedAt == 0 {
		t.Fatalf("agent completion: %#v %v", finished, err)
	}
	remove := feed.TodoStateInput{IdempotencyKey: "remove", TaskID: first.Task.ID, ExpectedRevision: 4}
	removed, err := s.RemoveTodo(ctx, alice, remove)
	if err != nil || !removed.Removed || removed.Task.Revision != 5 || removed.Task.CompletedAt != finished.Task.CompletedAt {
		t.Fatalf("remove: %#v %v", removed, err)
	}
	if _, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: first.Task.ID}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("ordinary read exposed pending removal: %v", err)
	}
	if got, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: first.Task.ID, Removed: true}); err != nil || !reflect.DeepEqual(got, removed.Task) {
		t.Fatalf("explicit removal read: %#v %v", got, err)
	}
	undo := feed.TodoStateInput{IdempotencyKey: "undo", TaskID: first.Task.ID, ExpectedRevision: 5}
	restored, err := s.UndoTodoRemoval(ctx, alice, undo)
	if err != nil || restored.Removed || restored.Task.Revision != 6 || restored.Task.CompletedAt != finished.Task.CompletedAt {
		t.Fatalf("Undo changed completion: %#v %v", restored, err)
	}
	// Replaying this Undo must not undo a later removal of the very same task.
	if err := s.ChangeTodoState(ctx, alice, first.Task.ID, 6, sqlite.TodoRemove); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UndoTodoRemoval(ctx, alice, undo); err != nil || !reflect.DeepEqual(got, restored) {
		t.Fatalf("Undo retry after a later removal: %#v %v", got, err)
	}
	page, err := s.ListTodos(ctx, alice, feed.ListTodosInput{})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].ID != second.Task.ID || page.Undo == nil || page.Undo.ID != first.Task.ID || page.Undo.Revision != 7 {
		t.Fatalf("Undo replay changed the later removal: %#v %v", page, err)
	}
	secondRemove := feed.TodoStateInput{IdempotencyKey: "second-remove", TaskID: second.Task.ID, ExpectedRevision: 1}
	secondRemoved, err := s.RemoveTodo(ctx, alice, secondRemove)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: first.Task.ID, Removed: true}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("superseded removal remained readable: %v", err)
	}
	if _, err := s.UndoTodoRemoval(ctx, alice, feed.TodoStateInput{IdempotencyKey: "late-undo", TaskID: first.Task.ID, ExpectedRevision: 7}); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatalf("new Undo revived a superseded removal: %v", err)
	}

	f.Reopen(t)
	policy, _ := thumbnail.NewPolicy("images.example.com")
	s = feed.New(f.Store, policy)
	for _, tc := range []struct {
		name string
		call func() (sqlite.TodoMutationResult, error)
		want sqlite.TodoMutationResult
	}{
		{"add", func() (sqlite.TodoMutationResult, error) { return s.AddTodoTask(ctx, alice, add) }, first},
		{"no-op", func() (sqlite.TodoMutationResult, error) { return s.SetTodoCompleted(ctx, alice, noop) }, nooped},
		{"complete", func() (sqlite.TodoMutationResult, error) { return s.SetTodoCompleted(ctx, alice, complete) }, finished},
		{"remove", func() (sqlite.TodoMutationResult, error) { return s.RemoveTodo(ctx, alice, remove) }, removed},
		{"Undo", func() (sqlite.TodoMutationResult, error) { return s.UndoTodoRemoval(ctx, alice, undo) }, restored},
		{"second removal", func() (sqlite.TodoMutationResult, error) { return s.RemoveTodo(ctx, alice, secondRemove) }, secondRemoved},
	} {
		if got, err := tc.call(); err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s replay after restart: %#v %v", tc.name, got, err)
		}
	}
	list, err := s.ListTodo(ctx, alice)
	if err != nil || len(list.Tasks) != 0 || list.Undo == nil || !reflect.DeepEqual(*list.Undo, secondRemoved.Task) {
		t.Fatalf("retries changed the browser's latest Undo: %#v %v", list, err)
	}
	changed := add
	changed.Title = "Changed intent"
	if _, err := s.AddTodoTask(ctx, alice, changed); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("changed add key: %v", err)
	}
	changedState := complete
	changedState.Completed = false
	if _, err := s.SetTodoCompleted(ctx, alice, changedState); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("changed target key: %v", err)
	}
	remove.IdempotencyKey = add.IdempotencyKey
	if _, err := s.RemoveTodo(ctx, alice, remove); !errors.Is(err, sqlite.ErrIdempotencyConflict) {
		t.Fatalf("key reused for another operation: %v", err)
	}
}

func TestTodoScopesAndEnrollmentAreCheckedBeforeReplay(t *testing.T) {
	f, s, alice, _ := setup(t)
	ctx := t.Context()
	add := feed.AddTodoInput{IdempotencyKey: "add", Title: "Private task"}
	first, err := s.AddTodoTask(ctx, alice, add)
	if err != nil {
		t.Fatal(err)
	}
	complete := feed.SetTodoCompletedInput{IdempotencyKey: "complete", TaskID: first.Task.ID, ExpectedRevision: 1, Completed: true}
	if _, err := s.SetTodoCompleted(ctx, alice, complete); err != nil {
		t.Fatal(err)
	}
	remove := feed.TodoStateInput{IdempotencyKey: "remove", TaskID: first.Task.ID, ExpectedRevision: 2}
	if _, err := s.RemoveTodo(ctx, alice, remove); err != nil {
		t.Fatal(err)
	}
	undo := feed.TodoStateInput{IdempotencyKey: "undo", TaskID: first.Task.ID, ExpectedRevision: 3}
	if _, err := s.UndoTodoRemoval(ctx, alice, undo); err != nil {
		t.Fatal(err)
	}
	readOnly := f.Principal(t, "alice", identity.Read)
	writeOnly := f.Principal(t, "alice", identity.ArticlesWrite)
	if page, err := s.ListTodos(ctx, readOnly, feed.ListTodosInput{Status: "all"}); err != nil || len(page.Tasks) != 1 {
		t.Fatalf("read grant could not list tasks: %#v %v", page, err)
	}
	if _, err := s.GetTodo(ctx, readOnly, feed.GetTodoInput{TaskID: first.Task.ID}); err != nil {
		t.Fatalf("read grant could not get a task: %v", err)
	}
	for i, principal := range []identity.Principal{readOnly, writeOnly, alice} {
		want := identity.ErrForbidden
		if i == 2 {
			if err := f.Store.DisableAccount(ctx, alice.Identity()); err != nil {
				t.Fatal(err)
			}
			want = identity.ErrUnauthenticated
		}
		for _, call := range []func() (sqlite.TodoMutationResult, error){
			func() (sqlite.TodoMutationResult, error) { return s.AddTodoTask(ctx, principal, add) },
			func() (sqlite.TodoMutationResult, error) { return s.SetTodoCompleted(ctx, principal, complete) },
			func() (sqlite.TodoMutationResult, error) { return s.RemoveTodo(ctx, principal, remove) },
			func() (sqlite.TodoMutationResult, error) { return s.UndoTodoRemoval(ctx, principal, undo) },
		} {
			if _, err := call(); !errors.Is(err, want) {
				t.Fatalf("stored result bypassed authorization: %v, want %v", err, want)
			}
		}
	}
	if _, err := s.ListTodos(ctx, writeOnly, feed.ListTodosInput{}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("write grant implied list read: %v", err)
	}
	if _, err := s.GetTodo(ctx, writeOnly, feed.GetTodoInput{TaskID: first.Task.ID}); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("write grant implied single read: %v", err)
	}
	if _, err := s.ListTodos(ctx, alice, feed.ListTodosInput{}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("disabled owner read: %v", err)
	}
	if _, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: first.Task.ID}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("disabled owner single read: %v", err)
	}
}

func TestTodoBoundedViewsAndCursors(t *testing.T) {
	_, s, alice, bob := setup(t)
	ctx := t.Context()
	var completed int
	for i := range 53 {
		id := fmt.Sprintf("browser-%02d", i)
		if err := s.AddTodo(ctx, alice, id, id, "", 0); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if err := s.ChangeTodoState(ctx, alice, id, 1, sqlite.TodoComplete); err != nil {
				t.Fatal(err)
			}
			completed++
		}
	}
	if err := s.ChangeTodoState(ctx, alice, "browser-01", 1, sqlite.TodoRemove); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ListTodos(ctx, alice, feed.ListTodosInput{}); err != nil || len(page.Tasks) != 20 || page.NextCursor == "" {
		t.Fatalf("default list was not bounded: %#v %v", page, err)
	}
	for _, status := range []string{"open", "completed", "all"} {
		seen := map[string]bool{}
		cursor := ""
		for {
			page, err := s.ListTodos(ctx, alice, feed.ListTodosInput{Status: status, Limit: 7, Cursor: cursor})
			if err != nil || len(page.Tasks) > 7 || page.Undo == nil || page.Undo.ID != "browser-01" {
				t.Fatalf("%s page: %#v %v", status, page, err)
			}
			for _, task := range page.Tasks {
				if seen[task.ID] || task.ID == "browser-01" || (status == "open" && task.CompletedAt != 0) || (status == "completed" && task.CompletedAt == 0) {
					t.Fatalf("%s list duplicated or crossed views: %#v", status, task)
				}
				seen[task.ID] = true
			}
			if cursor == "" {
				if page.NextCursor == "" {
					t.Fatal("fixture did not produce a cursor")
				}
				if _, err := s.ListTodos(ctx, bob, feed.ListTodosInput{Status: status, Cursor: page.NextCursor}); !errors.Is(err, feed.ErrInvalid) {
					t.Fatalf("cursor crossed owners: %v", err)
				}
				other := "all"
				if status == "all" {
					other = "completed"
				}
				if _, err := s.ListTodos(ctx, alice, feed.ListTodosInput{Status: other, Cursor: page.NextCursor}); !errors.Is(err, feed.ErrInvalid) {
					t.Fatalf("cursor crossed views: %v", err)
				}
				if _, err := s.ListArticles(ctx, alice, feed.ListInput{Cursor: page.NextCursor}); !errors.Is(err, feed.ErrInvalid) {
					t.Fatalf("To-do cursor entered News: %v", err)
				}
				raw, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
				if err != nil {
					t.Fatal(err)
				}
				var boundary sqlite.TodoCursor
				if err := json.Unmarshal(raw, &boundary); err != nil {
					t.Fatal(err)
				}
				boundary.Collection = "opportunities"
				raw, err = json.Marshal(boundary)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.ListTodos(ctx, alice, feed.ListTodosInput{Status: status, Cursor: base64.RawURLEncoding.EncodeToString(raw)}); !errors.Is(err, feed.ErrInvalid) {
					t.Fatalf("another collection's boundary entered To-do: %v", err)
				}
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		want := 52
		if status == "open" {
			want -= completed
		} else if status == "completed" {
			want = completed
		}
		if len(seen) != want {
			t.Fatalf("%s returned %d tasks, want %d", status, len(seen), want)
		}
	}
	for _, in := range []feed.ListTodosInput{{Limit: -1}, {Limit: 51}, {Status: "removed"}, {Cursor: "garbage"},
		{Cursor: strings.Repeat("x", feed.MaxTodoCursorBytes+1)}, {Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"collection":"opportunities"}`))}} {
		if _, err := s.ListTodos(ctx, alice, in); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid list input accepted: %#v %v", in, err)
		}
	}
}

func TestTodoCursorsContinueAcrossEscapedBrowserIDs(t *testing.T) {
	for _, status := range []string{"open", "completed", "all"} {
		t.Run(status, func(t *testing.T) {
			_, s, alice, _ := setup(t)
			ctx := t.Context()
			// The oldest ID sorts last even when creation timestamps tie, so
			// every escaped ID must produce a usable continuation cursor.
			ids := []string{"!", strings.Repeat("<", 64), strings.Repeat(">", 64),
				strings.Repeat("&", 64), strings.Repeat(`"`, 64), strings.Repeat(`\`, 64)}
			for i, id := range ids {
				if err := s.AddTodo(ctx, alice, id, "Browser task", "", 0); err != nil {
					t.Fatal(err)
				}
				if status == "completed" || (status == "all" && i%2 == 0) {
					if err := s.ChangeTodoState(ctx, alice, id, 1, sqlite.TodoComplete); err != nil {
						t.Fatal(err)
					}
				}
			}
			seen := map[string]bool{}
			cursor := ""
			for i := range len(ids) {
				page, err := s.ListTodos(ctx, alice, feed.ListTodosInput{Status: status, Limit: 1, Cursor: cursor})
				if err != nil || len(page.Tasks) != 1 {
					t.Fatalf("page %d: %#v %v", i, page, err)
				}
				id := page.Tasks[0].ID
				if !slices.Contains(ids, id) || seen[id] {
					t.Fatalf("page %d returned an unknown or duplicate ID %q", i, id)
				}
				seen[id] = true
				if len(page.NextCursor) > feed.MaxTodoCursorBytes {
					t.Fatalf("page %d cursor has %d bytes, limit %d", i, len(page.NextCursor), feed.MaxTodoCursorBytes)
				}
				if (page.NextCursor == "") != (i == len(ids)-1) {
					t.Fatalf("page %d returned an unexpected end of pagination: %#v", i, page)
				}
				cursor = page.NextCursor
			}
		})
	}
}

func TestTodoValidationAndConcurrentIntentKeys(t *testing.T) {
	_, s, alice, _ := setup(t)
	ctx := t.Context()
	for _, in := range []feed.AddTodoInput{
		{IdempotencyKey: "", Title: "Task"}, {IdempotencyKey: " key", Title: "Task"},
		{IdempotencyKey: "invalid", Title: " "}, {IdempotencyKey: "invalid", Title: strings.Repeat("é", 181)},
		{IdempotencyKey: "invalid", Title: "Task\n" + "bad"}, {IdempotencyKey: "invalid", Title: "Task", Tags: strings.Repeat("é", 126)},
		{IdempotencyKey: "invalid", Title: "\xff"}, {IdempotencyKey: "invalid", Title: "Task", Tags: "\xff"},
		{IdempotencyKey: "invalid", Title: "Task", Tags: "Study\t"}, {IdempotencyKey: "invalid", Title: "Task", EstimateMinutes: 31},
	} {
		if _, err := s.AddTodoTask(ctx, alice, in); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid add accepted: %#v %v", in, err)
		}
	}
	valid := feed.AddTodoInput{IdempotencyKey: "invalid", Title: strings.Repeat("é", 180), Tags: strings.Repeat("é", 125), EstimateMinutes: 480}
	first, err := s.AddTodoTask(ctx, alice, valid)
	if err != nil || len(first.Task.Tags) != 1 || first.Task.Tags[0] != valid.Tags {
		t.Fatalf("existing title/tag bounds or failed-key reuse changed: %#v %v", first, err)
	}
	for _, id := range []string{"", " wrong", strings.Repeat("x", 65)} {
		if _, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: id}); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid single read accepted: %q %v", id, err)
		}
	}
	for _, in := range []feed.TodoStateInput{{TaskID: first.Task.ID, ExpectedRevision: 1}, {IdempotencyKey: "x", ExpectedRevision: 1},
		{IdempotencyKey: "x", TaskID: first.Task.ID}, {IdempotencyKey: "x", TaskID: strings.Repeat("x", 65), ExpectedRevision: 1}} {
		if _, err := s.RemoveTodo(ctx, alice, in); !errors.Is(err, feed.ErrInvalid) {
			t.Fatalf("invalid state accepted: %#v %v", in, err)
		}
	}

	add := feed.AddTodoInput{IdempotencyKey: "concurrent", Title: "One intended task"}
	results := make(chan sqlite.TodoMutationResult, 8)
	failures := make(chan error, 8)
	var writers sync.WaitGroup
	for range 8 {
		writers.Go(func() {
			out, err := s.AddTodoTask(ctx, alice, add)
			results <- out
			failures <- err
		})
	}
	writers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var saved sqlite.TodoMutationResult
	for out := range results {
		if saved.Task.ID != "" && !reflect.DeepEqual(out, saved) {
			t.Fatal("concurrent exact add did not return identical snapshots")
		}
		saved = out
	}
	page, err := s.ListTodos(ctx, alice, feed.ListTodosInput{Status: "all"})
	if err != nil || len(page.Tasks) != 2 {
		t.Fatalf("unexpected tasks after concurrent retries: %#v %v", page, err)
	}
}

func TestTodoExplicitReopenNoopAndConflictsPreserveIntentKeys(t *testing.T) {
	_, s, alice, _ := setup(t)
	ctx := t.Context()
	add := feed.AddTodoInput{IdempotencyKey: "add", Title: "Open task"}
	created, err := s.AddTodoTask(ctx, alice, add)
	if err != nil {
		t.Fatal(err)
	}
	reopen := feed.SetTodoCompletedInput{IdempotencyKey: "reopen-noop", TaskID: created.Task.ID, ExpectedRevision: 1, Completed: false}
	nooped, err := s.SetTodoCompleted(ctx, alice, reopen)
	if err != nil || !reflect.DeepEqual(nooped, created) {
		t.Fatalf("explicit open target changed an open task: %#v %v", nooped, err)
	}
	if err := s.ChangeTodoState(ctx, alice, created.Task.ID, 1, sqlite.TodoComplete); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetTodoCompleted(ctx, alice, reopen); err != nil || !reflect.DeepEqual(got, nooped) {
		t.Fatalf("open target replay changed its result: %#v %v", got, err)
	}
	current, err := s.GetTodo(ctx, alice, feed.GetTodoInput{TaskID: created.Task.ID})
	if err != nil || current.Revision != 2 || current.CompletedAt == 0 {
		t.Fatalf("replay reopened later completion: %#v %v", current, err)
	}
	reopen.IdempotencyKey, reopen.ExpectedRevision = "reopen", 2
	reopened, err := s.SetTodoCompleted(ctx, alice, reopen)
	if err != nil || reopened.Task.Revision != 3 || reopened.Task.CompletedAt != 0 {
		t.Fatalf("explicit false did not reopen: %#v %v", reopened, err)
	}
	remove := feed.TodoStateInput{IdempotencyKey: "remove", TaskID: created.Task.ID, ExpectedRevision: 2}
	if _, err := s.RemoveTodo(ctx, alice, remove); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("stale removal accepted: %v", err)
	}
	remove.ExpectedRevision = 3
	removed, err := s.RemoveTodo(ctx, alice, remove)
	if err != nil || !removed.Removed || removed.Task.Revision != 4 {
		t.Fatalf("failed removal key was reserved: %#v %v", removed, err)
	}
	failed := feed.SetTodoCompletedInput{IdempotencyKey: "reuse-failed", TaskID: created.Task.ID, ExpectedRevision: 4, Completed: true}
	if _, err := s.SetTodoCompleted(ctx, alice, failed); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("completion of pending removal accepted: %v", err)
	}
	if _, err := s.UndoTodoRemoval(ctx, alice, feed.TodoStateInput{IdempotencyKey: failed.IdempotencyKey, TaskID: created.Task.ID, ExpectedRevision: 4}); err != nil {
		t.Fatalf("failed completion reserved its key: %v", err)
	}
}
