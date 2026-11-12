SHELL := /bin/sh
GO ?= go
PYTHON ?= python3
TEST_IMAGE ?= vocabbank:local
export GOCACHE := $(CURDIR)/.cache/go-build
export GOMODCACHE := $(CURDIR)/.cache/go-mod
export npm_config_cache := $(CURDIR)/.cache/npm
export NEXT_TELEMETRY_DISABLED := 1
export PLAYWRIGHT_BROWSERS_PATH := $(CURDIR)/.cache/playwright
.PHONY: deps format check test race assets build browser-install browser-test docker-build chart-test container-test

deps:
	npm ci --prefix web --no-fund --no-audit --cache $(CURDIR)/.cache/npm
	$(GO) mod download
format:
	gofmt -w cmd internal
	npm --prefix web run format
check:
	@test -z "$$(gofmt -l cmd internal)" || { echo 'Run make format.'; exit 1; }
	$(GO) vet ./...
	npm --prefix web run typecheck
	npm --prefix web run format:check
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
assets:
	npm --prefix web run build
build: assets
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/vocabbank ./cmd/vocabbank
browser-install:
	npm --prefix web exec -- playwright install chromium
browser-test: assets
	npm --prefix web test
docker-build:
	docker build --platform linux/amd64 --tag $(TEST_IMAGE) .

# Helm and check-only Python dependencies are separate from the application runtime.
chart-test:
	$(PYTHON) -m unittest discover -s deploy/gitops -p 'test_*.py'
container-test:
	$(PYTHON) scripts/check-container.py --image $(TEST_IMAGE)
