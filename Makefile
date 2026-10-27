.PHONY: check test build xtrace-check xtrace-test xtrace-build website-check website-test website-build go-case-study-check go-case-study-test go-case-study-build

check: xtrace-check sshmcp-check google-health-check shopping-check newsfeed-check website-check webhook-control-check go-case-study-check

test: xtrace-test sshmcp-test google-health-test shopping-test newsfeed-test website-test webhook-control-test go-case-study-test

build: xtrace-build sshmcp-build google-health-build shopping-build newsfeed-build website-build webhook-control-build go-case-study-build

go-case-study-check:
	$(MAKE) -C examples/go-lang-case-study check

go-case-study-test:
	$(MAKE) -C examples/go-lang-case-study test

go-case-study-build:
	$(MAKE) -C examples/go-lang-case-study build

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

.PHONY: newsfeed-check newsfeed-test newsfeed-build

newsfeed-check:
	$(MAKE) -C AgentNewsfeed check

newsfeed-test:
	$(MAKE) -C AgentNewsfeed test

newsfeed-build:
	$(MAKE) -C AgentNewsfeed build

.PHONY: webhook-control-check webhook-control-test webhook-control-build

webhook-control-check:
	$(MAKE) -C WebhookControl check

webhook-control-test:
	$(MAKE) -C WebhookControl test

webhook-control-build:
	$(MAKE) -C WebhookControl build
