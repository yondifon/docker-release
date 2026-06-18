ARG BASE_IMAGE=malico/docker-release:latest

FROM golang:1.24-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags bundled_nginx -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM ${BASE_IMAGE}

RUN apk add --no-cache nginx \
  && mkdir -p /run/nginx /shared/nginx-config /shared/nginx-routes \
    /etc/docker-release/nginx/conf.d /etc/docker-release/nginx/http.d \
    /etc/docker-release/nginx/server.d /etc/docker-release/nginx/ssl.d \
    /etc/docker-release/nginx/https.d

COPY --from=builder /bin/dr /usr/local/bin/dr
COPY packaging/nginx/nginx.conf /etc/nginx/nginx.conf

LABEL org.opencontainers.image.title="docker-release" \
  com.malico.docker-release.bundled-proxy="nginx"

ENV DR_BUNDLED_PROXY=nginx \
  DR_DEFAULT_PROVIDER=nginx

EXPOSE 80 443 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
ENTRYPOINT ["dr"]
CMD ["watch"]
