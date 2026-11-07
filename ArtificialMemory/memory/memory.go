// Package memory defines the account-owned, append-only decision journal.
// Callers supply authenticated account/client identities; source text is data,
// never an instruction or evidence that a human approved a decision.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid memory input")
	ErrNotFound = errors.New("memory reference not found")
	ErrConflict = errors.New("idempotency key reused with different input")
	ErrCursor   = errors.New("invalid memory continuation")
)

const (
	MaxEntries      = 100
	MaxTags         = 32
	MaxLinks        = 32
	MaxSpaces       = 1000
	MaxPayloadBytes = 32 << 10
	DefaultTimezone = "America/New_York"
)

type Kind string

const (
	Topic      Kind = "topic"
	Context    Kind = "context"
	Proposal   Kind = "proposal"
	Decision   Kind = "decision"
	Note       Kind = "note"
	Retraction Kind = "retraction"
)

type LinkType string

const (
	DependsOn  LinkType = "depends_on"
	RelatedTo  LinkType = "related_to"
	Supersedes LinkType = "supersedes"
	Corrects   LinkType = "corrects"
	Retracts   LinkType = "retracts"
)

type Actor struct {
	AccountID string `json:"account_id"`
	ClientID  string `json:"client_id"`
}

// Reference identifies either a saved node or an earlier entry's local_id.
type Reference struct {
	SpanID  string `json:"span_id,omitempty"`
	LocalID string `json:"local_id,omitempty"`
}

type LinkInput struct {
	Type   LinkType  `json:"type"`
	Target Reference `json:"target"`
}

type Source struct {
	Reference string `json:"reference"`
	Excerpt   string `json:"excerpt,omitempty"`
}

type Provenance struct {
	Attribution string   `json:"attribution,omitempty"`
	Sources     []Source `json:"sources,omitempty"`
}

type Alternative struct {
	Option      string   `json:"option"`
	Disposition string   `json:"disposition"`
	Reasons     []string `json:"reasons"`
}

// DecisionPayload is the required v1 core. Additional JSON fields are preserved.
// Empty reasons/alternatives are allowed: absent evidence must not be fabricated.
type DecisionPayload struct {
	Question     string        `json:"question"`
	Outcome      string        `json:"outcome"`
	Reasons      []string      `json:"reasons"`
	Alternatives []Alternative `json:"alternatives"`
}

type Entry struct {
	LocalID       string          `json:"local_id,omitempty"`
	Parent        Reference       `json:"parent,omitzero"`
	Kind          Kind            `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	Title         string          `json:"title"`
	Context       string          `json:"context,omitempty"`
	ExternalRef   string          `json:"external_ref,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    *time.Time      `json:"occurred_at,omitempty"`
	Historical    bool            `json:"historical,omitzero"`
	TimeUnknown   bool            `json:"time_unknown,omitzero"`
	Provenance    Provenance      `json:"provenance,omitzero"`
	Tags          []string        `json:"tags,omitempty"`
	Links         []LinkInput     `json:"links,omitempty"`
}

type RememberRequest struct {
	Space   string  `json:"space"`
	Key     string  `json:"idempotency_key"`
	TopicID string  `json:"topic_id,omitempty"`
	Entries []Entry `json:"entries"`
}

type Record struct {
	SpanID        string          `json:"span_id"`
	TopicID       string          `json:"topic_id"`
	ParentID      string          `json:"parent_id,omitempty"`
	Sequence      int64           `json:"sequence"`
	Space         string          `json:"space"`
	Kind          Kind            `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	Title         string          `json:"title"`
	Context       string          `json:"context,omitempty"`
	ExternalRef   string          `json:"external_ref,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	OccurredAt    *time.Time      `json:"occurred_at,omitempty"`
	RecordedAt    time.Time       `json:"recorded_at"`
	Historical    bool            `json:"historical,omitzero"`
	TimeUnknown   bool            `json:"time_unknown,omitzero"`
	Provenance    Provenance      `json:"provenance,omitzero"`
	Tags          []string        `json:"tags,omitempty"`
	Writer        Actor           `json:"writer"`
	RequestKey    string          `json:"request_key"`
}

type RememberResult struct {
	TopicID string   `json:"topic_id"`
	Records []Record `json:"records"`
}

type Space struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type CalendarRange struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"` // exclusive
	Timezone  string `json:"timezone,omitempty"`
}

// TimeRange is half-open. Calendar and instant bounds are mutually exclusive.
type TimeRange struct {
	From     *time.Time     `json:"from,omitempty"`
	Before   *time.Time     `json:"before,omitempty"`
	Calendar *CalendarRange `json:"calendar,omitempty"`
}

type Order string

const (
	Chronological Order = "chronological"
	Newest        Order = "newest"
	Relevance     Order = "relevance"
)

type Query struct {
	Spaces       []string  `json:"spaces,omitempty"`
	AllSpaces    bool      `json:"all_spaces,omitzero"`
	TopicIDs     []string  `json:"topic_ids,omitempty"`
	SpanIDs      []string  `json:"span_ids,omitempty"`
	ExternalRef  string    `json:"external_ref,omitempty"`
	Keywords     string    `json:"keywords,omitempty"`
	Tags         []string  `json:"tags,omitempty"`
	Kinds        []Kind    `json:"kinds,omitempty"`
	Occurred     TimeRange `json:"occurred,omitzero"`
	Recorded     TimeRange `json:"recorded,omitzero"`
	Order        Order     `json:"order,omitempty"`
	Limit        int       `json:"limit,omitzero"`
	ContextLimit int       `json:"context_limit,omitzero"`
	LinkLimit    int       `json:"link_limit,omitzero"`
	Cursor       string    `json:"cursor,omitempty"`
}

// NodeReference carries no expanded payload. Related nodes may be in another
// space of the same account; retrieve that space explicitly to expand it.
type NodeReference struct {
	SpanID  string `json:"span_id"`
	TopicID string `json:"topic_id"`
	Space   string `json:"space"`
}

type Relationship struct {
	Type   LinkType      `json:"type"`
	Source NodeReference `json:"source"`
	Target NodeReference `json:"target"`
}

// Page separates filtered matches from unfiltered required ancestors. Support
// pages emit no repeated Matches; MatchIDs identifies their fixed match page.
// Follow NextCursor until empty to consume all matching and supporting data.
// Completeness flags describe this snapshot, not an entire topic's history.
type Page struct {
	Matches               []Record       `json:"matches"`
	MatchIDs              []string       `json:"match_ids"`
	Context               []Record       `json:"context"`
	Relationships         []Relationship `json:"relationships"`
	SnapshotSequence      int64          `json:"snapshot_sequence"`
	AppliedOccurred       TimeRange      `json:"applied_occurred,omitzero"`
	AppliedRecorded       TimeRange      `json:"applied_recorded,omitzero"`
	ContextComplete       bool           `json:"context_complete"`
	RelationshipsComplete bool           `json:"relationships_complete"`
	MatchesComplete       bool           `json:"matches_complete"`
	Complete              bool           `json:"complete"`
	NextCursor            string         `json:"next_cursor,omitempty"`
}

type Store interface {
	Remember(context.Context, Actor, RememberRequest) (RememberResult, error)
	Retrieve(context.Context, string, Query) (Page, error)
	ListSpaces(context.Context, string) ([]Space, error)
	Close() error
}
