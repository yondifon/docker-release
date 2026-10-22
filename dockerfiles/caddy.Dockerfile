FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_caddy -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM caddy:2-alpine AS runtime

RUN apk add --no-cache tini \
  && mkdir -p /shared/caddy-config

COPY --from=builder /bin/dr /usr/local/bin/dr

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
