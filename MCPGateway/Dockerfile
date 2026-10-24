# syntax=docker/dockerfile:1
FROM golang:1.26.9-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN --mount=type=secret,id=ca_bundle \
    if [ -f /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle GIT_SSL_CAINFO=/run/secrets/ca_bundle; fi; \
    go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/mcp-gateway ./cmd/mcp-gateway
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcp-gateway /mcp-gateway
USER 65532:65532
ENTRYPOINT ["/mcp-gateway"]
CMD ["--config", "/config/gateway.json"]
