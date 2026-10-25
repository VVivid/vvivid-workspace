# Projects

Small engineering case studies that support posts on [vvivid.dev](https://vvivid.dev/).

Each folder is intentionally self-contained so the benchmark code can be run, checked, and cited without coupling it to the Hugo site.

## Case Studies

- [`go/string-concat-builder`](go/string-concat-builder): compares repeated string concatenation with `strings.Builder` in Go.
- [`go/process-api-shell`](go/process-api-shell): demonstrates the shell process pattern with Go's `os/exec` package.

## Common Commands

Run from this module's directory (`cd examples/go-lang-case-study` from the workspace root), using Go 1.25 or newer:

```sh
gofmt -w ./go
go vet ./...
go test ./...
go build ./...
go test ./... -bench=BenchmarkStringConstruction -benchmem -count=5
```

`make check`, `make test`, and `make build` provide formatting/vet checks, race-enabled tests, and a build, respectively.

## CI

GitHub Actions runs formatting checks, `go vet`, race-enabled tests, and a build through the [case-study workflow](../../.github/workflows/go-lang-case-study.yml). Benchmarks are intentionally run locally when generating evidence for a case study, because benchmark timings are noisy across shared CI runners.

## Source

Imported from the attached `go-lang-case-study-main.tar.gz` snapshot without original Git history. Go source and `benchmark_results.txt` are unchanged; the GitLab package names in that benchmark output are historical.
