// These tests run only as an explicitly requested local container acceptance
// binary. They use synthetic private records and never configure a live issuer.
package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
)

const database = "/data/newsfeed.sqlite"
const expectedFile = "/data/container-expected.json"

type persistenceExpected struct {
	Retry          sqlite.MutationResult   `json:"retry"`
	Current        sqlite.Article          `json:"current"`
	Trashed        sqlite.Article          `json:"trashed"`
	ThumbnailPatch sqlite.MutationResult   `json:"thumbnail_patch"`
	TrashRetry     sqlite.MutationResult   `json:"trash_retry"`
	RestoreRetry   sqlite.MutationResult   `json:"restore_retry"`
	Todos          todoPersistenceExpected `json:"todos"`
}

type todoPersistenceExpected struct {
	Open      sqlite.TodoTask           `json:"open"`
	Completed sqlite.TodoTask           `json:"completed"`
	Undo      sqlite.TodoTask           `json:"undo"`
	Mutations []todoPersistenceMutation `json:"mutations"`
}

type todoPersistenceMutation struct {
	Key              string                    `json:"key"`
	Operation        string                    `json:"operation"`
	InputTask        sqlite.TodoTask           `json:"task"`
	ExpectedRevision int64                     `json:"expected_revision"`
	Completed        bool                      `json:"completed"`
	ResultSnapshot   sqlite.TodoMutationResult `json:"result"`
}

var syntheticIdentity = sqlite.ExternalIdentity{Issuer: "https://issuer.example.com/realms/container-test", Subject: "synthetic-container-owner"}

func TestContainerProcessAndPrivatePersistence(t *testing.T) {
	if os.Getenv("NEWSFEED_CONTAINER_TEST") != "seed" {
		t.Skip("requires explicit isolated container acceptance")
	}
	if os.Geteuid() != 65532 || os.Getegid() != 65532 {
		t.Fatal("container must run as uid/gid 65532")
	}
	info, err := os.Stat("/data")
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory must be private: %v", err)
	}
	if err := os.WriteFile("/tmp/newsfeed-must-stay-readonly", []byte("should fail"), 0o600); err == nil {
		t.Fatal("root filesystem accepted a write")
	}
	store, err := sqlite.Open(t.Context(), database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.EnrollAccount(t.Context(), syntheticIdentity)
	if err != nil {
		t.Fatal(err)
	}
	article := sqlite.Article{URL: "https://example.com/container-story", Title: "Synthetic container article", Summary: "Only local acceptance fixture data.", ThumbnailURL: "https://images.example.com/container.jpg", ContentMarkdown: "## Synthetic original\n\nPersisted source Markdown."}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	subjectFile := "/data/container-subject"
	seedFile := "/data/container-seed.json"
	if err := os.WriteFile(subjectFile, []byte(syntheticIdentity.Subject), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs, _ := json.Marshal([]feed.AddInput{{IdempotencyKey: "container-write", Article: feed.Content{URL: article.URL, Title: article.Title, Summary: article.Summary, ThumbnailURL: article.ThumbnailURL, ContentMarkdown: article.ContentMarkdown}}})
	if err := os.WriteFile(seedFile, inputs, 0o600); err != nil {
		t.Fatal(err)
	}
	seed := exec.CommandContext(t.Context(), "/newsfeed-admin", "seed", "--db", database, "--issuer", syntheticIdentity.Issuer, "--subject-file", subjectFile, "--articles-file", seedFile)
	seed.Env = []string{}
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("operator seed executable failed: %v %s", err, output)
	}
	store, err = sqlite.Open(t.Context(), database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	articles, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{}, 10)
	if err != nil || len(articles) != 1 {
		t.Fatal("operator seed did not persist the synthetic article")
	}
	payload, _ := json.Marshal(article)
	expected, err := store.MutateArticle(t.Context(), owner, "container-write", "add_article", payload, article, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared := expected.Article
	thumbnailPatch, err := store.MutateArticle(t.Context(), owner, "container-thumbnail", "patch_article", []byte("container-thumbnail"), prepared, 1, sqlite.ThumbnailChange{Replace: true, JPEG: containerThumbnail(t)})
	if err != nil || thumbnailPatch.Article.ThumbnailStatus != "stored" {
		t.Fatalf("stored thumbnail fixture: %v", err)
	}
	archived, err := store.SetArchived(t.Context(), owner, expected.Article.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(t.Context(), database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	trash, err := store.MutateArticle(t.Context(), owner, "container-trash", "trash_article", []byte("container-trash"), sqlite.Article{ID: archived.ID}, archived.Revision)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.MutateArticle(t.Context(), owner, "container-restore", "restore_article", []byte("container-restore"), sqlite.Article{ID: archived.ID}, trash.Article.Revision)
	if err != nil || restored.Article.ArchivedAt != archived.ArchivedAt {
		t.Fatal("restore did not preserve Archive fixture")
	}
	retained := article
	retained.URL = "https://example.com/container-retained-trash"
	retainedPayload, _ := json.Marshal(retained)
	added, err := store.MutateArticle(t.Context(), owner, "container-retained", "add_article", retainedPayload, retained, 0, sqlite.ThumbnailChange{Replace: true, JPEG: containerThumbnail(t)})
	if err != nil {
		t.Fatal(err)
	}
	trashed, err := store.MutateArticle(t.Context(), owner, "container-retained-trash", "trash_article", []byte("container-retained-trash"), sqlite.Article{ID: added.Article.ID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	todos := seedTodoPersistence(t, store, owner)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(persistenceExpected{Retry: expected, Current: restored.Article, Trashed: trashed.Article, ThumbnailPatch: thumbnailPatch, TrashRetry: trash, RestoreRetry: restored, Todos: todos})
	if err := os.WriteFile(expectedFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual production executable, without any identity bundle.
	for range 2 {
		stop := startApplication(t)
		assertFailClosed(t)
		stop()
		verifyPersistence(t)
	}
}

func TestContainerPersistedFixture(t *testing.T) {
	if os.Getenv("NEWSFEED_CONTAINER_TEST") != "verify" {
		t.Skip("requires explicit container stop/start verification")
	}
	assertFailClosed(t)
	verifyPersistence(t)
}

func TestTodoPersistenceFixtureReopensLocalDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todo-fixture.sqlite")
	store, err := sqlite.Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, err := store.EnrollAccount(t.Context(), syntheticIdentity)
	if err != nil {
		t.Fatal(err)
	}
	expected := seedTodoPersistence(t, store, owner)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	verifyTodoPersistence(t, store, owner, expected)
}

func startApplication(t *testing.T) func() {
	t.Helper()
	log, err := os.Create(filepath.Join(t.TempDir(), "application.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	command := exec.Command("/newsfeed")
	command.Env = []string{"NEWSFEED_HTTP_ADDR=:8091", "NEWSFEED_DB_PATH=" + database}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = command.Process.Signal(syscall.SIGTERM)
		finished := make(chan error, 1)
		go func() { finished <- command.Wait() }()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("production executable stopped unsuccessfully: %v", err)
			}
		case <-time.After(12 * time.Second):
			_ = command.Process.Kill()
			<-finished
			t.Error("production executable did not stop within its shutdown deadline")
		}
	}
	t.Cleanup(stop)
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := probe(t, "live"); err == nil {
			return stop
		}
		select {
		case <-deadline.C:
			t.Fatal("production executable did not become live")
		case <-ticker.C:
		}
	}
}

func probe(t *testing.T, kind string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/newsfeed", "healthcheck", kind)
	// A deliberately partial/invalid identity setting proves the probe does not
	// load credentials; the application process itself has no identity settings.
	command.Env = []string{"NEWSFEED_HTTP_ADDR=:8091", "NEWSFEED_WEB_CLIENT_SECRET_FILE=/never-read"}
	return command.Run()
}

func assertFailClosed(t *testing.T) {
	t.Helper()
	if err := probe(t, "live"); err != nil {
		t.Fatalf("live probe: %v", err)
	}
	if err := probe(t, "ready"); err == nil {
		t.Fatal("unconfigured identity incorrectly reported ready")
	} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		// A command execution failure must not masquerade as readiness denial.
		t.Fatalf("ready probe did not execute: %v", err)
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, path := range []string{"/", "/archive", "/trash", "/articles/synthetic", "/thumbnails/synthetic", "/health/ready", "/api/v1/articles", "/api/v1/articles/synthetic", "/todo", "/api/v1/todos", "/mcp"} {
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:8091"+path, nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured %s returned %d", path, response.StatusCode)
		}
	}
	for _, path := range []string{"/static/assets/app.js", "/static/assets/app.css"} {
		request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:8091"+path, nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || response.ContentLength < 100 {
			t.Fatalf("production frontend asset missing: %s", path)
		}
	}
}

func verifyPersistence(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatal(err)
	}
	var expected persistenceExpected
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), database, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := store.OwnerForIdentity(t.Context(), syntheticIdentity)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{}, 10)
	if err != nil || len(active) != 0 {
		t.Fatal("archived fixture reappeared in active feed")
	}
	articles, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{Archived: true}, 10)
	summary := expected.Current
	summary.ContentMarkdown = ""
	if err != nil || len(articles) != 1 || articles[0] != summary {
		t.Fatalf("durable article changed: %#v %v", articles, err)
	}
	current, err := store.Article(t.Context(), owner, expected.Current.ID)
	if err != nil || current != expected.Current || current.ContentMarkdown == "" || current.ArchivedAt == 0 {
		t.Fatal("archived original did not persist")
	}
	trashed, err := store.ArticleInView(t.Context(), owner, expected.Trashed.ID, true)
	if err != nil || trashed != expected.Trashed {
		t.Fatal("retained Trash fixture did not persist")
	}
	trashPage, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{Trashed: true}, 10)
	trashSummary := expected.Trashed
	trashSummary.ContentMarkdown = ""
	if err != nil || len(trashPage) != 1 || trashPage[0] != trashSummary {
		t.Fatal("retained Trash list did not persist")
	}
	for _, a := range []sqlite.Article{current, trashed} {
		body, err := store.Thumbnail(t.Context(), owner, a.ID, a.TrashedAt != 0)
		if err != nil || !bytes.Equal(body, containerThumbnail(t)) || len(body) > sqlite.MaxThumbnailBytes {
			t.Fatal("stored JPEG did not survive network-none restart")
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(body))
		sum := sha256.Sum256(body)
		if err != nil || cfg.Width > 640 || cfg.Height > 640 || a.ThumbnailHash != hex.EncodeToString(sum[:]) || a.ThumbnailStatus != "stored" {
			t.Fatal("stored JPEG metadata changed")
		}
	}
	if _, err := store.Thumbnail(t.Context(), owner, trashed.ID, false); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("ordinary thumbnail read included Trash")
	}
	if replay, err := store.MutateArticle(t.Context(), owner, "container-thumbnail", "patch_article", []byte("container-thumbnail"), sqlite.Article{ID: current.ID}, 1); err != nil || replay != expected.ThumbnailPatch {
		t.Fatal("stored thumbnail retry changed after restart")
	}
	if replay, err := store.MutateArticle(t.Context(), owner, "container-trash", "trash_article", []byte("container-trash"), sqlite.Article{ID: current.ID}, 3); err != nil || replay != expected.TrashRetry {
		t.Fatal("Trash retry changed after restore/restart")
	}
	if replay, err := store.MutateArticle(t.Context(), owner, "container-restore", "restore_article", []byte("container-restore"), sqlite.Article{ID: current.ID}, 4); err != nil || replay != expected.RestoreRetry {
		t.Fatal("Restore retry changed after restart")
	}
	if actual, err := store.Article(t.Context(), owner, current.ID); err != nil || actual != expected.Current {
		t.Fatal("historical retries reapplied state changes")
	}
	article := sqlite.Article{URL: "https://example.com/container-story", Title: "Synthetic container article", Summary: "Only local acceptance fixture data.", ThumbnailURL: "https://images.example.com/container.jpg", ContentMarkdown: "## Synthetic original\n\nPersisted source Markdown."}
	payload, _ := json.Marshal(article)
	replay, err := store.MutateArticle(t.Context(), owner, "container-write", "add_article", payload, article, 0)
	if err != nil || replay != expected.Retry {
		t.Fatalf("durable retry result changed: %#v %v", replay, err)
	}
	verifyTodoPersistence(t, store, owner, expected.Todos)
}

func containerThumbnail(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := jpeg.Encode(&output, image.NewRGBA(image.Rect(0, 0, 160, 80)), &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func seedTodoPersistence(t *testing.T, store *sqlite.Store, owner sqlite.Owner) todoPersistenceExpected {
	t.Helper()
	var expected todoPersistenceExpected
	mutate := func(key, operation string, task sqlite.TodoTask, revision int64, completed bool) sqlite.TodoTask {
		t.Helper()
		// Synthetic stable payloads exercise the same durable storage boundary;
		// host SDK tests separately prove normalization and authentication.
		result, err := store.MutateTodo(t.Context(), owner, key, operation, []byte(key), task, revision, completed)
		if err != nil {
			t.Fatalf("To-do persistence fixture %s: %v", key, err)
		}
		expected.Mutations = append(expected.Mutations, todoPersistenceMutation{
			Key: key, Operation: operation, InputTask: task,
			ExpectedRevision: revision, Completed: completed, ResultSnapshot: result,
		})
		return result.Task
	}
	open := mutate("container-todo-add", "add_todo", sqlite.TodoTask{Title: "Synthetic open task", Tags: []string{"Container", "Reading"}, EstimateMinutes: 120}, 0, false)
	completed := mutate("container-todo-complete", "set_todo_completed", sqlite.TodoTask{ID: open.ID}, open.Revision, true)
	if err := store.ChangeTodoState(t.Context(), owner, completed.ID, completed.Revision, sqlite.TodoReopen); err != nil {
		t.Fatal(err)
	}
	current, err := store.Todo(t.Context(), owner, open.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	removed := mutate("container-todo-remove", "remove_todo", sqlite.TodoTask{ID: current.ID}, current.Revision, false)
	expected.Open = mutate("container-todo-undo", "undo_todo_removal", sqlite.TodoTask{ID: removed.ID}, removed.Revision, false)
	finished := mutate("container-todo-finished-add", "add_todo", sqlite.TodoTask{Title: "Synthetic finished task", Tags: []string{"Container"}, EstimateMinutes: 30}, 0, false)
	expected.Completed = mutate("container-todo-finished", "set_todo_completed", sqlite.TodoTask{ID: finished.ID}, finished.Revision, true)
	pending := mutate("container-todo-pending-add", "add_todo", sqlite.TodoTask{Title: "Synthetic latest Undo", Tags: []string{"Container"}, EstimateMinutes: 15}, 0, false)
	pending = mutate("container-todo-pending-complete", "set_todo_completed", sqlite.TodoTask{ID: pending.ID}, pending.Revision, true)
	expected.Undo = mutate("container-todo-pending-remove", "remove_todo", sqlite.TodoTask{ID: pending.ID}, pending.Revision, false)
	return expected
}

func verifyTodoPersistence(t *testing.T, store *sqlite.Store, owner sqlite.Owner, expected todoPersistenceExpected) {
	t.Helper()
	want := sqlite.TodoList{Tasks: []sqlite.TodoTask{expected.Completed, expected.Open}, Undo: &expected.Undo}
	assertCurrent := func() {
		t.Helper()
		list, err := store.ListTodo(t.Context(), owner)
		if err != nil || !reflect.DeepEqual(list, want) || list.Tasks[0].CompletedAt == 0 || list.Tasks[1].CompletedAt != 0 || list.Undo.CompletedAt == 0 {
			t.Fatalf("open/completed/latest Undo did not persist: %#v %v", list, err)
		}
		for _, task := range []sqlite.TodoTask{expected.Open, expected.Completed} {
			if current, err := store.Todo(t.Context(), owner, task.ID, false); err != nil || !reflect.DeepEqual(current, task) {
				t.Fatal("visible task detail did not persist")
			}
		}
		if undo, err := store.Todo(t.Context(), owner, expected.Undo.ID, true); err != nil || !reflect.DeepEqual(undo, expected.Undo) {
			t.Fatal("explicit latest Undo detail did not persist")
		}
	}
	assertCurrent()
	for _, mutation := range expected.Mutations {
		replay, err := store.MutateTodo(t.Context(), owner, mutation.Key, mutation.Operation, []byte(mutation.Key), mutation.InputTask, mutation.ExpectedRevision, mutation.Completed)
		if err != nil || !reflect.DeepEqual(replay, mutation.ResultSnapshot) {
			t.Fatalf("To-do historical %s snapshot changed after restart: %v", mutation.Operation, err)
		}
	}
	// Old complete/remove/Undo results remain historical even after the
	// browser reopened the task and a different removal became the latest Undo.
	assertCurrent()
}
