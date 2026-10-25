# String Concatenation vs strings.Builder

This case study compares repeated string concatenation with `strings.Builder` in Go.

The benchmark is intentionally small and focused:

- `ConcatWithPlus`: appends with `s += part`
- `ConcatWithBuilder`: appends with `strings.Builder`
- `ConcatWithBuilderGrow`: computes the final size and calls `Grow` before writing

## Run

From the module root (`examples/go-lang-case-study`):

```sh
go test ./...
go test ./... -bench=BenchmarkStringConstruction -benchmem -count=5
```

## Local Results

Environment:

- Go: `go1.25.4`
- OS/arch: `darwin/arm64`
- CPU: `Apple M4 Pro`

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

The exact numbers are machine-specific. The important result is the shape: repeated `+=` grows allocation and copy work with each append, while a pre-grown builder writes into one backing buffer.
