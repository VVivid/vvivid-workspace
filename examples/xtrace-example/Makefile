.PHONY: check vet test build compose-check

check:
	$(MAKE) vet
	$(MAKE) test
	$(MAKE) build
	$(MAKE) compose-check

vet:
	go vet ./...
	go -C traefik-plugin vet ./...

test:
	go test -race ./...
	go -C traefik-plugin test -race ./...

build:
	mkdir -p bin
	go build -o bin/xtrace-demo .
	go -C traefik-plugin build ./...

compose-check:
	docker compose config --quiet
