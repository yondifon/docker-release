# HAProxy Provider

Use this when your app runs behind HAProxy.

Start with the bundle: HAProxy and `docker-release` in one service, with routes generated from app labels. Run HAProxy yourself only when you already do, or need to control its whole config.

## Bundled image

Use `malico/docker-release-haproxy:<version>` in place of the image in the [quick start](../../README.md#quick-start). The [shared bundle setup](../bundled-images.md) lists the labels. You do not need `release.provider`, a HAProxy config file, or a shared config volume.

The bundle routes each hostname and strips `release.path` before forwarding. Mounted certificates, separate private keys, and HTTP 308 redirects use the shared TLS labels. HAProxy does not obtain or renew certificates for you.

App releases keep client connections open while new requests use the replacement containers. Set `release.drain_timeout` long enough for your app's requests to finish before an old container stops.

For a custom frontend, set `DR_PROXY_SKIP_CONFIG=true` and mount your config at `/usr/local/etc/haproxy/haproxy.cfg:ro`. The bundle loads backend files from `/shared/haproxy-config`; reference `<service>_be` in your frontend rules. Shared routing labels do not add routes in this mode.

Keep these entries in your custom config so health checks and app releases work:

```haproxy
global
    stats socket /run/haproxy/admin.sock mode 600 level admin
    user haproxy
    group haproxy

defaults
    mode http
    timeout connect 5s
    timeout client 30s
    timeout server 30s
    timeout http-request 5s
    timeout http-keep-alive 5s

frontend http
    bind :80
    http-request return status 200 if { path /health }
    default_backend app_be
```

Do not expose the HAProxy control sockets over TCP.

## Separate HAProxy service

`docker-release` writes backend files to a shared volume and reloads HAProxy after each deploy. You write the `frontend` rules — `docker-release` only manages the `backend` blocks.

## What Gets Written

For a service named `app`, `docker-release` writes:

```haproxy
backend app_be
    mode http
    option http-keep-alive
    http-reuse safe
    balance roundrobin
    server s1 172.18.0.4:80
    server s2 172.18.0.5:80
```

Your HAProxy config references `app_be` in `use_backend` rules.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - haproxy-config:/shared/haproxy-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  haproxy:
    image: haproxy:lts-alpine
    ports:
      - "80:80"
    volumes:
      - haproxy-config:/etc/haproxy/conf.d:ro
      - ./haproxy.cfg:/etc/haproxy/haproxy.cfg:ro
    command: ["haproxy", "-W", "-f", "/etc/haproxy/haproxy.cfg", "-f", "/etc/haproxy/conf.d"]
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: haproxy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  haproxy-config:
```

## HAProxy Config

```haproxy
global
    master-worker
    log stdout format raw local0
    stats socket /tmp/haproxy.sock mode 660 level admin expose-fd listeners

defaults
    mode http
    timeout connect 5s
    timeout client 30s
    timeout server 30s
    option http-keep-alive
    log global

frontend http
    bind *:80
    use_backend app_be if { path_beg /app }
```

## Required labels for a separate proxy

```yaml
release.enable: "true"
release.provider: haproxy
```

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Overrides

| Label | Default | Override when |
|---|---|---|
| `release.haproxy.service` | auto-detected | multiple HAProxy containers in the project |
| `release.haproxy.config_dir` | `/shared/haproxy-config` | volume mounted at a different path |

## Multiple Apps

```haproxy
frontend http
    bind *:80

    acl is_app path_beg /app
    acl is_api path_beg /api

    use_backend app_be if is_app
    use_backend api_be if is_api
```

`docker-release` writes both `app_be` and `api_be` backend files automatically.

## Strip a Path Prefix

Use this when your app expects requests at `/`, not `/app`.

```haproxy
frontend http
    bind *:80

    acl is_app path_beg /app
    http-request set-path %[path,regsub(^/app,/)] if is_app
    use_backend app_be if is_app
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

With a separate HAProxy, `docker-release` also writes `backend app_preview_be` into the same file as `app_be`. It holds only the new containers during a release, and no servers the rest of the time, so HAProxy answers 503 then. Add one rule to your `frontend`, above your other `use_backend` rules:

```haproxy
use_backend app_preview_be if { hdr(host) -i next.app.example.com }
```

In the bundle you can also set `release.preview.token`, so one browser sees the new version on the normal host; see [Preview link](../readme.md#preview-link). With a separate HAProxy, `release.preview.token` is refused.

## Notes

- HAProxy must run with `-W` (master-worker mode) for graceful reloads.
- Cookie affinity uses a generated cookie name like `_srr_a172cedcae`.

## Common Problems

| Problem | Fix |
|---|---|
| Reload fails | Run HAProxy with `-W` in the command |
| Backend file not loaded | Pass `/etc/haproxy/conf.d` as a second `-f` arg in the command |
| App receives the wrong path | Add a path-strip rule in `haproxy.cfg` |
| Rollback state lost after `docker compose down` | Mount `/var/lib/docker-release` to a named volume |
