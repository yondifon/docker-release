# docker-release Docs

## Concepts

`docker-release` runs as a service inside your Compose stack. It connects to the Docker socket and watches for deploy commands.

When you run `docker release app`, it:

1. Starts new containers from the current image
2. Waits for each new container to pass its Docker health check
3. Adds new containers to your proxy config and removes old ones
4. Drains old containers (waits for in-flight requests to finish)
5. Stops old containers

It never touches traffic directly. Your proxy — nginx-proxy, Nginx, Caddy, Traefik, HAProxy, or Angie — serves all requests.

## Images

| Image | Use |
|---|---|
| `malico/docker-release` | controller-only sidecar; bring your own proxy service |
| `malico/docker-release-nginx` | draft bundled Nginx + controller image |

The bundled Nginx image keeps the same `dr` entrypoint, so `docker release help` and all host plugin commands still work.

The controller-only image is built without bundled proxy code. Bundled images overlay a `dr` binary built with the provider-specific build tag, so dormant proxy code does not ship in the default runtime.

## Deploy Strategies

Three strategies control how traffic shifts from old to new containers during a deploy.

### Linear (default)

Replaces containers one at a time. Traffic shifts gradually as each pair is swapped.

No label needed. Linear is the default.

```yaml
release.drain_timeout: 10s
release.health_check_timeout: 60s
```

### Blue/Green

Starts a full new set of containers before shifting any traffic. Keeps the old set running during a soak period so you can roll back instantly.

```yaml
release.strategy: blue-green
release.bg.soak_time: 5m       # how long to keep old containers alive after traffic shifts
release.bg.green_weight: 50    # traffic weight on new containers during soak (0-100)
```

### Canary

Sends a small percentage of traffic to new containers, then increases it in steps until the new version takes all traffic.

```yaml
release.strategy: canary
release.canary.start_percentage: 10   # initial traffic weight on new containers
release.canary.step: 20               # weight added each interval
release.canary.interval: 2m           # time between weight increases
```

For provider-specific strategy examples, see each [provider guide](./providers/).

## Session Affinity

Controls how requests are pinned to a backend while old and new containers run side by side.

| Value | Behavior |
|---|---|
| `ip` (default) | Routes by client IP. Supported by all providers. |
| `cookie` | Routes by sticky cookie. Not supported by Nginx or nginx-proxy — both fall back to IP hashing. Use with Angie, Caddy, HAProxy, or Traefik. |
| `""` (empty) | No affinity. Requests are load-balanced freely. |

```yaml
release.affinity: ip      # default
release.affinity: cookie  # sticky sessions (Angie, Caddy, HAProxy, Traefik only)
```

## Labels Reference

### Required

| Label | Value |
|---|---|
| `release.enable` | `"true"` — marks this service for management |

### Common

| Label | Default | Description |
|---|---|---|
| `release.strategy` | `linear` | Deploy strategy: `linear`, `blue-green`, or `canary` |
| `release.provider` | `nginx-proxy` or `DR_DEFAULT_PROVIDER` | `nginx-proxy`, `nginx`, `caddy`, `traefik`, `angie`, `haproxy`, or `none` |
| `release.health_check_timeout` | `60s` | Max time to wait for a new container to become healthy |
| `release.drain_timeout` | `10s` | Time to wait for in-flight requests before stopping old containers |
| `release.affinity` | `ip` | Session affinity: `ip`, `cookie`, or empty |
| `release.upstream` | service name | Override the upstream name used in proxy config |

`DR_DEFAULT_PROVIDER` can set the provider default for a controller image. `malico/docker-release-nginx` sets it to `nginx`, so managed app services only need `release.enable: "true"` unless they need an override.

### Strategy Labels

| Label | Default | Description |
|---|---|---|
| `release.bg.soak_time` | `5m` | Blue/green: how long to keep old containers alive after traffic shifts |
| `release.bg.green_weight` | `50` | Blue/green: traffic weight on new containers during soak |
| `release.canary.start_percentage` | `10` | Canary: initial traffic weight on new containers |
| `release.canary.step` | `20` | Canary: weight added each interval |
| `release.canary.interval` | `2m` | Canary: time between weight increases |

### Provider Labels

| Label | Default | Description |
|---|---|---|
| `release.nginx.service` | auto-detected | Compose service name of Nginx in this stack |
| `release.nginx.config_dir` | `/shared/nginx-config` | Shared volume path for Nginx upstream files |
| `release.nginx.route_dir` | `/shared/nginx-routes` | Bundled Nginx route snippet path |
| `release.nginx.host` | empty | Bundled Nginx hostname for this service |
| `release.nginx.path` | empty | Bundled Nginx route path for this service |
| `release.nginx.ssl.cert` | empty | Mounted TLS certificate path for this service |
| `release.nginx.ssl.key` | empty | Mounted TLS key path for this service |
| `release.nginx.ssl.redirect` | `false` | Redirect HTTP to HTTPS for this service |
| `release.angie.service` | auto-detected | Compose service name of Angie |
| `release.angie.config_dir` | `/shared/angie-config` | Shared volume path for Angie upstream files |
| `release.caddy.service` | auto-detected | Compose service name of Caddy |
| `release.caddy.config_dir` | `/shared/caddy-config` | Shared volume path for Caddy snippet files |
| `release.haproxy.service` | auto-detected | Compose service name of HAProxy |
| `release.haproxy.config_dir` | `/shared/haproxy-config` | Shared volume path for HAProxy backend files |
| `release.traefik.config_dir` | `/shared/traefik-config` | Shared volume path for Traefik dynamic config files |
| `release.nginx_proxy.config_dir` | `/shared/nginx-tmpl` | Shared volume path for the nginx-proxy template |

### Bundled Nginx Env

| Env var | Default | Description |
|---|---|---|
| `DR_NGINX_SKIP_CONFIG` | off | Use a fully mounted `/etc/nginx/nginx.conf` instead of generated config |

## Health Checks

Every managed service needs a Docker health check. `docker-release` waits for `healthy` before it sends traffic to a new container. Without a health check, Docker never reports `healthy` and the deploy stalls.

```yaml
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost/health"]
  interval: 10s
  timeout: 5s
  retries: 3
```

Use any check that proves the container can serve work — a health endpoint, a TCP check, or a CLI command.

## Rollback State

By default, rollback state lives inside the `docker-release` container and is lost if it restarts. Mount a volume to persist it:

```yaml
services:
  docker-release:
    volumes:
      - docker-release-state:/var/lib/docker-release

volumes:
  docker-release-state:
```

## Global Mode

Run one `docker-release` instance as shared infra for all projects on a server.

→ [docs/global.md](./global.md)

## Provider Guides

Each guide is self-contained. Open the one for your proxy and follow it end to end.

| Proxy | Guide |
|---|---|
| nginx-proxy | [providers/nginx-proxy.md](./providers/nginx-proxy.md) |
| Nginx | [providers/nginx.md](./providers/nginx.md) |
| Caddy | [providers/caddy.md](./providers/caddy.md) |
| Traefik | [providers/traefik.md](./providers/traefik.md) |
| Angie | [providers/angie.md](./providers/angie.md) |
| HAProxy | [providers/haproxy.md](./providers/haproxy.md) |
| No proxy | [providers/none.md](./providers/none.md) |
