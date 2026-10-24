.PHONY: check test build xtrace-check xtrace-test xtrace-build website-check website-test website-build

check: xtrace-check sshmcp-check google-health-check shopping-check website-check webhook-control-check

test: xtrace-test sshmcp-test google-health-test shopping-test website-test webhook-control-test

build: xtrace-build sshmcp-build google-health-build shopping-build website-build webhook-control-build

website-check:
	$(MAKE) -C vvivid.dev check

website-test:
	$(MAKE) -C vvivid.dev test

website-build:
	$(MAKE) -C vvivid.dev build

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

.PHONY: webhook-control-check webhook-control-test webhook-control-build

webhook-control-check:
	$(MAKE) -C WebhookControl check

webhook-control-test:
	$(MAKE) -C WebhookControl test

webhook-control-build:
	$(MAKE) -C WebhookControl build
