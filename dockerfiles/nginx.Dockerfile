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
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_nginx -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

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
  && apk add --no-cache ca-certificates nginx tini \
  && mkdir -p /run/nginx /shared/nginx-config /shared/nginx-routes \
    /etc/docker-release/nginx/conf.d /etc/docker-release/nginx/http.d \
    /etc/docker-release/nginx/server.d /etc/docker-release/nginx/ssl.d \
    /etc/docker-release/nginx/https.d /var/lib/docker-release
VOLUME /var/lib/docker-release

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
