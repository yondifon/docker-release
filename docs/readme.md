# docker-release Docs

## Concepts

`docker-release` runs as a service inside your Compose stack, alone or bundled with a proxy. It connects to the Docker socket and watches for deploy commands.

When you run `docker release app`, it:

1. Starts new containers from the current image
2. Waits for each new container to pass its Docker health check
3. Adds new containers to your proxy config and removes old ones
4. Drains old containers (waits for in-flight requests to finish)
5. Stops old containers

It never touches traffic directly. The proxy serves all requests: the one in your bundle, or one you run yourself (nginx-proxy, Nginx, Caddy, Traefik, HAProxy, or Angie).

## Images

Start with a bundle: one `docker-release` service that runs your proxy and the controller together. Add `release.host` to an app and it is routed. Setup, labels, and TLS: [Bundled proxy images](bundled-images.md). The [README quick start](../README.md#quick-start) has a working file.

| Image | Use |
|---|---|
| `malico/docker-release-nginx` | Nginx + controller |
| `malico/docker-release-angie` | Angie + controller |
| `malico/docker-release-caddy` | Caddy + controller, automatic HTTPS |
| `malico/docker-release-haproxy` | HAProxy + controller |
| `malico/docker-release-traefik` | Traefik + controller |
| `malico/docker-release` | Controller only; for a proxy you already run, [global mode](global.md), or workers |

All images support the same `docker release` commands.

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

#### Release checks

A container that passes its Docker health check can still fail at a task such as sign-up. Add `release.check.*` labels to say what a bad release means for your app. `docker-release` then acts before and after users move to the new version.

```yaml
release.check.command: "./smoke.sh"       # must pass before users move
release.check.service: smoke              # optional: run it in this Compose service instead
release.check.timeout: 2m
release.check.fail_on: "5xx"              # statuses that count as errors, e.g. "5xx" or "502,504"
release.check.paths: "/signup,/checkout"  # optional: count only paths that start with these
release.check.max_errors: 1               # errors allowed in the window
release.check.window: 5m                  # how long to watch after users move
```

**Before users move.** Once the new containers are healthy, the command runs once for each of them. It runs inside the new container, or, with `release.check.service`, in a one-off container of that Compose service on the same network. Create that service's container first with `docker compose create smoke`. The command runs with `sh -c` and gets two variables:

| Variable | Value |
|---|---|
| `RELEASE_TARGET` | URL of the one new container being checked, such as `http://172.18.0.5:80` |
| `RELEASE_HOST` | The route host from `release.host`, if you set one |

Exit code 0 means go on. Any other exit code, or running past `release.check.timeout`, removes the new containers. Users stay on the old version, `docker release` exits with an error that includes the command's output, and `docker release status` shows the same output.

**After users move.** When any of `fail_on`, `paths`, `max_errors`, or `window` is set, all users move to the new containers at once, and the old ones stay up with no traffic. `release.bg.green_weight` and `release.bg.soak_time` do not apply. For `window`, the proxy's access log is read, and every response from a new container that matches `fail_on` and `paths` counts as an error. When errors pass `max_errors`, users go back to the old containers at once and the new ones are removed. If the window ends with no more errors than that, the old containers are removed.

Defaults: `fail_on` is `5xx`, `max_errors` is `0` (the first error rolls back), `window` is `5m`, and `timeout` is `2m`. Setting any one of these four labels turns the watch on.

The error rules need a [bundled proxy image](bundled-images.md), because only a bundle lets `docker-release` read the access log. With a separate proxy, `release.check.command` still works, and the four error labels are rejected with a message when you start the release. Without any `release.check.*` label, blue/green works as before. These labels work only with `release.strategy: blue-green`.

In a Compose file, write `$$RELEASE_TARGET` so Compose does not fill in the variable itself.

#### Preview address

Give the new version an address of its own, so you or your tests can try it, sign-up included, before users move:

```yaml
release.host: app.example.com
release.preview.host: next.app.example.com
```

During a blue/green release, `next.app.example.com` shows only the new version, and `app.example.com` keeps showing the old one. The preview address starts working once the new containers are healthy. It stops working, and answers 404, as soon as users move to the new version or the release fails.

If you set `release.check.command`, it runs while the preview address works, and gets one more variable:

| Variable | Value |
|---|---|
| `RELEASE_PREVIEW_URL` | The preview address, such as `https://next.app.example.com`. It starts with `https://` when `release.tls.cert` is set, and with `http://` otherwise. |
| `RELEASE_PREVIEW_TOKEN` | The preview token, when `release.preview.token` is set. See [Preview link](#preview-link). |

Before you use it:

- Point the preview host's DNS at the proxy, as you do for `release.host`.
- Use the same certificate settings as the main host. The certificate must also cover the preview host, for example with a wildcard certificate. Caddy gets its certificate by itself.
- `release.preview.host` is one hostname. It cannot be a wildcard, and it must differ from `release.host` and from every other service's hosts.
- It works only with `release.strategy: blue-green`. With the `nginx-proxy` provider it is refused, because that provider reads hosts from `VIRTUAL_HOST`.
- Anyone who knows the preview address can open it while it works. To lock it, add a [preview token](#preview-link).

With a [bundled proxy image](bundled-images.md), the route is added for you. With a separate proxy, `docker-release` writes an upstream for the preview, and you add one rule that uses it; see your proxy's page: [Nginx](providers/nginx.md#preview-address), [Angie](providers/angie.md#preview-address), [Caddy](providers/caddy.md#preview-address), [HAProxy](providers/haproxy.md#preview-address), [Traefik](providers/traefik.md#preview-address).

#### Preview link

Try the new version on your real address, with your real certificate and cookies, and with no extra DNS. Only people who have the link see it; everyone else keeps seeing the old version.

```yaml
release.host: app.example.com
release.preview.token: auto
```

While a release waits, `docker release status app` prints the link:

```
Preview:    https://app.example.com/?release-preview=<token>
```

- Open the link once. It sets a cookie and takes you to the same page without the token in the address. From then on, that browser sees the new version on `app.example.com`.
- A script sends the header `X-Release-Preview: <token>` with each request instead.
- A wrong or missing token shows the old version. It never shows an error page.
- With `auto`, every release gets a new random token. The link, the cookie, and the header stop working when users move to the new version or the release fails. After that, a browser that still has the cookie sees the live version.
- To use a token of your own, for example one from your environment, write `release.preview.token: ${PREVIEW_TOKEN}`. It must be 16 to 128 letters, digits, `-` or `_`. It works only while a new version waits, like an `auto` token.
- The link keeps any other query parameters on the page you open. The cookie has `HttpOnly` and `SameSite=Lax`, and `Secure` when `release.tls.cert` is set.
- With `release.preview.host` also set, the preview address needs the token too: the link, the cookie, or the header. Without the token label, the preview address is open as before.
- `release.check.command` gets the token as `RELEASE_PREVIEW_TOKEN`. A check can send it as the header to test the new version on the real host.
- Treat the token like a password for the new version. `docker release status` shows it. The API shows it only to a caller who sends `DR_API_TOKEN`, and the web page never shows it. It is not written to the logs. Anyone who can read the shared volume can read it.
- It works only with a [bundled proxy image](bundled-images.md). With a separate proxy, or with `nginx-proxy`, the release is refused with a message. Use `release.preview.host` there.

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
| `ip` (default) | Routes by client IP, except Traefik uses sticky cookies for an unweighted service and HRW for weighted groups. |
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

`DR_DEFAULT_PROVIDER` sets the provider default. Bundles set it to their proxy; add `release.host` or `release.path` to route requests to an app. See [Bundled proxy images](bundled-images.md) for the shared labels, defaults, TLS, and environment variables. Existing `release.nginx.*` routing labels remain aliases.

### Strategy Labels

| Label | Default | Description |
|---|---|---|
| `release.bg.soak_time` | `5m` | Blue/green: how long to keep old containers alive after traffic shifts |
| `release.bg.green_weight` | `50` | Blue/green: traffic weight on new containers during soak |
| `release.check.command` | empty | Blue/green: a command that must exit 0 before users move; see [Release checks](#release-checks) |
| `release.check.service` | empty | Blue/green: run the command in a one-off container of this Compose service |
| `release.check.timeout` | `2m` | Blue/green: how long the command may run |
| `release.check.fail_on` | `5xx` | Blue/green, bundled images: statuses that count as errors |
| `release.check.paths` | all paths | Blue/green, bundled images: count only paths that start with one of these |
| `release.check.max_errors` | `0` | Blue/green, bundled images: errors allowed in the window |
| `release.check.window` | `5m` | Blue/green, bundled images: how long to watch after users move |
| `release.preview.host` | empty | Blue/green: a host that shows only the new version until users move; see [Preview address](#preview-address) |
| `release.preview.token` | empty | Blue/green, bundled images: `auto`, or a token of your own. A link with it shows the new version on the normal host to one browser; see [Preview link](#preview-link) |
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

Rollback history lives in `/var/lib/docker-release`. The image gives that folder its own volume, so it survives restarts and image upgrades. To keep it through `docker compose down` too, mount a named volume there:

```yaml
services:
  docker-release:
    volumes:
      - docker-release-state:/var/lib/docker-release

volumes:
  docker-release-state:
```

## Global Mode

Run one controller-only `docker-release` instance with nginx-proxy as shared infra for all projects on a server. Bundles manage only their own Compose project.

→ [docs/global.md](./global.md)

## Provider Guides

Each guide opens with the bundled image for that proxy, then covers running the proxy as a separate service. Open the one for your proxy and follow it end to end.

| Proxy | Guide |
|---|---|
| Nginx | [providers/nginx.md](./providers/nginx.md) |
| Caddy | [providers/caddy.md](./providers/caddy.md) |
| Traefik | [providers/traefik.md](./providers/traefik.md) |
| Angie | [providers/angie.md](./providers/angie.md) |
| HAProxy | [providers/haproxy.md](./providers/haproxy.md) |
| nginx-proxy (separate service only) | [providers/nginx-proxy.md](./providers/nginx-proxy.md) |
| No proxy | [providers/none.md](./providers/none.md) |
