.PHONY: check test race build integration fmt

.DEFAULT_GOAL := check

check:
	@test -z "$$(gofmt -l account cmd internal memory store tests)" || (gofmt -l account cmd internal memory store tests; exit 1)
	go vet ./...
	go test ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	go build -buildvcs=false -trimpath -o bin/artificial-memory ./cmd/artificial-memory

integration: build
	MEMORY_TEST_BINARY="$(CURDIR)/bin/artificial-memory" go test -tags=integration -count=1 ./tests

fmt:
	gofmt -w account cmd internal memory store tests
