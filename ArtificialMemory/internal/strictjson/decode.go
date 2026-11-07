// Package strictjson rejects ambiguous envelopes before decoding typed inputs.
// RawMessage fields retain their JSON numbers and may contain arbitrary data.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid JSON envelope")

// Check validates one UTF-8 JSON value, rejecting duplicate object members and
// excessive nesting, including inside otherwise opaque payloads.
func Check(data []byte) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := checkValue(d, 0); err != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func checkValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return ErrInvalid
			}
			seen[name] = true
			if err := checkValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := checkValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalid
	}
	_, err = d.Token() // Decoder verifies the matching closing delimiter.
	return err
}

// Decode requires exact, case-sensitive JSON field names and rejects explicit
// null envelope fields. Omit optional fields instead. Nulls inside a RawMessage
// object's data remain valid. Semantic bounds belong to the domain adapter.
func Decode(data []byte, out any) error {
	if Check(data) != nil {
		return ErrInvalid
	}
	targetType := reflect.TypeOf(out)
	if targetType == nil || targetType.Kind() != reflect.Pointer || checkEnvelopeShape(data, targetType.Elem()) != nil {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if d.Decode(out) != nil {
		return ErrInvalid
	}
	return nil
}

// checkEnvelopeShape enforces field spelling and non-null envelope values
// recursively. The final decoder handles value types; RawMessage payloads stay
// opaque here after Check has rejected duplicates and excessive nesting.
func checkEnvelopeShape(data json.RawMessage, targetType reflect.Type) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return ErrInvalid
	}
	if targetType == reflect.TypeFor[json.RawMessage]() || targetType == reflect.TypeFor[time.Time]() {
		return nil
	}
	if targetType.Kind() == reflect.Pointer {
		return checkEnvelopeShape(data, targetType.Elem())
	}
	switch targetType.Kind() {
	case reflect.Struct:
		var members map[string]json.RawMessage
		if json.Unmarshal(data, &members) != nil || members == nil {
			return ErrInvalid
		}
		fields := make(map[string]reflect.Type, targetType.NumField())
		for i := range targetType.NumField() {
			field := targetType.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name != "" && name != "-" {
				fields[name] = field.Type
			}
		}
		for name, value := range members {
			field, ok := fields[name]
			if !ok || checkEnvelopeShape(value, field) != nil {
				return ErrInvalid
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return ErrInvalid
		}
		for _, value := range values {
			if checkEnvelopeShape(value, targetType.Elem()) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}
