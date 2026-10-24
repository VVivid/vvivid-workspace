.PHONY: check test build

check:
	test -z "$$(gofmt -l .)"
	go vet ./...

test:
	go test -race ./...

build:
	go build ./...
	mkdir -p bin
	go build -o bin/webhook-control ./cmd/webhook-control
	go build -o bin/webhook-control-admin ./cmd/webhook-control-admin
