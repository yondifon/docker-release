# Nginx Provider

Use this when you run Nginx and control its config.

`docker-release` writes upstream files to a shared volume and reloads Nginx after each deploy. You write the `server` block and `location` rules — `docker-release` only manages the `upstream` blocks.

## What Gets Written

For a service named `app`, `docker-release` writes:

```nginx
upstream app_upstream {
    ip_hash;
    server 172.18.0.4:80;
    server 172.18.0.5:80;
}
```

Your Nginx config references `app_upstream` wherever you need it.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - nginx-config:/shared/nginx-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
    volumes:
      - nginx-config:/etc/nginx/conf.d/custom:ro
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: nginx
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  nginx-config:
```

## Bundled Image (Draft)

Use `malico/docker-release-nginx` when you want one service instead of a controller sidecar plus a separate Nginx service.

```yaml
services:
  docker-release:
    image: malico/docker-release-nginx:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - docker-release-state:/var/lib/docker-release

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.nginx.path: "/"
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  docker-release-state:
```

`docker-release-nginx` sets `DR_DEFAULT_PROVIDER=nginx`, so app services can omit `release.provider`. Explicit `release.provider` labels still win.

The base `malico/docker-release` image does not include bundled Nginx runtime code. This image overlays a `dr` binary built with the `bundled_nginx` tag.

Publish ports only when this container should receive traffic directly:

```yaml
ports:
  - "80:80"
  - "443:443"
```

Enable HTTPS by mounting your cert/key and pointing Nginx at them:

```yaml
services:
  docker-release:
    image: malico/docker-release-nginx:latest
    environment:
      DR_NGINX_SERVER_NAME: example.com
      DR_NGINX_SSL_CERT: /certs/fullchain.pem
      DR_NGINX_SSL_KEY: /certs/privkey.pem
      DR_NGINX_REDIRECT_HTTPS: "true"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./certs:/certs:ro
```

Custom config paths:

| Path | Purpose |
|---|---|
| `/shared/nginx-config/*.conf` | generated upstreams, managed by `docker-release` |
| `/shared/nginx-routes/*.location` | generated `release.nginx.path` routes |
| `/etc/docker-release/nginx/http.d/*.conf` | custom `http` context snippets |
| `/etc/docker-release/nginx/conf.d/*.conf` | custom top-level `http` snippets, including extra `server` blocks |
| `/etc/docker-release/nginx/server.d/*.conf` | custom snippets inside the generated HTTP/HTTPS server |
| `/etc/docker-release/nginx/ssl.d/*.conf` | custom snippets inside the generated HTTPS server before routes |
| `/etc/docker-release/nginx/https.d/*.conf` | custom snippets inside the generated HTTPS server after routes |
| `/etc/nginx/nginx.conf` | full bundled config override; set `DR_NGINX_SKIP_CONFIG=true` |

The bundled image waits for initial upstream and route files before starting Nginx. This lets custom mounted config reference generated upstreams like `app_upstream` on first boot.

## Nginx Config

```nginx
include /etc/nginx/conf.d/custom/*.conf;

server {
    listen 80;

    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    location / {
        proxy_pass http://app_upstream/;
    }
}
```

## Required Labels

```yaml
release.enable: "true"
release.provider: nginx
```

With `malico/docker-release-nginx`, `release.provider` is optional because the image sets `DR_DEFAULT_PROVIDER=nginx`.

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Overrides

| Label | Default | Override when |
|---|---|---|
| `release.nginx.service` | auto-detected | multiple Nginx containers in the project |
| `release.nginx.config_dir` | `/shared/nginx-config` | volume mounted at a different path |
| `release.nginx.route_dir` | `/shared/nginx-routes` | generated `release.nginx.path` route files need a different path |
| `release.nginx.path` | empty | bundled Nginx should generate a route for this service |

## Multiple Apps

```nginx
include /etc/nginx/conf.d/custom/*.conf;

server {
    listen 80;

    location /app/ {
        proxy_pass http://app_upstream/;
    }

    location /api/ {
        proxy_pass http://api_upstream/;
    }
}
```

Add `release.enable: "true"` and `release.provider: nginx` to each app service.

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

- Nginx open source does not support sticky cookies. `release.affinity: cookie` falls back to IP hashing. Use Angie if you need real cookie sticky sessions with an Nginx-compatible config format.
- If `release.nginx.service` is not set, `docker-release` auto-detects the Nginx container in the same Compose project by image name.

## Common Problems

| Problem | Fix |
|---|---|
| Nginx ignores upstream files | Check `include /etc/nginx/conf.d/custom/*.conf;` is in your config |
| Reload does not run | Set `release.nginx.service` to your Nginx Compose service name |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
