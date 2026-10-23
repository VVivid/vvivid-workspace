FROM golang:1.26.5-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
COPY traefik-plugin/go.mod ./traefik-plugin/go.mod
RUN go mod download
COPY *.go ./
COPY traefik-plugin ./traefik-plugin
RUN CGO_ENABLED=1 go build -o /demo .

FROM alpine:3.22
COPY --from=build /demo /demo
RUN mkdir /data
ENTRYPOINT ["/demo"]
