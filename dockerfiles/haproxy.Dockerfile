ARG COMPOSE_VERSION=v2.40.3
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_haproxy -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM docker/compose-bin:${COMPOSE_VERSION} AS compose

FROM haproxy:3.2-alpine AS runtime
USER root
RUN apk add --no-cache ca-certificates tini \
  && mkdir -p /run/haproxy /shared/haproxy-config/routes \
    /var/lib/docker-release
VOLUME /var/lib/docker-release

COPY --from=builder /bin/dr /usr/local/bin/dr
COPY --from=compose /docker-compose /usr/local/bin/docker-compose

LABEL org.opencontainers.image.title="docker-release" \
  com.malico.docker-release.bundled-proxy="haproxy"

ENV DR_BUNDLED_PROXY=haproxy \
  DR_DEFAULT_PROVIDER=haproxy

EXPOSE 80 443 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/sbin/tini", "--", "dr"]
CMD ["watch"]
