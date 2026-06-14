# docker-release

Zero-downtime deploys for Docker Compose. Starts new containers, waits for health checks, updates your proxy, stops old containers.

## How It Works

1. You push a new image and run `docker release <service>`
2. `docker-release` starts new containers and waits for them to pass health checks
3. It updates your proxy config and drains the old containers

Your proxy still serves all traffic. `docker-release` only manages the container lifecycle and proxy config.

## Pick Your Proxy

| Proxy | Guide |
|---|---|
| nginx-proxy | [docs/providers/nginx-proxy.md](docs/providers/nginx-proxy.md) |
| Nginx | [docs/providers/nginx.md](docs/providers/nginx.md) |
| Caddy | [docs/providers/caddy.md](docs/providers/caddy.md) |
| Traefik | [docs/providers/traefik.md](docs/providers/traefik.md) |
| Angie | [docs/providers/angie.md](docs/providers/angie.md) |
| HAProxy | [docs/providers/haproxy.md](docs/providers/haproxy.md) |
| No proxy (workers) | [docs/providers/none.md](docs/providers/none.md) |

## Quick Start

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - nginx-tmpl:/shared/nginx-tmpl:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  nginx-proxy:
    image: nginxproxy/nginx-proxy:alpine
    ports:
      - "80:80"
    volumes:
      - /var/run/docker.sock:/tmp/docker.sock:ro
      - nginx-tmpl:/app/custom:ro
    environment:
      NGINX_TMPL: /app/custom/nginx.tmpl
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    environment:
      VIRTUAL_HOST: app.example.com
    labels:
      release.enable: "true"
      release.provider: nginx-proxy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      retries: 3

volumes:
  nginx-tmpl:
```

```sh
docker compose up -d
docker release app
```

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

## Global Mode

Run one `docker-release` instance as shared infra across all projects on a server — no need to add it to every app stack.

→ [docs/global.md](docs/global.md)

## Full Docs

→ [docs/readme.md](docs/readme.md)
