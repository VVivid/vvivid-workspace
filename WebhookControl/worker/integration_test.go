package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/clock"
	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ingress"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

const originalEvent = `{"message":"synthetic","contact":"synthetic private contact","private":"discard me"}`
const renderedEvent = `{"contact":"***","message":"synthetic"}`

type configurationReader struct{ config domain.Configuration }

func (r *configurationReader) ActiveConfiguration(context.Context, domain.ID) (domain.Configuration, error) {
	return r.config, nil
}

type syntheticSecrets struct{}

func (syntheticSecrets) ReadSecret(context.Context, domain.SecretRef) ([]byte, error) {
	return []byte("synthetic-source-authentication-token"), nil
}

func testConfiguration(urls []string, ttl domain.Retention, timeout int64) domain.Configuration {
	c := domain.Configuration{
		Source:    domain.Source{ID: "synthetic-source", Revision: 1, Enabled: true, OriginalTTL: 0, DeliveryTTL: ttl, Auth: domain.SourceAuth{Kind: domain.AuthBearer, Secret: domain.SecretRef{Name: "source-auth", Version: "v1"}}},
		Templates: []domain.Template{{ID: "template", Revision: 1, SourceID: "synthetic-source", Fields: map[string]string{"message": "event.message", "contact": "mask(event.contact)"}}},
		Routes:    []domain.Route{{ID: "route", Revision: 1, SourceID: "synthetic-source", Enabled: true, Template: domain.RevisionRef{ID: "template", Revision: 1}}},
	}
	for i, raw := range urls {
		id := domain.ID(fmt.Sprintf("receiver-%d", i))
		c.Transporters = append(c.Transporters, domain.Transporter{ID: id, Revision: 1, SourceID: c.Source.ID, Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: raw, TimeoutSeconds: timeout}})
		c.Routes[0].Transporters = append(c.Routes[0].Transporters, domain.RevisionRef{ID: id, Revision: 1})
	}
	return c
}

func enqueueHTTP(t *testing.T, store *sqlite.Store, policy *httptransport.Policy, testClock ports.Clock, urls []string, ttl domain.Retention, timeout int64) ingress.Response {
	t.Helper()
	reader := &configurationReader{config: testConfiguration(urls, ttl, timeout)}
	handler, err := ingress.New(ingress.Dependencies{Configurations: reader, Secrets: syntheticSecrets{}, Storage: store, Policy: policy, Clock: testClock}, ingress.Options{DigestKey: bytes.Repeat([]byte{0x5a}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/hooks/synthetic-source", bytes.NewBufferString(originalEvent))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "synthetic-event-key")
	r.Header.Set("Authorization", "Bearer synthetic-source-authentication-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	if response.Code != http.StatusAccepted {
		t.Fatalf("ingress acceptance: status %d", response.Code)
	}
	var accepted ingress.Response
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || len(accepted.DeliveryIDs) != len(urls) {
		t.Fatalf("acceptance IDs: %v", err)
	}
	// Subsequent edits to an active draft cannot alter already frozen jobs.
	reader.config.Templates[0].Fields["message"] = "'changed draft'"
	reader.config.Transporters[0].HTTP.URL = "https://unapproved.invalid/synthetic"
	return accepted
}

func openStore(t *testing.T, testClock ports.Clock) (*sqlite.Store, string, []byte) {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	store, err := sqlite.Open(path, key, sqlite.WithClock(testClock.Now))
	if err != nil {
		t.Fatal(err)
	}
	return store, path, key
}

func receiverTransport(t *testing.T, origin string) (*httptransport.Policy, *httptransport.Transporter) {
	t.Helper()
	policy, err := httptransport.NewPolicy([]httptransport.OriginRule{{Origin: origin, AllowPrivate: true}})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := httptransport.New(policy, syntheticSecrets{}, httptransport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return policy, transport
}

func startWorker(t *testing.T, queue ports.Queue, testClock ports.Clock, transport ports.Transporter, options Options) {
	t.Helper()
	w, err := New(Dependencies{Queue: queue, Clock: testClock, Transporter: transport}, options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("worker stopped: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("worker did not stop after cancellation")
		}
	})
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(4 * time.Second)
	for {
		if condition() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition did not become true")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func deliveryState(t *testing.T, store *sqlite.Store, id domain.ID) domain.Delivery {
	t.Helper()
	delivery, err := store.Delivery(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func TestLocalHTTPRetryFanoutAndRestartRecovery(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != renderedEvent || r.Header.Get("Content-Type") != "application/json" {
			t.Error("receiver got original or changed bytes")
		}
		mu.Lock()
		seen[r.URL.Path] = append(seen[r.URL.Path], r.Header.Get("Idempotency-Key"))
		attempt := len(seen[r.URL.Path])
		mu.Unlock()
		if r.URL.Path == "/flaky" && attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	testClock := clock.Real{}
	store, path, key := openStore(t, testClock)
	policy, transport := receiverTransport(t, server.URL)
	accepted := enqueueHTTP(t, store, policy, testClock, []string{server.URL + "/flaky", server.URL + "/independent"}, 60, 1)
	// Simulate death after Claim but before Send. Reopening loses the wakeup hint
	// and leaves the old lease for startup/timer recovery.
	abandoned, err := store.Claim(t.Context(), testClock.Now(), 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = sqlite.Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	startWorker(t, store, testClock, transport, Options{Concurrency: 2, PollInterval: 5 * time.Millisecond, LeaseDuration: 200 * time.Millisecond, RenewInterval: 40 * time.Millisecond, InitialBackoff: 300 * time.Millisecond, MaxBackoff: 300 * time.Millisecond})
	flakyID, independentID := accepted.DeliveryIDs[0], accepted.DeliveryIDs[1]
	await(t, func() bool {
		flaky := deliveryState(t, store, flakyID)
		independent := deliveryState(t, store, independentID)
		return flaky.State == domain.DeliveryPending && flaky.LastErrorCode == "http_retryable" && independent.State == domain.DeliveryHTTPAccepted
	})
	await(t, func() bool { return deliveryState(t, store, flakyID).State == domain.DeliveryHTTPAccepted })
	mu.Lock()
	defer mu.Unlock()
	if len(seen["/flaky"]) != 2 || seen["/flaky"][0] != string(flakyID) || seen["/flaky"][1] != string(flakyID) || len(seen["/independent"]) != 1 || seen["/independent"][0] != string(independentID) || flakyID == independentID {
		t.Fatal("retry IDs or independent fanout changed")
	}
	for _, id := range accepted.DeliveryIDs {
		got := deliveryState(t, store, id)
		wantAttempt := uint64(1)
		if id == flakyID {
			wantAttempt++
		}
		if id == abandoned.Delivery.ID {
			wantAttempt++
		}
		if got.Attempt != wantAttempt || got.HTTPStatusCode != 204 || string(got.Snapshot.Body) != renderedEvent {
			t.Fatalf("recovered frozen state: attempts=%d status=%d", got.Attempt, got.HTTPStatusCode)
		}
	}
}

func TestLocalHTTPRetryExhaustion(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	store, _, _ := openStore(t, clock.Real{})
	t.Cleanup(func() { store.Close() })
	policy, transport := receiverTransport(t, server.URL)
	accepted := enqueueHTTP(t, store, policy, clock.Real{}, []string{server.URL}, 60, 1)
	startWorker(t, store, clock.Real{}, transport, Options{Concurrency: 1, PollInterval: 5 * time.Millisecond, MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	await(t, func() bool { return deliveryState(t, store, accepted.DeliveryIDs[0]).State == domain.DeliveryFailed })
	got := deliveryState(t, store, accepted.DeliveryIDs[0])
	if got.Attempt != 3 || got.LastErrorCode != "retries_exhausted" || attempts.Load() != 3 {
		t.Fatalf("retry exhaustion: attempt=%d error=%s sends=%d", got.Attempt, got.LastErrorCode, attempts.Load())
	}
}

type trackingQueue struct {
	ports.Queue
	renewals    atomic.Int64
	transitions atomic.Int64
	loseLease   bool
}

func (q *trackingQueue) Renew(ctx context.Context, token ports.LeaseToken, now time.Time, duration time.Duration) (time.Time, error) {
	q.renewals.Add(1)
	if q.loseLease {
		return time.Time{}, ports.ErrLeaseLost
	}
	return q.Queue.Renew(ctx, token, now, duration)
}

func (q *trackingQueue) Complete(ctx context.Context, token ports.LeaseToken, now time.Time, status int) error {
	q.transitions.Add(1)
	return q.Queue.Complete(ctx, token, now, status)
}

func (q *trackingQueue) Retry(ctx context.Context, token ports.LeaseToken, now, next time.Time, code string) error {
	q.transitions.Add(1)
	return q.Queue.Retry(ctx, token, now, next, code)
}

func (q *trackingQueue) Terminate(ctx context.Context, token ports.LeaseToken, now time.Time, code string) error {
	q.transitions.Add(1)
	return q.Queue.Terminate(ctx, token, now, code)
}

func TestAmbiguousAcceptanceRetriesStableIDAndReceiverDeduplicates(t *testing.T) {
	var mu sync.Mutex
	acceptedIDs := map[string]bool{}
	var attempts, acceptedCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != renderedEvent {
			t.Error("retry body changed")
		}
		mu.Lock()
		id := r.Header.Get("Idempotency-Key")
		if !acceptedIDs[id] {
			acceptedIDs[id] = true
			acceptedCount.Add(1)
		}
		mu.Unlock()
		if attempts.Add(1) == 1 {
			// Receiver has applied the effect, but the acknowledgement is lost.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(server.Close)
	store, _, _ := openStore(t, clock.Real{})
	t.Cleanup(func() { store.Close() })
	policy, transport := receiverTransport(t, server.URL)
	accepted := enqueueHTTP(t, store, policy, clock.Real{}, []string{server.URL}, 60, 1)
	queue := &trackingQueue{Queue: store}
	startWorker(t, queue, clock.Real{}, transport, Options{Concurrency: 2, LeaseDuration: 200 * time.Millisecond, RenewInterval: 40 * time.Millisecond, PollInterval: 5 * time.Millisecond, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	await(t, func() bool {
		return deliveryState(t, store, accepted.DeliveryIDs[0]).State == domain.DeliveryHTTPAccepted
	})
	if attempts.Load() != 2 || acceptedCount.Load() != 1 || queue.renewals.Load() < 3 || deliveryState(t, store, accepted.DeliveryIDs[0]).Attempt != 2 {
		t.Fatalf("ambiguous acceptance: sends=%d effects=%d renewals=%d", attempts.Load(), acceptedCount.Load(), queue.renewals.Load())
	}
}

func TestLeaseLossCancelsLocalHTTPRequestWithoutTransition(t *testing.T) {
	canceled := make(chan struct{}, 1)
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		sends.Add(1)
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	t.Cleanup(server.Close)
	store, _, _ := openStore(t, clock.Real{})
	t.Cleanup(func() { store.Close() })
	policy, transport := receiverTransport(t, server.URL)
	enqueueHTTP(t, store, policy, clock.Real{}, []string{server.URL}, 60, 2)
	queue := &trackingQueue{Queue: store, loseLease: true}
	startWorker(t, queue, clock.Real{}, transport, Options{Concurrency: 1, LeaseDuration: time.Second, RenewInterval: 20 * time.Millisecond, PollInterval: 5 * time.Millisecond})
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("lost lease did not cancel receiver request")
	}
	if queue.transitions.Load() != 0 || sends.Load() != 1 {
		t.Fatal("lost owner transitioned or resent the delivery")
	}
}

type adjustableClock struct{ nanos atomic.Int64 }

func (c *adjustableClock) Now() time.Time                       { return time.Unix(0, c.nanos.Load()).UTC() }
func (*adjustableClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func TestExpiredPayloadMakesNoHTTPRequest(t *testing.T) {
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sends.Add(1) }))
	t.Cleanup(server.Close)
	testClock := new(adjustableClock)
	testClock.nanos.Store(time.Now().UnixNano())
	store, _, _ := openStore(t, testClock)
	t.Cleanup(func() { store.Close() })
	policy, transport := receiverTransport(t, server.URL)
	accepted := enqueueHTTP(t, store, policy, testClock, []string{server.URL}, 1, 1)
	testClock.nanos.Add(int64(2 * time.Second))
	startWorker(t, store, testClock, transport, Options{Concurrency: 1, PollInterval: 5 * time.Millisecond})
	await(t, func() bool { return deliveryState(t, store, accepted.DeliveryIDs[0]).State == domain.DeliveryExpired })
	if sends.Load() != 0 || deliveryState(t, store, accepted.DeliveryIDs[0]).Attempt != 0 {
		t.Fatal("expired job was claimed or sent")
	}
}

func TestPayloadExpiryCancelsActiveHTTPRequest(t *testing.T) {
	canceled := make(chan struct{}, 1)
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		sends.Add(1)
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	t.Cleanup(server.Close)
	store, _, _ := openStore(t, clock.Real{})
	t.Cleanup(func() { store.Close() })
	policy, transport := receiverTransport(t, server.URL)
	accepted := enqueueHTTP(t, store, policy, clock.Real{}, []string{server.URL}, 1, 2)
	startWorker(t, store, clock.Real{}, transport, Options{Concurrency: 2, LeaseDuration: 2 * time.Second, RenewInterval: 50 * time.Millisecond, PollInterval: 5 * time.Millisecond})
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("payload expiry did not cancel the receiver request")
	}
	await(t, func() bool { return deliveryState(t, store, accepted.DeliveryIDs[0]).State == domain.DeliveryExpired })
	if sends.Load() != 1 {
		t.Fatal("expired payload was sent again")
	}
}
