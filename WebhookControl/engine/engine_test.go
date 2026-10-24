package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/VVivid/vvivid-workspace/WebhookControl/domain"
	"github.com/VVivid/vvivid-workspace/WebhookControl/ports"
	"github.com/google/cel-go/common/types"
)

type testPolicy bool

func (allowed testPolicy) ApproveDestination(context.Context, domain.ID, domain.HTTPConfig) error {
	if !allowed {
		return ports.ErrPolicyDenied
	}
	return nil
}

func configuration() domain.Configuration {
	return domain.Configuration{
		Source:       domain.Source{ID: "synthetic", Revision: 1, Enabled: true, Auth: domain.SourceAuth{Kind: domain.AuthBearer, Secret: domain.SecretRef{Name: "source-auth", Version: "v1"}}, OriginalTTL: 0, DeliveryTTL: 120},
		Rules:        []domain.Rule{{ID: "sender", Revision: 2, SourceID: "synthetic", Expression: "event.sender == 'expected'"}},
		Templates:    []domain.Template{{ID: "notice", Revision: 3, SourceID: "synthetic", Fields: map[string]string{"message": "event.message", "masked": "mask(event.private)", "count": "event.count", "optional": "has(event.optional) ? event.optional : 'absent'"}}},
		Routes:       []domain.Route{{ID: "route", Revision: 4, SourceID: "synthetic", Enabled: true, Rules: []domain.RevisionRef{{ID: "sender", Revision: 2}}, Template: domain.RevisionRef{ID: "notice", Revision: 3}, Transporters: []domain.RevisionRef{{ID: "receiver", Revision: 5}}}},
		Transporters: []domain.Transporter{{ID: "receiver", Revision: 5, SourceID: "synthetic", Kind: domain.TransporterHTTP, HTTP: domain.HTTPConfig{URL: "https://receiver.example.test/hook", TimeoutSeconds: 10, SecretHeaders: map[string]domain.SecretRef{"Authorization": {Name: "receiver-auth", Version: "v2"}}}}},
	}
}

const eventJSON = `{"sender":"expected","message":"synthetic notice","private":"synthetic private field","count":9223372036854775807}`

func TestFilteringMaskingFanoutAndImmutablePlan(t *testing.T) {
	config := configuration()
	second := config.Transporters[0]
	second.ID, second.HTTP.URL = "receiver-b", "https://second.example.test/hook"
	config.Transporters = append(config.Transporters, second)
	config.Routes[0].Transporters = append(config.Routes[0].Transporters, domain.RevisionRef{ID: second.ID, Revision: second.Revision})
	plan, err := Compile(t.Context(), config, testPolicy(true))
	if err != nil {
		t.Fatal(err)
	}
	config.Templates[0].Fields["private"] = "event.private"
	config.Transporters[0].HTTP.SecretHeaders["Authorization"] = domain.SecretRef{Name: "changed", Version: "v9"}
	config.Routes[0].Rules[0].Revision = 999
	var previous string
	for range 10 {
		snapshots, err := plan.Evaluate(t.Context(), []byte(eventJSON))
		if err != nil || len(snapshots) != 2 {
			t.Fatalf("render: len=%d, err=%v", len(snapshots), err)
		}
		body := string(snapshots[0].Body)
		if strings.Contains(body, "synthetic private") || strings.Contains(body, `"private"`) || !strings.Contains(body, `"masked":"***"`) || !strings.Contains(body, "9223372036854775807") {
			t.Fatalf("unsafe or inaccurate render: %s", body)
		}
		if previous != "" && body != previous {
			t.Fatal("render is not deterministic")
		}
		previous = body
		if snapshots[0].Rules[0].Revision != 2 || snapshots[0].Transporter.HTTP.SecretHeaders["Authorization"].Version != "v2" {
			t.Fatal("caller mutation changed compiled plan")
		}
		snapshots[0].Body[0] = '!'
		if snapshots[1].Body[0] != '{' {
			t.Fatal("fanout payloads share mutable storage")
		}
	}
	snapshots, err := plan.Evaluate(t.Context(), []byte(strings.Replace(eventJSON, "expected", "other", 1)))
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("sender filter: %+v, %v", snapshots, err)
	}
}

func TestConfigurationRejections(t *testing.T) {
	tests := map[string]func(*domain.Configuration){
		"cross source":       func(c *domain.Configuration) { c.Templates[0].SourceID = "another" },
		"missing revision":   func(c *domain.Configuration) { c.Routes[0].Template.Revision++ },
		"invalid revision":   func(c *domain.Configuration) { c.Source.Revision = domain.Revision(math.MaxUint64) },
		"bad ttl":            func(c *domain.Configuration) { c.Source.DeliveryTTL = -2 },
		"duplicate resource": func(c *domain.Configuration) { c.Rules = append(c.Rules, c.Rules[0]) },
		"duplicate binding": func(c *domain.Configuration) {
			c.Routes[0].Transporters = append(c.Routes[0].Transporters, c.Routes[0].Transporters[0])
		},
		"syntax":                     func(c *domain.Configuration) { c.Rules[0].Expression = "event." },
		"nonboolean rule":            func(c *domain.Configuration) { c.Rules[0].Expression = "42" },
		"clock unavailable":          func(c *domain.Configuration) { c.Rules[0].Expression = "now() == now()" },
		"secret unavailable":         func(c *domain.Configuration) { c.Templates[0].Fields["masked"] = "secrets.receiver" },
		"comprehensions unavailable": func(c *domain.Configuration) { c.Rules[0].Expression = "[1, 2].exists(x, x > 0)" },
		"expression size":            func(c *domain.Configuration) { c.Rules[0].Expression = strings.Repeat(" ", MaxExpressionBytes+1) },
		"too many fields": func(c *domain.Configuration) {
			for i := range MaxTemplateFields + 1 {
				c.Templates[0].Fields[fmt.Sprint(i)] = "true"
			}
		},
		"credentials in url": func(c *domain.Configuration) {
			c.Transporters[0].HTTP.URL = "https://user:password@receiver.example.test"
		},
		"timeout": func(c *domain.Configuration) { c.Transporters[0].HTTP.TimeoutSeconds = 0 },
		"literal credential": func(c *domain.Configuration) {
			c.Transporters[0].HTTP.Headers = map[string]string{"Authorization": "synthetic"}
		},
		"controlled secret header": func(c *domain.Configuration) {
			c.Transporters[0].HTTP.SecretHeaders = map[string]domain.SecretRef{"Content-Length": {Name: "receiver", Version: "v1"}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := configuration()
			mutate(&config)
			if _, err := Compile(t.Context(), config, testPolicy(true)); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("Compile error = %v", err)
			}
		})
	}
	if _, err := Compile(t.Context(), configuration(), testPolicy(false)); !errors.Is(err, ports.ErrPolicyDenied) {
		t.Fatalf("policy error = %v", err)
	}
	config := configuration()
	config.Transporters[0].HTTP.URL = "http://127.0.0.1:1234/hook"
	if _, err := Compile(t.Context(), config, testPolicy(true)); err != nil {
		t.Fatalf("explicit HTTP policy exception rejected: %v", err)
	}
}

func TestEvaluationLimitsAndFailures(t *testing.T) {
	tests := []struct {
		name   string
		change func(*domain.Configuration)
		input  string
	}{
		{name: "missing template field", change: func(c *domain.Configuration) { c.Templates[0].Fields["missing"] = "event.absent" }, input: eventJSON},
		{name: "dynamic nonboolean", change: func(c *domain.Configuration) { c.Rules[0].Expression = "event.count" }, input: eventJSON},
		{name: "nonstring output key", change: func(c *domain.Configuration) { c.Templates[0].Fields["bad"] = "{1: 'value'}" }, input: eventJSON},
		{name: "oversized render", change: func(c *domain.Configuration) { c.Templates[0].Fields = map[string]string{"large": "event.large"} }, input: `{"sender":"expected","large":"` + strings.Repeat("a", MaxRenderedBytes) + `"}`},
		{name: "expression cost", change: func(c *domain.Configuration) { c.Rules[0].Expression = "event.large.contains('z')" }, input: `{"large":"` + strings.Repeat("a", 100000) + `"}`},
		{name: "total evaluation cost", change: func(c *domain.Configuration) {
			c.Rules = nil
			c.Routes[0].Rules = nil
			c.Templates[0].Fields = map[string]string{}
			for i := range MaxTemplateFields {
				c.Templates[0].Fields[fmt.Sprint(i)] = "event.large.contains('z')"
			}
			second := c.Routes[0]
			second.ID = "route-b"
			c.Routes = append(c.Routes, second)
		}, input: `{"large":"` + strings.Repeat("a", 10000) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := configuration()
			test.change(&config)
			plan, err := Compile(t.Context(), config, testPolicy(true))
			if err != nil {
				t.Fatal(err)
			}
			if result, err := plan.Evaluate(t.Context(), []byte(test.input)); err == nil || result != nil {
				t.Fatalf("failure was accepted: result=%+v err=%v", result, err)
			}
		})
	}
	plan, err := Compile(t.Context(), configuration(), testPolicy(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := plan.Evaluate(ctx, []byte(eventJSON)); !errors.Is(err, context.Canceled) {
		t.Fatalf("context ignored: %v", err)
	}
}

func TestObjectJSONBounds(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `42`, `"text"`, `{}`, `{"ok":true} {}`, `{"number":1e999}`, `{"nested":` + strings.Repeat("[", MaxJSONDepth+1) + `0` + strings.Repeat("]", MaxJSONDepth+1) + `}`, `{"nodes":[` + strings.Repeat("0,", MaxJSONNodes) + `0]}`} {
		_, err := DecodeEvent([]byte(raw))
		if raw == `{}` {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("invalid event accepted: %.80s", raw)
		}
	}
	event, err := DecodeEvent([]byte(`{"number":9223372036854775807}`))
	if err != nil || event["number"] != int64(math.MaxInt64) {
		t.Fatalf("integer decode: %+v, %v", event, err)
	}
	encoded, err := json.Marshal(event)
	if err != nil || string(encoded) != `{"number":9223372036854775807}` {
		t.Fatal("integer round trip failed")
	}
}

func TestRenderRejectsAliasedLargeStringsBeforeEncoding(t *testing.T) {
	config := configuration()
	config.Rules, config.Routes[0].Rules = nil, nil
	config.Templates[0].Fields = map[string]string{}
	for i := range MaxTemplateFields {
		config.Templates[0].Fields[fmt.Sprint(i)] = "event.large"
	}
	plan, err := Compile(t.Context(), config, testPolicy(true))
	if err != nil {
		t.Fatal(err)
	}
	// The incoming body is near 1 MiB. Its 32 aliases would produce almost
	// 32 MiB of JSON even though CEL cost and output node count remain small.
	raw := []byte(`{"large":"` + strings.Repeat("a", MaxEventBytes-64) + `"}`)
	if len(raw) > MaxEventBytes {
		t.Fatal("test input exceeds the ingress limit")
	}
	// Warm CEL's bounded initialization before measuring a complete evaluation.
	if _, err := plan.Evaluate(t.Context(), raw); !errors.Is(err, ErrLimit) {
		t.Fatalf("render error = %v", err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := plan.Evaluate(t.Context(), raw)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrLimit) || result != nil {
		t.Fatalf("oversized aliases accepted: result=%v err=%v", result, err)
	}
	// Permit JSON decoding and CEL overhead, but not even one expanded output
	// buffer. The regression never needs to construct that output to reject it.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16*MaxEventBytes {
		t.Fatalf("oversized render allocated %d bytes", allocated)
	}
}

func TestRenderBudgetCountsEncodedValues(t *testing.T) {
	for name, input := range map[string]any{
		"escaped strings and keys": map[string]any{"<&>\"\\\n\x00\u2028\u2029": "<&>\"\\\b\f\n\r\t\x00\u2028\u2029"},
		"UTF-8 and replacement":    "α🙂\ufffd" + string([]byte{0xff}),
		"scalar encodings": []any{nil, true, false, int64(math.MinInt64), uint64(math.MaxUint64),
			math.SmallestNonzeroFloat64, math.MaxFloat64, 1e-6, 1e20, math.Copysign(0, -1)},
		"container punctuation": []any{map[string]any{}, []any{}, map[string]any{"a": []any{nil, true}, "b": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			value := types.DefaultTypeAdapter.NativeToValue(input)
			budget := renderBudget{remaining: len(encoded)}
			native, err := jsonValue(t.Context(), value, 0, &budget)
			if err != nil || budget.remaining != 0 {
				t.Fatalf("encoded-byte count differs: remaining=%d err=%v", budget.remaining, err)
			}
			actual, err := json.Marshal(native)
			if err != nil || string(actual) != string(encoded) {
				t.Fatalf("JSON changed: %s, %v", actual, err)
			}
			short := renderBudget{remaining: len(encoded) - 1}
			if _, err := jsonValue(t.Context(), value, 0, &short); !errors.Is(err, ErrLimit) {
				t.Fatalf("one byte over budget accepted: %v", err)
			}
		})
	}
}

func TestRenderBudgetCountsTemplateKeysAndOuterObject(t *testing.T) {
	config := configuration()
	config.Rules, config.Routes[0].Rules = nil, nil
	const key = "<key>"
	config.Templates[0].Fields = map[string]string{key: "event.text"}
	plan, err := Compile(t.Context(), config, testPolicy(true))
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	// Outer braces, colon, string quotes and Encoder's removed trailing LF.
	text := strings.Repeat("a", MaxRenderedBytes-len(encodedKey)-6)
	raw, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	results, err := plan.Evaluate(t.Context(), raw)
	if err != nil || len(results) != 1 || len(results[0].Body) != MaxRenderedBytes-1 {
		t.Fatalf("boundary render: snapshots=%d err=%v", len(results), err)
	}
	raw, err = json.Marshal(map[string]string{"text": text + "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Evaluate(t.Context(), raw); !errors.Is(err, ErrLimit) {
		t.Fatalf("over-budget template key/object accepted: %v", err)
	}
}
