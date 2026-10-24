package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/engine"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/internal/sqlite"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
	"github.com/google/jsonschema-go/jsonschema"
)

type fixture struct {
	t       *testing.T
	store   *sqlite.Store
	service *Service
	actor   Identity
	grant   domain.Grant
	config  domain.Configuration
	now     time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), actor: Identity{"https://issuer.example", "synthetic-subject"}}
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"), bytes.Repeat([]byte{7}, 32), sqlite.WithClock(func() time.Time { return f.now }))
	if err != nil {
		t.Fatal(err)
	}
	f.store = s
	t.Cleanup(func() { s.Close() })
	for _, item := range []struct {
		ref     domain.SecretRef
		purpose domain.SecretPurpose
		value   []byte
	}{
		{domain.SecretRef{Name: "inbound", Version: "1"}, domain.SecretSourceAuth, bytes.Repeat([]byte("synthetic-inbound-"), 2)},
		{domain.SecretRef{Name: "outbound", Version: "1"}, domain.SecretHTTPHeader, []byte("synthetic-outbound-value")},
	} {
		if err := s.PutSecret(t.Context(), f.actor.PrincipalID(), item.ref, item.purpose, item.value); err != nil {
			t.Fatal(err)
		}
	}
	f.config = domain.Configuration{
		Source:       domain.Source{ID: "source-a", Revision: 1, Name: "Synthetic source", Enabled: true, Auth: domain.SourceAuth{Kind: domain.AuthBearer, Secret: domain.SecretRef{Name: "inbound", Version: "1"}}, OriginalTTL: 120, DeliveryTTL: 120},
		Templates:    []domain.Template{{ID: "template-a", Revision: 1, SourceID: "source-a", Fields: map[string]string{"message": "event.message"}}},
		Transporters: []domain.Transporter{{ID: "http-a", Revision: 1, SourceID: "source-a", Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: "https://receiver.example/hook", TimeoutSeconds: 3, SecretHeaders: map[string]domain.SecretRef{"Authorization": {Name: "outbound", Version: "1"}}}}},
		Routes:       []domain.Route{{ID: "route-a", Revision: 1, SourceID: "source-a", Enabled: true, Template: domain.RevisionRef{ID: "template-a", Revision: 1}, Transporters: []domain.RevisionRef{{ID: "http-a", Revision: 1}}}},
	}
	f.grant = domain.Grant{ID: "grant-a", PrincipalID: f.actor.PrincipalID(), SourceIDs: []domain.ID{"source-a"}, Actions: []domain.Action{domain.ActionReadConfig, domain.ActionWriteConfig, domain.ActionActivateConfig, domain.ActionPreview, domain.ActionReadEvents, domain.ActionReplayEvents, domain.ActionReadDeliveries, domain.ActionRetryDelivery}, DestinationOrigins: []string{"https://receiver.example"}, SecretNames: []string{"inbound", "outbound"}, OriginalRetention: domain.RetentionLimit{MaxSeconds: 600}, DeliveryRetention: domain.RetentionLimit{MaxSeconds: 600}, ExpiresAt: new(f.now.Add(time.Hour))}
	f.saveGrant()
	policy, err := httptransport.NewPolicy([]httptransport.OriginRule{{Origin: "https://receiver.example"}})
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = New(s, policy, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) saveGrant() {
	f.t.Helper()
	if err := f.store.ProvisionGrant(f.t.Context(), f.actor.PrincipalID(), f.grant); err != nil {
		f.t.Fatal(err)
	}
}
func request(source domain.ID, input any, expected *domain.Revision) []byte {
	raw, _ := json.Marshal(Request[any]{source, expected, input})
	return raw
}
func (f *fixture) execute(op string, input any, expected *domain.Revision) (any, error) {
	f.t.Helper()
	return f.service.Execute(f.t.Context(), f.actor, op, request("source-a", input, expected))
}
func (f *fixture) must(op string, input any, expected *domain.Revision) any {
	f.t.Helper()
	out, err := f.execute(op, input, expected)
	if err != nil {
		f.t.Fatalf("%s: %v", op, err)
	}
	return out
}
func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil || PublicError(err).Status != status {
		t.Fatalf("error=%v status=%d want=%d", err, PublicError(err).Status, status)
	}
}
func (f *fixture) active() {
	f.t.Helper()
	f.must("config_put", PutInput{f.config}, new(domain.Revision(0)))
	f.must("config_activate", ActivateInput{1}, new(domain.Revision(0)))
}

func (f *fixture) accept(id domain.ID, failed bool) domain.Delivery {
	f.t.Helper()
	policy, _ := httptransport.NewPolicy([]httptransport.OriginRule{{Origin: "https://receiver.example"}})
	plan, err := engine.Compile(f.t.Context(), f.config, policy)
	if err != nil {
		f.t.Fatal(err)
	}
	frozen, err := plan.Evaluate(f.t.Context(), []byte(`{"message":"private-event-value","discarded":"private-original-only"}`))
	if err != nil {
		f.t.Fatal(err)
	}
	d := domain.Delivery{ID: domain.ID("delivery-" + string(id)), EventID: id, SourceID: "source-a", CreatedAt: f.now, TTL: 120, ExpiresAt: new(f.now.Add(120 * time.Second)), State: domain.DeliveryPending, NextAttemptAt: f.now, Snapshot: frozen[0]}
	_, err = f.store.Accept(f.t.Context(), ports.AcceptRequest{Event: domain.Event{ID: id, SourceID: "source-a", SourceRevision: 1, AcceptedAt: f.now, OriginalTTL: 120, OriginalExpiresAt: new(f.now.Add(120 * time.Second))}, Original: []byte(`{"message":"private-event-value","discarded":"private-original-only"}`), Dedup: domain.Deduplication{SourceID: "source-a", KeyHash: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), ExpiresAt: f.now.Add(time.Hour)}, Deliveries: []domain.Delivery{d}})
	if err != nil {
		f.t.Fatal(err)
	}
	if failed {
		lease, err := f.store.Claim(f.t.Context(), f.now, time.Minute)
		if err != nil {
			f.t.Fatal(err)
		}
		if err := f.store.Terminate(f.t.Context(), lease.Owner, f.now, "http_permanent"); err != nil {
			f.t.Fatal(err)
		}
	}
	return d
}

func TestImmutableConfigurationCASAndMonotonicRollback(t *testing.T) {
	f := setup(t)
	f.active()
	f.config.Templates[0].Fields["message"] = "'changed'"
	_, err := f.execute("config_put", PutInput{f.config}, new(domain.Revision(2)))
	wantStatus(t, err, 409)
	f.config.Templates[0].Revision = 2
	f.config.Routes[0].Revision = 2
	f.config.Routes[0].Template.Revision = 2
	f.must("config_put", PutInput{f.config}, new(domain.Revision(2)))
	_, err = f.execute("config_put", PutInput{f.config}, new(domain.Revision(2)))
	wantStatus(t, err, 409)
	_, err = f.execute("config_activate", ActivateInput{1}, new(domain.Revision(2)))
	wantStatus(t, err, 409)
	_, err = f.execute("config_activate", ActivateInput{3}, new(domain.Revision(0)))
	wantStatus(t, err, 409)
	rolled := f.must("config_rollback", RollbackInput{1}, new(domain.Revision(2))).(ConfigResult)
	if rolled.Head.Active != 4 || rolled.Head.Latest != 4 || rolled.Snapshot.SelectedFrom != 1 || rolled.Snapshot.Configuration.Templates[0].Revision != 1 {
		t.Fatalf("rollback=%+v", rolled)
	}
	active, err := f.store.ActiveConfiguration(t.Context(), "source-a")
	if err != nil || active.Templates[0].Fields["message"] != "event.message" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, stale := 0, 0
	for range 8 {
		wg.Go(func() {
			_, err := f.execute("config_put", PutInput{f.config}, new(domain.Revision(4)))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else if PublicError(err).Status == 409 {
				stale++
			} else {
				t.Errorf("write: %v", err)
			}
		})
	}
	wg.Wait()
	if success != 1 || stale != 7 {
		t.Fatalf("CAS success=%d stale=%d", success, stale)
	}
}

func TestGrantIntersectionsAndNoRemoteAuthorityFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fixture)
	}{
		{"empty actions", func(f *fixture) { f.grant.Actions = nil }},
		{"empty sources", func(f *fixture) { f.grant.SourceIDs = nil }},
		{"empty destinations", func(f *fixture) { f.grant.DestinationOrigins = nil }},
		{"empty secrets", func(f *fixture) { f.grant.SecretNames = nil }},
		{"expired", func(f *fixture) { f.grant.ExpiresAt = new(f.now) }},
		{"original ceiling", func(f *fixture) { f.grant.OriginalRetention.MaxSeconds = 119 }},
		{"delivery ceiling", func(f *fixture) { f.grant.DeliveryRetention.MaxSeconds = 119 }},
		{"indefinite original", func(f *fixture) { f.config.Source.OriginalTTL = -1 }},
		{"indefinite delivery", func(f *fixture) { f.config.Source.DeliveryTTL = -1 }},
		{"server policy intersection", func(f *fixture) {
			f.config.Transporters[0].HTTP.URL = "https://elsewhere.example/hook"
			f.grant.DestinationOrigins = append(f.grant.DestinationOrigins, "https://elsewhere.example")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			test.change(f)
			f.saveGrant()
			_, err := f.execute("config_put", PutInput{f.config}, new(domain.Revision(0)))
			wantStatus(t, err, 403)
		})
	}
	f := setup(t)
	_, err := f.service.Execute(t.Context(), Identity{"https://another-issuer.example", f.actor.Subject}, "config_get", request("source-a", EmptyInput{}, nil))
	wantStatus(t, err, 403)
	_, err = f.service.Execute(t.Context(), f.actor, "config_get", request("source-b", EmptyInput{}, nil))
	wantStatus(t, err, 403)
	for _, raw := range []string{`{"source_id":"source-a","input":{},"admin":true}`, `{"source_id":"source-a","input":{"principal_id":"attacker"}}`, `{"source_id":"source-a","input":{},"principal":{"subject":"attacker"}}`} {
		_, err := f.service.Execute(t.Context(), f.actor, "config_get", []byte(raw))
		wantStatus(t, err, 400)
	}
}

func TestSecretStrengthPurposeAndActivationRevalidation(t *testing.T) {
	f := setup(t)
	if err := f.store.PutSecret(t.Context(), f.actor.PrincipalID(), domain.SecretRef{Name: "weak", Version: "1"}, domain.SecretSourceAuth, []byte("short")); err != nil {
		t.Fatal(err)
	}
	f.grant.SecretNames = append(f.grant.SecretNames, "weak")
	f.saveGrant()
	f.config.Source.Auth.Secret = domain.SecretRef{Name: "weak", Version: "1"}
	_, err := f.execute("config_put", PutInput{f.config}, new(domain.Revision(0)))
	wantStatus(t, err, 422)
	f.config.Source.Auth.Secret = domain.SecretRef{Name: "inbound", Version: "1"}
	f.config.Transporters[0].HTTP.SecretHeaders["Authorization"] = f.config.Source.Auth.Secret
	_, err = f.execute("config_put", PutInput{f.config}, new(domain.Revision(0)))
	wantStatus(t, err, 422)
	f.config.Transporters[0].HTTP.SecretHeaders["Authorization"] = domain.SecretRef{Name: "outbound", Version: "1"}
	f.active()
	f.grant.DeliveryRetention.MaxSeconds = 0
	f.saveGrant()
	_, err = f.execute("config_rollback", RollbackInput{1}, new(domain.Revision(2)))
	wantStatus(t, err, 403)
	_, err = f.execute("config_activate", ActivateInput{1}, new(domain.Revision(2)))
	wantStatus(t, err, 403)
	f.grant.DeliveryRetention.MaxSeconds = 600
	f.grant.OriginalRetention.AllowIndefinite = true
	f.grant.DeliveryRetention.AllowIndefinite = true
	f.saveGrant()
	f.config.Source.Revision = 2
	f.config.Source.OriginalTTL = -1
	f.config.Source.DeliveryTTL = -1
	f.must("config_put", PutInput{f.config}, new(domain.Revision(2)))
}

func TestPreviewIsSyntheticAndHasNoEffects(t *testing.T) {
	f := setup(t)
	f.active()
	before, _ := f.store.AuditMetadata(t.Context(), 100)
	in := PreviewInput{Revision: 1, Synthetic: true, Event: json.RawMessage(`{"message":"synthetic-notice"}`)}
	out := f.must("preview", in, nil)
	raw, _ := json.Marshal(out)
	if !bytes.Contains(raw, []byte("synthetic-notice")) || bytes.Contains(raw, []byte("Authorization")) {
		t.Fatalf("preview=%s", raw)
	}
	in.Synthetic = false
	_, err := f.execute("preview", in, nil)
	wantStatus(t, err, 400)
	events := f.must("events_list", PageInput{}, nil).(Page[domain.Event])
	after, _ := f.store.AuditMetadata(t.Context(), 100)
	if len(events.Items) != 0 || len(before) != len(after) {
		t.Fatal("preview changed storage or audit")
	}
	if _, err := f.store.Claim(t.Context(), f.now, time.Minute); !errors.Is(err, ports.ErrNoReadyDelivery) {
		t.Fatalf("preview queued work: %v", err)
	}
}

func TestMetadataOwnershipPaginationAndForgedCursor(t *testing.T) {
	f := setup(t)
	f.active()
	f.accept("event-a", true)
	for _, op := range []string{"events_get", "deliveries_get", "events_list", "deliveries_list"} {
		var in any
		switch op {
		case "events_get":
			in = EventInput{"event-a"}
		case "deliveries_get":
			in = DeliveryInput{"delivery-event-a"}
		case "events_list":
			in = PageInput{Limit: 100000}
		case "deliveries_list":
			in = DeliveriesInput{Limit: -1}
		}
		raw, _ := json.Marshal(f.must(op, in, nil))
		for _, forbidden := range []string{"private-event-value", "private-original-only", "synthetic-outbound-value", "secret_headers", "snapshot", "https://receiver"} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("%s leaked %s: %s", op, forbidden, raw)
			}
		}
		f.grant.SourceIDs = append(f.grant.SourceIDs, "source-b")
		f.saveGrant()
		if op == "events_get" || op == "deliveries_get" {
			_, err := f.service.Execute(t.Context(), f.actor, op, request("source-b", in, nil))
			wantStatus(t, err, 404)
		}
	}
	for _, c := range []string{encodeCursor("source-b", "events_list", "", "event-a"), encodeCursor("source-a", "deliveries_list", "", "event-a"), "forged"} {
		_, err := f.execute("events_list", PageInput{Cursor: c}, nil)
		wantStatus(t, err, 400)
	}
	_, err := f.execute("deliveries_list", DeliveriesInput{EventID: "missing"}, nil)
	wantStatus(t, err, 404)
}

func TestReplayRetryIdempotencyLineageAuditAndOriginalExpiry(t *testing.T) {
	f := setup(t)
	f.active()
	old := f.accept("event-a", true)
	retry := RetryInput{DeliveryID: old.ID, IdempotencyKey: "retry-synthetic-1"}
	first := f.must("deliveries_retry", retry, nil).(ports.ManagementReceipt)
	duplicate := f.must("deliveries_retry", retry, nil).(ports.ManagementReceipt)
	if !duplicate.Duplicate || first.DeliveryIDs[0] != duplicate.DeliveryIDs[0] || first.DeliveryIDs[0] == old.ID {
		t.Fatalf("retry receipts=%+v %+v", first, duplicate)
	}
	got, err := f.store.Delivery(t.Context(), first.DeliveryIDs[0])
	if err != nil || !got.ExpiresAt.Equal(*old.ExpiresAt) || !bytes.Equal(got.Snapshot.Body, old.Snapshot.Body) {
		t.Fatalf("retry=%+v err=%v", got, err)
	}
	status := f.must("deliveries_get", DeliveryInput{first.DeliveryIDs[0]}, nil).(domain.DeliveryStatus)
	if status.RetryOf != old.ID || status.Transporter.Revision != 1 {
		t.Fatalf("status=%+v", status)
	}
	replay := ReplayInput{EventID: "event-a", Revision: 2, IdempotencyKey: "replay-synthetic-1"}
	r1 := f.must("events_replay", replay, new(domain.Revision(2))).(ports.ManagementReceipt)
	r2 := f.must("events_replay", replay, new(domain.Revision(2))).(ports.ManagementReceipt)
	if !r2.Duplicate || r1.EventID != r2.EventID || r1.EventID == "event-a" {
		t.Fatalf("replay=%+v %+v", r1, r2)
	}
	metadata := f.must("events_get", EventInput{r1.EventID}, nil).(domain.Event)
	if metadata.ReplayOf != "event-a" {
		t.Fatalf("lineage=%+v", metadata)
	}
	replay.Revision = 1
	_, err = f.execute("events_replay", replay, new(domain.Revision(2)))
	wantStatus(t, err, 409)
	audit, _ := f.store.AuditMetadata(t.Context(), 100)
	retries, replays := 0, 0
	for _, a := range audit {
		if a.Operation == "deliveries_retry" {
			retries++
		}
		if a.Operation == "events_replay" {
			replays++
		}
	}
	if retries != 1 || replays != 1 {
		t.Fatalf("audit retries=%d replays=%d", retries, replays)
	}
	f.now = f.now.Add(120 * time.Second)
	_, err = f.execute("deliveries_retry", RetryInput{old.ID, "fresh-after-expiry"}, nil)
	wantStatus(t, err, 410)
	_, err = f.execute("events_replay", ReplayInput{"event-a", 2, "fresh-after-expiry"}, new(domain.Revision(2)))
	wantStatus(t, err, 410)
	if err := f.store.Cleanup(t.Context(), f.now); err != nil {
		t.Fatal(err)
	}
	status = f.must("deliveries_get", DeliveryInput{old.ID}, nil).(domain.DeliveryStatus)
	if status.Template.Revision != 1 {
		t.Fatalf("frozen metadata lost after expiry: %+v", status)
	}
}

func TestReplayRetryRecheckCurrentAuthorityAndTTLZero(t *testing.T) {
	for _, op := range []string{"events_replay", "deliveries_retry"} {
		t.Run(op, func(t *testing.T) {
			f := setup(t)
			f.active()
			f.accept("event-a", true)
			f.grant.DestinationOrigins = nil
			f.saveGrant()
			var in any = RetryInput{"delivery-event-a", "test"}
			var expected *domain.Revision
			if op == "events_replay" {
				in = ReplayInput{"event-a", 2, "test"}
				expected = new(domain.Revision(2))
			}
			_, err := f.execute(op, in, expected)
			wantStatus(t, err, 403)
		})
	}
	f := setup(t)
	f.active()
	f.accept("event-a", true)
	f.config.Source.Revision = 2
	f.config.Source.DeliveryTTL = 0
	f.must("config_put", PutInput{f.config}, new(domain.Revision(2)))
	f.must("config_activate", ActivateInput{3}, new(domain.Revision(2)))
	_, err := f.execute("events_replay", ReplayInput{"event-a", 4, "ttl-zero"}, new(domain.Revision(4)))
	wantStatus(t, err, 422)
	_, err = f.store.Accept(t.Context(), ports.AcceptRequest{Mode: domain.ModeImmediate, Event: domain.Event{ID: "zero-event", SourceID: "source-a", SourceRevision: 2, AcceptedAt: f.now, OriginalTTL: 0}, Dedup: domain.Deduplication{SourceID: "source-a", KeyHash: strings.Repeat("c", 64), Fingerprint: strings.Repeat("d", 64), ExpiresAt: f.now.Add(time.Hour)}, ImmediateDeliveryIDs: []domain.ID{"zero-delivery"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.execute("deliveries_retry", RetryInput{"zero-delivery", "zero"}, nil)
	wantStatus(t, err, 410)
	_, err = f.execute("events_replay", ReplayInput{"zero-event", 4, "zero-original"}, new(domain.Revision(4)))
	wantStatus(t, err, 410)
}

func TestOpenAPIRequestAndResultSchemasMatchTheCommandRegistry(t *testing.T) {
	raw, err := os.ReadFile("../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, op := range Operations() {
		name := ""
		for part := range strings.SplitSeq(op.Name, "_") {
			name += strings.ToUpper(part[:1]) + part[1:]
		}
		for _, item := range []struct {
			suffix string
			schema any
		}{{"Request", op.InputSchema}, {"Result", op.OutputSchema}} {
			stored := doc.Components.Schemas[name+item.suffix]
			var reference struct {
				Ref string `json:"$ref"`
			}
			if json.Unmarshal(stored, &reference) == nil && reference.Ref != "" {
				stored = doc.Components.Schemas[strings.TrimPrefix(reference.Ref, "#/components/schemas/")]
			}
			expected, _ := json.Marshal(item.schema)
			var want, got any
			if json.Unmarshal(expected, &want) != nil || json.Unmarshal(stored, &got) != nil || !reflect.DeepEqual(want, got) {
				t.Fatalf("OpenAPI schema drift for %s%s", name, item.suffix)
			}
		}
	}
}

func TestOpenAPIGenericUnionsValidateActualRequestsAndResponses(t *testing.T) {
	f := setup(t)
	f.active()
	f.accept("event-a", true)
	requestSchema, responseSchema := genericOpenAPISchemas(t)
	for _, test := range []struct {
		name             string
		input            any
		expectedRevision *domain.Revision
	}{
		{"config_get", EmptyInput{}, nil},
		{"events_list", PageInput{}, nil},
		{"events_replay", ReplayInput{"event-a", 2, "schema-replay"}, new(domain.Revision(2))},
		{"deliveries_retry", RetryInput{"delivery-event-a", "schema-retry"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requestValue any
			if err := json.Unmarshal(request("source-a", test.input, test.expectedRevision), &requestValue); err != nil {
				t.Fatal(err)
			}
			if err := requestSchema.Validate(requestValue); err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
			result := f.must(test.name, test.input, test.expectedRevision)
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var responseValue any
			if err := json.Unmarshal(raw, &responseValue); err != nil {
				t.Fatal(err)
			}
			if err := responseSchema.Validate(responseValue); err != nil {
				t.Fatalf("valid response rejected: %v", err)
			}
		})
	}
	if err := requestSchema.Validate(map[string]any{"input": map[string]any{}}); err != nil {
		t.Fatalf("valid source discovery request rejected: %v", err)
	}
	if err := requestSchema.Validate(map[string]any{"source_id": "source-a", "input": map[string]any{}, "admin": true}); err == nil {
		t.Fatal("generic request accepted an unknown authority field")
	}
	if err := responseSchema.Validate(map[string]any{"private_payload": "unexpected"}); err == nil {
		t.Fatal("generic response accepted an unknown payload field")
	}
}

func genericOpenAPISchemas(t *testing.T) (*jsonschema.Resolved, *jsonschema.Resolved) {
	t.Helper()
	raw, err := os.ReadFile("../docs/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
		Paths map[string]struct {
			Post struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema json.RawMessage `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			} `json:"post"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	resolve := func(schema json.RawMessage) *jsonschema.Resolved {
		t.Helper()
		// OpenAPI components become local JSON Schema definitions without
		// changing the stored union or any operation-specific constraints.
		wrapped, err := json.Marshal(map[string]any{"allOf": []json.RawMessage{schema}, "$defs": doc.Components.Schemas})
		if err != nil {
			t.Fatal(err)
		}
		wrapped = bytes.ReplaceAll(wrapped, []byte("#/components/schemas/"), []byte("#/$defs/"))
		var parsed jsonschema.Schema
		if err := json.Unmarshal(wrapped, &parsed); err != nil {
			t.Fatal(err)
		}
		resolved, err := parsed.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	return resolve(doc.Components.Schemas["ManagementRequest"]), resolve(doc.Paths["/v1/manage/{operation}"].Post.Responses["200"].Content["application/json"].Schema)
}
