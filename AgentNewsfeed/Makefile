.PHONY: check test race build fmt run container-build container-test compose-check

VERSION ?= dev
COMMIT ?= unknown
TEST_IMAGE ?= agentnewsfeed:local
BUILD_CA_FILE ?=

check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	go vet ./...
	go test ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	go build -trimpath -buildvcs=false -ldflags="-X main.version=$(VERSION)" -o bin/newsfeed ./cmd/newsfeed
	go build -trimpath -buildvcs=false -o bin/newsfeed-admin ./cmd/newsfeed-admin

fmt:
	gofmt -w cmd internal

run:
	go run ./cmd/newsfeed

container-build:
	mkdir -p .development/private-feed/container
	@set --; if [ -n "$(BUILD_CA_FILE)" ]; then set -- --secret "id=build-ca,src=$(BUILD_CA_FILE)"; fi; \
	docker build "$$@" --pull --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --tag $(TEST_IMAGE) --iidfile .development/private-feed/container/image-id.txt .

container-test:
	NEWSFEED_TEST_IMAGE=$(TEST_IMAGE) sh scripts/test-container.sh

compose-check:
	sh scripts/test-compose.sh
