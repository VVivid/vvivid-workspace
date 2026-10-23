package shopping

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"

	"agentshoppinglist/internal/identity"
	"agentshoppinglist/internal/store/sqlite"
)

type CreateShareInput struct {
	WriteMeta
	ListID           string                  `json:"list_id"`
	ExpectedRevision int64                   `json:"expected_revision"`
	Items            []sqlite.ShareSelection `json:"items"`
}

func (s *Service) CreateShare(ctx context.Context, p identity.Principal, input CreateShareInput) (WriteResult[sqlite.CreatedShare], error) {
	if err := s.require(ctx, p, identity.Read); err != nil {
		return WriteResult[sqlite.CreatedShare]{}, err
	}
	if err := s.require(ctx, p, identity.ListsWrite); err != nil {
		return WriteResult[sqlite.CreatedShare]{}, err
	}
	if !validID(input.ListID) || input.ExpectedRevision < 1 || len(input.Items) == 0 || len(input.Items) > 500 {
		return WriteResult[sqlite.CreatedShare]{}, invalid("Select 1 to 500 items from a current list.")
	}
	seen := make(map[string]bool, len(input.Items))
	for _, item := range input.Items {
		if !validID(item.ItemID) || item.ExpectedRevision < 1 || item.ExpectedProductRevision < 1 || seen[item.ItemID] {
			return WriteResult[sqlite.CreatedShare]{}, invalid("Select distinct items with their current revisions.")
		}
		seen[item.ItemID] = true
	}
	result, err := write(s, ctx, p, input.WriteMeta, "create_temporary_share", input, func(tx *sqlite.Tx) (sqlite.CreatedShare, error) {
		return tx.CreateShare(input.ListID, input.ExpectedRevision, input.Items, randomShareCode)
	})
	if errors.Is(err, sqlite.ErrShareLimit) {
		return WriteResult[sqlite.CreatedShare]{}, invalid("Sharing limit reached. Try again in an hour.")
	}
	return result, err
}

func randomShareCode() (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var code [5]byte
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		code[i] = letters[n.Int64()]
	}
	return string(code[:]), nil
}

func (s *Service) PublicShare(ctx context.Context, code string) (sqlite.PublicShare, error) {
	return s.store.PublicShare(ctx, code)
}

func (s *Service) SetSharedItem(ctx context.Context, code, token string, meta WriteMeta, input sqlite.SharedItemInput) (WriteResult[sqlite.SharedItem], error) {
	if !validID(meta.IdempotencyKey) || !validID(meta.RequestID) || len(token) != 26 || input.Position < 1 || input.Position > 500 || input.ExpectedRevision < 1 {
		return WriteResult[sqlite.SharedItem]{}, invalid("Send a picked-up state and current revision for one shared item.")
	}
	result, err := s.store.SetSharedItem(ctx, code, token, meta.IdempotencyKey, meta.RequestID, input)
	if err != nil {
		return WriteResult[sqlite.SharedItem]{}, err
	}
	var item sqlite.SharedItem
	if err := json.Unmarshal(result.Data, &item); err != nil {
		return WriteResult[sqlite.SharedItem]{}, err
	}
	return WriteResult[sqlite.SharedItem]{Record: item, Replayed: result.Replayed}, nil
}
