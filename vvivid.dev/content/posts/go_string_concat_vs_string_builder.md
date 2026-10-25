---
title: "String Concatenation vs strings.Builder in Go"
date: 2026-06-13T10:34:19+07:00
lastmod: 2026-10-25
author: "Ou David"
draft: false
tags: ["golang", "performance", "benchmarking", "strings"]
categories: ["Technical Deep Dives"]
description: "A small Go benchmark showing when repeated string concatenation becomes expensive and how strings.Builder changes the allocation profile."
toc: false
math: false
---

Repeated `+=` in a Go loop copies the accumulated string on each append. In this benchmark, `strings.Builder` reduces those copies and allocations; `Grow` reduces them further when the final size is cheap to calculate. For a few fixed values, ordinary concatenation is still fine.


I often leave the same comment when I see string concatenation inside a loop:

> If you build a string in a loop, use `strings.Builder`.

That comment sometimes gets pushback from the team: the two operations probably will not matter much. Today, I want to show a simple benchmark to demonstrate why `strings.Builder` is superior to doing `+=` inside a loop.

The runnable code and raw benchmark output are available in the public repo: [gitlab.vvivid.dev/showcase/go-lang-case-study](https://gitlab.vvivid.dev/showcase/go-lang-case-study).

## Why repeated concatenation copies the whole string {#the-real-problem}

Strings in Go are immutable. When we do this:

```go
var out string
for _, part := range parts {
    out += part
}
```

Go cannot keep extending the same string in place. Each append creates a new string value large enough to hold the old content plus the new content, then copies data into it.

For a few strings, that cost is usually irrelevant. For a loop with many parts, it becomes more expensive than it looks.

The benchmark compares three versions:

```go
func ConcatWithPlus(parts []string) string {
    var out string
    for _, part := range parts {
        out += part
    }
    return out
}
```

```go
func ConcatWithBuilder(parts []string) string {
    var builder strings.Builder
    for _, part := range parts {
        builder.WriteString(part)
    }
    return builder.String()
}
```

```go
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
```

The third version is the same idea as `strings.Builder`, but it tells the builder the final size before writing. That lets it allocate the backing buffer once.

## Benchmark inputs and environment {#the-test-setup}

The correctness test is simple: every strategy must produce the same string as `strings.Join(parts, "")`.

The benchmark uses three input sizes:

- small: 8 parts
- medium: 128 parts
- large: 4,096 parts

I ran it with:

```sh
go test ./... -bench=BenchmarkStringConstruction -benchmem -count=5
```

Environment:

- Go: `go1.25.4`
- OS/arch: `darwin/arm64`
- CPU: `Apple M4 Pro`

## Benchmark results: time, bytes, and allocations {#the-result}

Average across five benchmark runs:

| Case | Strategy | ns/op | B/op | allocs/op |
| --- | --- | ---: | ---: | ---: |
| small, 8 parts | `+=` | 134.36 | 544 | 7 |
| small, 8 parts | `strings.Builder` | 72.76 | 240 | 4 |
| small, 8 parts | `strings.Builder` + `Grow` | 42.95 | 112 | 1 |
| medium, 128 parts | `+=` | 22,684.40 | 194,080 | 127 |
| medium, 128 parts | `strings.Builder` | 1,615.80 | 11,176 | 10 |
| medium, 128 parts | `strings.Builder` + `Grow` | 610.18 | 3,072 | 1 |
| large, 4,096 parts | `+=` | 17,958,170.00 | 264,987,839 | 4,101.6 |
| large, 4,096 parts | `strings.Builder` | 53,604.60 | 514,785 | 22 |
| large, 4,096 parts | `strings.Builder` + `Grow` | 19,980.40 | 122,880 | 1 |

The exact numbers are not the main point. They will change across machines, Go versions, and input shapes.

The shape is the point.

For 4096 parts, the easy `+=` version allocated about 265 MB per operation. The pre-grown builder allocated about 120 KB and stayed at one allocation. That is about 2156x less memory per operation. That is the difference between repeatedly rebuilding the output string and writing into a buffer sized for the final result.

## Why the compiler cannot remove these copies {#the-wrong-assumption}

The wrong assumption is:

> The compiler will probably optimize this away.

Sometimes Go does optimize string operations well, especially simple expressions. But a dynamic loop is a different case. The next value depends on the previous full string, so the program keeps creating larger intermediate strings.

That is why the allocation count for `+=` follows the number of appended parts:

- 8 parts: 7 allocations
- 128 parts: 127 allocations
- 4,096 parts: about 4,102 allocations

Those allocations also copy increasingly larger strings. The later appends are not just appending the new part. They are copying almost the whole result again.

## Use strings.Builder and preallocate when useful {#the-fix}

Use `strings.Builder` when building a string from repeated writes:

```go
var builder strings.Builder
for _, part := range parts {
    builder.WriteString(part)
}
return builder.String()
```

If you know the final size, call `Grow` first:

```go
var builder strings.Builder
builder.Grow(totalLen)
```

That is what moved the benchmark to one allocation across all input sizes.

## Choose string construction for the workload {#takeaway}

You do not have to turn this into a micro-optimization. If there are only two or three values, normal concatenation is readable and fine:

```go
name := firstName + " " + lastName
```

But once you are constructing a string in a loop, especially from dynamic input, `strings.Builder` should be the default. If you can calculate the final size cheaply, `Grow` is even better.

The practical rule is simple: use `+=` for obvious small expressions, use `strings.Builder` for repeated construction.

## Related articles

[Follow an HTTP request through Go](/posts/http_in_golang/) for another look beneath the standard library, or run the [Go case studies](/projects/go-lang-case-study/) to measure the examples yourself.
