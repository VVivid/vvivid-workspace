.PHONY: assets build check fmt run

assets:
	npm run build:css

build:
	go build -o bin/shopping ./cmd/shopping
	go build -o bin/shopping-admin ./cmd/shopping-admin

check:
	@test -z "$$(gofmt -l cmd internal web/assets.go)" || (gofmt -l cmd internal web/assets.go; exit 1)
	go vet ./...
	go test ./...

fmt:
	gofmt -w cmd internal web/assets.go

run:
	go run ./cmd/shopping
