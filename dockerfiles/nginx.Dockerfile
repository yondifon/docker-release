ARG COMPOSE_VERSION=v2.40.3
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_nginx -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM docker/compose-bin:${COMPOSE_VERSION} AS compose

FROM alpine:3.23 AS runtime

RUN apk add --no-cache ca-certificates nginx tini \
  && mkdir -p /run/nginx /shared/nginx-config /shared/nginx-routes \
    /etc/docker-release/nginx/conf.d /etc/docker-release/nginx/http.d \
    /etc/docker-release/nginx/server.d /etc/docker-release/nginx/ssl.d \
    /etc/docker-release/nginx/https.d

COPY --from=builder /bin/dr /usr/local/bin/dr
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY packaging/nginx/nginx.conf /etc/nginx/nginx.conf
RUN nginx -t

LABEL org.opencontainers.image.title="docker-release" \
  com.malico.docker-release.bundled-proxy="nginx"

ENV DR_BUNDLED_PROXY=nginx \
  DR_DEFAULT_PROVIDER=nginx

EXPOSE 80 443 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
ENTRYPOINT ["/sbin/tini", "--", "dr"]
CMD ["watch"]
