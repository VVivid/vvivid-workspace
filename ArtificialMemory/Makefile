.PHONY: check test race build integration demo chart-test container-build container-test fmt

.DEFAULT_GOAL := check

PYTHON ?= python3
TEST_IMAGE ?= artificial-memory:local
VERSION ?= 0.1.0
COMMIT ?= unknown
BUILD_CA_FILE ?=

check:
	@test -z "$$(gofmt -l account cmd examples internal memory store tests)" || (gofmt -l account cmd examples internal memory store tests; exit 1)
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

demo:
	go run ./examples/synthetic

chart-test:
	$(PYTHON) -m unittest discover -s deploy -p 'test_*.py'

# Build context is this project only; defaults preserve Docker's normal proxy
# configuration. Managed environments supply their public CA via BuildKit.
container-build:
	@set --; if [ -n "$(BUILD_CA_FILE)" ]; then set -- --secret "id=build-ca,src=$(BUILD_CA_FILE)"; fi; \
	docker build "$$@" --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --tag $(TEST_IMAGE) .

container-test:
	MEMORY_TEST_IMAGE=$(TEST_IMAGE) go test -tags=container -count=1 -v ./tests

fmt:
	gofmt -w account cmd examples internal memory store tests
