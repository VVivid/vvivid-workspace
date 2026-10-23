FROM golang:1.26.5-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/google-health ./cmd/google-health

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/google-health /usr/local/bin/google-health
COPY --chmod=0555 deploy/collector-entrypoint.sh /usr/local/bin/google-health-container
USER 1000:1000
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/google-health"]
CMD ["sync"]
