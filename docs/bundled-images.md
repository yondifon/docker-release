# Bundled proxy images

Run a proxy and docker-release in one Compose service. Add routing labels to your apps; you do not need a proxy config file or a shared config volume.

## Choose an image

| Proxy | Image |
|---|---|
| Nginx | `malico/docker-release-nginx:<version>` |
| Angie | `malico/docker-release-angie:<version>` |
| Caddy | `malico/docker-release-caddy:<version>` |

Use a release version in production. `latest` follows the newest release. Images support `linux/amd64` and `linux/arm64`.

HAProxy and Traefik bundles will use the same setup and image naming, but are not available yet.

## Add the service

Copy this service and volume into your Compose file. Add the two labels to an app that has a Docker health check:

```yaml
services:
  docker-release:
    image: malico/docker-release-angie:latest
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
      release.host: app.example.com
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  docker-release-state:
```

Run `docker compose up -d`. Point the hostname at your server. Requests go to your app's lowest exposed port, or port 80 if none is exposed. Expose only its HTTP port. The bundle and apps must share a Docker network. Do not publish app ports just for routing.

The Docker socket lets docker-release manage containers and grants control of the Docker host. Use bundles only with trusted services. The state volume keeps rollback history when you recreate the bundle. In Caddy, it also keeps certificates and the local certificate authority. Keep this volume when upgrading. Generated proxy files stay inside the bundle and are recreated from app labels.

No environment variables, `release.provider` label, proxy service name, or health check override is needed. The image supplies its own health check. It becomes healthy when routing is ready and the proxy accepts HTTP requests.

For later releases, install the [host command](../README.md#install-cli), then run `docker release app`. Keep a health check that proves each app can serve requests.

Caddy app releases keep existing client connections open. Changes to hostnames, routes, or TLS settings can close idle connections; apply those changes during a maintenance window.

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
| `release.affinity` | `ip` | Keep each client on one backend; use `cookie` for Angie or Caddy sticky cookies or `""` to turn this off |
| `release.health_check_timeout` | `60s` | How long a replacement has to become healthy |
| `release.drain_timeout` | `10s` | Time allowed for requests to finish before an old container stops |

Give each app a different host. For several paths on one host, mount your own proxy config instead. Without a host, a path adds a route to the default HTTP server; `/health` is reserved. For example, `/app/orders` at path `/app/` reaches your app as `/orders`.

## Add TLS

Nginx and Angie do not obtain or renew certificates for you. Use your certificate tool, mount its output read-only, and publish port 443. This setup also works with Caddy:

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

Add these entries to the complete setup above; keep its socket and state mounts and app labels. TLS requires `release.host`. After renewing a certificate, run `docker release app` to apply it. If TLS ends at a proxy in front of the bundle, leave these labels unset and use HTTP between the proxies.

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

If apps explicitly set `release.provider`, remove that label or change it to the new proxy. Custom proxy config and provider-specific features are not portable; check the [Nginx](providers/nginx.md), [Angie](providers/angie.md), or [Caddy](providers/caddy.md) page before switching.

Existing Nginx labels remain aliases: `release.nginx.host`, `release.nginx.path`, and `release.nginx.ssl.cert`, `.key`, and `.redirect`. The shared label wins when both names are set. These aliases also work in the Angie and Caddy bundles.

`DR_NGINX_SKIP_CONFIG` remains an alias for `DR_PROXY_SKIP_CONFIG` in the Nginx bundle. `DR_PROXY_SKIP_CONFIG` wins when both variables are set. Existing Nginx mounts and config paths still work.
