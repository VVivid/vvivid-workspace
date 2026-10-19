package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Mutation struct {
	Key       string
	Operation string
	Payload   []byte
	RequestID string
	ClientID  string
}

type MutationResult struct {
	Data     []byte
	Replayed bool
}

// Tx exposes owner-qualified repository primitives, never raw SQL or Commit.
// Services validate and research before Write; callbacks perform database work
// only and return a bounded JSON result to persist atomically with their changes.
type Tx struct {
	sql      *sql.Tx
	ctx      context.Context
	owner    Owner
	identity ExternalIdentity
	now      int64
	affected []affectedRecord
}

type affectedRecord struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Write compares exact payload bytes and operation names. Adapters must build the
// canonical payload from all semantic inputs before calling it, including any
// expected revisions. The key namespace is per account across all operations.
func (s *Store) Write(ctx context.Context, owner Owner, mutation Mutation, apply func(*Tx) ([]byte, error)) (MutationResult, error) {
	if owner.id == "" {
		return MutationResult{}, ErrOwnerRequired
	}
	if mutation.Key == "" || len(mutation.Key) > 128 || mutation.Operation == "" || len(mutation.Operation) > 128 ||
		mutation.RequestID == "" || len(mutation.RequestID) > 128 || mutation.ClientID == "" || len(mutation.ClientID) > 256 ||
		len(mutation.Payload) > 1<<20 || !json.Valid(mutation.Payload) || apply == nil {
		return MutationResult{}, fmt.Errorf("mutation requires bounded identifiers, JSON payload and database callback")
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, err
	}
	defer tx.Rollback()
	mutationTx := &Tx{sql: tx, ctx: ctx, owner: owner, now: time.Now().UTC().UnixMilli(), affected: []affectedRecord{}}
	if err := tx.QueryRowContext(ctx, `SELECT i.issuer, i.subject FROM identities i JOIN accounts a ON a.id = i.owner_id
		WHERE i.owner_id = ? AND a.disabled_at IS NULL`, owner.id).Scan(&mutationTx.identity.Issuer, &mutationTx.identity.Subject); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MutationResult{}, ErrOwnerRequired
		}
		return MutationResult{}, err
	}
	hash := sha256.Sum256(mutation.Payload)
	var previousHash, previousResult []byte
	var previousOperation string
	err = tx.QueryRowContext(ctx, "SELECT operation, payload_hash, result FROM idempotency_results WHERE owner_id = ? AND key = ?", owner.id, mutation.Key).
		Scan(&previousOperation, &previousHash, &previousResult)
	if err == nil {
		if previousOperation != mutation.Operation || !bytes.Equal(previousHash, hash[:]) {
			return MutationResult{}, ErrIdempotencyConflict
		}
		return MutationResult{Data: previousResult, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MutationResult{}, err
	}
	result, err := apply(mutationTx)
	if err != nil {
		return MutationResult{}, err
	}
	if len(result) > 1<<20 || !json.Valid(result) {
		return MutationResult{}, fmt.Errorf("mutation result must be bounded JSON")
	}
	// Domain changes, audit evidence and the replay result share one commit. Any
	// error below also rolls back the repository writes made by the callback.
	affected, err := json.Marshal(mutationTx.affected)
	if err != nil {
		return MutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(owner_id, id, actor_issuer, actor_subject, actor_client, operation, affected_records_json, request_id, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, owner.id, rand.Text(), mutationTx.identity.Issuer, mutationTx.identity.Subject, mutation.ClientID, mutation.Operation, string(affected), mutation.RequestID, mutationTx.now); err != nil {
		return MutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_results(owner_id, key, operation, payload_hash, result, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, owner.id, mutation.Key, mutation.Operation, hash[:], result, mutationTx.now); err != nil {
		return MutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{Data: bytes.Clone(result)}, nil
}
