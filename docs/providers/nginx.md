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

Use `malico/docker-release-nginx` when you want one service instead of a controller sidecar plus a separate Nginx service.

```yaml
services:
  docker-release:
    image: malico/docker-release-nginx:latest
    ports:
      - "80:80"
    stop_grace_period: 90s
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - docker-release-state:/var/lib/docker-release

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.nginx.host: app.example.com
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

Omit published ports if another proxy connects to this container over the Compose network. Publish port 443 only when you configure TLS:

```yaml
ports:
  - "443:443"
```

Give each service a different host. You can set several comma-separated hosts for one service. Do not assign the same host to separate services; use your own server block for several paths on one host.

Add the certificate mount and TLS labels to the Compose setup above:

```yaml
services:
  docker-release:
    image: malico/docker-release-nginx:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./certs:/certs:ro

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.nginx.host: app.example.com
      release.nginx.path: "/"
      release.nginx.ssl.cert: /certs/app/fullchain.pem
      release.nginx.ssl.key: /certs/app/privkey.pem
      release.nginx.ssl.redirect: "true"
```

Custom config paths:

| Path | Purpose |
|---|---|
| `/shared/nginx-config/*.conf` | generated upstreams, managed by `docker-release` |
| `/shared/nginx-routes/*.server` | generated per-service `release.nginx.host` server blocks |
| `/shared/nginx-routes/*.location` | generated hostless `release.nginx.path` fallback routes |
| `/etc/docker-release/nginx/http.d/*.conf` | custom `http` context snippets |
| `/etc/docker-release/nginx/conf.d/*.conf` | custom top-level `http` snippets, including extra `server` blocks |
| `/etc/docker-release/nginx/server.d/*.conf` | custom snippets inside the generated HTTP/HTTPS server |
| `/etc/docker-release/nginx/ssl.d/*.conf` | custom snippets inside the generated HTTPS server before routes |
| `/etc/docker-release/nginx/https.d/*.conf` | custom snippets inside the generated HTTPS server after routes |
| `/etc/nginx/nginx.conf` | full bundled config override; set `DR_NGINX_SKIP_CONFIG=true` |

The container becomes healthy only after its initial config is ready and Nginx responds at `/health`. A full config override must keep `master_process` enabled and serve HTTP 200 at `http://127.0.0.1/health` without a redirect.

`release.nginx.host` defaults to path `/` when no path is set. A path without a host adds a route to the default HTTP server. `/health` is reserved there. Each generated path removes its prefix before forwarding: `/app/orders` at path `/app/` reaches your app as `/orders`.

Nginx checks each config change before reloading. If it rejects a change, the previous files are restored and the deployment fails. Check the Nginx logs, labels, and mounted certificates before trying again.

A release keeps old containers running until Nginx accepts the new config.

Keep generated config directories writable and use the same directories for all services sharing this Nginx instance. The default config includes only the paths listed above; changing directory labels also needs a matching config override.

The Nginx master and controller run as root to access the Docker socket and ports 80/443. Nginx workers run as `nginx`. Mounting the Docker socket grants control of the Docker host; use this image only for trusted services.

Restarting the bundled service also restarts Nginx. App releases do not restart it.

Clients must send HTTP headers and finish TLS setup within 10 seconds. This limit does not cap application response times.

To check shutdown handling, start `make up-nginx-bundled`, then run `python3 tests/nginx-bundled/check-shutdown.py` in another terminal. The check opens an incomplete HTTP request and stops the test controller; it must exit cleanly within 15 seconds.

Run `python3 tests/nginx-bundled/check-reload.py` against the running test stack to check delayed reloads. It briefly pauses the Nginx master and replaces `linear_app`; requests must keep succeeding, and the old container must stay alive until Nginx accepts the update.

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
