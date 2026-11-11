package sqlite

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestTodoMigrationPreservesExistingContentAndPersistsTasks(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v4.sqlite")
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:4]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{read: db, write: db}
	enrollment := ExternalIdentity{Issuer: "https://issuer.example", Subject: "legacy-owner"}
	owner, err := legacy.EnrollAccount(ctx, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	jpeg := thumbnailJPEG(t, 150)
	article := storeArticle(t, legacy, owner, "old-article", jpeg).Article
	// Create a retired listing and retry record as they would appear in a v4 database.
	const listingID = "legacy-listing"
	type listingSnapshot struct {
		title, tags, description, note  string
		revision, archivedAt, trashedAt int64
	}
	wantListing := listingSnapshot{
		title: "A role", tags: `["Go"]`, description: "Source description", note: "Agent note",
		revision: 3, archivedAt: 200, trashedAt: 300,
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO opportunities
		(owner_id,id,canonical_url,kind,title,company,tags_json,source_description,agent_note,revision,created_at,updated_at,archived_at,trashed_at)
		VALUES (?,?,'https://jobs.example/old','internship',?,'Example',?,?,?,?,100,300,?,?)`,
		owner.id, listingID, wantListing.title, wantListing.tags, wantListing.description, wantListing.note,
		wantListing.revision, wantListing.archivedAt, wantListing.trashedAt); err != nil {
		t.Fatal(err)
	}
	retryHash := sha256.Sum256([]byte("prior listing intent"))
	retryResult := []byte(`{"opportunity":{"id":"legacy-listing","revision":1},"created":true}`)
	if _, err := db.ExecContext(ctx, `INSERT INTO opportunity_idempotency_results (owner_id,key,operation,payload_hash,result,created_at)
		VALUES (?,'old-job','add_opportunity',?,?,100)`, owner.id, retryHash[:], retryResult); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := store.OwnerForIdentity(ctx, enrollment); err != nil || got != owner {
		t.Fatal("migration changed enrollment")
	}
	if got, err := store.Article(ctx, owner, article.ID); err != nil || got != article {
		t.Fatalf("migration changed News: %#v %v", got, err)
	}
	if image, err := store.Thumbnail(ctx, owner, article.ID, false); err != nil || !bytes.Equal(image, jpeg) {
		t.Fatal("migration changed the thumbnail")
	}
	var gotListing listingSnapshot
	if err := store.read.QueryRowContext(ctx, `SELECT title,tags_json,source_description,agent_note,revision,archived_at,trashed_at
		FROM opportunities WHERE owner_id=? AND id=?`, owner.id, listingID).
		Scan(&gotListing.title, &gotListing.tags, &gotListing.description, &gotListing.note,
			&gotListing.revision, &gotListing.archivedAt, &gotListing.trashedAt); err != nil {
		t.Fatalf("read the retained listing: %v", err)
	}
	if gotListing != wantListing {
		t.Fatalf("retained listing changed: got %+v, want %+v", gotListing, wantListing)
	}
	var storedHash, storedResult []byte
	if err := store.read.QueryRowContext(ctx, `SELECT payload_hash,result FROM opportunity_idempotency_results
		WHERE owner_id=? AND key='old-job'`, owner.id).Scan(&storedHash, &storedResult); err != nil {
		t.Fatalf("read the retained listing retry record: %v", err)
	}
	if !bytes.Equal(storedHash, retryHash[:]) || !bytes.Equal(storedResult, retryResult) {
		t.Fatal("migration changed the retained listing retry bytes")
	}
	if list, err := store.ListTodo(ctx, owner); err != nil || len(list.Tasks) != 0 || list.Undo != nil {
		t.Fatalf("migration did not leave a new owner checklist: %#v %v", list, err)
	}
	task := TodoTask{ID: "saved", Title: "Read", Tags: []string{"Study", "Work"}, EstimateMinutes: 240}
	if err := store.AddTodo(ctx, owner, task); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeTodoState(ctx, owner, task.ID, 1, TodoComplete); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeTodoState(ctx, owner, task.ID, 2, TodoRemove); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	list, err := reopened.ListTodo(ctx, owner)
	if err != nil || list.Undo == nil || len(list.Tasks) != 0 {
		t.Fatalf("restart did not preserve Undo: %#v %v", list, err)
	}
	task.Revision = 3
	task.CompletedAt = list.Undo.CompletedAt
	if task.CompletedAt == 0 || !reflect.DeepEqual(*list.Undo, task) {
		t.Fatalf("restart changed task fields: %#v", list.Undo)
	}
	if err := reopened.ChangeTodoState(ctx, owner, task.ID, task.Revision, TodoUndo); err != nil {
		t.Fatal(err)
	}
	list, err = reopened.ListTodo(ctx, owner)
	task.Revision++
	if err != nil || list.Undo != nil || !reflect.DeepEqual(list.Tasks, []TodoTask{task}) {
		t.Fatalf("Undo after restart changed task fields: %#v %v", list, err)
	}
}

func TestTodoConcurrentWritesAndUndoAreOwnerScoped(t *testing.T) {
	store, _, alice, bob := thumbnailStore(t)
	ctx := t.Context()
	task := TodoTask{ID: "shared-key", Title: "First owner's task", Tags: []string{}}
	if err := store.AddTodo(ctx, alice, task); err != nil {
		t.Fatal(err)
	}
	task.Title = "Second owner's task"
	if err := store.AddTodo(ctx, bob, task); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeTodoState(ctx, bob, task.ID, 1, TodoRemove); err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan error, 2)
	var writers sync.WaitGroup
	for _, action := range []TodoAction{TodoComplete, TodoRemove} {
		writers.Go(func() {
			outcomes <- store.ChangeTodoState(ctx, alice, task.ID, 1, action)
		})
	}
	writers.Wait()
	close(outcomes)
	var succeeded, conflicted int
	for err := range outcomes {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatal("concurrent writes both changed the same revision")
	}
	list, err := store.ListTodo(ctx, bob)
	if err != nil || list.Undo == nil || list.Undo.Title != task.Title || list.Undo.Revision != 2 {
		t.Fatalf("concurrent writes changed another owner's Undo: %#v %v", list, err)
	}
}

func TestTodoRetryMigrationPreservesV5TasksAndPriorRetryBytes(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v5.sqlite")
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:5]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{read: db, write: db}
	owner, err := legacy.EnrollAccount(ctx, ExternalIdentity{Issuer: "https://issuer.example", Subject: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	// Seed the historical schema directly; current repository code uses the new column.
	if _, err := db.ExecContext(ctx, `INSERT INTO todo_tasks
		(owner_id,id,title,tags_json,estimate_minutes,revision,created_at,completed_at,removal_state)
		VALUES (?, 'visible', 'Visible', '[]', 0, 1, 100, NULL, 'visible'),
		(?, 'undo', 'Removed', '["A long existing tag"]', 90, 3, 200, 250, 'undoable')`, owner.id, owner.id); err != nil {
		t.Fatal(err)
	}
	before := TodoList{Tasks: []TodoTask{{ID: "visible", Title: "Visible", Tags: []string{}, Revision: 1}},
		Undo: new(TodoTask{ID: "undo", Title: "Removed", Tags: []string{"A long existing tag"}, EstimateMinutes: 90, Revision: 3, CompletedAt: 250})}
	hash := sha256.Sum256([]byte("prior intent bytes"))
	result := []byte("{ \"unchanged\" : true }\n")
	for _, table := range []string{"idempotency_results", "opportunity_idempotency_results"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO `+table+` (owner_id,key,operation,payload_hash,result,created_at)
			VALUES (?, 'same-key', 'existing-operation', ?, ?, 123)`, owner.id, hash[:], result); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ListTodo(ctx, owner); err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("migration changed browser tasks/Undo: %#v %v", got, err)
	}
	if err := store.AddTodo(ctx, owner, TodoTask{ID: "undo", Title: "Removed", Tags: []string{"A long existing tag"}, EstimateMinutes: 90}); err != nil {
		t.Fatalf("migration changed the browser draft retry: %v", err)
	}
	for _, table := range []string{"idempotency_results", "opportunity_idempotency_results"} {
		var storedHash, storedResult []byte
		if err := store.read.QueryRowContext(ctx, `SELECT payload_hash,result FROM `+table+` WHERE owner_id=? AND key='same-key'`, owner.id).
			Scan(&storedHash, &storedResult); err != nil || !bytes.Equal(storedHash, hash[:]) || !bytes.Equal(storedResult, result) {
			t.Fatalf("migration changed %s retry bytes: %v", table, err)
		}
	}
	for _, previous := range migrations[:5] {
		var checksum string
		if err := store.read.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version=?`, previous.version).
			Scan(&checksum); err != nil || checksum != previous.checksum {
			t.Fatalf("migration changed prior checksum %d: %v", previous.version, err)
		}
	}
	if _, err := migrationVersion(ctx, store.read, migrations[:5]); err == nil {
		t.Fatal("v5 binary accepted the newer schema")
	}
	// The new To-do retry namespace is independent from News and Jobs keys.
	if _, err := store.MutateTodo(ctx, owner, "same-key", "add_todo", []byte("todo"), TodoTask{Title: "New", Tags: []string{}}, 0, false); err != nil {
		t.Fatal(err)
	}
}

func TestTodoRetryWriteFailureRollsBackTaskAndLatestUndo(t *testing.T) {
	store, _, owner, _ := thumbnailStore(t)
	ctx := t.Context()
	for _, id := range []string{"first", "second"} {
		if err := store.AddTodo(ctx, owner, TodoTask{ID: id, Title: id, Tags: []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ChangeTodoState(ctx, owner, "first", 1, TodoRemove); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListTodo(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.write.ExecContext(ctx, `CREATE TRIGGER reject_todo_retry BEFORE INSERT ON todo_idempotency_results
		BEGIN SELECT RAISE(ABORT, 'synthetic retry persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"add_todo", "set_todo_completed", "remove_todo", "undo_todo_removal"} {
		task, revision := TodoTask{ID: "second", Title: "Synthetic", Tags: []string{}}, int64(1)
		if operation == "undo_todo_removal" {
			task.ID, revision = "first", 2
		}
		if _, err := store.MutateTodo(ctx, owner, "failed", operation, []byte(operation), task, revision, true); err == nil {
			t.Fatalf("%s succeeded without persisting its retry result", operation)
		}
		if got, err := store.ListTodo(ctx, owner); err != nil || !reflect.DeepEqual(got, before) {
			t.Fatalf("%s partially changed task or replaced Undo: %#v %v", operation, got, err)
		}
	}
	var count int
	if err := store.read.QueryRowContext(ctx, `SELECT count(*) FROM todo_idempotency_results`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failure reserved retry keys: %d %v", count, err)
	}
	if _, err := store.write.ExecContext(ctx, `DROP TRIGGER reject_todo_retry`); err != nil {
		t.Fatal(err)
	}
	if out, err := store.MutateTodo(ctx, owner, "failed", "remove_todo", []byte("remove_todo"), TodoTask{ID: "second"}, 1, false); err != nil || !out.Removed || out.Task.Revision != 2 {
		t.Fatalf("failed key could not be reused: %#v %v", out, err)
	}
}

func TestTodoKeysetPaginationOrdersTiedCreationTimes(t *testing.T) {
	store, _, owner, _ := thumbnailStore(t)
	ctx := t.Context()
	for i := range 8 {
		id := fmt.Sprintf("task-%02d", i)
		if err := store.AddTodo(ctx, owner, TodoTask{ID: id, Title: id, Tags: []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.write.ExecContext(ctx, `UPDATE todo_tasks SET created_at=42 WHERE owner_id=?`, owner.id); err != nil {
		t.Fatal(err)
	}
	cursor := TodoCursor{Collection: "todos", Owner: owner.id, Status: "all"}
	var ids []string
	for {
		page, err := store.ListTodos(ctx, owner, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page.Tasks {
			ids = append(ids, task.ID)
		}
		if page.Next == nil {
			break
		}
		if page.Next.Timestamp != 42 || page.Next.Collection != cursor.Collection || page.Next.Owner != owner.id || page.Next.Status != cursor.Status {
			t.Fatalf("boundary lost its tied timestamp or binding: %#v", page.Next)
		}
		cursor = *page.Next
	}
	want := []string{"task-07", "task-06", "task-05", "task-04", "task-03", "task-02", "task-01", "task-00"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("tied task ordering skipped or repeated rows: %v", ids)
	}
}

func TestTodoOrderPersistsAndRetriesCannotRepeatMoves(t *testing.T) {
	store, path, owner, other := thumbnailStore(t)
	ctx := t.Context()
	for _, id := range []string{"a", "b", "c"} {
		if err := store.AddTodo(ctx, owner, TodoTask{ID: id, Title: id, Tags: []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ChangeTodoState(ctx, owner, "b", 1, TodoComplete); err != nil {
		t.Fatal(err)
	}
	move := TodoMove{TaskID: "a", ExpectedRevision: 1, NeighborID: "c", NeighborRevision: 1, Direction: "up"}
	payload, _ := json.Marshal(move)
	first, err := store.ReorderTodo(ctx, owner, "move", payload, move)
	if err != nil || first.Task.Revision != 2 {
		t.Fatalf("move: %#v %v", first, err)
	}
	assertOrder := func(want ...string) {
		t.Helper()
		list, err := store.ListTodo(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, task := range list.Tasks {
			if task.CompletedAt == 0 {
				ids = append(ids, task.ID)
			}
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("open order: got %v want %v", ids, want)
		}
	}
	assertOrder("a", "c")
	if err := store.ChangeTodoState(ctx, owner, "b", 2, TodoReopen); err != nil {
		t.Fatal(err)
	}
	assertOrder("a", "b", "c")
	if err := store.ChangeTodoState(ctx, owner, "a", 2, TodoRemove); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeTodoState(ctx, owner, "a", 3, TodoUndo); err != nil {
		t.Fatal(err)
	}
	assertOrder("a", "b", "c")
	if got, err := store.ReorderTodo(ctx, owner, "move", payload, move); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("retry: %#v %v", got, err)
	}
	assertOrder("a", "b", "c")
	if _, err := store.ReorderTodo(ctx, owner, "move", []byte("another intent"), move); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("key reuse: %v", err)
	}
	if _, err := store.ReorderTodo(ctx, other, "foreign", payload, move); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner isolation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertOrder("a", "b", "c")
	if got, err := store.ReorderTodo(ctx, owner, "move", payload, move); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("restart replay: %#v %v", got, err)
	}

	// Both requests observed the same pair; exactly one may swap it.
	concurrent := TodoMove{TaskID: "b", ExpectedRevision: 3, NeighborID: "a", NeighborRevision: 4, Direction: "up"}
	body, _ := json.Marshal(concurrent)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			_, err := store.ReorderTodo(ctx, owner, fmt.Sprintf("concurrent-%d", i), body, concurrent)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent move outcomes: %d successes %d conflicts", successes, conflicts)
	}
	assertOrder("b", "a", "c")
}

func TestTodoOrderFailureRollsBackPairAndDoesNotReserveKey(t *testing.T) {
	store, _, owner, _ := thumbnailStore(t)
	ctx := t.Context()
	for _, id := range []string{"a", "b", "c"} {
		if err := store.AddTodo(ctx, owner, TodoTask{ID: id, Title: id, Tags: []string{}}); err != nil {
			t.Fatal(err)
		}
	}
	move := TodoMove{TaskID: "a", ExpectedRevision: 1, NeighborID: "b", NeighborRevision: 1, Direction: "up"}
	for _, bad := range []TodoMove{
		{TaskID: "a", ExpectedRevision: 1, NeighborID: "c", NeighborRevision: 1, Direction: "up"},
		{TaskID: "a", ExpectedRevision: 1, NeighborID: "b", NeighborRevision: 2, Direction: "up"},
	} {
		body, _ := json.Marshal(bad)
		if _, err := store.ReorderTodo(ctx, owner, "retryable", body, bad); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale/nonadjacent move: %v", err)
		}
	}
	if _, err := store.write.ExecContext(ctx, `CREATE TRIGGER reject_move BEFORE INSERT ON todo_idempotency_results BEGIN SELECT RAISE(ABORT, 'synthetic retry failure'); END`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(move)
	if _, err := store.ReorderTodo(ctx, owner, "retryable", body, move); err == nil {
		t.Fatal("failed retry write committed")
	}
	list, err := store.ListTodo(ctx, owner)
	if err != nil || len(list.Tasks) != 3 || list.Tasks[0].ID != "c" || list.Tasks[1].ID != "b" || list.Tasks[2].ID != "a" || list.Tasks[1].Revision != 1 || list.Tasks[2].Revision != 1 {
		t.Fatalf("partial failed swap: %#v %v", list, err)
	}
	if _, err := store.write.ExecContext(ctx, `DROP TRIGGER reject_move`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReorderTodo(ctx, owner, "retryable", body, move); err != nil {
		t.Fatalf("failed key reserved: %v", err)
	}
}

func TestTodoOrderMigrationKeepsLegacyOrderAndRetryBytes(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v6.sqlite")
	db, err := sql.Open("sqlite", dataSource(path, time.Second, false))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrations[:6]); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{read: db, write: db}
	owner, err := legacy.EnrollAccount(ctx, ExternalIdentity{Issuer: "https://issuer.example", Subject: "order-migration"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO todo_tasks(owner_id,id,title,tags_json,estimate_minutes,created_at) VALUES(?,?,?,'[]',0,100)`, owner.id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	hash := sha256.Sum256([]byte("legacy todo intent"))
	result := []byte(`{ "task": {"id":"a","title":"a","revision":1}, "removed":false }`)
	if _, err := db.ExecContext(ctx, `INSERT INTO todo_idempotency_results(owner_id,key,operation,payload_hash,result,created_at) VALUES(?,'old','add_todo',?,?,100)`, owner.id, hash[:], result); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	list, err := store.ListTodo(ctx, owner)
	if err != nil || len(list.Tasks) != 3 || list.Tasks[0].ID != "c" || list.Tasks[1].ID != "b" || list.Tasks[2].ID != "a" {
		t.Fatalf("migration order: %#v %v", list, err)
	}
	var gotHash, gotResult []byte
	if err := store.read.QueryRowContext(ctx, `SELECT payload_hash,result FROM todo_idempotency_results WHERE owner_id=? AND key='old'`, owner.id).Scan(&gotHash, &gotResult); err != nil || !bytes.Equal(gotHash, hash[:]) || !bytes.Equal(gotResult, result) {
		t.Fatalf("migration changed retry bytes: %v", err)
	}
}
