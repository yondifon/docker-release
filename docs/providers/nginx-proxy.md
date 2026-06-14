# nginx-proxy Provider

Use this when your stack uses `nginxproxy/nginx-proxy`.

`docker-release` writes a managed `nginx.tmpl` file to a shared volume. nginx-proxy reads that template and generates its Nginx config from it. Your `VIRTUAL_HOST`, `VIRTUAL_PATH`, and other routing env vars work exactly as before — `docker-release` only controls which container IPs are live in the upstream.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - nginx-tmpl:/shared/nginx-tmpl:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  nginx-proxy:
    image: nginxproxy/nginx-proxy:alpine
    ports:
      - "80:80"
    volumes:
      - /var/run/docker.sock:/tmp/docker.sock:ro
      - nginx-tmpl:/app/custom:ro
    environment:
      NGINX_TMPL: /app/custom/nginx.tmpl
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    environment:
      VIRTUAL_HOST: app.example.com
      VIRTUAL_PATH: /
      VIRTUAL_DEST: /
    labels:
      release.enable: "true"
      release.provider: nginx-proxy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  nginx-tmpl:
```

## Required Labels

```yaml
release.enable: "true"
release.provider: nginx-proxy
```

## Environment Variables

| Variable | Description |
|---|---|
| `VIRTUAL_HOST` | Hostname nginx-proxy listens on |
| `VIRTUAL_PATH` | URL path prefix, e.g. `/app/` (include trailing slash) |
| `VIRTUAL_DEST` | Path sent to your app, e.g. `/` to strip the prefix |
| `VIRTUAL_PORT` | App port when the image exposes more than one |

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Override

| Label | Default | Override when |
|---|---|---|
| `release.nginx_proxy.config_dir` | `/shared/nginx-tmpl` | volume mounted at a different path |

## Multiple Apps

Each app uses a distinct `VIRTUAL_HOST` or `VIRTUAL_PATH`. `docker-release` manages each as a separate upstream.

```yaml
app:
  environment:
    VIRTUAL_HOST: example.com
    VIRTUAL_PATH: /app/
    VIRTUAL_DEST: /
  labels:
    release.enable: "true"
    release.provider: nginx-proxy

api:
  environment:
    VIRTUAL_HOST: example.com
    VIRTUAL_PATH: /api/
    VIRTUAL_DEST: /
  labels:
    release.enable: "true"
    release.provider: nginx-proxy
```

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
```

### Blue/Green

```yaml
release.strategy: blue-green
release.bg.soak_time: 5m
release.bg.green_weight: 50
```

## Notes

- Nginx open source does not support sticky cookies. `release.affinity: cookie` falls back to IP hashing with this provider. Use Angie, Caddy, HAProxy, or Traefik if you need real cookie-based sticky sessions.
- nginx-proxy is the recommended provider for [global mode](../global.md). `VIRTUAL_HOST` values are already unique per hostname so no extra namespacing is needed.

## Common Problems

| Problem | Fix |
|---|---|
| Route does not appear | Check `VIRTUAL_HOST` is set on the app container |
| Path routing fails | Include the trailing slash in `VIRTUAL_PATH`, e.g. `/app/` |
| Template not updated | Check `release.nginx_proxy.config_dir` matches the shared volume mount path |
