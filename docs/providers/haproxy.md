# HAProxy Provider

Use this when your app runs behind HAProxy.

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

## Required Labels

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

## Notes

- HAProxy must run with `-W` (master-worker mode) for graceful reloads.
- Cookie affinity uses a generated cookie name like `_srr_a172cedcae`.

## Common Problems

| Problem | Fix |
|---|---|
| Reload fails | Run HAProxy with `-W` in the command |
| Backend file not loaded | Pass `/etc/haproxy/conf.d` as a second `-f` arg in the command |
| App receives the wrong path | Add a path-strip rule in `haproxy.cfg` |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
