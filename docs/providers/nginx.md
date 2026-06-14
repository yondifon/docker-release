# Nginx Provider

Use this when you run Nginx and control its config.

`docker-release` writes upstream files to a shared volume and reloads Nginx after each deploy. You write the `server` block and `location` rules — `docker-release` only manages the `upstream` blocks.

## What Gets Written

For a service named `app`, `docker-release` writes:

```nginx
upstream app_upstream {
    ip_hash;
    server 172.18.0.4:80;
    server 172.18.0.5:80;
}
```

Your Nginx config references `app_upstream` wherever you need it.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - nginx-config:/shared/nginx-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  nginx:
    image: nginx:alpine
    ports:
      - "80:80"
    volumes:
      - nginx-config:/etc/nginx/conf.d/custom:ro
      - ./nginx.conf:/etc/nginx/conf.d/default.conf:ro
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: nginx
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  nginx-config:
```

## Nginx Config

```nginx
include /etc/nginx/conf.d/custom/*.conf;

server {
    listen 80;

    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    location / {
        proxy_pass http://app_upstream/;
    }
}
```

## Required Labels

```yaml
release.enable: "true"
release.provider: nginx
```

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Overrides

| Label | Default | Override when |
|---|---|---|
| `release.nginx.service` | auto-detected | multiple Nginx containers in the project |
| `release.nginx.config_dir` | `/shared/nginx-config` | volume mounted at a different path |

## Multiple Apps

```nginx
include /etc/nginx/conf.d/custom/*.conf;

server {
    listen 80;

    location /app/ {
        proxy_pass http://app_upstream/;
    }

    location /api/ {
        proxy_pass http://api_upstream/;
    }
}
```

Add `release.enable: "true"` and `release.provider: nginx` to each app service.

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
```

### Blue/Green

```yaml
release.strategy: blue-green
release.bg.soak_time: 5m
release.bg.green_weight: 50
```

## Notes

- Nginx open source does not support sticky cookies. `release.affinity: cookie` falls back to IP hashing. Use Angie if you need real cookie sticky sessions with an Nginx-compatible config format.
- If `release.nginx.service` is not set, `docker-release` auto-detects the Nginx container in the same Compose project by image name.

## Common Problems

| Problem | Fix |
|---|---|
| Nginx ignores upstream files | Check `include /etc/nginx/conf.d/custom/*.conf;` is in your config |
| Reload does not run | Set `release.nginx.service` to your Nginx Compose service name |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
