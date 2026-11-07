package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"agentnewsfeed/internal/feed"
)

// validateArgumentObject requires original UTF-8 object JSON with unique keys.
// Check before SDK defaults (which cannot accept a null map) and normalization
// (which erases duplicate keys). Property types remain the schema's job.
func validateArgumentObject(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if !utf8.Valid(body) || len(trimmed) == 0 || trimmed[0] != '{' {
		return feed.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := validateUniqueJSONKeys(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return feed.ErrInvalid
	}
	return nil
}

// validateUniqueJSONKeys consumes one value, checking every nested object.
func validateUniqueJSONKeys(decoder *json.Decoder, depth int) error {
	// Tool inputs need at most two object levels and an array. Bound recursion
	// before schema validation so invalid input cannot exhaust the stack.
	if depth > 8 {
		return feed.ErrInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return feed.ErrInvalid
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return feed.ErrInvalid
			}
			seen[name] = true
			if err := validateUniqueJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return feed.ErrInvalid
		}
	case json.Delim('['):
		for decoder.More() {
			if err := validateUniqueJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return feed.ErrInvalid
		}
	case json.Delim('}'), json.Delim(']'):
		return feed.ErrInvalid
	}
	return nil
}
