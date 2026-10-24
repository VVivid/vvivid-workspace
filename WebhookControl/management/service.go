package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/engine"
	"github.com/VVivid/vvivid-workspace/WebhookControl/httptransport"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

type Service struct {
	store           ports.ManagementStore
	policy          ports.DestinationPolicy
	now             func() time.Time
	requestCounters func(domain.ID) *domain.IngressCounters
}

type Option func(*Service)

// WithIngressCounters attaches optional bounded, source-scoped daemon counters.
// Local CLI invocations have no process observations and omit this field.
func WithIngressCounters(read func(domain.ID) *domain.IngressCounters) Option {
	return func(s *Service) { s.requestCounters = read }
}

func New(store ports.ManagementStore, policy ports.DestinationPolicy, now func() time.Time, options ...Option) (*Service, error) {
	if store == nil || policy == nil || now == nil {
		return nil, errors.New("management dependencies required")
	}
	s := &Service{store: store, policy: policy, now: now}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("invalid management option")
		}
		option(s)
	}
	return s, nil
}

func liveGrants(grants []domain.Grant, now time.Time) []domain.Grant {
	return slices.DeleteFunc(grants, func(g domain.Grant) bool {
		return len(g.SourceIDs) == 0 || len(g.Actions) == 0 || g.ExpiresAt != nil && !now.Before(*g.ExpiresAt)
	})
}

func grantsFor(grants []domain.Grant, source domain.ID, action domain.Action) []domain.Grant {
	out := []domain.Grant{}
	for _, g := range grants {
		if slices.Contains(g.SourceIDs, source) && slices.Contains(g.Actions, action) {
			out = append(out, g)
		}
	}
	return out
}

func (s *Service) PermittedOperations(ctx context.Context, actor Identity) ([]Operation, error) {
	principal := actor.PrincipalID()
	if principal == "" {
		return nil, fail(401, "unauthenticated", "Verified identity required")
	}
	out := []Operation{}
	err := s.store.Manage(ctx, func(tx ports.ManagementTx) error {
		grants, err := tx.Grants(principal)
		if err != nil {
			return err
		}
		grants = liveGrants(grants, s.now())
		if len(grants) == 0 {
			return fail(403, "forbidden", "An unexpired source grant is required")
		}
		for _, op := range Operations() {
			if op.Action == "" {
				out = append(out, op)
				continue
			}
			for _, g := range grants {
				if slices.Contains(g.Actions, op.Action) {
					out = append(out, op)
					break
				}
			}
		}
		return nil
	})
	return out, err
}

// Execute runs exactly one command. No caller JSON can choose identity, grant,
// administrator status or secret values. All selected content is authorized in
// the same serialized transaction that writes history, receipts and audit.
func (s *Service) Execute(ctx context.Context, actor Identity, operation string, raw []byte) (any, error) {
	principal := actor.PrincipalID()
	if principal == "" {
		return nil, fail(401, "unauthenticated", "Verified identity required")
	}
	if len(raw) > 2<<20 {
		return nil, fail(413, "request_too_large", "Request exceeds management limit")
	}
	var request Request[json.RawMessage]
	if err := decode(raw, &request); err != nil {
		return nil, err
	}
	if len(request.Input) == 0 || string(request.Input) == "null" {
		return nil, fail(400, "invalid_request", "input object is required")
	}
	var definition *Operation
	for _, op := range Operations() {
		if op.Name == operation {
			definition = new(op)
			break
		}
	}
	if definition == nil {
		return nil, fail(404, "unknown_operation", "Unknown management operation")
	}
	requiresExpectedRevision := operation == "config_put" || operation == "config_activate" || operation == "config_rollback" || operation == "events_replay"
	if requiresExpectedRevision != (request.ExpectedRevision != nil) {
		return nil, fail(400, "invalid_request", "expected_revision is required only for configuration mutations and replay")
	}
	if definition.Action != "" && !engine.ValidID(request.SourceID) {
		return nil, fail(400, "invalid_request", "A valid source_id is required")
	}
	var result any
	err := s.store.Manage(ctx, func(tx ports.ManagementTx) error {
		grants, err := tx.Grants(principal)
		if err != nil {
			return err
		}
		grants = liveGrants(grants, s.now())
		if len(grants) == 0 {
			return fail(403, "forbidden", "An unexpired source grant is required")
		}
		if definition.Action != "" {
			grants = grantsFor(grants, request.SourceID, definition.Action)
			if len(grants) == 0 {
				return fail(403, "forbidden", "Source action is not granted")
			}
		}
		result, err = s.dispatch(ctx, tx, actor, operation, request, grants)
		return err
	})
	if err != nil {
		return nil, PublicError(err)
	}
	return result, nil
}

func (s *Service) dispatch(ctx context.Context, tx ports.ManagementTx, actor Identity, op string, r Request[json.RawMessage], grants []domain.Grant) (any, error) {
	source := r.SourceID
	configResult := func(snapshot *domain.ConfigurationSnapshot) (any, error) {
		h, err := tx.Head(source)
		return ConfigResult{h, snapshot}, err
	}
	loadChecked := func(rev domain.Revision) (domain.ConfigurationSnapshot, *engine.Plan, error) {
		v, err := tx.Configuration(source, rev)
		if err != nil {
			return v, nil, err
		}
		p, err := s.checkedPlan(ctx, tx, v.Configuration, grants)
		return v, p, err
	}
	switch op {
	case "monitor":
		if err := decode(r.Input, new(EmptyInput)); err != nil {
			return nil, err
		}
		metrics, err := tx.SourceMetrics(source, s.now())
		if err == nil && s.requestCounters != nil {
			metrics.Ingress = s.requestCounters(source)
		}
		return metrics, err
	case "sources_list", "health":
		if err := decode(r.Input, new(EmptyInput)); err != nil {
			return nil, err
		}
		if r.SourceID != "" {
			return nil, fail(400, "invalid_request", "Discovery and health require empty source_id")
		}
		if op == "health" {
			return HealthResult{"ok"}, nil
		}
		sources := []domain.ID{}
		for _, g := range grants {
			sources = append(sources, g.SourceIDs...)
		}
		slices.Sort(sources)
		sources = slices.Compact(sources)
		return SourcesResult{sources}, nil
	case "config_get":
		if err := decode(r.Input, new(EmptyInput)); err != nil {
			return nil, err
		}
		h, err := tx.Head(source)
		if err != nil {
			return nil, err
		}
		if h.Active == 0 {
			return ConfigResult{Head: h}, nil
		}
		v, err := tx.Configuration(source, h.Active)
		if err != nil {
			return nil, err
		}
		return ConfigResult{h, new(v)}, nil
	case "config_put":
		var in PutInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		if in.Configuration.Source.ID != source {
			return nil, fail(400, "invalid_request", "Configuration source does not match source_id")
		}
		if _, err := s.checkedPlan(ctx, tx, in.Configuration, grants); err != nil {
			return nil, err
		}
		v, err := tx.PutConfiguration(source, *r.ExpectedRevision, in.Configuration)
		if err != nil {
			return nil, err
		}
		if err := s.audit(tx, actor, op, source, "", v.Revision); err != nil {
			return nil, err
		}
		return configResult(new(v))
	case "config_activate", "config_rollback":
		var selected domain.Revision
		if op == "config_activate" {
			var in ActivateInput
			if err := decode(r.Input, &in); err != nil {
				return nil, err
			}
			selected = in.DraftRevision
		} else {
			var in RollbackInput
			if err := decode(r.Input, &in); err != nil {
				return nil, err
			}
			selected = in.TargetRevision
		}
		if _, _, err := loadChecked(selected); err != nil {
			return nil, err
		}
		v, err := tx.SelectConfiguration(source, *r.ExpectedRevision, selected, op == "config_rollback")
		if err != nil {
			return nil, err
		}
		if err := s.audit(tx, actor, op, source, "", v.Revision); err != nil {
			return nil, err
		}
		return configResult(new(v))
	case "preview":
		var in PreviewInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		if !in.Synthetic {
			return nil, fail(400, "synthetic_required", "Preview requires synthetic=true and caller-supplied test input")
		}
		_, plan, err := loadChecked(in.Revision)
		if err != nil {
			return nil, err
		}
		frozen, err := plan.Evaluate(ctx, in.Event)
		if err != nil {
			return nil, fail(422, "evaluation_failed", "Synthetic input could not be evaluated")
		}
		out := []PreviewRender{}
		for _, f := range frozen {
			out = append(out, PreviewRender{f.Route, domain.RevisionRef{ID: f.Transporter.ID, Revision: f.Transporter.Revision}, f.Body})
		}
		return PreviewResult{out}, nil
	case "events_get":
		var in EventInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		return tx.Event(source, in.EventID)
	case "events_list":
		var in PageInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		after, err := decodeCursor(in.Cursor, source, op, "")
		if err != nil {
			return nil, err
		}
		limit := pageLimit(in.Limit)
		items, err := tx.Events(source, after, limit+1)
		if err != nil {
			return nil, err
		}
		out := Page[domain.Event]{Items: items}
		if len(items) > limit {
			out.Items = items[:limit]
			out.Cursor = encodeCursor(source, op, "", items[limit-1].ID)
		}
		return out, nil
	case "deliveries_get":
		var in DeliveryInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		return tx.DeliveryStatus(source, in.DeliveryID)
	case "deliveries_list":
		var in DeliveriesInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		if in.EventID != "" {
			if _, err := tx.Event(source, in.EventID); err != nil {
				return nil, err
			}
		}
		after, err := decodeCursor(in.Cursor, source, op, in.EventID)
		if err != nil {
			return nil, err
		}
		limit := pageLimit(in.Limit)
		items, err := tx.Deliveries(source, in.EventID, after, limit+1)
		if err != nil {
			return nil, err
		}
		out := Page[domain.DeliveryStatus]{Items: items}
		if len(items) > limit {
			out.Items = items[:limit]
			out.Cursor = encodeCursor(source, op, in.EventID, items[limit-1].ID)
		}
		return out, nil
	case "events_replay":
		var in ReplayInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		if !validKey(in.IdempotencyKey) {
			return nil, fail(400, "invalid_request", "A visible ASCII request idempotency key of 1..128 bytes is required")
		}
		h, err := tx.Head(source)
		if err != nil {
			return nil, err
		}
		if h.Active != *r.ExpectedRevision {
			return nil, ports.ErrStaleRevision
		}
		v, plan, err := loadChecked(in.Revision)
		if err != nil {
			return nil, err
		}
		// Ownership and current authority are checked even for an existing receipt.
		if _, err := tx.Event(source, in.EventID); err != nil {
			return nil, err
		}
		key, fingerprint := requestDigests(in.IdempotencyKey, struct {
			EventID  domain.ID
			Revision domain.Revision
		}{in.EventID, in.Revision})
		if out, err := tx.Receipt(actor.PrincipalID(), op, source, key, fingerprint); err == nil {
			return out, nil
		} else if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		original, err := tx.Original(source, in.EventID)
		if err != nil {
			return nil, err
		}
		frozen, err := plan.Evaluate(ctx, original)
		if err != nil {
			return nil, fail(422, "evaluation_failed", "Retained input could not be evaluated")
		}
		if len(frozen) > 0 && v.Configuration.Source.DeliveryTTL == 0 {
			return nil, fail(422, "durable_replay_required", "Replay with matching routes requires a retained delivery TTL")
		}
		now := s.now()
		config := v.Configuration.Source
		e := domain.Event{
			ID: newID(), SourceID: source, SourceRevision: config.Revision,
			AcceptedAt: now, OriginalTTL: config.OriginalTTL,
			OriginalExpiresAt: expiry(now, config.OriginalTTL), ReplayOf: in.EventID,
		}
		acceptance := ports.AcceptRequest{
			Event: e, Mode: domain.ModeDurable,
			Dedup: domain.Deduplication{SourceID: source, KeyHash: key, Fingerprint: fingerprint, ExpiresAt: now.Add(time.Second)},
		}
		if config.OriginalTTL != 0 {
			acceptance.Original = original
		}
		out := ports.ManagementReceipt{EventID: e.ID, DeliveryIDs: []domain.ID{}}
		for _, f := range frozen {
			id := newID()
			acceptance.Deliveries = append(acceptance.Deliveries, domain.Delivery{
				ID: id, EventID: e.ID, SourceID: source, CreatedAt: now,
				TTL: config.DeliveryTTL, ExpiresAt: expiry(now, config.DeliveryTTL),
				State: domain.DeliveryPending, NextAttemptAt: now, Snapshot: f,
			})
			out.DeliveryIDs = append(out.DeliveryIDs, id)
		}
		if err := tx.Replay(acceptance, in.EventID); err != nil {
			return nil, err
		}
		if err := tx.SaveReceipt(actor.PrincipalID(), op, source, key, fingerprint, out); err != nil {
			return nil, err
		}
		if err := s.audit(tx, actor, op, source, e.ID, v.Revision); err != nil {
			return nil, err
		}
		return out, nil
	case "deliveries_retry":
		var in RetryInput
		if err := decode(r.Input, &in); err != nil {
			return nil, err
		}
		if !validKey(in.IdempotencyKey) {
			return nil, fail(400, "invalid_request", "A visible ASCII request idempotency key of 1..128 bytes is required")
		}
		status, err := tx.DeliveryStatus(source, in.DeliveryID)
		if err != nil {
			return nil, err
		}
		// Missing/expired payloads cannot be retried, including TTL-zero metadata.
		d, err := tx.Delivery(source, in.DeliveryID)
		if err != nil {
			return nil, err
		}
		if status.State != domain.DeliveryFailed {
			return nil, fail(409, "invalid_state", "Only a failed delivery can be retried")
		}
		if err := s.checkFrozen(ctx, tx, d, grants); err != nil {
			return nil, err
		}
		key, fingerprint := requestDigests(in.IdempotencyKey, in.DeliveryID)
		if out, err := tx.Receipt(actor.PrincipalID(), op, source, key, fingerprint); err == nil {
			return out, nil
		} else if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		id := newID()
		d.ID = id
		if err := tx.Retry(d, in.DeliveryID); err != nil {
			return nil, err
		}
		out := ports.ManagementReceipt{EventID: d.EventID, DeliveryIDs: []domain.ID{id}}
		if err := tx.SaveReceipt(actor.PrincipalID(), op, source, key, fingerprint, out); err != nil {
			return nil, err
		}
		if err := s.audit(tx, actor, op, source, id, 0); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, fail(404, "unknown_operation", "Unknown management operation")
}

func (s *Service) audit(tx ports.ManagementTx, actor Identity, op string, source, resource domain.ID, revision domain.Revision) error {
	return tx.Audit(domain.Audit{ID: newID(), At: s.now(), PrincipalID: actor.PrincipalID(), Operation: op, SourceID: source, ResourceID: resource, Revision: revision})
}

func retentionAllowed(ttl domain.Retention, limit domain.RetentionLimit) bool {
	return limit.MaxSeconds >= 0 && (ttl == -1 && limit.AllowIndefinite || ttl >= 0 && int64(ttl) <= limit.MaxSeconds)
}

type grantedPolicy struct {
	server ports.DestinationPolicy
	grant  domain.Grant
}

func (p grantedPolicy) ApproveDestination(ctx context.Context, source domain.ID, c domain.HTTPConfig) error {
	origin, err := httptransport.CanonicalOrigin(c.URL)
	if err != nil || !slices.Contains(p.grant.DestinationOrigins, origin) {
		return ports.ErrPolicyDenied
	}
	return p.server.ApproveDestination(ctx, source, c)
}

func checkSecret(tx ports.ManagementTx, g domain.Grant, ref domain.SecretRef, purpose domain.SecretPurpose, minBytes int) error {
	if !slices.Contains(g.SecretNames, ref.Name) {
		return fail(403, "forbidden", "Secret reference is not granted")
	}
	actual, err := tx.SecretPurpose(ref)
	if err != nil || actual != purpose {
		return fail(422, "invalid_secret", "Secret reference is unavailable for this purpose")
	}
	value, err := tx.Secret(ref)
	if err != nil {
		return fail(422, "invalid_secret", "Secret reference is unavailable")
	}
	defer clear(value)
	if len(value) < minBytes || len(value) > 4096 {
		return fail(422, "invalid_secret", "Secret does not satisfy credential requirements")
	}
	return nil
}

func checkSource(tx ports.ManagementTx, g domain.Grant, source domain.Source) error {
	if !retentionAllowed(source.OriginalTTL, g.OriginalRetention) || !retentionAllowed(source.DeliveryTTL, g.DeliveryRetention) {
		return fail(403, "forbidden", "Retention exceeds the grant ceiling")
	}
	minimum := 16
	if source.Auth.Kind == domain.AuthHMAC {
		minimum = 32
	}
	return checkSecret(tx, g, source.Auth.Secret, domain.SecretSourceAuth, minimum)
}

func (s *Service) checkedPlan(ctx context.Context, tx ports.ManagementTx, c domain.Configuration, grants []domain.Grant) (*engine.Plan, error) {
	var last error
	// One grant must authorize the entire snapshot. Combining partial grants
	// would let their separate destination, secret and retention limits widen access.
	for _, g := range grants {
		if err := checkSource(tx, g, c.Source); err != nil {
			last = err
			continue
		}
		allowed := true
		for _, t := range c.Transporters {
			for _, ref := range t.HTTP.SecretHeaders {
				if err := checkSecret(tx, g, ref, domain.SecretHTTPHeader, 1); err != nil {
					last = err
					allowed = false
				}
			}
		}
		if !allowed {
			continue
		}
		plan, err := engine.Compile(ctx, c, grantedPolicy{s.policy, g})
		if errors.Is(err, ports.ErrPolicyDenied) {
			last = err
			continue
		}
		if err != nil {
			return nil, fail(422, "invalid_configuration", "Configuration validation failed")
		}
		return plan, nil
	}
	if last == nil {
		last = fail(403, "forbidden", "Configuration is not granted")
	}
	return nil, last
}

func (s *Service) checkFrozen(ctx context.Context, tx ports.ManagementTx, d domain.Delivery, grants []domain.Grant) error {
	source, err := tx.SourceRevision(d.SourceID, d.Snapshot.Source.Revision)
	if err != nil {
		return err
	}
	var last error
	for _, g := range grants {
		if err := checkSource(tx, g, source); err != nil {
			last = err
			continue
		}
		if !retentionAllowed(d.TTL, g.DeliveryRetention) {
			last = fail(403, "forbidden", "Frozen retention exceeds grant ceiling")
			continue
		}
		p := grantedPolicy{s.policy, g}
		if err := p.ApproveDestination(ctx, d.SourceID, d.Snapshot.Transporter.HTTP); err != nil {
			last = err
			continue
		}
		allowed := true
		for _, ref := range d.Snapshot.Transporter.HTTP.SecretHeaders {
			if err := checkSecret(tx, g, ref, domain.SecretHTTPHeader, 1); err != nil {
				last = err
				allowed = false
			}
		}
		if allowed {
			return nil
		}
	}
	if last == nil {
		last = fail(403, "forbidden", "Frozen configuration is not granted")
	}
	return last
}

func newID() domain.ID { return domain.ID(rand.Text()) }
func expiry(now time.Time, ttl domain.Retention) *time.Time {
	if ttl <= 0 || int64(ttl) > math.MaxInt64/int64(time.Second) {
		return nil
	}
	return new(now.Add(time.Duration(ttl) * time.Second))
}
func validKey(key string) bool {
	if len(key) == 0 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func requestDigests(key string, input any) (string, string) {
	k := sha256.Sum256([]byte(key))
	raw, _ := json.Marshal(input)
	f := sha256.Sum256(raw)
	return hex.EncodeToString(k[:]), hex.EncodeToString(f[:])
}
func pageLimit(limit int) int {
	if limit == 0 {
		return 50
	}
	return min(max(limit, 1), 100)
}

type cursor struct {
	Source    domain.ID `json:"source"`
	Operation string    `json:"operation"`
	Event     domain.ID `json:"event"`
	After     domain.ID `json:"after"`
}

func encodeCursor(source domain.ID, op string, event, after domain.ID) string {
	raw, _ := json.Marshal(cursor{source, op, event, after})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeCursor(encoded string, source domain.ID, op string, event domain.ID) (domain.ID, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	var c cursor
	if len(encoded) > 1024 || err != nil || decode(raw, &c) != nil || c.Source != source || c.Operation != op || c.Event != event || !engine.ValidID(c.After) {
		return "", fail(400, "invalid_cursor", "Cursor does not match this source and list")
	}
	return c.After, nil
}
