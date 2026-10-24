.PHONY: check test build integration
check:
	@test -z "$$(gofmt -l cmd gateway trust)" || (gofmt -l cmd gateway trust; exit 1)
	go vet ./...
	go test -race ./...
test:
	go test -race ./...
build:
	go build -trimpath -o bin/mcp-gateway ./cmd/mcp-gateway
	go build -trimpath -o bin/gateway-keygen ./cmd/gateway-keygen
	go build -trimpath -o bin/example-mcp ./cmd/example-mcp
integration: build
	cd ../AgentShoppingList && MCP_GATEWAY_BIN="$(CURDIR)/bin/mcp-gateway" go test -race -count=1 ./internal/web -run TestSharedGatewayShoppingMCP
	cd ../SSHMCP && MCP_GATEWAY_BIN="$(CURDIR)/bin/mcp-gateway" go test -race -count=1 ./internal/gateway -run TestSharedGatewaySSHProtocol
