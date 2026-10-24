package daemon_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ingress"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/management"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var eventBody = []byte(`{"kind":"notice","message":"synthetic-notice","contact":"private-contact-marker","discarded":"private-original-only-marker"}`)

func TestExecutableSignedDeliveryMonitoringAndLatency(t *testing.T) {
	f := newFixture(t)
	f.cfg.WorkerPollMilliseconds = 250 // Match the daemon's production default.
	f.provision()
	p := f.start()
	token := f.token("agent", false)
	for _, test := range []struct {
		token  string
		status int
	}{{"", 401}, {"not-a-token", 401}, {f.token("agent", true), 401}, {f.token("ungranted", false), 403}} {
		status, _ := f.manage("monitor", "source-a", management.EmptyInput{}, test.token)
		if status != test.status {
			t.Fatalf("wrong OAuth credential status=%d want=%d", status, test.status)
		}
	}
	if status, _ := f.manage("monitor", "source-b", management.EmptyInput{}, token); status != 403 {
		t.Fatal("monitor leaked another source")
	}
	if status, _ := f.manage("monitor", "source-a", management.EmptyInput{}, f.token("agent", true)); status != 401 {
		t.Fatal("expired OAuth token accepted")
	}
	status, _, err := f.send("wrong-source-key", eventBody, []byte("different-synthetic-HMAC-credential"))
	if err != nil || status != 401 {
		t.Fatalf("wrong source credential status=%d err=%v", status, err)
	}
	f.mustSend("oversized-body", bytes.Repeat([]byte{'x'}, (1<<20)+1), 413)
	headerRequest, _ := http.NewRequestWithContext(t.Context(), "GET", "http://"+f.cfg.Listen+"/readyz", nil)
	headerRequest.Header.Set("X-Oversized", strings.Repeat("x", 64<<10))
	headerResponse, err := f.client.Do(headerRequest)
	if err != nil {
		t.Fatal(err)
	}
	headerResponse.Body.Close()
	if headerResponse.StatusCode != 431 {
		t.Fatalf("unbounded request headers: %d", headerResponse.StatusCode)
	}
	filtered := f.mustSend("filtered", []byte(`{"kind":"discard","message":"ignored","contact":"unused"}`), 202)
	if len(filtered.DeliveryIDs) != 0 {
		t.Fatal("filter queued an unmatched event")
	}
	accepted := f.mustSend("one", eventBody, 202)
	got := f.receive()
	if len(accepted.DeliveryIDs) != 1 || got.id != string(accepted.DeliveryIDs[0]) || got.authorization != string(f.outbound) || string(got.body) != `{"contact":"***","message":"synthetic-notice"}` {
		t.Fatal("delivery did not preserve sanitization, immutable header secret or idempotency ID")
	}
	f.waitState(accepted.DeliveryIDs[0], domain.DeliveryHTTPAccepted)
	duplicate := f.mustSend("one", eventBody, 202)
	if !duplicate.Duplicate || duplicate.EventID != accepted.EventID || !slices.Equal(duplicate.DeliveryIDs, accepted.DeliveryIDs) {
		t.Fatal("same raw bytes/key did not resolve original receipt")
	}
	f.mustSend("one", append(slices.Clone(eventBody), ' '), 409)
	store, err := sqlite.Open(f.cfg.DatabasePath, f.key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Original(t.Context(), accepted.EventID)
	store.Close()
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("original TTL 0 persisted bytes: %v", err)
	}
	// Modest load: 20 small, signed events, at most two concurrent ingress
	// requests, four delivery workers and one local consumer; no live network.
	type sent struct {
		response ingress.Response
		started  time.Time
		err      error
		status   int
	}
	sends := make(chan sent, 20)
	capacity := make(chan struct{}, 2)
	var tasks sync.WaitGroup
	for i := range 20 {
		tasks.Go(func() {
			capacity <- struct{}{}
			defer func() { <-capacity }()
			start := time.Now()
			status, response, err := f.send(fmt.Sprintf("load-%d", i), eventBody, f.inbound)
			sends <- sent{response: response, started: start, err: err, status: status}
		})
	}
	tasks.Wait()
	close(sends)
	starts := map[string]time.Time{}
	for item := range sends {
		if item.err != nil || item.status != 202 || len(item.response.DeliveryIDs) != 1 {
			t.Fatalf("load acceptance status=%d err=%v", item.status, item.err)
		}
		starts[string(item.response.DeliveryIDs[0])] = item.started
	}
	var maximum time.Duration
	for range 20 {
		got := f.receive()
		start, ok := starts[got.id]
		if !ok {
			t.Fatal("unexpected or duplicate receiver attempt under healthy load")
		}
		delete(starts, got.id)
		maximum = max(maximum, got.at.Sub(start))
	}
	if maximum >= time.Second {
		t.Fatalf("first dispatch target exceeded: %s", maximum)
	}
	t.Logf("20 events, 2 concurrent ingress requests, 4 workers: maximum ingress-to-consumer first dispatch %s", maximum)
	status, raw := f.manage("monitor", "source-a", management.EmptyInput{}, token)
	var metrics domain.SourceMetrics
	if status != 200 || json.Unmarshal(raw, &metrics) != nil || metrics.RetainedEvents != 22 || metrics.FirstDispatchCount != 21 || metrics.Ingress == nil || metrics.Ingress.AcceptedEvents != 22 || metrics.Ingress.DuplicateRequests != 1 || metrics.Ingress.RejectedRequests != 3 || metrics.CiphertextBytes <= 0 {
		t.Fatalf("source operational metrics incorrect: status=%d %s", status, raw)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic-process-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: f.cfg.OAuth.Resource, HTTPClient: &http.Client{Timeout: 3 * time.Second, Transport: bearerTransport{token: token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "monitor", Arguments: map[string]any{"source_id": "source-a", "input": map[string]any{}}})
	if err != nil || result.IsError || !bytes.Contains(encode(result.StructuredContent), []byte(`"first_dispatch_count":21`)) {
		t.Fatalf("MCP operational monitoring failed: %v", err)
	}
	result, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "monitor", Arguments: map[string]any{"source_id": "source-b", "input": map[string]any{}}})
	if err != nil || !result.IsError {
		t.Fatal("MCP monitor bypassed source grant")
	}
	session.Close()
	request, _ := http.NewRequestWithContext(t.Context(), "GET", "http://"+f.cfg.Listen+"/.well-known/oauth-protected-resource", nil)
	request.Host = "untrusted-host.invalid"
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !bytes.Contains(metadata, []byte(f.cfg.OAuth.Resource)) || bytes.Contains(metadata, []byte("untrusted-host")) {
		t.Fatal("resource metadata was not pinned")
	}
	p.stop()
}

func TestExecutableImmediateModeAndQueueOnlyReplay(t *testing.T) {
	f := newFixture(t)
	f.config.Source.OriginalTTL, f.config.Source.DeliveryTTL = 60, 0
	f.provision()
	p := f.start()
	accepted := f.mustSend("immediate", eventBody, 200)
	got := f.receive()
	if accepted.Mode != domain.ModeImmediate || accepted.ImmediateResults == nil || len(*accepted.ImmediateResults) != 1 || (*accepted.ImmediateResults)[0].State != domain.DeliveryHTTPAccepted || got.id != string(accepted.DeliveryIDs[0]) {
		t.Fatal("immediate native attempt failed")
	}
	duplicate := f.mustSend("immediate", eventBody, 200)
	if !duplicate.Duplicate || duplicate.EventID != accepted.EventID {
		t.Fatal("immediate duplicate did not retain receipt")
	}
	var queued int
	if err := f.db().QueryRow("SELECT count(*) FROM delivery_payloads").Scan(&queued); err != nil || queued != 0 {
		t.Fatal("TTL-zero delivery persisted a queue payload")
	}
	raw := encode(management.Request[management.ReplayInput]{SourceID: "source-a", ExpectedRevision: new(domain.Revision(2)), Input: management.ReplayInput{EventID: accepted.EventID, Revision: 2, IdempotencyKey: "replay-zero"}})
	req, _ := http.NewRequestWithContext(t.Context(), "POST", "http://"+f.cfg.Listen+"/v1/manage/events_replay", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token("agent", false))
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 422 || !bytes.Contains(body, []byte("durable_replay_required")) {
		t.Fatalf("TTL-zero replay status=%d body=%s", resp.StatusCode, body)
	}
	p.stop()
	select {
	case <-f.received:
		t.Fatal("TTL-zero duplicate/replay sent again")
	default:
	}
}

func TestExecutableRestartAndOnlineBackupRestore(t *testing.T) {
	f := newFixture(t)
	f.config.Source.OriginalTTL = 60
	f.consumerStatus.Store(503)
	f.provision()
	p := f.start()
	accepted := f.mustSend("recover-pending", eventBody, 202)
	first := f.receive()
	f.waitState(accepted.DeliveryIDs[0], domain.DeliveryPending)
	backup := filepath.Join(f.dir, "online.backup")
	f.mustAdmin("backup", "-destination", backup)
	if _, err := f.admin("backup", "-destination", backup); err == nil {
		t.Fatal("backup overwrote existing file")
	}
	backupBytes, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	f.noSecrets(backupBytes)
	p.stop()
	f.consumerStatus.Store(204)
	p = f.start()
	recovered := f.receive()
	if recovered.id != first.id || recovered.authorization != string(f.outbound) || !bytes.Equal(recovered.body, first.body) {
		t.Fatal("restart changed frozen delivery or credentials")
	}
	f.waitState(accepted.DeliveryIDs[0], domain.DeliveryHTTPAccepted)
	f.mustSend("recover-pending", eventBody, 202)
	p.stop()
	originalDB := f.cfg.DatabasePath
	f.cfg.DatabasePath = filepath.Join(f.dir, "restored.db")
	f.mustAdmin("restore", "-backup-file", backup)
	if _, err := f.admin("restore", "-backup-file", backup); err == nil {
		t.Fatal("restore overwrote existing file")
	}
	p = f.start()
	restored := f.receive()
	if restored.id != first.id || restored.authorization != string(f.outbound) || !bytes.Equal(restored.body, first.body) {
		t.Fatal("backup did not preserve encrypted pending work and secret version")
	}
	f.waitState(accepted.DeliveryIDs[0], domain.DeliveryHTTPAccepted)
	status, _ := f.manage("config_get", "source-a", management.EmptyInput{}, f.token("agent", false))
	if status != 200 {
		t.Fatal("restored management grants/configuration unavailable")
	}
	p.stop()
	store, err := sqlite.Open(f.cfg.DatabasePath, f.key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Original(t.Context(), accepted.EventID)
	store.Close()
	if err != nil || !bytes.Equal(raw, eventBody) {
		t.Fatal("backup lost encrypted original")
	}
	wrong := f.file("wrong.key", bytes.Repeat([]byte{0x42}, 32))
	oldKey := f.cfg.KeyFile
	f.cfg.KeyFile = wrong
	f.cfg.DatabasePath = filepath.Join(f.dir, "must-not-restore.db")
	if _, err := f.admin("restore", "-backup-file", backup); err == nil {
		t.Fatal("restore accepted wrong master key")
	}
	if _, err := os.Stat(f.cfg.DatabasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrong-key restore created destination")
	}
	f.cfg.DatabasePath = originalDB
	f.launch().wait(false)
	f.cfg.KeyFile = oldKey
	f.cfg.DigestKeyFile = wrong
	f.launch().wait(false)
}

func TestExecutableExpiryRetainsStatusThenCleansMetadata(t *testing.T) {
	f := newFixture(t)
	f.config.Source.OriginalTTL, f.config.Source.DeliveryTTL = 1, 1
	f.cfg.MetadataRetentionSeconds, f.cfg.DeduplicationRetentionSeconds = 3, 3
	f.consumerStatus.Store(-1)
	f.provision()
	p := f.start()
	accepted := f.mustSend("expires", eventBody, 202)
	f.receive()
	f.waitState(accepted.DeliveryIDs[0], domain.DeliveryExpired)
	store, err := sqlite.Open(f.cfg.DatabasePath, f.key)
	if err != nil {
		t.Fatal(err)
	}
	_, originalErr := store.Original(t.Context(), accepted.EventID)
	_, payloadErr := store.Delivery(t.Context(), accepted.DeliveryIDs[0])
	store.Close()
	if !errors.Is(originalErr, ports.ErrNotFound) || payloadErr != nil {
		t.Fatalf("expiry original=%v delivery metadata=%v", originalErr, payloadErr)
	}
	token := f.token("agent", false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := f.manage("events_get", "source-a", management.EventInput{EventID: accepted.EventID}, token)
		if status == 404 {
			p.stop()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cleanup did not remove expired retained metadata")
}

func TestExecutableSQLiteCapacityRollsBackWithoutFalseAcceptance(t *testing.T) {
	f := newFixture(t)
	f.config.Source.OriginalTTL = 60
	f.provision()
	var pages int64
	if err := f.db().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	f.cfg.MaxDatabasePages = pages + 16
	p := f.start()
	raw := encode(map[string]any{"kind": "notice", "message": "small", "contact": "private-contact-marker", "discarded": string(bytes.Repeat([]byte{'x'}, 512<<10))})
	f.mustSend("capacity", raw, 503)
	f.assertEmptyAcceptance()
	response, err := f.client.Get("http://" + f.cfg.Listen + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("capacity rejection left unhealthy queue")
	}
	p.stop()
	f.cfg.MaxDatabasePages = 0
	p = f.start()
	accepted := f.mustSend("capacity", raw, 202)
	got := f.receive()
	if got.id != string(accepted.DeliveryIDs[0]) {
		t.Fatal("failed acceptance left receipt or partial work")
	}
	p.stop()
}

func TestExecutableQueueFailureStopsServingAndShutdownCancelsSend(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint("queue-failure-", failure), func(t *testing.T) {
			f := newFixture(t)
			f.consumerStatus.Store(-1)
			f.cfg.CleanupIntervalSeconds = 3600
			f.provision()
			p := f.start()
			f.mustSend("inflight", eventBody, 202)
			f.receive()
			if failure {
				// A real schema/storage failure on an independent local SQLite
				// connection reaches the worker's Claim/Renew path, not a mock.
				if _, err := f.db().Exec("DROP TABLE deliveries"); err != nil {
					t.Fatal(err)
				}
				p.wait(false)
				if !bytes.Contains(p.output.Bytes(), []byte("required component failed")) {
					t.Fatal("daemon did not report safe fail-stop error")
				}
			} else {
				p.stop()
			}
			response, err := f.client.Get("http://" + f.cfg.Listen + "/readyz")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					t.Fatal("stopped workers still ready")
				}
			}
			status, _, err := f.send("after-stop", eventBody, f.inbound)
			if err == nil && status == 202 {
				t.Fatal("dead worker pool silently accepted work")
			}
		})
	}
}
