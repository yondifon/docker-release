# docker-release

Deploy Docker Compose services with no planned downtime.

`docker-release` runs as a small controller in your Compose stack. It starts new containers, waits for health checks, updates your proxy, then stops old containers.

It does not handle traffic. Your proxy still handles traffic.

## Start Here

Pick your proxy:

| Proxy | Guide |
|---|---|
| Nginx | [docs/providers/nginx.md](docs/providers/nginx.md) |
| Angie | [docs/providers/angie.md](docs/providers/angie.md) |
| Caddy | [docs/providers/caddy.md](docs/providers/caddy.md) |
| HAProxy | [docs/providers/haproxy.md](docs/providers/haproxy.md) |
| Traefik | [docs/providers/traefik.md](docs/providers/traefik.md) |
| nginx-proxy | [docs/providers/nginx-proxy.md](docs/providers/nginx-proxy.md) |
| No proxy | [docs/providers/none.md](docs/providers/none.md) |

## Quick Example

This is the smallest Nginx example.

```yaml
services:
  docker-release:
    image: malico/docker-release:0.1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - nginx-config:/shared/nginx-config:rw # docker-release writes Nginx upstream files here

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
    volumes:
      - nginx-config:/etc/nginx/conf.d/custom:ro # Nginx reads generated upstream files here
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro # Your base Nginx routes
    depends_on:
      docker-release:
        condition: service_healthy # wait until upstream files are written

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: nginx
      release.nginx.service: nginx
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  nginx-config:
```

`nginx.conf`:

```nginx
include /etc/nginx/conf.d/custom/*.conf;

server {
    listen 80;

    location / {
        proxy_pass http://app_upstream/;
    }
}
```

## Install CLI

```sh
curl -fsSL https://raw.githubusercontent.com/yondifon/docker-release/main/scripts/docker-release \
  | sudo tee ~/.docker/cli-plugins/docker-release >/dev/null \
  && sudo chmod +x ~/.docker/cli-plugins/docker-release
```

## Deploy

```sh
docker compose up -d
docker release app
```

## Commands

```sh
docker release app           # deploy app
docker release app --force   # deploy even if one is running
docker release status        # show all services
docker release status app    # show one service
docker release rollback app  # roll back app
```

## More Docs

- [Main docs](docs/readme.md)
- [Nginx guide](docs/providers/nginx.md)
- [Caddy guide](docs/providers/caddy.md)
- [Traefik guide](docs/providers/traefik.md)
- [HAProxy guide](docs/providers/haproxy.md)

## Local Development

```sh
make dev         # install local CLI plugin
make dev-remove  # remove local CLI plugin
make test        # run Go tests
```
