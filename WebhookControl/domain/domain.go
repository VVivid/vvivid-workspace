// Package domain defines the configuration and durable records of WebhookControl.
package domain

import (
	"encoding/json"
	"time"
)

type ID string
type Revision uint64

type RevisionRef struct {
	ID       ID       `json:"id"`
	Revision Revision `json:"revision"`
}

// Retention is measured from acceptance: 0 never stores payload bytes, -1 keeps
// them indefinitely, and a positive value expires them after that many seconds.
// Values below -1 are invalid. Metadata and deduplication have separate lifetimes.
type Retention int64

const (
	NeverPersist       Retention = 0
	KeepIndefinitely   Retention = -1
	DefaultOriginalTTL Retention = 86400
	DefaultDeliveryTTL Retention = 604800
)

// SecretRef names an immutable version in the server-owned secret store.
// Configuration and delivery records contain references, never secret values.
type SecretRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type AuthKind string

const (
	AuthBearer AuthKind = "bearer"
	AuthHMAC   AuthKind = "hmac-sha256"
)

type SourceAuth struct {
	Kind   AuthKind  `json:"kind"`
	Secret SecretRef `json:"secret"`
}

type Source struct {
	ID          ID         `json:"id"`
	Revision    Revision   `json:"revision"`
	Name        string     `json:"name"`
	Enabled     bool       `json:"enabled"`
	Auth        SourceAuth `json:"auth"`
	OriginalTTL Retention  `json:"original_ttl"`
	DeliveryTTL Retention  `json:"delivery_ttl"`
}

type Rule struct {
	ID         ID       `json:"id"`
	Revision   Revision `json:"revision"`
	SourceID   ID       `json:"source_id"`
	Expression string   `json:"expression"`
}

// Template maps output field names to CEL expressions. Only explicitly mapped
// fields survive rendering; the original input is never forwarded implicitly.
type Template struct {
	ID       ID                `json:"id"`
	Revision Revision          `json:"revision"`
	SourceID ID                `json:"source_id"`
	Fields   map[string]string `json:"fields"`
}

// Route matches when enabled and every referenced rule returns true.
// An empty rule list matches every valid event from the source.
type Route struct {
	ID           ID            `json:"id"`
	Revision     Revision      `json:"revision"`
	SourceID     ID            `json:"source_id"`
	Enabled      bool          `json:"enabled"`
	Rules        []RevisionRef `json:"rules"`
	Template     RevisionRef   `json:"template"`
	Transporters []RevisionRef `json:"transporters"`
}

type TransporterKind string

const TransporterHTTP TransporterKind = "http"

// HTTPConfig has public literal headers and separately referenced secret headers.
// URL must pass the server destination policy as well as the caller's grant.
type HTTPConfig struct {
	URL            string               `json:"url"`
	Headers        map[string]string    `json:"headers,omitempty"`
	SecretHeaders  map[string]SecretRef `json:"secret_headers,omitempty"`
	TimeoutSeconds int64                `json:"timeout_seconds"`
}

type Transporter struct {
	ID       ID              `json:"id"`
	Revision Revision        `json:"revision"`
	SourceID ID              `json:"source_id"`
	Kind     TransporterKind `json:"kind"`
	HTTP     HTTPConfig      `json:"http"`
}

// Configuration is one consistent active source snapshot. All referenced
// revisions must exist in this snapshot and belong to the same source.
type Configuration struct {
	Source       Source        `json:"source"`
	Rules        []Rule        `json:"rules"`
	Templates    []Template    `json:"templates"`
	Routes       []Route       `json:"routes"`
	Transporters []Transporter `json:"transporters"`
}

// Event is metadata only. Original JSON is read through a separate authorized
// storage operation so ordinary status queries cannot expose it accidentally.
type Event struct {
	ID                ID         `json:"id"`
	SourceID          ID         `json:"source_id"`
	SourceRevision    Revision   `json:"source_revision"`
	AcceptedAt        time.Time  `json:"accepted_at"`
	OriginalTTL       Retention  `json:"original_ttl"`
	OriginalExpiresAt *time.Time `json:"original_expires_at,omitempty"`
	ReplayOf          ID         `json:"replay_of,omitempty"`
}

// Deduplication hashes are keyed digests, never raw keys or original input.
// Reusing a key with a different fingerprint is a conflict, not a duplicate.
type Deduplication struct {
	SourceID    ID
	KeyHash     string
	Fingerprint string
	ExpiresAt   time.Time
}

// FrozenDelivery is copied at acceptance. Workers never reload active rules,
// templates, destinations, or secret versions when retrying this delivery.
type FrozenDelivery struct {
	Source      RevisionRef     `json:"source"`
	Route       RevisionRef     `json:"route"`
	Rules       []RevisionRef   `json:"rules"`
	Template    RevisionRef     `json:"template"`
	Transporter Transporter     `json:"transporter"`
	Body        json.RawMessage `json:"body"`
}

type DeliveryState string

const (
	DeliveryPending      DeliveryState = "pending"
	DeliveryLeased       DeliveryState = "leased"
	DeliveryHTTPAccepted DeliveryState = "http_accepted"
	DeliveryFailed       DeliveryState = "failed"
	DeliveryExpired      DeliveryState = "expired"
	// DeliveryUnknown is a metadata-only immediate attempt whose result has not
	// been recorded. It is never claimable and must never be resent.
	DeliveryUnknown DeliveryState = "unknown"
)

type DeliveryMode string

const (
	ModeDurable   DeliveryMode = "durable"
	ModeImmediate DeliveryMode = "immediate"
)

type Delivery struct {
	ID             ID             `json:"id"`
	EventID        ID             `json:"event_id"`
	SourceID       ID             `json:"source_id"`
	CreatedAt      time.Time      `json:"created_at"`
	TTL            Retention      `json:"ttl"`
	ExpiresAt      *time.Time     `json:"expires_at,omitempty"`
	State          DeliveryState  `json:"state"`
	Attempt        uint64         `json:"attempt"`
	NextAttemptAt  time.Time      `json:"next_attempt_at"`
	LastErrorCode  string         `json:"last_error_code,omitempty"`
	HTTPStatusCode int            `json:"http_status_code,omitzero"`
	Snapshot       FrozenDelivery `json:"snapshot"`
	RetryOf        ID             `json:"retry_of,omitempty"`
}

// Configuration revisions are monotonic snapshot selections, separate from the
// immutable resource revisions inside Configuration. Rollback creates a new one.
type ConfigurationSnapshot struct {
	Revision      Revision      `json:"revision"`
	SelectedFrom  Revision      `json:"selected_from,omitzero"`
	Configuration Configuration `json:"configuration"`
}

type ConfigurationHead struct {
	Latest Revision `json:"latest_revision"`
	Draft  Revision `json:"draft_revision"`
	Active Revision `json:"active_revision"`
}

// DeliveryStatus deliberately cannot contain bodies, URLs, headers or secret refs.
// Frozen reference metadata survives ciphertext expiry and cleanup.
type DeliveryStatus struct {
	ID             ID            `json:"id"`
	EventID        ID            `json:"event_id"`
	SourceID       ID            `json:"source_id"`
	CreatedAt      time.Time     `json:"created_at"`
	TTL            Retention     `json:"ttl"`
	ExpiresAt      *time.Time    `json:"expires_at,omitempty"`
	State          DeliveryState `json:"state"`
	Attempt        uint64        `json:"attempt"`
	NextAttemptAt  time.Time     `json:"next_attempt_at"`
	LastErrorCode  string        `json:"last_error_code,omitempty"`
	HTTPStatusCode int           `json:"http_status_code,omitzero"`
	Source         RevisionRef   `json:"source"`
	Route          RevisionRef   `json:"route"`
	Rules          []RevisionRef `json:"rules"`
	Template       RevisionRef   `json:"template"`
	Transporter    RevisionRef   `json:"transporter"`
	RetryOf        ID            `json:"retry_of,omitempty"`
}

// SourceMetrics aggregates only one authorized source's retained records.
// CiphertextBytes counts payload rows, including expired rows awaiting cleanup;
// it is logical usage rather than total database or disk size.
type SourceMetrics struct {
	SourceID                ID                      `json:"source_id"`
	RetainedEvents          int64                   `json:"retained_events"`
	Deliveries              map[DeliveryState]int64 `json:"deliveries"`
	OldestQueuedAgeSeconds  float64                 `json:"oldest_queued_age_seconds"`
	RetryAttempts           int64                   `json:"retry_attempts"`
	CiphertextBytes         int64                   `json:"ciphertext_bytes"`
	FirstDispatchCount      int64                   `json:"first_dispatch_count"`
	FirstDispatchSumSeconds float64                 `json:"first_dispatch_sum_seconds"`
	FirstDispatchMaxSeconds float64                 `json:"first_dispatch_max_seconds"`
	Ingress                 *IngressCounters        `json:"ingress,omitzero"`
}

// IngressCounters are bounded process-local observations, reset on restart.
type IngressCounters struct {
	AcceptedEvents    uint64 `json:"accepted_events"`
	DuplicateRequests uint64 `json:"duplicate_requests"`
	RejectedRequests  uint64 `json:"rejected_requests"`
}

type Audit struct {
	ID          ID        `json:"id"`
	At          time.Time `json:"at"`
	PrincipalID string    `json:"principal_id"`
	Operation   string    `json:"operation"`
	SourceID    ID        `json:"source_id,omitempty"`
	ResourceID  ID        `json:"resource_id,omitempty"`
	Revision    Revision  `json:"revision,omitzero"`
}

type SecretMetadata struct {
	Ref       SecretRef     `json:"ref"`
	CreatedAt time.Time     `json:"created_at"`
	Purpose   SecretPurpose `json:"purpose"`
}

type SecretPurpose string

const (
	SecretSourceAuth SecretPurpose = "source-auth"
	SecretHTTPHeader SecretPurpose = "http-header"
)

type Action string

const (
	ActionReadConfig     Action = "config.read"
	ActionWriteConfig    Action = "config.write"
	ActionActivateConfig Action = "config.activate"
	ActionPreview        Action = "preview"
	ActionReadEvents     Action = "events.read"
	ActionReplayEvents   Action = "events.replay"
	ActionReadDeliveries Action = "deliveries.read"
	ActionRetryDelivery  Action = "deliveries.retry"
)

// RetentionLimit permits 0 through MaxSeconds (which must be nonnegative).
// A TTL of -1 requires AllowIndefinite; it is never implied by MaxSeconds.
type RetentionLimit struct {
	MaxSeconds      int64 `json:"max_seconds"`
	AllowIndefinite bool  `json:"allow_indefinite"`
}

// Grant is an intersection of explicit permissions, never a wildcard. Empty
// lists confer no access. Retention limits also constrain activation and replay.
type Grant struct {
	ID                 ID             `json:"id"`
	PrincipalID        string         `json:"principal_id"`
	SourceIDs          []ID           `json:"source_ids"`
	Actions            []Action       `json:"actions"`
	DestinationOrigins []string       `json:"destination_origins"`
	SecretNames        []string       `json:"secret_names"`
	OriginalRetention  RetentionLimit `json:"original_retention"`
	DeliveryRetention  RetentionLimit `json:"delivery_retention"`
	ExpiresAt          *time.Time     `json:"expires_at,omitempty"`
}
