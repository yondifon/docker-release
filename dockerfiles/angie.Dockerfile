FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags bundled_angie -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM docker.angie.software/angie:1.12.4-minimal AS runtime

RUN apk add --no-cache ca-certificates tini \
  && mkdir -p /run/angie /shared/angie-config /shared/angie-routes

COPY --from=builder /bin/dr /usr/local/bin/dr

LABEL org.opencontainers.image.title="docker-release" \
  com.malico.docker-release.bundled-proxy="angie"

ENV DR_BUNDLED_PROXY=angie \
  DR_DEFAULT_PROVIDER=angie

EXPOSE 80 443 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
ENTRYPOINT ["/sbin/tini", "--", "dr"]
CMD ["watch"]
