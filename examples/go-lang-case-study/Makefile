.PHONY: check test build

check:
	@set -e; unformatted="$$(gofmt -l go)"; \
	if [ -n "$$unformatted" ]; then \
		printf 'Go files need gofmt:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...

test:
	go test -race ./...

build:
	go build ./...
