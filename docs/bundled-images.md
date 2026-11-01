# Bundled proxy images

Run a proxy and docker-release in one Compose service. Add routing labels to your apps; you do not need a proxy config file or a shared config volume.

## Choose an image

| Proxy | Image |
|---|---|
| Nginx | `malico/docker-release-nginx:<version>` |
| Angie | `malico/docker-release-angie:<version>` |
| Caddy | `malico/docker-release-caddy:<version>` |
| HAProxy | `malico/docker-release-haproxy:<version>` |
| Traefik | `malico/docker-release-traefik:<version>` |

Use a release version in production, such as `:0.2.0`. `latest` follows the newest release. Images support `linux/amd64` and `linux/arm64`.

## Add the service

The [quick start](../README.md#quick-start) has the complete Compose file. To use another proxy, change its `image` to a row in the table above.

Run `docker compose up -d`. Point the hostname at your server. The bundle and apps must share a Docker network, which Compose gives them by default. Requests go to your app's lowest exposed port, or port 80 if none is exposed. Expose only its HTTP port. Do not publish app ports just for routing.

The Docker socket lets docker-release manage containers and grants control of the Docker host. Use bundles only with trusted services. Generated proxy files stay inside the bundle and are recreated from app labels.

No environment variables, `release.provider` label, proxy service name, or health check override is needed. The image supplies its own health check. It becomes healthy when routing is ready and the proxy accepts HTTP requests.

Deploy with `docker release app` from the [host command](../README.md#install-cli). Keep a health check that proves each app can serve requests.

Caddy, HAProxy, and Traefik app releases keep existing client connections open. Changes to hostnames, routes, or TLS settings can close idle connections; apply those changes during a maintenance window.

## Keep state

The bundle keeps rollback history in `/var/lib/docker-release`. Caddy also keeps its certificates and local certificate authority there. The image gives this folder its own volume, so your data survives `docker compose restart` and `docker compose up -d` after an image change.

`docker compose down` starts you over with an empty volume. To keep your data through `down`, and always with Caddy, name the volume:

```yaml
services:
  docker-release:
    volumes:
      - docker-release-state:/var/lib/docker-release

volumes:
  docker-release-state:
```

Keep this volume when you upgrade.

## Stop time

Compose waits 10 seconds for a service to stop, and an idle bundle stops well inside that. If requests can stay open longer, set `stop_grace_period: 90s` on the service so they can finish.

## Project mount

For apps with a prebuilt `image`, `docker release app` needs no project mount unless the app uses the file-backed dependencies below. The host command resolves your Compose configuration on the host and sends it to the controller over stdin, so Compose file changes apply. Run it from the project directory, or use `docker release -f /path/to/compose.yml app` from another folder. The host needs the Docker Compose plugin (`docker compose`), which is included in most Docker installations.

Update the host CLI plugin and controller image together. An existing `.:${PWD}:ro` project mount remains supported; you can keep it or remove it.

Services with only `build` and no `image` still need the existing project mount. App bind mounts and `env_file` work without a project mount. File-backed Compose configs, secrets, and custom seccomp profiles still need their files readable at matching paths inside the controller. For these dependencies, keep an existing optional project mount, or replace it with read-only mounts of only the needed files at matching paths.

Direct controller commands and API, web, or queued detached releases without readable project files copy running containers and do not pick up Compose file changes.

## Routing labels

| Label | Default | What you get |
|---|---|---|
| `release.enable` | off | Set `"true"` to manage this app |
| `release.host` | empty | A route for this hostname; comma-separated names are also accepted |
| `release.path` | `/` with a host; no route without a host | Route requests under this path; the prefix is removed before forwarding |
| `release.tls.cert` | empty | Path to a mounted PEM certificate, including its chain |
| `release.tls.key` | empty | Path to the matching PEM private key; set both certificate and key |
| `release.tls.redirect` | `false` | Set `"true"` to redirect HTTP to HTTPS with status 308 |
| `release.strategy` | `linear` | Replace containers one at a time; also accepts `blue-green` and `canary` |
| `release.affinity` | `ip` | Keep each client on one backend; use `cookie` for Angie, Caddy, HAProxy, or Traefik sticky cookies or `""` to turn this off |
| `release.health_check_timeout` | `60s` | How long a replacement has to become healthy |
| `release.drain_timeout` | `10s` | Time allowed for requests to finish before an old container stops |

Give each app a different host. For several paths on one host, mount your own proxy config instead. Without a host, a path adds a route to the default HTTP server; `/health` is reserved. For example, `/app/orders` at path `/app/` reaches your app as `/orders`.

Traefik uses sticky cookies for unweighted services even with `release.affinity: ip`; weighted groups use HRW. Its bundle uses only file-based routing, not `traefik.*` Docker labels. Other bundles keep their own affinity rules; see the provider guide before switching if your app relies on sticky sessions.

## Release checks

A bundle can watch real traffic after a blue/green release and undo it when users hit errors. This works because the proxy and the controller run in one container, so `docker-release` can read the proxy's access log. A separate proxy cannot do this.

This example moves users to a new version, then watches sign-up for five minutes. One failed sign-up is allowed; a second sends users back to the old version.

```yaml
services:
  app:
    image: myapp:2.0
    labels:
      release.enable: "true"
      release.host: app.example.com
      release.strategy: blue-green
      release.check.command: 'wget -qO- "$$RELEASE_TARGET/signup"'
      release.check.paths: /signup
      release.check.max_errors: "1"
      release.check.window: 5m
```

Before users move, the command runs inside each new container. If it fails, users never reach the new version. After they move, every 5xx response on `/signup` from a new container counts as an error. The old containers keep running, without traffic, until the window ends cleanly. See [Release checks](readme.md#release-checks) for every label.

`paths` match the path the client sent, before any `release.path` prefix is removed.

The watch reads the access log lines the bundle writes. Each line now says which app container answered; the earlier fields stay. If you replace the bundle's proxy config with your own (`DR_PROXY_SKIP_CONFIG`), keep those lines, or the watch sees no errors. HAProxy writes them only for the frontend `docker-release` generates.

## Preview address

Add `release.preview.host: next.app.example.com` to a blue/green app, and every bundle adds a route for that host during a release. It shows only the new version, and answers 404 once users move or the release fails. The route uses the same certificate settings as `release.host`, so a certificate you mount must cover the preview host too. Caddy gets its own certificate. See [Preview address](readme.md#preview-address).

To try the new version on the normal host instead, with no extra DNS, add `release.preview.token: auto`. `docker release status app` then prints a link that shows the new version to one browser. This works only in a bundled image. See [Preview link](readme.md#preview-link).

## Add TLS

Nginx, Angie, HAProxy, and Traefik do not obtain or renew certificates for you. Use your certificate tool, mount its output read-only, and publish port 443. The same mounted-certificate setup works with all five bundles:

```yaml
services:
  docker-release:
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./certs:/certs:ro

  app:
    labels:
      release.tls.cert: /certs/app/fullchain.pem
      release.tls.key: /certs/app/privkey.pem
      release.tls.redirect: "true"
```

Add these entries to the [quick start](../README.md#quick-start) file; keep its socket and state mounts and app labels. TLS requires `release.host`. After renewing a certificate, run `docker release app` to apply it. If TLS ends at a proxy in front of the bundle, leave these labels unset and use HTTP between the proxies.

### Caddy automatic HTTPS

With the Caddy image, publish `443:443` alongside `80:80` to use automatic HTTPS. Keep `release.host` and leave the certificate labels unset. For public names, point DNS at this server and allow inbound traffic on ports 80 and 443. Caddy obtains and renews certificates and stores them in the existing `docker-release-state` volume; no extra mount is needed. You can also publish `443:443/udp` for HTTP/3.

HTTP stays available without a redirect, so switching images does not change existing HTTP routes. To redirect HTTP to HTTPS, use the certificate labels above or a custom Caddyfile. Local names such as `app.localhost` use Caddy's local CA; clients must trust its root certificate. See the [Caddy provider](providers/caddy.md#bundled-image) for local testing and custom config.

## Environment variables

| Variable | Default | When to set it |
|---|---|---|
| `DR_PROXY_SKIP_CONFIG` | `false` | Use a full proxy config you mount yourself; see the provider page for its path |
| `DR_DEFAULT_PROVIDER` | The image's proxy | Only when deliberately changing the provider default; an explicit `release.provider` wins |
| `DR_BUNDLED_PROXY` | The image's proxy | Set by the image; leave it unchanged |

Keep the default project detection. A bundle manages the apps in its own Compose project; all-project mode is not supported for bundled routing.

## Switch bundles

Change only the image to another bundle in the table above and recreate that service. Keep the ports, mounts, health checks, and shared labels. For Caddy automatic HTTPS, also publish port 443. Recreating the proxy interrupts traffic; use app releases, not bundle replacement, for zero-downtime app updates.

If apps explicitly set `release.provider`, remove that label or change it to the new proxy. Custom proxy config and provider-specific features are not portable; check the [Nginx](providers/nginx.md), [Angie](providers/angie.md), [Caddy](providers/caddy.md), [HAProxy](providers/haproxy.md), or [Traefik](providers/traefik.md) page before switching.

Existing Nginx labels remain aliases in all five bundles: `release.nginx.host`, `release.nginx.path`, and `release.nginx.ssl.cert`, `.key`, and `.redirect`. The shared label wins when both names are set.

`DR_NGINX_SKIP_CONFIG` remains an alias for `DR_PROXY_SKIP_CONFIG` in the Nginx bundle. `DR_PROXY_SKIP_CONFIG` wins when both variables are set. Existing Nginx mounts and config paths still work.
