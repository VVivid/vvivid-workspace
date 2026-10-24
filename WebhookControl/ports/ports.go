// Package ports defines independent persistence, queue, time, and send boundaries.
// SQLite may implement both Storage and Queue without exposing a shared API.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrDedupConflict     = errors.New("deduplication key reused for different input")
	ErrNoReadyDelivery   = errors.New("no ready delivery")
	ErrLeaseLost         = errors.New("delivery lease lost")
	ErrPolicyDenied      = errors.New("destination policy denied")
	ErrNetwork           = errors.New("delivery network failure")
	ErrInvalidPayload    = errors.New("invalid frozen delivery")
	ErrSecretUnavailable = errors.New("delivery secret unavailable")
	ErrStaleRevision     = errors.New("stale configuration revision")
	ErrImmutable         = errors.New("immutable revision conflict")
	ErrPayloadGone       = errors.New("payload unavailable or expired")
)

type AcceptRequest struct {
	Event      domain.Event
	Dedup      domain.Deduplication
	Original   []byte
	Deliveries []domain.Delivery
	// Mode defaults to durable for existing callers. Immediate IDs create only
	// nonclaimable status metadata; payloads must never be included here.
	Mode                 domain.DeliveryMode
	ImmediateDeliveryIDs []domain.ID
}

type Acceptance struct {
	EventID     domain.ID
	DeliveryIDs []domain.ID
	Duplicate   bool
	Mode        domain.DeliveryMode
}

// ReceiptReader resolves retries before current rules/templates are evaluated.
// It returns ErrNotFound after the finite dedup window and ErrDedupConflict for
// a different fingerprint. It never exposes raw keys or payloads.
type ReceiptReader interface {
	LookupAcceptance(ctx context.Context, sourceID domain.ID, keyHash, fingerprint string) (Acceptance, error)
}

// DeduplicationLimits lets ingress reject a window the storage adapter cannot
// honor before accepting traffic.
type DeduplicationLimits interface {
	DeduplicationWindow() time.Duration
}

type ImmediateResult struct {
	DeliveryID domain.ID            `json:"delivery_id"`
	State      domain.DeliveryState `json:"state"`
	StatusCode int                  `json:"status_code,omitzero"`
	ErrorCode  string               `json:"error_code,omitempty"`
}

// ImmediateStorage records a single outcome for already committed TTL-zero
// metadata. A missing outcome means unknown, never permission to send again.
type ImmediateStorage interface {
	RecordImmediate(context.Context, ImmediateResult) error
}

type Storage interface {
	// Accept atomically commits event metadata, permitted original bytes,
	// deduplication, and all initial durable jobs, or commits none of them.
	// A matching duplicate returns the first Acceptance without inserting jobs.
	// ErrDedupConflict means the key exists with a different fingerprint.
	// Original must be nil for original TTL 0; jobs with delivery TTL 0 are
	// rejected. Such deliveries use the separate immediate-send path.
	Accept(context.Context, AcceptRequest) (Acceptance, error)

	Event(context.Context, domain.ID) (domain.Event, error)
	Original(context.Context, domain.ID) ([]byte, error)
	Delivery(context.Context, domain.ID) (domain.Delivery, error)
}

type ConfigurationStore interface {
	ActiveConfiguration(context.Context, domain.ID) (domain.Configuration, error)
}

// ManagementStore serializes authorization, reads, mutations and audit in one
// transaction. A callback error rolls back everything. Implementations must not
// reuse a transaction after the callback, or signal queue work before commit.
type ManagementStore interface {
	Manage(context.Context, func(ManagementTx) error) error
}

type ManagementTx interface {
	Grants(principal string) ([]domain.Grant, error)
	Head(sourceID domain.ID) (domain.ConfigurationHead, error)
	Configuration(sourceID domain.ID, snapshotRevision domain.Revision) (domain.ConfigurationSnapshot, error)
	SourceRevision(sourceID domain.ID, resourceRevision domain.Revision) (domain.Source, error)
	PutConfiguration(sourceID domain.ID, expectedLatest domain.Revision, config domain.Configuration) (domain.ConfigurationSnapshot, error)
	// SelectConfiguration compares active and, unless rolling back, requires the
	// selected snapshot to be the current draft. Either selection creates a new revision.
	SelectConfiguration(sourceID domain.ID, expectedActive, selectedSnapshot domain.Revision, rollback bool) (domain.ConfigurationSnapshot, error)
	Secret(ref domain.SecretRef) ([]byte, error)
	SecretPurpose(ref domain.SecretRef) (domain.SecretPurpose, error)
	Event(sourceID, eventID domain.ID) (domain.Event, error)
	Original(sourceID, eventID domain.ID) ([]byte, error)
	Events(sourceID, afterID domain.ID, limit int) ([]domain.Event, error)
	DeliveryStatus(sourceID, deliveryID domain.ID) (domain.DeliveryStatus, error)
	Deliveries(sourceID, eventID, afterID domain.ID, limit int) ([]domain.DeliveryStatus, error)
	SourceMetrics(sourceID domain.ID, now time.Time) (domain.SourceMetrics, error)
	Delivery(sourceID, deliveryID domain.ID) (domain.Delivery, error)
	Receipt(principal, operation string, source domain.ID, keyHash, fingerprint string) (ManagementReceipt, error)
	Replay(request AcceptRequest, parentEventID domain.ID) error
	Retry(delivery domain.Delivery, parentDeliveryID domain.ID) error
	SaveReceipt(principal, operation string, source domain.ID, keyHash, fingerprint string, result ManagementReceipt) error
	Audit(entry domain.Audit) error
}

type ManagementReceipt struct {
	EventID     domain.ID   `json:"event_id,omitempty"`
	DeliveryIDs []domain.ID `json:"delivery_ids"`
	Duplicate   bool        `json:"duplicate"`
}

// LocalAdministrator is never exposed through remote operation dispatch.
type LocalAdministrator interface {
	ProvisionGrant(context.Context, string, domain.Grant) error
	PutSecret(context.Context, string, domain.SecretRef, domain.SecretPurpose, []byte) error
	SecretMetadata(context.Context) ([]domain.SecretMetadata, error)
	GrantMetadata(context.Context) ([]domain.Grant, error)
	AuditMetadata(context.Context, int) ([]domain.Audit, error)
}

// SecretReader resolves an immutable server-owned version. Ingress uses it only
// for source authentication; receiver secrets are resolved by the Transporter.
type SecretReader interface {
	ReadSecret(context.Context, domain.SecretRef) ([]byte, error)
}

// DestinationPolicy approves configured URLs. Transporters must recheck the
// frozen destination and DNS/IP policy at every actual attempt as well.
type DestinationPolicy interface {
	ApproveDestination(context.Context, domain.ID, domain.HTTPConfig) error
}

// LeaseToken fences one claim. Every new claim, including recovery of an
// expired lease, receives a new token; an old worker cannot change the job.
type LeaseToken struct {
	DeliveryID domain.ID
	Token      string
}

type Lease struct {
	Delivery domain.Delivery
	Owner    LeaseToken
	Until    time.Time
}

type Queue interface {
	// Claim atomically acquires one due, unexpired job (or an expired lease),
	// increments its attempt, and returns an exclusive token until now+leaseFor.
	// It returns ErrNoReadyDelivery if no job is eligible.
	Claim(ctx context.Context, now time.Time, leaseFor time.Duration) (Lease, error)
	// All transitions require the current, unexpired token at now; otherwise
	// they return ErrLeaseLost. Expired payloads cannot be renewed or sent.
	Renew(ctx context.Context, owner LeaseToken, now time.Time, leaseFor time.Duration) (time.Time, error)
	// Complete records receiver HTTP acceptance, not completion of agent work.
	Complete(ctx context.Context, owner LeaseToken, now time.Time, statusCode int) error
	// Retry releases the lease and schedules a pending job; next must be before
	// payload expiry. Terminate releases the lease into the failed state.
	Retry(ctx context.Context, owner LeaseToken, now, next time.Time, errorCode string) error
	Terminate(ctx context.Context, owner LeaseToken, now time.Time, errorCode string) error
	// Wakeups are coalesced hints after commits. Workers must also poll durable
	// state on startup and on timers; correctness cannot depend on a channel.
	Wakeups() <-chan struct{}
}

type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type SendRequest struct {
	DeliveryID domain.ID
	Snapshot   domain.FrozenDelivery
}

type SendResult struct {
	StatusCode int
	RetryAfter time.Duration
}

type Transporter interface {
	// Send performs exactly one attempt using only the frozen sanitized payload
	// and configuration. A 2xx result is receiver acceptance. Network errors can
	// be ambiguous; the worker decides whether to retry with the same delivery ID.
	// Implementations must stop promptly when the context is canceled.
	Send(context.Context, SendRequest) (SendResult, error)
}
