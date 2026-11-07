FROM --platform=$BUILDPLATFORM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=build-ca,target=/run/secrets/build-ca \
    if [ -f /run/secrets/build-ca ]; then SSL_CERT_FILE=/run/secrets/build-ca go mod download; else go mod download; fi \
    && go mod verify
COPY account ./account
COPY cmd/artificial-memory ./cmd/artificial-memory
COPY internal ./internal
COPY memory ./memory
COPY store ./store
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/artificial-memory ./cmd/artificial-memory \
    && mkdir -p -m 0700 /out/runtime/data/sqlite

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
ARG VERSION=0.1.0
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="ArtificialMemory" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT
COPY --from=build /out/artificial-memory /artificial-memory
COPY --from=build --chown=65532:65532 /out/runtime/ /
USER 65532:65532
WORKDIR /data
ENV MEMORY_LISTEN=:8080 MEMORY_DATABASE=/data/sqlite/memory.db TMPDIR=/data/sqlite SQLITE_TMPDIR=/data/sqlite
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 CMD ["/artificial-memory", "healthcheck", "ready"]
ENTRYPOINT ["/artificial-memory"]
CMD ["serve"]
