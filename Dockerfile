ARG COMPOSE_VERSION=v2.40.3
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM docker/compose-bin:${COMPOSE_VERSION} AS compose

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates \
  && mkdir -p /var/lib/docker-release
VOLUME /var/lib/docker-release
COPY --from=builder /bin/dr /usr/local/bin/dr
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
LABEL org.opencontainers.image.title="docker-release"
EXPOSE 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
ENTRYPOINT ["dr"]
CMD ["watch"]
