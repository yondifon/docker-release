# Nginx Provider

Use this when you run Nginx and control its config.

`docker-release` writes upstream files and reloads Nginx after each deploy. With a separate Nginx service, you supply the `server` and `location` rules. The bundled image can generate those rules from labels.

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

## Bundled Image

Use `malico/docker-release-nginx` for Nginx and docker-release in one service. Follow [Bundled proxy images](../bundled-images.md) for the Compose setup, routing labels, TLS, defaults, and switching providers.

### Custom config

Mount extra snippets read-only at these paths. The `proxy` paths work in both bundles; existing `nginx` paths are also supported.

| Path | Purpose |
|---|---|
| `/shared/nginx-config/*.conf` | generated upstreams, managed by `docker-release` |
| `/shared/nginx-routes/*.server` | generated hostname routes |
| `/shared/nginx-routes/*.location` | generated hostless path routes |
| `/etc/docker-release/proxy/http.d/*.conf` | custom `http` context snippets |
| `/etc/docker-release/proxy/conf.d/*.conf` | extra `server` blocks |
| `/etc/docker-release/proxy/server.d/*.conf` | snippets inside generated HTTP/HTTPS servers |
| `/etc/docker-release/proxy/ssl.d/*.conf` | snippets before routes in generated HTTPS servers |
| `/etc/docker-release/proxy/https.d/*.conf` | snippets after routes in generated HTTPS servers |
| `/etc/nginx/nginx.conf` | full config override; set `DR_PROXY_SKIP_CONFIG=true` |

The container becomes healthy only after its initial config is ready and Nginx responds at `/health`. A full config override must keep `master_process` enabled and serve HTTP 200 at `http://127.0.0.1/health` without a redirect.

Nginx checks each config change before reloading. If it rejects a change, the previous files are restored and the deployment fails. Check the Nginx logs, labels, and mounted certificates before trying again.

A release keeps old containers running until Nginx accepts the new config.

Keep generated config directories writable and use the same directories for all services sharing this Nginx instance. The default config includes only the paths listed above; changing directory labels also needs a matching config override.

The Nginx master and controller run as root to access the Docker socket and ports 80/443. Nginx workers run as `nginx`. Mounting the Docker socket grants control of the Docker host; use this image only for trusted services.

Restarting the bundled service also restarts Nginx. App releases do not restart it.

Clients must send HTTP headers and finish TLS setup within 10 seconds. This limit does not cap application response times.

To check the bundle locally, run `make up-nginx-bundled`. In another terminal, run `python3 tests/bundled/check-reload.py nginx`, then `python3 tests/bundled/check-shutdown.py nginx`. The checks replace an app during a delayed reload and stop the bundle with an incomplete HTTP request. Run `make down-nginx-bundled` to remove the test stack and its volumes.

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
| `release.nginx.route_dir` | `/shared/nginx-routes` | generated Nginx route files need a different path |
| `release.nginx.host` | empty | bundled Nginx should generate a server block for this host |
| `release.nginx.path` | empty | bundled Nginx should generate a route for this service |
| `release.nginx.ssl.cert` | empty | mounted TLS certificate path for this service |
| `release.nginx.ssl.key` | empty | mounted TLS key path for this service |
| `release.nginx.ssl.redirect` | `false` | redirect HTTP to HTTPS for this service |

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
