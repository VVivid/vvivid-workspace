.PHONY: check test build deployment-test content-test indexing-test

# Rendering the production site also checks the Hugo version and guide exclusion.
check test: deployment-test content-test indexing-test build

build:
	./scripts/build.sh

# Exercises live and preview scopes using fake commands, including shared .env locking.
# Never connects to a deployment server.
deployment-test:
	python3 -m unittest discover -s deploy/live -p 'test_*.py' -v

# Temporary source fixtures exercise the production content selection and digests.
content-test:
	python3 scripts/test-generated.py

indexing-test:
	python3 scripts/test-indexnow.py
