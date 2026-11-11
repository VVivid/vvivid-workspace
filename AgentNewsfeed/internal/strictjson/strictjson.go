// Package strictjson rejects duplicate and unknown request properties before adapters normalize JSON.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var errInvalid = errors.New("invalid JSON object")

// ValidateObject requires original UTF-8 object JSON with unique keys.
// Check before SDK defaults (which cannot accept a null map) and normalization
// (which erases duplicate keys). Property types remain the schema's job.
func ValidateObject(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if !utf8.Valid(body) || len(trimmed) == 0 || trimmed[0] != '{' {
		return errInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := validateUniqueJSONKeys(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalid
	}
	return nil
}

// validateUniqueJSONKeys consumes one value, checking every nested object.
func validateUniqueJSONKeys(decoder *json.Decoder, depth int) error {
	// Tool inputs need at most two object levels and an array. Bound recursion
	// before schema validation so invalid input cannot exhaust the stack.
	if depth > 8 {
		return errInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return errInvalid
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return errInvalid
			}
			seen[name] = true
			if err := validateUniqueJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return errInvalid
		}
	case json.Delim('['):
		for decoder.More() {
			if err := validateUniqueJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return errInvalid
		}
	case json.Delim('}'), json.Delim(']'):
		return errInvalid
	}
	return nil
}

// Decode accepts one bounded UTF-8 object with no duplicate or unknown fields.
func Decode(body []byte, out any) error {
	if err := ValidateObject(body); err != nil {
		return err
	}
	if err := requiredFields(body, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

// requiredFields mirrors the inferred tool schema: omission is allowed only
// with an explicit omitempty/omitzero tag; null never stands in for a value.
func requiredFields(raw json.RawMessage, target reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errInvalid
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		for i := range target.NumField() {
			field := target.Field(i)
			tag := field.Tag.Get("json")
			name, options, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			value, exists := object[name]
			if !exists {
				if options != "omitempty" && options != "omitzero" {
					return errInvalid
				}
				continue
			}
			delete(object, name)
			if err := requiredFields(value, field.Type); err != nil {
				return err
			}
		}
		// encoding/json also matches field names case-insensitively. Every
		// original key must instead match one declared JSON name exactly.
		if len(object) != 0 {
			return errInvalid
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := requiredFields(value, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
