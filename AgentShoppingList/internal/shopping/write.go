package shopping

import (
	"context"
	"encoding/json"
	"errors"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

func write[T any](s *Service, ctx context.Context, p identity.Principal, meta WriteMeta, operation string, input any, apply func(*sqlite.Tx) (T, error)) (WriteResult[T], error) {
	if !validID(meta.IdempotencyKey) || !validID(meta.RequestID) {
		return WriteResult[T]{}, invalid("bounded request_id and idempotency_key are required")
	}
	// Request IDs identify attempts; they do not change mutation semantics. Keep
	// retries with a new correlation ID bound to the same canonical domain input.
	raw, err := json.Marshal(input)
	if err != nil {
		return WriteResult[T]{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return WriteResult[T]{}, err
	}
	delete(fields, "request_id")
	delete(fields, "idempotency_key")
	payload, err := json.Marshal(fields)
	if err != nil {
		return WriteResult[T]{}, err
	}
	result, err := s.store.Write(ctx, p.Owner(), sqlite.Mutation{Key: meta.IdempotencyKey, Operation: operation, Payload: payload, RequestID: meta.RequestID, ClientID: p.ClientID()}, func(tx *sqlite.Tx) ([]byte, error) {
		value, err := apply(tx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)
	})
	if err != nil {
		if errors.Is(err, sqlite.ErrLimit) {
			return WriteResult[T]{}, invalid("This list or quantity reached its limit. Use a new list or a smaller quantity.")
		}
		return WriteResult[T]{}, err
	}
	var value T
	if err := json.Unmarshal(result.Data, &value); err != nil {
		return WriteResult[T]{}, err
	}
	return WriteResult[T]{Record: value, Replayed: result.Replayed}, nil
}
