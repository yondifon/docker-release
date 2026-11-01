FROM golang:1.26-alpine AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -o /app main.go

FROM alpine:3.23
ARG MODE=ok
ENV MODE=$MODE
COPY --from=build /app /usr/local/bin/app
EXPOSE 80
HEALTHCHECK --interval=1s --timeout=1s --retries=10 \
  CMD ["wget", "-qO-", "http://127.0.0.1/health"]
ENTRYPOINT ["app"]
