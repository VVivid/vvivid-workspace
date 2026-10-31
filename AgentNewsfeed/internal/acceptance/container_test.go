// These tests run only as an explicitly requested local container acceptance
// binary. They use synthetic private records and never configure a live issuer.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
)

const database = "/data/newsfeed.sqlite"
const expectedFile = "/data/container-expected.json"

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
	article := sqlite.Article{URL: "https://example.com/container-story", Title: "Synthetic container article", Summary: "Only local acceptance fixture data."}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	subjectFile := "/data/container-subject"
	seedFile := "/data/container-seed.json"
	if err := os.WriteFile(subjectFile, []byte(syntheticIdentity.Subject), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs, _ := json.Marshal([]feed.AddInput{{IdempotencyKey: "container-write", Article: feed.Content{URL: article.URL, Title: article.Title, Summary: article.Summary}}})
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(expected)
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
	for _, path := range []string{"/", "/health/ready", "/api/v1/articles", "/mcp"} {
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
}

func verifyPersistence(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatal(err)
	}
	var expected sqlite.MutationResult
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
	articles, err := store.ListArticles(t.Context(), owner, sqlite.Cursor{}, 10)
	if err != nil || len(articles) != 1 || articles[0] != expected.Article {
		t.Fatalf("durable article changed: %#v %v", articles, err)
	}
	article := sqlite.Article{URL: "https://example.com/container-story", Title: "Synthetic container article", Summary: "Only local acceptance fixture data."}
	payload, _ := json.Marshal(article)
	replay, err := store.MutateArticle(t.Context(), owner, "container-write", "add_article", payload, article, 0)
	if err != nil || replay != expected {
		t.Fatalf("durable retry result changed: %#v %v", replay, err)
	}
}
