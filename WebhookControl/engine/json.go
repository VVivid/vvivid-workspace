package engine

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
)

// DecodeEvent requires exactly one object and preserves representable integers.
// Container depth and total JSON values are bounded in addition to body bytes.
func DecodeEvent(raw []byte) (map[string]any, error) {
	if len(raw) > MaxEventBytes {
		return nil, ErrLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrEvent
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrEvent
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, ErrEvent
	}
	nodes := 0
	normalized, err := normalizeJSON(object, 0, &nodes)
	if err != nil {
		return nil, err
	}
	return normalized.(map[string]any), nil
}

func normalizeJSON(value any, depth int, nodes *int) (any, error) {
	*nodes++
	if depth > MaxJSONDepth || *nodes > MaxJSONNodes {
		return nil, ErrLimit
	}
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			normalized, err := normalizeJSON(child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			value[key] = normalized
		}
	case []any:
		for i, child := range value {
			normalized, err := normalizeJSON(child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			value[i] = normalized
		}
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer, nil
		}
		number, err := value.Float64()
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, ErrEvent
		}
		return number, nil
	}
	return value, nil
}
