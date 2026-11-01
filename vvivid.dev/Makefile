.PHONY: check test build deployment-test legacy-deployment-test content-test indexing-test chart-test

# Rendering the production site also checks the Hugo version and guide exclusion.
check test: deployment-test content-test indexing-test build

build:
	./scripts/build.sh

# Exercises release policy and real local Git transaction races. No external writes.
deployment-test:
	python3 -m unittest discover -s deploy/gitops -p 'test_*.py' -v

# Archived SSH helpers remain manual recovery references, outside active CI.
legacy-deployment-test:
	python3 -m unittest discover -s deploy/live -p 'test_deploy.py' -v

chart-test:
	python3 deploy/gitops/test_chart.py

# Temporary source fixtures exercise the production content selection and digests.
content-test:
	python3 scripts/test-generated.py

indexing-test:
	python3 scripts/test-indexnow.py
