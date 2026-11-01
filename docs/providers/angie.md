# Angie Provider

Use this when your app runs behind Angie.

Start with the bundle: Angie and `docker-release` in one service, with routes generated from app labels. Run Angie yourself only when you already do, or need to control its whole config.

Angie is API-compatible with Nginx but adds native sticky cookie support, which makes it the right choice when you need `release.affinity: cookie`.

## Bundled image

Use `malico/docker-release-angie:<version>` in place of the image in the [quick start](../../README.md#quick-start). The [shared bundle setup](../bundled-images.md) lists the labels. You do not need `release.provider`, an Angie config file, or a shared config volume.

Set `release.affinity: cookie` for native sticky cookies. The default remains `ip`. You can also use `release.angie.sticky.learn.name` to route by a cookie your app sets.

### Custom config

| Path | Purpose |
|---|---|
| `/shared/angie-config/*.conf` | generated upstreams; keep writable |
| `/shared/angie-routes/*.server` | generated hostname routes; keep writable |
| `/shared/angie-routes/*.location` | generated hostless path routes; keep writable |
| `/etc/docker-release/proxy/http.d/*.conf` | custom `http` context snippets |
| `/etc/docker-release/proxy/conf.d/*.conf` | extra `server` blocks |
| `/etc/docker-release/proxy/server.d/*.conf` | snippets inside generated HTTP/HTTPS servers |
| `/etc/docker-release/proxy/ssl.d/*.conf` | snippets before routes in generated HTTPS servers |
| `/etc/docker-release/proxy/https.d/*.conf` | snippets after routes in generated HTTPS servers |
| `/etc/angie/angie.conf` | full config override; set `DR_PROXY_SKIP_CONFIG=true` |

Mount your custom files read-only. `/etc/docker-release/angie/` also accepts the snippet directories listed under `proxy/`. A full config override must keep `master_process` enabled and serve HTTP 200 at `http://127.0.0.1/health` without a redirect. Use the same generated directories for every app sharing this Angie instance.

Angie checks config changes before applying them. If a change is rejected, the previous files are restored and the deployment fails. Check the Angie logs, labels, and mounted certificates before trying again. Old app containers stay available until Angie accepts the new config.

The controller and Angie master run as root for the Docker socket and ports 80/443. Angie workers run as `angie`. Restarting this service restarts the proxy; app releases do not. Clients must finish HTTP headers and TLS setup within 10 seconds; application response times are not capped by this limit.

### Check locally

Run `make up-angie-bundled`, then run these commands in another terminal:

```sh
python3 tests/bundled/check-routing.py angie
python3 tests/bundled/check-reload.py angie
python3 tests/bundled/check-shutdown.py angie
make down-angie-bundled
```

The checks cover all three app routes, an app release during a delayed reload, and shutdown with an incomplete HTTP request. The last command removes the test stack and its volumes.

## Separate Angie service

Run Angie yourself when you need a config the bundle cannot take. `docker-release` writes upstream files to a shared volume and reloads Angie; you write the `server` and `location` rules.

## What Gets Written

For a service named `app`, `docker-release` writes:

```nginx
upstream app_upstream {
    sticky cookie _srr_a172cedcae path=/;
    server 172.18.0.4:80;
    server 172.18.0.5:80;
}
```

Your Angie config references `app_upstream` wherever you need it.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - angie-config:/shared/angie-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  angie:
    image: docker.angie.software/angie:latest
    ports:
      - "80:80"
    volumes:
      - angie-config:/etc/angie/http.d/custom:ro
      - ./angie.conf:/etc/angie/http.d/default.conf:ro
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: angie
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  angie-config:
```

## Angie Config

```nginx
include /etc/angie/http.d/custom/*.conf;

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

Note: many Angie images use `/etc/angie/http.d` instead of `/etc/nginx/conf.d`. Check your image.

## Required labels for a separate proxy

```yaml
release.enable: "true"
release.provider: angie
```

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Overrides

| Label | Default | Override when |
|---|---|---|
| `release.angie.service` | auto-detected | multiple Angie containers in the project |
| `release.angie.config_dir` | `/shared/angie-config` | volume mounted at a different path |
| `release.angie.route_dir` | `/shared/angie-routes` in the bundle; empty otherwise | generated route files need another path and a matching proxy config |

## Multiple Apps

```nginx
include /etc/angie/http.d/custom/*.conf;

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

Add `release.enable: "true"` and `release.provider: angie` to each app service.

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

## Preview address

Set `release.preview.host` on a blue/green app to try the new version on its own host before users move; see [Preview address](../readme.md#preview-address).

In the bundle, the route is added for you. The preview host uses the same certificate settings as the main host, so the certificate must also cover `next.app.example.com`.

With a separate Angie, `docker-release` also writes `app_preview_upstream` into the same file as `app_upstream`. During a release it holds only the new containers. The rest of the time it holds one server marked `down`, so your config still loads. Add one `server` block:

```nginx
server {
    listen 80;
    server_name next.app.example.com;

    location / {
        proxy_pass http://app_preview_upstream;
    }
}
```

## Notes

- Cookie affinity uses a generated cookie name like `_srr_a172cedcae`.
- Angie uses `http.d` in many images instead of `conf.d`. Check the config dir for your image.

## Common Problems

| Problem | Fix |
|---|---|
| Angie cannot find upstream files | Check the mount path — many Angie images use `/etc/angie/http.d`, not `/etc/nginx/conf.d` |
| Reload does not run | Set `release.angie.service` to your Angie Compose service name |
| Sticky sessions not working | Set `release.affinity: cookie` |
| Rollback state lost after `docker compose down` | Mount `/var/lib/docker-release` to a named volume |
