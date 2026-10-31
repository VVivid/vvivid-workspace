FROM --platform=$BUILDPLATFORM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=build-ca,target=/run/secrets/build-ca \
    if [ -f /run/secrets/build-ca ]; then SSL_CERT_FILE=/run/secrets/build-ca go mod download; else go mod download; fi \
    && go mod verify
COPY cmd ./cmd
COPY internal ./internal
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$VERSION" -o /out/newsfeed ./cmd/newsfeed \
    && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/newsfeed-admin ./cmd/newsfeed-admin \
    && mkdir -p -m 0700 /out/runtime/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="AgentNewsfeed" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT
COPY --from=build /out/newsfeed /newsfeed
COPY --from=build /out/newsfeed-admin /newsfeed-admin
WORKDIR /data
# Copy /data as a child directory so its private mode is applied to the directory itself.
COPY --from=build --chown=65532:65532 /out/runtime/ /
USER 65532:65532
ENV NEWSFEED_HTTP_ADDR=:8091 NEWSFEED_DB_PATH=/data/newsfeed.sqlite
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 CMD ["/newsfeed", "healthcheck", "ready"]
ENTRYPOINT ["/newsfeed"]
