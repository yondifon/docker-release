# docker-release

Zero-downtime deploys for Docker Compose. Starts new containers, waits for health checks, updates your proxy, stops old containers.

## How It Works

1. You push a new image and run `docker release <service>`
2. `docker-release` starts new containers and waits for them to pass health checks
3. It points the proxy at the new containers and drains the old ones

The proxy serves all traffic. `docker-release` only manages the container lifecycle and proxy config.

## Quick Start

Add one `docker-release` service to your Compose file. It runs Nginx and the release controller in one container. Your app gets two labels and a health check.

```yaml
services:
  docker-release:
    image: malico/docker-release-nginx:latest
    ports:
      - "80:80"
    stop_grace_period: 90s
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - .:${PWD}:ro
      - docker-release-state:/var/lib/docker-release

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.host: app.localhost
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  docker-release-state:
```

Start it, [install the CLI](#install-cli), and deploy:

```sh
docker compose up -d
docker release app
```

Without the CLI, run `docker compose exec docker-release dr release app`.

Open `http://app.localhost/`. Browsers send `*.localhost` to your own machine. If port 80 is taken, change the first number in `"80:80"` and use that port in the address. On a server, set `release.host` to your domain and point its DNS at the server.

- Your app needs a Docker health check. A new container gets traffic only after it is healthy.
- Requests go to your app's lowest exposed port, or port 80 if it exposes none. Do not publish the app's ports.
- Keep the project mount, the third volume. [What it does](docs/bundled-images.md#project-mount).
- The state volume keeps rollback history when you recreate the service.
- In production, pin a version such as `:0.2.0` instead of `latest`.

Labels, TLS, and the other options: [Bundled proxy images](docs/bundled-images.md).

## Pick Your Proxy

Every bundle uses the same Compose service and labels. To change proxy, change the image.

| Image | Good for |
|---|---|
| `malico/docker-release-nginx` | The usual choice. Plain Nginx |
| `malico/docker-release-caddy` | Automatic HTTPS with free certificates |
| `malico/docker-release-traefik` | No proxy reload on release |
| `malico/docker-release-haproxy` | No proxy reload on release; sticky cookies |
| `malico/docker-release-angie` | Nginx-compatible, with sticky cookies |

Per-proxy details are in the [provider guides](docs/readme.md#provider-guides).

## Install CLI

```sh
curl -fsSL https://raw.githubusercontent.com/yondifon/docker-release/main/scripts/docker-release \
  | sudo tee ~/.docker/cli-plugins/docker-release >/dev/null \
  && sudo chmod +x ~/.docker/cli-plugins/docker-release
```

## Commands

```sh
docker release app                     # deploy
docker release app --force             # deploy even if one is already running
docker release rollback app            # roll back (note: rollback comes before the service name)
docker release status                  # show all services
docker release status app              # show one service
```

## Already Run a Proxy?

Use the controller-only image, `malico/docker-release`, when you already run nginx-proxy, Nginx, Angie, Caddy, HAProxy, or Traefik yourself, or when you need one controller for many projects. It also deploys workers that have no proxy.

| Setup | Guide |
|---|---|
| nginx-proxy | [docs/providers/nginx-proxy.md](docs/providers/nginx-proxy.md) |
| Nginx | [docs/providers/nginx.md](docs/providers/nginx.md) |
| Caddy | [docs/providers/caddy.md](docs/providers/caddy.md) |
| Traefik | [docs/providers/traefik.md](docs/providers/traefik.md) |
| Angie | [docs/providers/angie.md](docs/providers/angie.md) |
| HAProxy | [docs/providers/haproxy.md](docs/providers/haproxy.md) |
| No proxy (workers) | [docs/providers/none.md](docs/providers/none.md) |
| One controller for all projects | [docs/global.md](docs/global.md) |

## Full Docs

→ [docs/readme.md](docs/readme.md)
