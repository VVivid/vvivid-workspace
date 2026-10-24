// Package ingress authenticates and accepts generic JSON webhooks. It has no
// concrete configuration, secret, destination-policy, or network implementation.
package ingress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/engine"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
)

const DefaultDeduplicationWindow = 7 * 24 * time.Hour
const DefaultImmediateBudget = 30 * time.Second

var ErrOptions = errors.New("invalid ingress options")

type Dependencies struct {
	Configurations ports.ConfigurationStore
	Secrets        ports.SecretReader
	Storage        ports.Storage
	Policy         ports.DestinationPolicy
	Clock          ports.Clock
	// Transporter is required only when a source uses immediate mode.
	Transporter ports.Transporter
}

type Options struct {
	// DigestKey is a separate server-owned key, never a source credential.
	DigestKey []byte
	// Zero uses seven days. The window cannot exceed the adapter's limit.
	DeduplicationWindow time.Duration
	// Zero uses 60. Authentication failures also consume this source's quota.
	RequestsPerMinute int
	// Zero uses 30 seconds; at most one minute for the entire foreground fanout.
	ImmediateBudget time.Duration
	// Observe receives only known-source metadata after processing. accepted
	// means a committed receipt, including immediate attempts that later fail.
	Observe func(source domain.ID, accepted, duplicate bool)
}

type Handler struct {
	dependencies    Dependencies
	receipts        ports.ReceiptReader
	immediate       ports.ImmediateStorage
	key             []byte
	window          time.Duration
	immediateBudget time.Duration
	limiter         sourceLimiter
	mux             *http.ServeMux
	observe         func(domain.ID, bool, bool)
}

type Response struct {
	EventID          domain.ID                `json:"event_id"`
	DeliveryIDs      []domain.ID              `json:"delivery_ids"`
	Duplicate        bool                     `json:"duplicate"`
	Mode             domain.DeliveryMode      `json:"mode"`
	ImmediateResults *[]ports.ImmediateResult `json:"immediate_results,omitzero"`
}

func New(dependencies Dependencies, options Options) (*Handler, error) {
	if dependencies.Configurations == nil || dependencies.Secrets == nil || dependencies.Storage == nil || dependencies.Policy == nil || dependencies.Clock == nil || len(options.DigestKey) != 32 {
		return nil, ErrOptions
	}
	receipts, ok := dependencies.Storage.(ports.ReceiptReader)
	limits, bounded := dependencies.Storage.(ports.DeduplicationLimits)
	if !ok || !bounded {
		return nil, ErrOptions
	}
	if options.DeduplicationWindow == 0 {
		options.DeduplicationWindow = DefaultDeduplicationWindow
	}
	if options.RequestsPerMinute == 0 {
		options.RequestsPerMinute = 60
	}
	if options.ImmediateBudget == 0 {
		options.ImmediateBudget = DefaultImmediateBudget
	}
	if options.DeduplicationWindow <= 0 || options.DeduplicationWindow > limits.DeduplicationWindow() || options.RequestsPerMinute < 1 || options.RequestsPerMinute > 10000 || options.ImmediateBudget <= 0 || options.ImmediateBudget > time.Minute {
		return nil, ErrOptions
	}
	immediate, _ := dependencies.Storage.(ports.ImmediateStorage)
	handler := &Handler{
		dependencies:    dependencies,
		receipts:        receipts,
		immediate:       immediate,
		key:             bytes.Clone(options.DigestKey),
		window:          options.DeduplicationWindow,
		immediateBudget: options.ImmediateBudget,
		limiter:         sourceLimiter{maximum: options.RequestsPerMinute, windows: map[domain.ID]sourceWindow{}},
		mux:             http.NewServeMux(),
		observe:         options.Observe,
	}
	handler.mux.HandleFunc("POST /v1/hooks/{source_id}", handler.accept)
	handler.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "Endpoint not found.")
	})
	return handler, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST is required.")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	sourceID := domain.ID(r.PathValue("source_id"))
	if !engine.ValidID(sourceID) {
		fail(w, http.StatusBadRequest, "invalid_source", "Invalid source ID.")
		return
	}
	key, ok := singleHeader(r.Header, "Idempotency-Key")
	if !ok || !validIdempotencyKey(key) {
		fail(w, http.StatusBadRequest, "invalid_idempotency_key", "A bounded visible ASCII Idempotency-Key is required.")
		return
	}
	now := h.dependencies.Clock.Now().Round(0).UTC()
	if !validTime(now) {
		fail(w, http.StatusServiceUnavailable, "unavailable", "Service unavailable.")
		return
	}
	config, err := h.dependencies.Configurations.ActiveConfiguration(r.Context(), sourceID)
	if errors.Is(err, ports.ErrNotFound) {
		fail(w, http.StatusUnauthorized, "unauthorized", "Source authentication failed.")
		return
	}
	if err != nil || config.Source.ID != sourceID || engine.ValidateSource(config.Source) != nil {
		fail(w, http.StatusServiceUnavailable, "configuration_unavailable", "Source configuration unavailable.")
		return
	}
	committed, duplicate := false, false
	if h.observe != nil {
		defer func() { h.observe(sourceID, committed, duplicate) }()
	}
	if !h.limiter.allow(sourceID, now) {
		w.Header().Set("Retry-After", "60")
		fail(w, http.StatusTooManyRequests, "rate_limited", "Source request limit exceeded.")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || parameters["charset"] != "" && parameters["charset"] != "utf-8" || r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity" {
		fail(w, http.StatusBadRequest, "invalid_content_type", "An uncompressed application/json body is required.")
		return
	}
	if r.ContentLength > engine.MaxEventBytes {
		fail(w, http.StatusRequestEntityTooLarge, "body_too_large", "JSON body exceeds 1 MiB.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, engine.MaxEventBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_body", "Unable to read JSON body.")
		return
	}
	if len(raw) > engine.MaxEventBytes {
		fail(w, http.StatusRequestEntityTooLarge, "body_too_large", "JSON body exceeds 1 MiB.")
		return
	}
	secret, err := h.dependencies.Secrets.ReadSecret(r.Context(), config.Source.Auth.Secret)
	authNow := h.dependencies.Clock.Now().Round(0).UTC()
	if !validTime(authNow) {
		fail(w, http.StatusServiceUnavailable, "unavailable", "Service unavailable.")
		return
	}
	if err != nil || !authenticate(r, config.Source, secret, key, raw, authNow) {
		fail(w, http.StatusUnauthorized, "unauthorized", "Source authentication failed.")
		return
	}
	if !config.Source.Enabled {
		fail(w, http.StatusForbidden, "source_disabled", "Source is disabled.")
		return
	}
	dedup := domain.Deduplication{SourceID: sourceID, KeyHash: keyedDigest(h.key, "dedup", sourceID, []byte(key)), Fingerprint: keyedDigest(h.key, "body", sourceID, raw)}
	// Resolve retained IDs and mode before current rules/templates can reject
	// or reinterpret a retry of an already accepted event.
	accepted, err := h.receipts.LookupAcceptance(r.Context(), sourceID, dedup.KeyHash, dedup.Fingerprint)
	if err == nil {
		committed, duplicate = true, true
		h.respond(w, r, accepted, nil)
		return
	}
	if !errors.Is(err, ports.ErrNotFound) {
		acceptanceError(w, err)
		return
	}
	if _, err := engine.DecodeEvent(raw); err != nil {
		evaluationError(w, err)
		return
	}
	plan, err := engine.Compile(r.Context(), config, h.dependencies.Policy)
	if err != nil {
		evaluationError(w, err)
		return
	}
	snapshots, err := plan.Evaluate(r.Context(), raw)
	if err != nil {
		evaluationError(w, err)
		return
	}
	request, err := h.prepare(config.Source, snapshots, dedup, raw, h.dependencies.Clock.Now().Round(0).UTC())
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "unavailable", "Service unavailable.")
		return
	}
	accepted, err = h.dependencies.Storage.Accept(r.Context(), request)
	if err != nil {
		acceptanceError(w, err)
		return
	}
	committed, duplicate = true, accepted.Duplicate
	if accepted.Duplicate || accepted.Mode != domain.ModeImmediate {
		h.respond(w, r, accepted, nil)
		return
	}
	// Accept also resolves concurrent first requests. Only its nonduplicate
	// immediate-mode winner may start these foreground attempts.
	results := make([]ports.ImmediateResult, 0, len(snapshots))
	budgetCtx, budgetCancel := context.WithTimeout(r.Context(), h.immediateBudget)
	defer budgetCancel()
	for i, snapshot := range snapshots {
		if budgetCtx.Err() != nil {
			for _, id := range request.ImmediateDeliveryIDs[i:] {
				results = append(results, ports.ImmediateResult{DeliveryID: id, State: domain.DeliveryUnknown})
			}
			break
		}
		ctx, cancel := context.WithTimeout(budgetCtx, time.Duration(snapshot.Transporter.HTTP.TimeoutSeconds)*time.Second)
		result, sendErr := h.dependencies.Transporter.Send(ctx, ports.SendRequest{DeliveryID: request.ImmediateDeliveryIDs[i], Snapshot: snapshot})
		cancel()
		outcome := immediateOutcome(request.ImmediateDeliveryIDs[i], result, sendErr)
		// A disconnected sender cannot turn an observed receiver result into a
		// reason to resend. Keep the short metadata write independent of it.
		recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		err := h.immediate.RecordImmediate(recordCtx, outcome)
		recordCancel()
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "outcome_unavailable", "Immediate outcome unavailable; retrying this key will not send again.")
			return
		}
		results = append(results, outcome)
	}
	h.respond(w, r, accepted, results)
}

func (h *Handler) prepare(source domain.Source, snapshots []domain.FrozenDelivery, dedup domain.Deduplication, raw []byte, now time.Time) (ports.AcceptRequest, error) {
	originalExpiry, err := expiry(now, source.OriginalTTL)
	if err != nil || now.UnixNano() > math.MaxInt64-int64(h.window) {
		return ports.AcceptRequest{}, ErrOptions
	}
	dedup.ExpiresAt = now.Add(h.window)
	request := ports.AcceptRequest{
		Event: domain.Event{
			ID:                domain.ID("evt_" + rand.Text()),
			SourceID:          source.ID,
			SourceRevision:    source.Revision,
			AcceptedAt:        now,
			OriginalTTL:       source.OriginalTTL,
			OriginalExpiresAt: originalExpiry,
		},
		Dedup: dedup,
		Mode:  domain.ModeDurable,
	}
	if source.OriginalTTL != domain.NeverPersist {
		request.Original = raw
	}
	deliveryExpiry, err := expiry(now, source.DeliveryTTL)
	if err != nil {
		return ports.AcceptRequest{}, err
	}
	if source.DeliveryTTL == domain.NeverPersist {
		if h.immediate == nil || h.dependencies.Transporter == nil {
			return ports.AcceptRequest{}, ErrOptions
		}
		request.Mode = domain.ModeImmediate
		for range snapshots {
			request.ImmediateDeliveryIDs = append(request.ImmediateDeliveryIDs, domain.ID("dlv_"+rand.Text()))
		}
		return request, nil
	}
	for _, snapshot := range snapshots {
		request.Deliveries = append(request.Deliveries, domain.Delivery{
			ID:            domain.ID("dlv_" + rand.Text()),
			EventID:       request.Event.ID,
			SourceID:      source.ID,
			CreatedAt:     now,
			TTL:           source.DeliveryTTL,
			ExpiresAt:     deliveryExpiry,
			State:         domain.DeliveryPending,
			NextAttemptAt: now,
			Snapshot:      snapshot,
		})
	}
	return request, nil
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, accepted ports.Acceptance, results []ports.ImmediateResult) {
	response := Response{EventID: accepted.EventID, DeliveryIDs: accepted.DeliveryIDs, Duplicate: accepted.Duplicate, Mode: accepted.Mode}
	if response.DeliveryIDs == nil {
		response.DeliveryIDs = []domain.ID{}
	}
	if accepted.Mode != domain.ModeImmediate {
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	if results == nil {
		results = make([]ports.ImmediateResult, 0, len(accepted.DeliveryIDs))
		for _, id := range accepted.DeliveryIDs {
			result := ports.ImmediateResult{DeliveryID: id, State: domain.DeliveryUnknown}
			delivery, err := h.dependencies.Storage.Delivery(r.Context(), id)
			if err == nil && delivery.EventID == accepted.EventID && delivery.TTL == domain.NeverPersist {
				result.State, result.StatusCode, result.ErrorCode = delivery.State, delivery.HTTPStatusCode, delivery.LastErrorCode
			}
			results = append(results, result)
		}
	}
	status := http.StatusOK
	for _, result := range results {
		if result.State == domain.DeliveryFailed || result.State == domain.DeliveryUnknown && !accepted.Duplicate {
			status = http.StatusBadGateway
		}
	}
	response.ImmediateResults = new(results)
	writeJSON(w, status, response)
}

func immediateOutcome(id domain.ID, result ports.SendResult, err error) ports.ImmediateResult {
	outcome := ports.ImmediateResult{DeliveryID: id, State: domain.DeliveryFailed}
	if result.StatusCode >= 100 && result.StatusCode <= 599 {
		outcome.StatusCode = result.StatusCode
	}
	switch {
	case errors.Is(err, ports.ErrPolicyDenied):
		outcome.ErrorCode = "policy_denied"
	case errors.Is(err, ports.ErrSecretUnavailable):
		outcome.ErrorCode = "secret_unavailable"
	case errors.Is(err, ports.ErrInvalidPayload):
		outcome.ErrorCode = "invalid_payload"
	case errors.Is(err, context.DeadlineExceeded):
		outcome.ErrorCode = "timeout"
	case errors.Is(err, context.Canceled):
		outcome.ErrorCode = "canceled"
	case err != nil:
		outcome.ErrorCode = "network_error"
	case result.StatusCode >= 200 && result.StatusCode <= 299:
		outcome.State = domain.DeliveryHTTPAccepted
	case result.StatusCode == 408 || result.StatusCode == 429 || result.StatusCode >= 500 && result.StatusCode <= 599:
		outcome.ErrorCode = "http_retryable"
	case outcome.StatusCode == 0:
		outcome.ErrorCode = "invalid_payload"
	default:
		outcome.ErrorCode = "http_permanent"
	}
	return outcome
}

func validTime(now time.Time) bool {
	return !now.IsZero() && !now.Before(time.Unix(0, 0)) && !now.After(time.Unix(0, math.MaxInt64))
}

func expiry(now time.Time, ttl domain.Retention) (*time.Time, error) {
	if !validTime(now) || ttl < -1 || ttl > domain.Retention(math.MaxInt64/int64(time.Second)) {
		return nil, ErrOptions
	}
	if ttl <= 0 {
		return nil, nil
	}
	duration := time.Duration(ttl) * time.Second
	if now.UnixNano() > math.MaxInt64-int64(duration) {
		return nil, ErrOptions
	}
	return new(now.Add(duration)), nil
}

func acceptanceError(w http.ResponseWriter, err error) {
	if errors.Is(err, ports.ErrDedupConflict) {
		fail(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was used for different input.")
		return
	}
	fail(w, http.StatusServiceUnavailable, "storage_unavailable", "Acceptance storage unavailable.")
}

func evaluationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ports.ErrPolicyDenied):
		fail(w, http.StatusForbidden, "destination_denied", "Destination is not approved.")
	case errors.Is(err, engine.ErrEvent):
		fail(w, http.StatusBadRequest, "invalid_json_object", "Body must contain exactly one JSON object.")
	default:
		fail(w, http.StatusUnprocessableEntity, "evaluation_failed", "Rule or template validation or evaluation failed.")
	}
}

func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
