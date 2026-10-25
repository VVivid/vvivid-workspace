package stringconcatbuilder

import (
	"fmt"
	"strings"
	"testing"
)

func TestConcatWithPlus(t *testing.T) {
	testConcatStrategy(t, ConcatWithPlus)
}

func TestConcatWithBuilder(t *testing.T) {
	testConcatStrategy(t, ConcatWithBuilder)
}

func TestConcatWithBuilderGrow(t *testing.T) {
	testConcatStrategy(t, ConcatWithBuilderGrow)
}

func TestStringConstructionStrategiesMatch(t *testing.T) {
	strategies := []struct {
		name string
		fn   func([]string) string
	}{
		{name: "plus", fn: ConcatWithPlus},
		{name: "builder", fn: ConcatWithBuilder},
		{name: "builder_grow", fn: ConcatWithBuilderGrow},
	}

	for _, tt := range stringConstructionTestCases() {
		expected := strings.Join(tt.parts, "")
		for _, strategy := range strategies {
			t.Run(tt.name+"/"+strategy.name, func(t *testing.T) {
				got := strategy.fn(tt.parts)
				if got != expected {
					t.Fatalf("got %q, want %q", got, expected)
				}
			})
		}
	}
}

func testConcatStrategy(t *testing.T, concat func([]string) string) {
	t.Helper()

	for _, tt := range stringConstructionTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			expected := strings.Join(tt.parts, "")
			got := concat(tt.parts)
			if got != expected {
				t.Fatalf("got %q, want %q", got, expected)
			}
		})
	}
}

func stringConstructionTestCases() []struct {
	name  string
	parts []string
} {
	return []struct {
		name  string
		parts []string
	}{
		{name: "nil"},
		{name: "empty", parts: []string{}},
		{name: "single", parts: []string{"hello"}},
		{name: "words", parts: []string{"hello", " ", "from", " ", "go"}},
		{name: "generated", parts: makeParts(64, 12)},
	}
}

var benchmarkSink string

func BenchmarkStringConstruction(b *testing.B) {
	cases := []struct {
		name  string
		parts []string
	}{
		{name: "small_8_parts", parts: makeParts(8, 8)},
		{name: "medium_128_parts", parts: makeParts(128, 16)},
		{name: "large_4096_parts", parts: makeParts(4096, 24)},
	}

	strategies := []struct {
		name string
		fn   func([]string) string
	}{
		{name: "plus", fn: ConcatWithPlus},
		{name: "builder", fn: ConcatWithBuilder},
		{name: "builder_grow", fn: ConcatWithBuilderGrow},
	}

	for _, tc := range cases {
		for _, strategy := range strategies {
			b.Run(tc.name+"/"+strategy.name, func(b *testing.B) {
				b.ReportAllocs()

				var result string
				for i := 0; i < b.N; i++ {
					result = strategy.fn(tc.parts)
				}
				benchmarkSink = result
			})
		}
	}
}

func makeParts(count int, width int) []string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf("%04d:%s;", i, strings.Repeat("x", width))
	}
	return parts
}
