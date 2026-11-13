// Package store owns VocabBank's owner-qualified data rules and SQLite transactions.
package store

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	Read         = "vocabbank:read"
	Write        = "vocabbank:write"
	NeedsDetails = "needs_details"
	Complete     = "complete"
)

var (
	ErrInvalid   = errors.New("invalid input")
	ErrNotFound  = errors.New("word not found")
	ErrForbidden = errors.New("permission denied")
	ErrConflict  = errors.New("conflict")
	ErrDuplicate = fmt.Errorf("%w: term already saved", ErrConflict)
	ErrStale     = fmt.Errorf("%w: word has changed", ErrConflict)
	ErrRetryKey  = fmt.Errorf("%w: idempotency key reused with different input", ErrConflict)
)

type Actor struct {
	Owner  string
	Scopes []string
}

func (a Actor) Require(scope string) error {
	if a.Owner == "" || !slices.Contains(a.Scopes, scope) {
		return ErrForbidden
	}
	return nil
}

type Owner struct {
	ID      string `json:"id"`
	Issuer  string `json:"issuer"`
	Subject string `json:"-"`
	Label   string `json:"label"`
}

type Example struct {
	Context  string `json:"context"`
	Sentence string `json:"sentence"`
}

type Sense struct {
	PartOfSpeech  string    `json:"part_of_speech"`
	Definition    string    `json:"definition"`
	WhenToUse     string    `json:"when_to_use"`
	Examples      []Example `json:"examples"`
	CommonPhrases []string  `json:"common_phrases"`
	UsageTip      string    `json:"usage_tip,omitempty"`
}

type Word struct {
	ID               string  `json:"id"`
	Term             string  `json:"term"`
	OriginalSentence string  `json:"original_sentence"`
	SourceURL        string  `json:"source_url"`
	PersonalNote     string  `json:"personal_note"`
	Senses           []Sense `json:"senses"`
	Status           string  `json:"status"`
	Revision         int     `json:"revision"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// Capture contains only user-owned fields. Completing learning content never
// replaces these fields, even when an agent's completion is retried later.
type Capture struct {
	Term             string `json:"term"`
	OriginalSentence string `json:"original_sentence,omitempty"`
	SourceURL        string `json:"source_url,omitempty"`
	PersonalNote     string `json:"personal_note,omitempty"`
}

type AddInput struct {
	Capture
	IdempotencyKey string `json:"idempotency_key"`
}

// AddWordsItem can capture a draft or create a complete word. Empty senses
// (including omitted or null JSON) mean a draft, just as add_word creates.
type AddWordsItem struct {
	AddInput
	Senses []Sense `json:"senses,omitempty"`
}

type AddWordsInput struct {
	Words []AddWordsItem `json:"words"`
}

// AddWordsItemResult belongs to the corresponding input position. A failed item
// has no saved Word; successful items commit independently of the other items.
type AddWordsItemResult struct {
	Word Word
	Err  error
}

type DeleteWordsItem struct {
	ID               string `json:"id"`
	ExpectedRevision int    `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type DeleteWordsInput struct {
	Words []DeleteWordsItem `json:"words"`
}

// DeleteWordsItemResult returns the removed word's snapshot on success. The
// snapshot is retry evidence, not a word that is still present in the bank.
type DeleteWordsItemResult struct {
	DeletedWord Word
	Err         error
}

type UpdateInput struct {
	Capture
	ID               string `json:"id"`
	ExpectedRevision int    `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type CompleteInput struct {
	ID               string  `json:"id"`
	ExpectedRevision int     `json:"expected_revision"`
	IdempotencyKey   string  `json:"idempotency_key"`
	Senses           []Sense `json:"senses"`
}

type Filter struct {
	Query    string `json:"query,omitempty"`
	Status   string `json:"status,omitempty"`
	Page     int    `json:"page,omitzero"`
	PageSize int    `json:"page_size,omitzero"`
}
type Page struct {
	Items    []Word `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type Session struct {
	Hash      []byte
	Owner     string
	Host      string
	Cipher    []byte
	ExpiresAt time.Time
}
