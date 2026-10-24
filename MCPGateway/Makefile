.PHONY: check test build integration demo
check:
	@test -z "$$(gofmt -l cmd gateway trust internal integration)" || (gofmt -l cmd gateway trust internal integration; exit 1)
	go vet ./...
	go test -race ./...
test:
	go test -race ./...
build:
	go build -trimpath -o bin/mcp-gateway ./cmd/mcp-gateway
	go build -trimpath -o bin/gateway-keygen ./cmd/gateway-keygen
	go build -trimpath -o bin/example-mcp ./cmd/example-mcp
	go build -trimpath -o bin/local-demo ./cmd/local-demo
integration: build
	MCP_GATEWAY_BIN="$(CURDIR)/bin/mcp-gateway" go test -race -tags=integration -count=1 -v ./integration
demo: build
	./bin/local-demo --gateway "$(CURDIR)/bin/mcp-gateway"
