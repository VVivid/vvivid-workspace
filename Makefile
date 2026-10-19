.PHONY: assets build check fmt run process-test container-test

VERSION ?= dev
COMMIT ?= unknown
BUILT_AT ?= unknown
LDFLAGS = -trimpath -buildvcs=false -ldflags="-X agentshoppinglist/internal/buildinfo.Version=$(VERSION) -X agentshoppinglist/internal/buildinfo.Commit=$(COMMIT) -X agentshoppinglist/internal/buildinfo.BuiltAt=$(BUILT_AT)"

assets:
	npm run build:css

build:
	go build $(LDFLAGS) -o bin/shopping ./cmd/shopping
	go build $(LDFLAGS) -o bin/shopping-admin ./cmd/shopping-admin

check:
	@test -z "$$(gofmt -l cmd internal web/assets.go)" || (gofmt -l cmd internal web/assets.go; exit 1)
	node --test web/tests/*.test.mjs
	go vet ./...
	go test ./...

fmt:
	gofmt -w cmd internal web/assets.go

run:
	go run ./cmd/shopping

process-test:
	SHOPPING_PROCESS_TEST=1 go test -count=1 -v ./internal/acceptance

container-test:
	sh scripts/test-container.sh
