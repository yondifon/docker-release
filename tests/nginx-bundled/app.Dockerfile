FROM traefik/whoami:v1.11.0 AS whoami
FROM alpine:3.23
COPY --from=whoami /whoami /usr/local/bin/whoami
EXPOSE 80
HEALTHCHECK --interval=1s --timeout=1s --retries=10 \
  CMD ["wget", "-qO-", "http://127.0.0.1/health"]
ENTRYPOINT ["whoami"]
