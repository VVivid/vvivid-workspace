FROM golang:1.26.10-bookworm AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOTELEMETRY=off GOFLAGS=-buildvcs=false
COPY go.mod go.sum ./
# The optional public proxy CA is mounted for this networked step only. It is
# never installed into system trust or copied into either image stage.
RUN --mount=type=secret,id=proxy_ca \
    if [ -f /run/secrets/proxy_ca ]; then \
      SSL_CERT_FILE=/run/secrets/proxy_ca go mod download; \
    else go mod download; fi
COPY . .
RUN --network=none CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webhook-control ./cmd/webhook-control && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webhook-control-admin ./cmd/webhook-control-admin

FROM debian:bookworm-slim
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/ /usr/local/bin/
RUN mkdir -p /data && chown 1000:1000 /data
USER 1000:1000
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/webhook-control", "-config", "/etc/webhook-control/config.json", "-check-ready"]
ENTRYPOINT ["/usr/local/bin/webhook-control"]
CMD ["-config", "/etc/webhook-control/config.json"]
