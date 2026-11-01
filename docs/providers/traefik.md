# Traefik Provider

Use this when your app runs behind Traefik.

Start with the bundle: Traefik and `docker-release` in one service, with routes generated from app labels. Run Traefik yourself only when you already do, or need to control its whole config.

## Bundled image

Use `malico/docker-release-traefik:<version>` in place of the image in the [quick start](../../README.md#quick-start). The [shared bundle setup](../bundled-images.md) lists the labels. You do not need `release.provider`, a Traefik config file, a separate proxy service, or a shared config volume.

The bundle routes `release.host`, strips `release.path`, and supports the shared certificate, key, and HTTP 308 redirect labels. Traefik does not obtain or renew certificates in this setup. Mount certificates read-only and publish port 443 to add HTTPS.

App releases keep existing client connections open. Old containers stay available until Traefik has applied the new routes and backend list. Keep `release.drain_timeout` long enough for your app's requests to finish. A rejected config leaves the previous routing files in place and fails the deployment; check the logs, labels, and mounted certificates before trying again.

Routing comes only from the shared `release.*` labels. Do not enable Traefik's Docker routing provider or add `traefik.*` routing labels to bundled apps.

### Custom config

Set `DR_PROXY_SKIP_CONFIG=true` and mount a full static config read-only at `/etc/traefik/traefik.yml`. Keep these settings from [the default config](../../packaging/traefik/traefik.yml):

- HTTP entry point `web` on port 80 and HTTPS entry point `websecure` on port 443.
- File provider watching `/shared/traefik-config`; keep this directory writable.
- API on `127.0.0.1:8080`, with `api.insecure: true` and the dashboard off.
- `ping.manualRouting: true` and `/shared/traefik-config/health.yml` for HTTP 200 at `/health`.
- Bounded request and shutdown timeouts.

Keep the API bound to loopback; do not publish its port or route `api@internal` publicly. Port `127.0.0.1:8082` is also reserved inside the bundle. Names starting with `dr-` in the file provider are reserved for generated routes, services, and middleware. Custom static config does not turn off the shared routing labels.

### Check locally

```sh
docker compose -f tests/traefik-bundled/docker-compose.yml up --build -d --wait
python3 tests/bundled/check-routing.py traefik
python3 tests/bundled/check-reload.py traefik
python3 tests/bundled/check-shutdown.py traefik
docker compose -f tests/traefik-bundled/docker-compose.yml down -v
```

The checks cover app bodies, stripped paths, unknown routes, all three release strategies, and shutdown with an incomplete request. See [bundle checks](../../tests/bundled/README.md) for local TLS and image cleanup.

## Separate Traefik service

`docker-release` writes dynamic YAML files to a shared volume. Traefik watches that directory and reloads automatically. Your router labels stay on the app service; `docker-release` only manages the file-provider service holding the live backend list. This setup does not confirm that Traefik has applied a file change before a release continues; use the bundle when you need that guarantee.

## What Gets Written

For a service named `app`, `docker-release` writes:

```yaml
http:
  services:
    app:
      loadBalancer:
        servers:
          - url: "http://172.18.0.4:80"
          - url: "http://172.18.0.5:80"
```

Your router label points to `app@file` to use this service.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - traefik-config:/shared/traefik-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  traefik:
    image: traefik:v3
    ports:
      - "80:80"
      - "127.0.0.1:8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - traefik-config:/etc/traefik/dynamic:ro
    command:
      - --api.insecure=true
      - --providers.docker=true
      - --providers.docker.exposedbydefault=false
      - --providers.file.directory=/etc/traefik/dynamic
      - --providers.file.watch=true
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: traefik
      traefik.enable: "true"
      traefik.http.routers.app.rule: "Host(`app.example.com`)"
      traefik.http.routers.app.service: "app@file"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  traefik-config:
```

## Required labels for a separate proxy

```yaml
release.enable: "true"
release.provider: traefik
traefik.http.routers.app.service: "app@file"
```

`app@file` is required. It tells Traefik to use the file-provider service that `docker-release` writes instead of the Docker-provider service.

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Override

| Label | Default | Override when |
|---|---|---|
| `release.traefik.config_dir` | `/shared/traefik-config` | volume mounted at a different path |

## Path Routing

```yaml
traefik.http.routers.app.rule: "PathPrefix(`/app`)"
traefik.http.routers.app.service: "app@file"
traefik.http.routers.app.middlewares: "strip-app"
traefik.http.middlewares.strip-app.stripprefix.prefixes: "/app"
```

## Multiple Apps

```yaml
app:
  labels:
    traefik.http.routers.app.rule: "Host(`example.com`) && PathPrefix(`/app`)"
    traefik.http.routers.app.service: "app@file"
    traefik.http.routers.app.middlewares: "strip-app"
    traefik.http.middlewares.strip-app.stripprefix.prefixes: "/app"

api:
  labels:
    traefik.http.routers.api.rule: "Host(`example.com`) && PathPrefix(`/api`)"
    traefik.http.routers.api.service: "api@file"
    traefik.http.routers.api.middlewares: "strip-api"
    traefik.http.middlewares.strip-api.stripprefix.prefixes: "/api"
```

Add `release.enable: "true"` and `release.provider: traefik` to each app service.

## Strategies

See [docs/readme.md#deploy-strategies](../readme.md#deploy-strategies) for full explanation.

### Linear (default)

```yaml
release.drain_timeout: 10s
release.health_check_timeout: 60s
```

### Canary

```yaml
release.strategy: canary
release.canary.start_percentage: 10
release.canary.step: 20
release.canary.interval: 2m
release.affinity: cookie
```

### Blue/Green

```yaml
release.strategy: blue-green
release.bg.soak_time: 5m
release.bg.green_weight: 50
```

## Notes

- `release.affinity: ip` uses sticky cookies for unweighted services and HRW (highest random weight) for weighted groups.
- `release.affinity: cookie` uses a generated cookie name like `_srr_a172cedcae`.

## Common Problems

| Problem | Fix |
|---|---|
| Traefik routes to the Docker service directly | Set the router service to `app@file`, not `app` |
| Dynamic config does not load | Check the shared volume is mounted to `/etc/traefik/dynamic` |
| File provider not enabled | Add `--providers.file.directory` and `--providers.file.watch=true` to Traefik command |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
