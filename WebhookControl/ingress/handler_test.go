package ingress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/engine"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

var testNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

const sourceToken = "synthetic-source-authentication-token-for-tests"
const incoming = `{"sender":"allowed","message":"synthetic notice","private":"synthetic-ingress-private-marker"}`

type fakeClock struct{ nanos atomic.Int64 }

func (c *fakeClock) Now() time.Time                       { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *fakeClock) After(time.Duration) <-chan time.Time { return nil }

type fakeConfigurations struct {
	mu     sync.Mutex
	config domain.Configuration
}

func (f *fakeConfigurations) ActiveConfiguration(_ context.Context, id domain.ID) (domain.Configuration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.config.Source.ID {
		return domain.Configuration{}, ports.ErrNotFound
	}
	data, _ := json.Marshal(f.config)
	var copied domain.Configuration
	if err := json.Unmarshal(data, &copied); err != nil {
		return domain.Configuration{}, err
	}
	return copied, nil
}

func (f *fakeConfigurations) change(mutate func(*domain.Configuration)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate(&f.config)
}

type fakeSecrets struct {
	mu   sync.Mutex
	refs []domain.SecretRef
}

func (s *fakeSecrets) ReadSecret(_ context.Context, ref domain.SecretRef) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs = append(s.refs, ref)
	if ref != (domain.SecretRef{Name: "source-auth", Version: "v1"}) {
		return nil, ports.ErrNotFound
	}
	return []byte(sourceToken), nil
}

type fakePolicy struct{ denied bool }

func (p fakePolicy) ApproveDestination(context.Context, domain.ID, domain.HTTPConfig) error {
	if p.denied {
		return ports.ErrPolicyDenied
	}
	return nil
}

type fakeTransporter struct {
	mu                  sync.Mutex
	requests            []ports.SendRequest
	result              ports.SendResult
	err                 error
	waitForCancellation bool
}

func (f *fakeTransporter) Send(ctx context.Context, request ports.SendRequest) (ports.SendResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	if f.waitForCancellation {
		<-ctx.Done()
		return ports.SendResult{}, ctx.Err()
	}
	return f.result, f.err
}

func (f *fakeTransporter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type observedStore struct {
	*sqlite.Store
	calls      atomic.Int64
	mu         sync.Mutex
	last       ports.AcceptRequest
	failAccept bool
	failRecord bool
}

func (s *observedStore) Accept(ctx context.Context, request ports.AcceptRequest) (ports.Acceptance, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.last = request
	s.mu.Unlock()
	if s.failAccept {
		return ports.Acceptance{}, errors.New("synthetic-private-storage-error")
	}
	return s.Store.Accept(ctx, request)
}

func (s *observedStore) RecordImmediate(ctx context.Context, result ports.ImmediateResult) error {
	if s.failRecord {
		return errors.New("synthetic-private-storage-error")
	}
	return s.Store.RecordImmediate(ctx, result)
}

func basicConfiguration() domain.Configuration {
	return domain.Configuration{
		Source:       domain.Source{ID: "synthetic", Revision: 1, Enabled: true, Auth: domain.SourceAuth{Kind: domain.AuthBearer, Secret: domain.SecretRef{Name: "source-auth", Version: "v1"}}, OriginalTTL: 0, DeliveryTTL: 120},
		Rules:        []domain.Rule{{ID: "sender", Revision: 2, SourceID: "synthetic", Expression: "event.sender == 'allowed'"}},
		Templates:    []domain.Template{{ID: "notice", Revision: 3, SourceID: "synthetic", Fields: map[string]string{"message": "event.message", "masked": "mask(event.private)"}}},
		Routes:       []domain.Route{{ID: "route", Revision: 4, SourceID: "synthetic", Enabled: true, Rules: []domain.RevisionRef{{ID: "sender", Revision: 2}}, Template: domain.RevisionRef{ID: "notice", Revision: 3}, Transporters: []domain.RevisionRef{{ID: "receiver", Revision: 5}}}},
		Transporters: []domain.Transporter{{ID: "receiver", Revision: 5, SourceID: "synthetic", Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: "https://receiver.example.test/hook", TimeoutSeconds: 10, SecretHeaders: map[string]domain.SecretRef{"Authorization": {Name: "receiver-auth", Version: "v2"}}}}},
	}
}

type fixture struct {
	handler     *Handler
	config      *fakeConfigurations
	secrets     *fakeSecrets
	store       *observedStore
	transporter *fakeTransporter
	clock       *fakeClock
	path        string
}

func setup(t *testing.T, config domain.Configuration, options Options, policy fakePolicy) *fixture {
	t.Helper()
	clock := new(fakeClock)
	clock.nanos.Store(testNow.UnixNano())
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "synthetic.sqlite")
	db, err := sqlite.Open(path, key, sqlite.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := &observedStore{Store: db}
	configurations := &fakeConfigurations{config: config}
	secrets := new(fakeSecrets)
	transporter := &fakeTransporter{result: ports.SendResult{StatusCode: 204}}
	if len(options.DigestKey) == 0 {
		options.DigestKey = make([]byte, 32)
		if _, err := rand.Read(options.DigestKey); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := New(Dependencies{Configurations: configurations, Secrets: secrets, Storage: store, Policy: policy, Clock: clock, Transporter: transporter}, options)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{handler: handler, config: configurations, secrets: secrets, store: store, transporter: transporter, clock: clock, path: path}
}

func bearerRequest(body, key string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/hooks/synthetic", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+sourceToken)
	r.Header.Set("Idempotency-Key", key)
	return r
}

func serve(t *testing.T, h *Handler, request *http.Request, expectedStatus int) (*httptest.ResponseRecorder, Response) {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != expectedStatus {
		t.Fatalf("status = %d; want %d; body = %s", response.Code, expectedStatus, response.Body)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatal("response is not JSON")
	}
	var accepted Response
	if expectedStatus == 200 || expectedStatus == 202 || expectedStatus == 502 {
		if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
			t.Fatal(err)
		}
	}
	return response, accepted
}

func TestDurableIngressFilteringFanoutAndSnapshot(t *testing.T) {
	config := basicConfiguration()
	second := config.Transporters[0]
	second.ID, second.HTTP.URL = "receiver-b", "https://second.example.test/hook"
	config.Transporters = append(config.Transporters, second)
	config.Routes[0].Transporters = append(config.Routes[0].Transporters, domain.RevisionRef{ID: second.ID, Revision: second.Revision})
	otherRoute := config.Routes[0]
	otherRoute.ID, otherRoute.Transporters = "route-b", []domain.RevisionRef{{ID: "receiver", Revision: 5}}
	config.Routes = append(config.Routes, otherRoute)
	f := setup(t, config, Options{}, fakePolicy{})
	_, accepted := serve(t, f.handler, bearerRequest(incoming, "fanout"), 202)
	if accepted.Mode != domain.ModeDurable || accepted.Duplicate || len(accepted.DeliveryIDs) != 3 || f.store.calls.Load() != 1 || f.transporter.count() != 0 {
		t.Fatalf("acceptance = %+v", accepted)
	}
	if f.store.last.Original != nil {
		t.Fatal("original TTL zero passed bytes to Storage")
	}
	if _, err := f.store.Original(t.Context(), accepted.EventID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("original was stored: %v", err)
	}
	f.config.change(func(c *domain.Configuration) {
		c.Source.Revision++
		c.Templates[0].Fields = map[string]string{"private": "event.private"}
		c.Transporters[0].HTTP.URL = "https://changed.example.test/hook"
		c.Transporters[0].HTTP.SecretHeaders["Authorization"] = domain.SecretRef{Name: "changed", Version: "v99"}
	})
	for _, id := range accepted.DeliveryIDs {
		delivery, err := f.store.Delivery(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if string(delivery.Snapshot.Body) != `{"masked":"***","message":"synthetic notice"}` || delivery.Snapshot.Source.Revision != 1 || delivery.Snapshot.Transporter.HTTP.SecretHeaders["Authorization"].Version != "v2" || strings.Contains(delivery.Snapshot.Transporter.HTTP.URL, "changed") {
			t.Fatalf("frozen delivery changed: %+v", delivery)
		}
		if delivery.ExpiresAt == nil || !delivery.ExpiresAt.Equal(testNow.Add(120*time.Second)) || !delivery.CreatedAt.Equal(testNow) {
			t.Fatal("clock/TTL was not normalized")
		}
	}
	for _, ref := range f.secrets.refs {
		if ref.Name != "source-auth" {
			t.Fatal("ingress resolved receiver credentials")
		}
	}
	_, ignored := serve(t, f.handler, bearerRequest(strings.Replace(incoming, "allowed", "other", 1), "no-match"), 202)
	if len(ignored.DeliveryIDs) != 0 {
		t.Fatal("unmatched sender created jobs")
	}
	if _, err := f.store.Event(t.Context(), ignored.EventID); err != nil {
		t.Fatal("no-match event was not committed")
	}
}

func TestReceiptsSurviveConfigurationChangesAndConflict(t *testing.T) {
	f := setup(t, basicConfiguration(), Options{}, fakePolicy{})
	_, first := serve(t, f.handler, bearerRequest(incoming, "receipt"), 202)
	f.config.change(func(c *domain.Configuration) { c.Source.DeliveryTTL = 0; c.Templates[0].Fields["broken"] = "event." })
	_, duplicate := serve(t, f.handler, bearerRequest(incoming, "receipt"), 202)
	if !duplicate.Duplicate || duplicate.Mode != domain.ModeDurable || duplicate.EventID != first.EventID || !reflect.DeepEqual(duplicate.DeliveryIDs, first.DeliveryIDs) || f.store.calls.Load() != 1 {
		t.Fatalf("receipt changed: %+v", duplicate)
	}
	serve(t, f.handler, bearerRequest(incoming+" ", "receipt"), 409)
	serve(t, f.handler, bearerRequest(incoming, "new-key"), 422)
	if f.transporter.count() != 0 {
		t.Fatal("duplicate/new invalid config sent")
	}
}

func TestHMACBindingTimestampAndReplay(t *testing.T) {
	config := basicConfiguration()
	config.Source.Auth.Kind = domain.AuthHMAC
	f := setup(t, config, Options{}, fakePolicy{})
	signed := func(source domain.ID, key, timestamp, body string) *http.Request {
		r := bearerRequest(body, key)
		r.Header.Del("Authorization")
		r.Header.Set("X-Webhook-Timestamp", timestamp)
		r.Header.Set("X-Webhook-Signature", HMACSignature([]byte(sourceToken), source, key, timestamp, []byte(body)))
		return r
	}
	timestamp := strconv.FormatInt(testNow.Unix(), 10)
	_, accepted := serve(t, f.handler, signed("synthetic", "signed", timestamp, incoming), 202)
	_, duplicate := serve(t, f.handler, signed("synthetic", "signed", timestamp, incoming), 202)
	if !duplicate.Duplicate || duplicate.EventID != accepted.EventID {
		t.Fatal("signed replay did not deduplicate")
	}
	for name, mutate := range map[string]func(*http.Request){
		"changed key":         func(r *http.Request) { r.Header.Set("Idempotency-Key", "other") },
		"changed raw bytes":   func(r *http.Request) { r.Body = http.NoBody },
		"changed timestamp":   func(r *http.Request) { r.Header.Set("X-Webhook-Timestamp", strconv.FormatInt(testNow.Unix()+1, 10)) },
		"duplicate signature": func(r *http.Request) { r.Header.Add("X-Webhook-Signature", r.Header.Get("X-Webhook-Signature")) },
	} {
		t.Run(name, func(t *testing.T) {
			r := signed("synthetic", "signed", timestamp, incoming)
			mutate(r)
			serve(t, f.handler, r, 401)
		})
	}
	serve(t, f.handler, signed("different-source", "source-bound", timestamp, incoming), 401)
	serve(t, f.handler, signed("synthetic", "stale", strconv.FormatInt(testNow.Add(-MaxTimestampSkew-time.Second).Unix(), 10), incoming), 401)
	serve(t, f.handler, signed("synthetic", "future", strconv.FormatInt(testNow.Add(MaxTimestampSkew+time.Second).Unix(), 10), incoming), 401)
	serve(t, f.handler, signed("synthetic", "noncanonical", "0"+timestamp, incoming), 401)
}

func TestCanonicalHMACVector(t *testing.T) {
	// Independently generated HMAC-SHA256 vector for the documented byte format.
	got := HMACSignature([]byte(sourceToken), "synthetic", "signed", "1792843200", []byte(`{"example":true}`))
	if got != "sha256=6f1a0bc1a4349c55e7ac8c0fa5159b9922bfcf3500ac723131a735fe1992293c" {
		t.Fatalf("canonical signature = %s", got)
	}
}

func TestExplicitRetentionValues(t *testing.T) {
	config := basicConfiguration()
	config.Source.OriginalTTL, config.Source.DeliveryTTL = 60, domain.KeepIndefinitely
	f := setup(t, config, Options{}, fakePolicy{})
	_, accepted := serve(t, f.handler, bearerRequest(incoming, "retained"), 202)
	original, err := f.store.Original(t.Context(), accepted.EventID)
	if err != nil || string(original) != incoming {
		t.Fatalf("permitted original missing: %v", err)
	}
	delivery, err := f.store.Delivery(t.Context(), accepted.DeliveryIDs[0])
	if err != nil || delivery.TTL != domain.KeepIndefinitely || delivery.ExpiresAt != nil {
		t.Fatalf("indefinite delivery: %+v, %v", delivery, err)
	}
	f.clock.nanos.Store(testNow.Add(time.Minute).UnixNano())
	if _, err := f.store.Original(t.Context(), accepted.EventID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("expired original visible")
	}
	if delivery, err := f.store.Delivery(t.Context(), accepted.DeliveryIDs[0]); err != nil || len(delivery.Snapshot.Body) == 0 {
		t.Fatal("original expiry removed sanitized delivery")
	}
}

func TestSafeErrorsRejectBeforeAcceptance(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		change func(*fixture, *http.Request)
	}{
		{name: "bad bearer", status: 401, change: func(_ *fixture, r *http.Request) {
			r.Header.Set("Authorization", "Bearer synthetic-private-auth-failure")
		}},
		{name: "missing key", status: 400, change: func(_ *fixture, r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{name: "duplicate key header", status: 400, change: func(_ *fixture, r *http.Request) { r.Header.Add("Idempotency-Key", "another") }},
		{name: "key too long", status: 400, change: func(_ *fixture, r *http.Request) {
			r.Header.Set("Idempotency-Key", strings.Repeat("k", MaxIdempotencyKeyBytes+1))
		}},
		{name: "key contains space", status: 400, change: func(_ *fixture, r *http.Request) { r.Header.Set("Idempotency-Key", "not visible") }},
		{name: "wrong content type", status: 400, change: func(_ *fixture, r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{name: "oversized chunked body", status: 413, body: strings.Repeat("a", engine.MaxEventBytes+1), change: func(_ *fixture, r *http.Request) { r.ContentLength = -1 }},
		{name: "array", status: 400, body: `[]`},
		{name: "null", status: 400, body: `null`},
		{name: "trailing object", status: 400, body: `{} {}`},
		{name: "malformed JSON", status: 400, body: `{"synthetic-private-parser-error"`},
		{name: "disabled", status: 403, change: func(f *fixture, _ *http.Request) {
			f.config.change(func(c *domain.Configuration) { c.Source.Enabled = false })
		}},
		{name: "template failure", status: 422, change: func(f *fixture, _ *http.Request) {
			f.config.change(func(c *domain.Configuration) { c.Templates[0].Fields["bad"] = "event.missing" })
		}},
		{name: "evaluation limit", status: 422, body: `{"large":"` + strings.Repeat("a", 100000) + `"}`, change: func(f *fixture, _ *http.Request) {
			f.config.change(func(c *domain.Configuration) { c.Rules[0].Expression = "event.large.contains('z')" })
		}},
		{name: "storage failure", status: 503, change: func(f *fixture, _ *http.Request) { f.store.failAccept = true }},
		{name: "denied policy", status: 403, change: func(f *fixture, _ *http.Request) { f.handler.dependencies.Policy = fakePolicy{denied: true} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t, basicConfiguration(), Options{}, fakePolicy{})
			body := test.body
			if body == "" {
				body = incoming
			}
			r := bearerRequest(body, "error")
			if test.change != nil {
				test.change(f, r)
			}
			response, _ := serve(t, f.handler, r, test.status)
			if strings.Contains(response.Body.String(), "synthetic-private") || strings.Contains(response.Body.String(), "synthetic-ingress-private") || strings.Contains(response.Body.String(), sourceToken) {
				t.Fatal("unsafe error response")
			}
			if test.name != "storage failure" && f.store.calls.Load() != 0 {
				t.Fatal("rejected input reached acceptance")
			}
			if f.transporter.count() != 0 {
				t.Fatal("rejected input sent")
			}
		})
	}
}

func TestImmediateModeHasOnlyMetadataAndNeverResends(t *testing.T) {
	config := basicConfiguration()
	config.Source.DeliveryTTL = 0
	f := setup(t, config, Options{}, fakePolicy{})
	_, first := serve(t, f.handler, bearerRequest(incoming, "immediate"), 200)
	if first.Mode != domain.ModeImmediate || first.ImmediateResults == nil || len(*first.ImmediateResults) != 1 || (*first.ImmediateResults)[0].State != domain.DeliveryHTTPAccepted || f.transporter.count() != 1 || f.store.calls.Load() != 1 {
		t.Fatalf("immediate response: %+v", first)
	}
	if f.store.last.Original != nil || len(f.store.last.Deliveries) != 0 {
		t.Fatal("TTL zero passed payloads to storage")
	}
	delivery, err := f.store.Delivery(t.Context(), first.DeliveryIDs[0])
	if err != nil || delivery.TTL != 0 || len(delivery.Snapshot.Body) != 0 || delivery.State != domain.DeliveryHTTPAccepted || delivery.HTTPStatusCode != 204 {
		t.Fatalf("immediate metadata: %+v, %v", delivery, err)
	}
	if _, err := f.store.Claim(t.Context(), testNow, time.Second); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("immediate delivery was queued: %v", err)
	}
	if _, err := f.store.Original(t.Context(), first.EventID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("TTL-zero original exists")
	}
	for _, suffix := range []string{"", "-wal"} {
		pages, err := os.ReadFile(f.path + suffix)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		for _, marker := range []string{"synthetic notice", "synthetic-ingress-private-marker", "receiver.example.test", sourceToken} {
			if bytes.Contains(pages, []byte(marker)) {
				t.Fatalf("immediate bytes stored in %s", suffix)
			}
		}
	}
	f.config.change(func(c *domain.Configuration) { c.Source.DeliveryTTL = 120; c.Templates[0].Fields["broken"] = "event." })
	_, duplicate := serve(t, f.handler, bearerRequest(incoming, "immediate"), 200)
	if !duplicate.Duplicate || duplicate.Mode != domain.ModeImmediate || duplicate.EventID != first.EventID || !reflect.DeepEqual(duplicate.DeliveryIDs, first.DeliveryIDs) || f.transporter.count() != 1 {
		t.Fatalf("immediate duplicate: %+v", duplicate)
	}
}

func TestImmediateFailureUnknownOutcomeAndTotalBudget(t *testing.T) {
	config := basicConfiguration()
	config.Source.DeliveryTTL = 0
	t.Run("known failure", func(t *testing.T) {
		f := setup(t, config, Options{}, fakePolicy{})
		f.transporter.err = errors.New("synthetic-private-network-error")
		response, first := serve(t, f.handler, bearerRequest(incoming, "failed"), 502)
		if strings.Contains(response.Body.String(), "synthetic-private") || (*first.ImmediateResults)[0].ErrorCode != "network_error" {
			t.Fatal("unsafe failure result")
		}
		_, duplicate := serve(t, f.handler, bearerRequest(incoming, "failed"), 502)
		if !duplicate.Duplicate || f.transporter.count() != 1 {
			t.Fatal("failed immediate delivery sent again")
		}
	})
	t.Run("crash before outcome", func(t *testing.T) {
		f := setup(t, config, Options{}, fakePolicy{})
		f.store.failRecord = true
		serve(t, f.handler, bearerRequest(incoming, "ambiguous"), 503)
		_, duplicate := serve(t, f.handler, bearerRequest(incoming, "ambiguous"), 200)
		if !duplicate.Duplicate || (*duplicate.ImmediateResults)[0].State != domain.DeliveryUnknown || f.transporter.count() != 1 {
			t.Fatal("unknown immediate attempt sent again")
		}
	})
	t.Run("total time budget", func(t *testing.T) {
		second := config.Routes[0]
		second.ID = "route-b"
		budgetConfig := config
		budgetConfig.Routes = append([]domain.Route{config.Routes[0]}, second)
		f := setup(t, budgetConfig, Options{ImmediateBudget: 10 * time.Millisecond}, fakePolicy{})
		f.transporter.waitForCancellation = true
		_, first := serve(t, f.handler, bearerRequest(incoming, "budget"), 502)
		if f.transporter.count() != 1 || len(*first.ImmediateResults) != 2 || (*first.ImmediateResults)[1].State != domain.DeliveryUnknown {
			t.Fatal("total budget did not stop further sends")
		}
		serve(t, f.handler, bearerRequest(incoming, "budget"), 502)
		if f.transporter.count() != 1 {
			t.Fatal("budget-exhausted delivery resent")
		}
	})
}

func TestConcurrentImmediateRetriesSendOnce(t *testing.T) {
	config := basicConfiguration()
	config.Source.DeliveryTTL = 0
	f := setup(t, config, Options{}, fakePolicy{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			response := httptest.NewRecorder()
			f.handler.ServeHTTP(response, bearerRequest(incoming, "concurrent"))
			if response.Code != 200 {
				t.Errorf("concurrent status = %d: %s", response.Code, response.Body)
			}
		})
	}
	workers.Wait()
	if f.transporter.count() != 1 {
		t.Fatalf("sent %d times", f.transporter.count())
	}
}

func TestSourceRateLimitAndConfiguredDedupWindow(t *testing.T) {
	f := setup(t, basicConfiguration(), Options{RequestsPerMinute: 2, DeduplicationWindow: time.Minute}, fakePolicy{})
	_, first := serve(t, f.handler, bearerRequest(incoming, "limited"), 202)
	bad := bearerRequest(incoming, "unauthorized")
	bad.Header.Set("Authorization", "Bearer invalid-synthetic-token")
	serve(t, f.handler, bad, 401)
	serve(t, f.handler, bearerRequest(incoming, "limited"), 429)
	f.clock.nanos.Store(testNow.Add(time.Minute).UnixNano())
	_, next := serve(t, f.handler, bearerRequest(incoming, "limited"), 202)
	if next.Duplicate || first.EventID == next.EventID {
		t.Fatal("expired dedup receipt reused")
	}
	dependencies := f.handler.dependencies
	if _, err := New(dependencies, Options{DigestKey: make([]byte, 32), DeduplicationWindow: 8 * 24 * time.Hour}); !errors.Is(err, ErrOptions) {
		t.Fatalf("oversized dedup window accepted: %v", err)
	}
}

func TestLimiterMemoryBound(t *testing.T) {
	limiter := sourceLimiter{maximum: 1, windows: map[domain.ID]sourceWindow{}}
	for i := range MaxTrackedSources {
		if !limiter.allow(domain.ID(fmt.Sprint(i)), testNow) {
			t.Fatal("early rate rejection")
		}
	}
	if limiter.allow("excess", testNow) || len(limiter.windows) != MaxTrackedSources {
		t.Fatal("limiter memory is not bounded")
	}
	if !limiter.allow("excess", testNow.Add(time.Minute)) {
		t.Fatal("expired slots not reclaimed")
	}
}
