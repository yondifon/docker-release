ARG COMPOSE_VERSION=v2.40.3
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_traefik -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM docker/compose-bin:${COMPOSE_VERSION} AS compose

FROM traefik:v3.6 AS runtime
RUN apk add --no-cache ca-certificates tini \
  && mkdir -p /etc/traefik /shared/traefik-config
COPY --from=builder /bin/dr /usr/local/bin/dr
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY packaging/traefik/health.yml /shared/traefik-config/health.yml

LABEL org.opencontainers.image.title="docker-release" \
  com.malico.docker-release.bundled-proxy="traefik"
ENV DR_BUNDLED_PROXY=traefik \
  DR_DEFAULT_PROVIDER=traefik
EXPOSE 80 443 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/sbin/tini", "--", "dr"]
CMD ["watch"]
