# Version arguments are pinned by release CI; the resulting image is deployed by digest.
FROM node:24.19.0-alpine3.23@sha256:244cc2b53f46f9e876304391d17682b0ddae9ac33491f4857e25e35a36ba7995 AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM golang:1.26.9-alpine3.23@sha256:6750308561953d451210ed1a8086d36c8c8bdcbf672d38d9d0d424b8a7a0216c AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /vocabbank ./cmd/vocabbank

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT
WORKDIR /
COPY --from=backend /vocabbank /vocabbank
COPY --from=frontend /src/web/out /static
USER 65532:65532
EXPOSE 8090
ENTRYPOINT ["/vocabbank"]
CMD ["-listen=:8090","-db=/data/vocabbank.sqlite","-static=/static"]
