ARG COMPOSE_VERSION=v2.40.3
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_caddy -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS caddy-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY packaging/caddy/runtime/go.mod packaging/caddy/runtime/go.sum ./
RUN go mod download
COPY packaging/caddy/runtime/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -p 2 -ldflags="-s -w" -o /bin/caddy ./cmd/caddy/

FROM docker/compose-bin:${COMPOSE_VERSION} AS compose

FROM alpine:3.23 AS runtime

RUN apk add --no-cache ca-certificates tini \
  && mkdir -p /etc/caddy /shared/caddy-config /var/lib/docker-release
VOLUME /var/lib/docker-release

COPY --from=builder /bin/dr /usr/local/bin/dr
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=caddy-builder /bin/caddy /usr/bin/caddy

LABEL org.opencontainers.image.title="docker-release" \
  com.malico.docker-release.bundled-proxy="caddy"

ENV DR_BUNDLED_PROXY=caddy \
  DR_DEFAULT_PROVIDER=caddy \
  XDG_DATA_HOME=/var/lib/docker-release \
  XDG_CONFIG_HOME=/var/lib/docker-release/config

EXPOSE 80 443 443/udp 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
ENTRYPOINT ["/sbin/tini", "--", "dr"]
CMD ["watch"]
