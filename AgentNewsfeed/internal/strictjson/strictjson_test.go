package strictjson

import (
	"reflect"
	"testing"
)

type decodePreferences struct {
	AllowNewSources bool   `json:"allow_new_sources"`
	Brief           string `json:"brief"`
}
type decodeItem struct {
	Name string `json:"name"`
}
type decodeRequest struct {
	Preferences decodePreferences `json:"preferences"`
	Items       []decodeItem      `json:"items"`
	Note        string            `json:"note,omitempty"`
}

func TestDecodeRequiresExactFieldNamesBeforeDecoding(t *testing.T) {
	const valid = `{"preferences":{"allow_new_sources":true,"brief":"saved"},"items":[{"name":"source"}]}`
	var got decodeRequest
	if err := Decode([]byte(valid), &got); err != nil {
		t.Fatal("valid exact names", err)
	}
	want := decodeRequest{Preferences: decodePreferences{AllowNewSources: true, Brief: "saved"}, Items: []decodeItem{{Name: "source"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("valid value changed", got)
	}
	for _, test := range []struct{ name, body string }{
		{"root alias", `{"preferences":{"allow_new_sources":true,"brief":"saved"},"PREFERENCES":{"allow_new_sources":false,"brief":"override"},"items":[]}`},
		{"root alias null", `{"preferences":{"allow_new_sources":true,"brief":"saved"},"PREFERENCES":null,"items":[]}`},
		{"nested alias", `{"preferences":{"allow_new_sources":true,"ALLOW_NEW_SOURCES":false,"brief":"saved"},"items":[]}`},
		{"nested alias null", `{"preferences":{"allow_new_sources":true,"ALLOW_NEW_SOURCES":null,"brief":"saved"},"items":[]}`},
		{"nested string alias null", `{"preferences":{"allow_new_sources":true,"brief":"saved","BRIEF":null},"items":[]}`},
		{"array alias", `{"preferences":{"allow_new_sources":true,"brief":"saved"},"items":[{"name":"source","NAME":"override"}]}`},
		{"array alias null", `{"preferences":{"allow_new_sources":true,"brief":"saved"},"items":[{"name":"source","NAME":null}]}`},
		{"optional alias", `{"preferences":{"allow_new_sources":true,"brief":"saved"},"items":[],"NOTE":"override"}`},
		{"optional alias null", `{"preferences":{"allow_new_sources":true,"brief":"saved"},"items":[],"NOTE":null}`},
		{"missing exact required name", `{"preferences":{"ALLOW_NEW_SOURCES":true,"brief":"saved"},"items":[]}`},
		{"unknown", `{"preferences":{"allow_new_sources":true,"brief":"saved","owner":"foreign"},"items":[]}`},
		{"duplicate", `{"preferences":{"allow_new_sources":true,"allow_new_sources":false,"brief":"saved"},"items":[]}`},
		{"escaped duplicate", `{"preferences":{"allow_new_sources":true,"\u0061llow_new_sources":false,"brief":"saved"},"items":[]}`},
		{"required null", `{"preferences":{"allow_new_sources":null,"brief":"saved"},"items":[]}`},
		{"missing required", `{"preferences":{"allow_new_sources":true},"items":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := got
			if err := Decode([]byte(test.body), &got); err == nil {
				t.Fatal("accepted ambiguous or invalid input")
			}
			if !reflect.DeepEqual(got, before) {
				t.Fatal("invalid input partially decoded", got)
			}
		})
	}
}

func TestValidateObjectStillChecksSyntaxAndDuplicatesOnly(t *testing.T) {
	// Existing MCP argument checks delegate only this structural validator; field
	// membership is independently supplied by their tool schemas.
	if err := ValidateObject([]byte(`{"name":"one","NAME":"two"}`)); err != nil {
		t.Fatal("unique object names rejected", err)
	}
	for _, body := range []string{`null`, `[]`, `{"name":"one","name":"two"}`, `{"name":"one","\u006eame":"two"}`, `{"name":"one"} {}`} {
		if err := ValidateObject([]byte(body)); err == nil {
			t.Fatal("invalid structure accepted", body)
		}
	}
}
