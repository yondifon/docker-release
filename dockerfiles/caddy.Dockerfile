# Built from source for a newer golang.org/x/net than docker/compose-bin has. Once a release
# ships x/net v0.62.0 or newer, make `compose` `FROM docker/compose-bin:${COMPOSE_VERSION}` again.
ARG COMPOSE_VERSION=v5.7.0
ARG XNET_VERSION=v0.62.0
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

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS compose
ARG COMPOSE_VERSION
ARG XNET_VERSION
ARG TARGETOS
ARG TARGETARCH
ADD https://github.com/docker/compose.git#${COMPOSE_VERSION} /src
WORKDIR /src
RUN go get golang.org/x/net@${XNET_VERSION} && go mod tidy
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-w -X github.com/docker/compose/v5/internal.Version=${COMPOSE_VERSION}" -o /docker-compose ./cmd

FROM alpine:3.23 AS runtime

RUN apk upgrade --no-cache \
  && apk add --no-cache ca-certificates tini \
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
