# Global Mode

Run one `docker-release` instance as shared infra for all projects on a server. You do not need to add it to every app stack.

This is the right setup when you already run shared infra — an nginx-proxy, a database, a shared network — and want deployments managed from the same place.

## How It Works

By default, `docker-release` locks to the Compose project it starts in and only manages services in that project.

Set `DR_ALL_PROJECTS=true` and it watches every Compose project on the Docker host. Services in any project with `release.enable: "true"` are managed automatically.

## Setup

### 1. Infra Stack

Create a dedicated Compose stack for your shared infra. This is where `docker-release` and nginx-proxy live.

```yaml
# infra/docker-compose.yml
name: infra

networks:
  web:
    name: shared-web

volumes:
  nginx-tmpl:

services:
  docker-release:
    image: malico/docker-release:latest
    environment:
      DR_ALL_PROJECTS: "true"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - nginx-tmpl:/shared/nginx-tmpl:rw
      - docker-release-state:/var/lib/docker-release
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
    networks: [web]

volumes:
  nginx-tmpl:
  docker-release-state:
```

Start this once. It runs permanently on the server.

```sh
docker compose -f infra/docker-compose.yml up -d
```

### 2. App Stacks

Each app stack joins the shared network and labels its services normally. No `docker-release` service needed.

```yaml
# myapp/docker-compose.yml
name: myapp

networks:
  web:
    external: true
    name: shared-web

services:
  app:
    image: your-registry/app:latest
    networks: [web]
    environment:
      VIRTUAL_HOST: app.example.com
    labels:
      release.enable: "true"
      release.provider: nginx-proxy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      retries: 3
```

```sh
docker compose -f myapp/docker-compose.yml up -d
```

## Deploying

In global mode, CLI commands require `--project` to identify which project to target.

```sh
# deploy
docker compose -f infra/docker-compose.yml exec docker-release dr release app --project myapp

# status
docker compose -f infra/docker-compose.yml exec docker-release dr status --project myapp

# rollback
docker compose -f infra/docker-compose.yml exec docker-release dr rollback app --project myapp
```

The `--project` value is the `name:` field from your app's `docker-compose.yml`.

## Multiple Projects, Same Service Name

Two projects can both have a service named `app`. `docker-release` tracks them as `alpha/app` and `beta/app` internally — separate state, separate deploy lock, separate proxy config. No collision.

```yaml
# project-alpha/docker-compose.yml
name: alpha
services:
  app:  # tracked as alpha/app
    labels:
      release.enable: "true"
      release.provider: nginx-proxy
    environment:
      VIRTUAL_HOST: alpha.example.com

# project-beta/docker-compose.yml
name: beta
services:
  app:  # tracked as beta/app
    labels:
      release.enable: "true"
      release.provider: nginx-proxy
    environment:
      VIRTUAL_HOST: beta.example.com
```

```sh
dr release app --project alpha   # deploys alpha/app only
dr release app --project beta    # deploys beta/app only
```

## Project Name Collision

Two Compose stacks with the same `name:` value cannot coexist on one Docker host — both claim the same container names (e.g. `alpha-app-1`). This is a Docker Compose constraint. Give each project a distinct `name:` field.

## Supported Providers

Global mode supports `nginx-proxy` and `none` (workers).

`nginx-proxy` is the natural fit: it routes by `VIRTUAL_HOST`, which is already unique per hostname, so no per-project namespacing is needed. The infra stack example above uses it for this reason.

The file-based providers (`nginx`, `angie`, `caddy`, `haproxy`, `traefik`) are **not** supported in global mode. Each derives its config filename and upstream name from the bare service name, so two projects with the same service name would collide. The controller logs an error and skips these providers when running with `DR_ALL_PROJECTS=true`. Use a dedicated per-project `docker-release` instance for them instead.
