package stringconcatbuilder

import "strings"

func ConcatWithPlus(parts []string) string {
	var out string
	for _, part := range parts {
		out += part
	}
	return out
}

func ConcatWithBuilder(parts []string) string {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(part)
	}
	return builder.String()
}

func ConcatWithBuilderGrow(parts []string) string {
	totalLen := 0
	for _, part := range parts {
		totalLen += len(part)
	}

	var builder strings.Builder
	builder.Grow(totalLen)
	for _, part := range parts {
		builder.WriteString(part)
	}
	return builder.String()
}
