.PHONY: check test build xtrace-check xtrace-test xtrace-build

check: xtrace-check

test: xtrace-test

build: xtrace-build

xtrace-check:
	$(MAKE) -C examples/xtrace-example check

xtrace-test:
	$(MAKE) -C examples/xtrace-example test

xtrace-build:
	$(MAKE) -C examples/xtrace-example build
