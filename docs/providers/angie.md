# Angie Provider

Use this when your app runs behind Angie.

`docker-release` writes upstream files to a shared volume and reloads Angie after each deploy. You write the `server` block and `location` rules — `docker-release` only manages the `upstream` blocks.

Angie is API-compatible with Nginx but adds native sticky cookie support, which makes it the right choice when you need `release.affinity: cookie`.

## What Gets Written

For a service named `app`, `docker-release` writes:

```nginx
upstream app_upstream {
    sticky cookie _srr_a172cedcae path=/;
    server 172.18.0.4:80;
    server 172.18.0.5:80;
}
```

Your Angie config references `app_upstream` wherever you need it.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - angie-config:/shared/angie-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  angie:
    image: docker.angie.software/angie:latest
    ports:
      - "80:80"
    volumes:
      - angie-config:/etc/angie/http.d/custom:ro
      - ./angie.conf:/etc/angie/http.d/default.conf:ro
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: angie
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  angie-config:
```

## Angie Config

```nginx
include /etc/angie/http.d/custom/*.conf;

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

Note: many Angie images use `/etc/angie/http.d` instead of `/etc/nginx/conf.d`. Check your image.

## Required Labels

```yaml
release.enable: "true"
release.provider: angie
```

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Overrides

| Label | Default | Override when |
|---|---|---|
| `release.angie.service` | auto-detected | multiple Angie containers in the project |
| `release.angie.config_dir` | `/shared/angie-config` | volume mounted at a different path |

## Multiple Apps

```nginx
include /etc/angie/http.d/custom/*.conf;

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

Add `release.enable: "true"` and `release.provider: angie` to each app service.

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

- Cookie affinity uses a generated cookie name like `_srr_a172cedcae`.
- Angie uses `http.d` in many images instead of `conf.d`. Check the config dir for your image.

## Common Problems

| Problem | Fix |
|---|---|
| Angie cannot find upstream files | Check the mount path — many Angie images use `/etc/angie/http.d`, not `/etc/nginx/conf.d` |
| Reload does not run | Set `release.angie.service` to your Angie Compose service name |
| Sticky sessions not working | Set `release.affinity: cookie` |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
