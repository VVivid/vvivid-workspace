package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
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
	content := OpportunityContent{Kind: "internship", URL: "https://jobs.example/old", Title: "A role", Company: "Example", Tags: []string{"Go"}}
	payload, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	const opportunityKey = "old-job"
	saved, err := legacy.MutateOpportunity(ctx, owner, opportunityKey, "add_opportunity", payload, Opportunity{OpportunityContent: content}, 0, false)
	if err != nil {
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
	if got, err := store.OpportunityInView(ctx, owner, saved.Opportunity.ID, false); err != nil || !reflect.DeepEqual(got, saved.Opportunity) {
		t.Fatalf("migration changed Jobs: %#v %v", got, err)
	}
	if got, found, err := store.ReplayOpportunityMutation(ctx, owner, opportunityKey, "add_opportunity", payload); err != nil || !found || !reflect.DeepEqual(got, saved) {
		t.Fatal("migration changed the Jobs retry result")
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
