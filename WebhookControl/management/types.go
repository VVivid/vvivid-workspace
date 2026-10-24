// Package management is the shared authorization and application-command layer
// for REST, MCP and local CLI operations. It performs no outbound delivery.
package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"sync"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
	"github.com/google/jsonschema-go/jsonschema"
)

// Identity is constructed by a trusted verifier or by the local administrator's
// explicit CLI arguments. It is never decoded from management request JSON.
type Identity struct {
	Issuer  string
	Subject string
}

func (i Identity) PrincipalID() string {
	if i.Issuer == "" || i.Subject == "" {
		return ""
	}
	raw, _ := json.Marshal([2]string{i.Issuer, i.Subject})
	return string(raw)
}

type Request[T any] struct {
	SourceID         domain.ID        `json:"source_id"`
	ExpectedRevision *domain.Revision `json:"expected_revision,omitempty"`
	Input            T                `json:"input"`
}

type EmptyInput struct{}
type PutInput struct {
	Configuration domain.Configuration `json:"configuration"`
}
type ActivateInput struct {
	DraftRevision domain.Revision `json:"draft_revision"`
}
type RollbackInput struct {
	TargetRevision domain.Revision `json:"target_revision"`
}
type PreviewInput struct {
	Revision  domain.Revision `json:"revision"`
	Synthetic bool            `json:"synthetic"`
	Event     json.RawMessage `json:"event"`
}
type PageInput struct {
	Limit  int    `json:"limit,omitzero"`
	Cursor string `json:"cursor,omitempty"`
}
type EventInput struct {
	EventID domain.ID `json:"event_id"`
}
type ReplayInput struct {
	EventID        domain.ID       `json:"event_id"`
	Revision       domain.Revision `json:"revision"`
	IdempotencyKey string          `json:"idempotency_key"`
}
type DeliveriesInput struct {
	EventID domain.ID `json:"event_id,omitempty"`
	Limit   int       `json:"limit,omitzero"`
	Cursor  string    `json:"cursor,omitempty"`
}
type DeliveryInput struct {
	DeliveryID domain.ID `json:"delivery_id"`
}
type RetryInput struct {
	DeliveryID     domain.ID `json:"delivery_id"`
	IdempotencyKey string    `json:"idempotency_key"`
}

type ConfigResult struct {
	Head     domain.ConfigurationHead      `json:"head"`
	Snapshot *domain.ConfigurationSnapshot `json:"snapshot,omitempty"`
}
type Page[T any] struct {
	Items  []T    `json:"items"`
	Cursor string `json:"cursor,omitempty"`
}
type PreviewRender struct {
	Route       domain.RevisionRef `json:"route"`
	Transporter domain.RevisionRef `json:"transporter"`
	Body        json.RawMessage    `json:"body"`
}

type PreviewResult struct {
	Renders []PreviewRender `json:"renders"`
}
type SourcesResult struct {
	Sources []domain.ID `json:"sources"`
}
type HealthResult struct {
	Status string `json:"status"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string                    { return e.Code }
func fail(status int, code, message string) error { return &Error{code, message, status} }
func PublicError(err error) *Error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	switch {
	case errors.Is(err, ports.ErrNotFound):
		return &Error{"not_found", "Resource not found", http.StatusNotFound}
	case errors.Is(err, ports.ErrPayloadGone):
		return &Error{"payload_gone", "Required payload was never stored or has expired", http.StatusGone}
	case errors.Is(err, ports.ErrStaleRevision):
		return &Error{"stale_revision", "Expected revision is stale", http.StatusConflict}
	case errors.Is(err, ports.ErrImmutable):
		return &Error{"immutable_revision", "Resource version already contains different content", http.StatusConflict}
	case errors.Is(err, ports.ErrDedupConflict):
		return &Error{"idempotency_conflict", "Request key already selects different input", http.StatusConflict}
	case errors.Is(err, ports.ErrPolicyDenied):
		return &Error{"forbidden", "Destination is not permitted", http.StatusForbidden}
	default:
		return &Error{"unavailable", "Management storage or configuration unavailable", http.StatusServiceUnavailable}
	}
}

func decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return fail(400, "invalid_request", "Malformed or unknown request fields")
	}
	return nil
}

type Operation struct {
	Name         string
	Action       domain.Action
	Description  string
	InputSchema  any
	OutputSchema any
}

func typeSchema[T any]() *jsonschema.Schema {
	s, err := jsonschema.ForType(reflect.TypeFor[T](), &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {Type: "object"}}})
	if err != nil {
		panic(err)
	}
	return s
}

func schema[T any](expected bool) any {
	s := typeSchema[Request[T]]()
	if expected {
		s.Required = append(s.Required, "expected_revision")
	} else {
		delete(s.Properties, "expected_revision")
	}
	return s
}

func globalSchema() any {
	s := schema[EmptyInput](false).(*jsonschema.Schema)
	s.Required = slices.DeleteFunc(s.Required, func(field string) bool { return field == "source_id" })
	return s
}

// Operations is the sole remote command registry. It intentionally excludes
// grant provisioning, secret provisioning and secret reads.
func Operations() []Operation { return slices.Clone(operationRegistry()) }

var operationRegistry = sync.OnceValue(func() []Operation {
	return []Operation{
		{"config_get", domain.ActionReadConfig, "Read active configuration and snapshot pointers", schema[EmptyInput](false), typeSchema[ConfigResult]()},
		{"config_put", domain.ActionWriteConfig, "Validate and save an immutable draft with CAS on latest_revision", schema[PutInput](true), typeSchema[ConfigResult]()},
		{"config_activate", domain.ActionActivateConfig, "Validate and activate the current draft with CAS on active_revision", schema[ActivateInput](true), typeSchema[ConfigResult]()},
		{"config_rollback", domain.ActionActivateConfig, "Create a new activation selecting retained configuration", schema[RollbackInput](true), typeSchema[ConfigResult]()},
		{"preview", domain.ActionPreview, "Evaluate explicitly synthetic input without storing, queuing or sending", schema[PreviewInput](false), typeSchema[PreviewResult]()},
		{"events_list", domain.ActionReadEvents, "List bounded source-scoped event metadata", schema[PageInput](false), typeSchema[Page[domain.Event]]()},
		{"events_get", domain.ActionReadEvents, "Read event metadata without original bytes", schema[EventInput](false), typeSchema[domain.Event]()},
		{"events_replay", domain.ActionReplayEvents, "Replay retained original into new lineage using an explicit configuration revision and request key", schema[ReplayInput](true), typeSchema[ports.ManagementReceipt]()},
		{"deliveries_list", domain.ActionReadDeliveries, "List bounded source-scoped delivery status without bodies or credentials", schema[DeliveriesInput](false), typeSchema[Page[domain.DeliveryStatus]]()},
		{"deliveries_get", domain.ActionReadDeliveries, "Read safe delivery status and frozen revision IDs", schema[DeliveryInput](false), typeSchema[domain.DeliveryStatus]()},
		{"deliveries_retry", domain.ActionRetryDelivery, "Create a new delivery from a failed frozen payload without extending its expiry", schema[RetryInput](false), typeSchema[ports.ManagementReceipt]()},
		{"monitor", domain.ActionReadDeliveries, "Read source-scoped queue, retention and dispatch metrics without payloads or credentials", schema[EmptyInput](false), typeSchema[domain.SourceMetrics]()},
		{"sources_list", "", "Discover only source IDs in current unexpired grants", globalSchema(), typeSchema[SourcesResult]()},
		{"health", "", "Read authenticated management readiness", globalSchema(), typeSchema[HealthResult]()},
	}
})
