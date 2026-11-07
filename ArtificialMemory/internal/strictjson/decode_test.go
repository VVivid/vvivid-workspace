package strictjson

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/VVivid/vvivid-workspace/ArtificialMemory/memory"
)

func TestStrictEnvelopeAndPrecisePayload(t *testing.T) {
	valid := `{"space":"GuestUp","idempotency_key":"key","entries":[{"kind":"topic","title":"Discussion","payload":{"n":9007199254740993,"optional":null,"future":{"x":true}}}]}`
	var input memory.RememberRequest
	if Decode([]byte(valid), &input) != nil || string(input.Entries[0].Payload) != `{"n":9007199254740993,"optional":null,"future":{"x":true}}` {
		t.Fatal("raw object payload or its exact numeric type changed")
	}
	for name, raw := range map[string]string{
		"duplicate":      `{"space":"one","space":"two"}`,
		"escaped member": `{"space":"one","sp\u0061ce":"two"}`,
		"case alias":     `{"Space":"one"}`,
		"owner":          `{"account_id":"other"}`,
		"nested owner":   `{"entries":[{"writer":{"account_id":"other"}}]}`,
		"null field":     `{"topic_id":null}`,
		"null payload":   `{"entries":[{"payload":null}]}`,
		"null element":   `{"entries":[null]}`,
		"array root":     `[]`,
		"trailing":       `{} {}`,
		"duplicate data": `{"entries":[{"payload":{"x":1,"x":2}}]}`,
		"depth":          `{"entries":[{"payload":` + strings.Repeat(`{"x":`, 70) + `{}` + strings.Repeat(`}`, 70) + `}]}`,
		"bad utf8":       "{\"space\":\"\xff\"}",
	} {
		t.Run(name, func(t *testing.T) {
			var input memory.RememberRequest
			if Decode([]byte(raw), &input) == nil {
				t.Fatal("ambiguous or malformed envelope accepted")
			}
		})
	}
	var object struct {
		Payload json.RawMessage `json:"payload"`
	}
	if Decode([]byte(`{"payload":{}}`), &object) != nil {
		t.Fatal("empty object payload rejected")
	}
}
