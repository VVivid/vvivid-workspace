.PHONY: check test race build fmt run frontend-check frontend-test frontend-build container-build container-test compose-check

.DEFAULT_GOAL := check

VERSION ?= dev
COMMIT ?= unknown
TEST_IMAGE ?= agentnewsfeed:local
BUILD_CA_FILE ?=
# node_modules can contain third-party Go examples; only application packages are checks.
GO_PACKAGES := ./cmd/... ./internal/...

node_modules/.package-lock.json: package.json package-lock.json
	npm ci --ignore-scripts

frontend-check: node_modules/.package-lock.json
	npm run check

frontend-test: node_modules/.package-lock.json
	npm test

frontend-build: node_modules/.package-lock.json
	npm run build

check: frontend-check frontend-test frontend-build
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	go vet $(GO_PACKAGES)
	go test $(GO_PACKAGES)

test: frontend-test frontend-build
	go test $(GO_PACKAGES)

race: frontend-build
	go test -race $(GO_PACKAGES)

build: frontend-build
	go build -trimpath -buildvcs=false -ldflags="-X main.version=$(VERSION)" -o bin/newsfeed ./cmd/newsfeed
	go build -trimpath -buildvcs=false -o bin/newsfeed-admin ./cmd/newsfeed-admin

fmt:
	gofmt -w cmd internal

run: frontend-build
	go run ./cmd/newsfeed

container-build:
	mkdir -p .development/private-feed/container
	@set --; if [ -n "$(BUILD_CA_FILE)" ]; then set -- --secret "id=build-ca,src=$(BUILD_CA_FILE)"; fi; \
	docker build "$$@" --pull --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --tag $(TEST_IMAGE) --iidfile .development/private-feed/container/image-id.txt .

container-test:
	NEWSFEED_TEST_IMAGE=$(TEST_IMAGE) sh scripts/test-container.sh

compose-check:
	sh scripts/test-compose.sh
