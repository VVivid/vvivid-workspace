.PHONY: check test build xtrace-check xtrace-test xtrace-build

check: mcp-gateway-check xtrace-check sshmcp-check google-health-check shopping-check

test: mcp-gateway-test xtrace-test sshmcp-test google-health-test shopping-test

build: mcp-gateway-build xtrace-build sshmcp-build google-health-build shopping-build

xtrace-check:
	$(MAKE) -C examples/xtrace-example check

xtrace-test:
	$(MAKE) -C examples/xtrace-example test

xtrace-build:
	$(MAKE) -C examples/xtrace-example build

.PHONY: sshmcp-check sshmcp-test sshmcp-build google-health-check google-health-test google-health-build shopping-check shopping-test shopping-build

sshmcp-check:
	$(MAKE) -C SSHMCP runner-check
	cd SSHMCP && python3 -m unittest discover -s tests

sshmcp-test:
	cd SSHMCP && go test -race ./...
	cd SSHMCP && python3 -m unittest discover -s tests

sshmcp-build:
	cd SSHMCP && go build ./...

google-health-check:
	$(MAKE) -C GoogleHealthMCP check

google-health-test:
	$(MAKE) -C GoogleHealthMCP test

google-health-build:
	$(MAKE) -C GoogleHealthMCP build

shopping-check:
	$(MAKE) -C AgentShoppingList check

shopping-test:
	cd AgentShoppingList && node --test web/tests/*.test.mjs
	cd AgentShoppingList && go test ./...

shopping-build:
	$(MAKE) -C AgentShoppingList build

.PHONY: mcp-gateway-check mcp-gateway-test mcp-gateway-build mcp-gateway-integration
mcp-gateway-check:
	$(MAKE) -C MCPGateway check
	python3 MCPGateway/scripts/check-contract.py
mcp-gateway-test:
	$(MAKE) -C MCPGateway test
mcp-gateway-build:
	$(MAKE) -C MCPGateway build
mcp-gateway-integration:
	$(MAKE) -C MCPGateway integration
