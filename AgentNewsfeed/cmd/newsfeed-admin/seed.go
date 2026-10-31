package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"agentnewsfeed/internal/feed"
	"agentnewsfeed/internal/store/sqlite"
	"agentnewsfeed/internal/thumbnail"
)

const maxSeedBytes = 1 << 20

type seedRequest struct {
	key     string
	article sqlite.Article
	payload []byte
}

func readSeedRequests(path string, policy thumbnail.Policy) ([]seedRequest, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open seed manifest")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSeedBytes {
		return nil, fmt.Errorf("seed manifest must be a regular file at most 1 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSeedBytes+1))
	if err != nil || len(raw) > maxSeedBytes {
		return nil, fmt.Errorf("cannot read bounded seed manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var inputs []feed.AddInput
	if decoder.Decode(&inputs) != nil || len(inputs) < 1 || len(inputs) > 50 {
		return nil, fmt.Errorf("seed manifest requires a JSON array of 1–50 add_article requests")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("seed manifest must contain exactly one JSON document")
	}
	prepared := make([]seedRequest, 0, len(inputs))
	keys := make(map[string]bool)
	for index, input := range inputs {
		article, err := feed.NormalizeAddInput(input, policy)
		if err != nil {
			return nil, fmt.Errorf("seed preflight request %d: %s", index+1, feed.SafeError(err))
		}
		if keys[input.IdempotencyKey] {
			return nil, fmt.Errorf("seed manifest must use a distinct stable idempotency_key for each request")
		}
		keys[input.IdempotencyKey] = true
		payload, err := json.Marshal(article)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, seedRequest{key: input.IdempotencyKey, article: article, payload: payload})
	}
	return prepared, nil
}

// The operator explicitly chooses a previously verified enrollment. This path
// never enrolls an identity or constructs an authenticated HTTP principal.
func seedArticles(ctx context.Context, store *sqlite.Store, identity sqlite.ExternalIdentity, path string, policy thumbnail.Policy) (int, error) {
	owner, err := store.OwnerForIdentity(ctx, identity)
	if err != nil {
		return 0, fmt.Errorf("seed requires an existing active enrollment for the exact verified issuer and subject")
	}
	// Preflight the whole manifest before writing any article or retry result.
	requests, err := readSeedRequests(path, policy)
	if err != nil {
		return 0, err
	}
	// Each mutation commits separately; a later durable conflict preserves the
	// earlier commits, which an unchanged-manifest retry safely replays.
	for index, request := range requests {
		if _, err := store.MutateArticle(ctx, owner, request.key, "add_article", request.payload, request.article, 0); err != nil {
			return index, fmt.Errorf("seed stopped at request %d after %d completed; retry the unchanged file: %s", index+1, index, feed.SafeError(err))
		}
	}
	return len(requests), nil
}
