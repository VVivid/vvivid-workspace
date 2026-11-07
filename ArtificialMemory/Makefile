.PHONY: check test race build fmt

.DEFAULT_GOAL := check

check:
	@test -z "$$(gofmt -l memory store)" || (gofmt -l memory store; exit 1)
	go vet ./...
	go test ./...

test:
	go test ./...

race:
	go test -race ./...

# Checkpoint 1 builds the library packages; the service command follows later.
build:
	go build ./...

fmt:
	gofmt -w memory store
