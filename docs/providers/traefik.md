# Traefik Provider

Use this when your app runs behind Traefik.

`docker-release` writes dynamic YAML files to a shared volume. Traefik watches that directory and reloads automatically. Your router labels stay on the app service — `docker-release` only manages the file-provider service that holds the live backend list.

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
      - "8080:8080"
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

## Required Labels

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

- `release.affinity: ip` uses Traefik HRW (highest random weight) routing.
- `release.affinity: cookie` uses a generated cookie name like `_srr_a172cedcae`.

## Common Problems

| Problem | Fix |
|---|---|
| Traefik routes to the Docker service directly | Set the router service to `app@file`, not `app` |
| Dynamic config does not load | Check the shared volume is mounted to `/etc/traefik/dynamic` |
| File provider not enabled | Add `--providers.file.directory` and `--providers.file.watch=true` to Traefik command |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
