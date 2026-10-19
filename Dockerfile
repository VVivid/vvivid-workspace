FROM --platform=$BUILDPLATFORM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5 AS assets
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY web ./web
COPY internal ./internal
RUN npm run build:css && node --check web/static/app.js

FROM --platform=$BUILDPLATFORM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY --from=assets /src/web ./web
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w -X agentshoppinglist/internal/buildinfo.Version=$VERSION -X agentshoppinglist/internal/buildinfo.Commit=$COMMIT -X agentshoppinglist/internal/buildinfo.BuiltAt=$BUILT_AT" -o /out/shopping ./cmd/shopping \
    && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -ldflags="-s -w -X agentshoppinglist/internal/buildinfo.Version=$VERSION -X agentshoppinglist/internal/buildinfo.Commit=$COMMIT -X agentshoppinglist/internal/buildinfo.BuiltAt=$BUILT_AT" -o /out/shopping-admin ./cmd/shopping-admin \
    && mkdir -m 0700 /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
LABEL org.opencontainers.image.title="AgentShoppingList" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT \
      org.opencontainers.image.created=$BUILT_AT
COPY --from=build /out/shopping /shopping
COPY --from=build /out/shopping-admin /shopping-admin
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /data
ENV SHOPPING_HTTP_ADDR=:8090 SHOPPING_DB_PATH=/data/shopping.sqlite
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 CMD ["/shopping", "healthcheck", "ready"]
ENTRYPOINT ["/shopping"]
