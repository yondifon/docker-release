FROM golang:1.24-alpine AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /bin/dr ./cmd/docker-release/

FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/dr /usr/local/bin/dr
LABEL org.opencontainers.image.title="docker-release"
EXPOSE 9080 9081
HEALTHCHECK --interval=5s --timeout=2s --start-period=30s --retries=6 \
  CMD ["dr", "healthcheck"]
ENTRYPOINT ["dr"]
CMD ["watch"]
