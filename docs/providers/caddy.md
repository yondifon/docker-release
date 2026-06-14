# Caddy Provider

Use this when your app runs behind Caddy.

`docker-release` writes named snippet files to a shared volume and reloads Caddy after each deploy. You write the `Caddyfile` and control routes, headers, and auth — `docker-release` only manages the `reverse_proxy` backend list inside a named snippet.

## What Gets Written

For a service named `app`, `docker-release` writes:

```caddy
(app_upstream) {
    reverse_proxy 172.18.0.4:80 172.18.0.5:80 {
        lb_policy cookie _srr_a172cedcae
    }
}
```

Your Caddyfile imports this snippet and uses it wherever needed.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - caddy-config:/shared/caddy-config:rw
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  caddy:
    image: caddy:alpine
    ports:
      - "80:80"
    volumes:
      - caddy-config:/etc/caddy/conf.d:ro
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
    depends_on:
      docker-release:
        condition: service_healthy

  app:
    image: your-registry/app:latest
    labels:
      release.enable: "true"
      release.provider: caddy
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/health"]
      interval: 10s
      timeout: 5s
      retries: 3

volumes:
  caddy-config:
```

## Caddyfile

```caddy
import /etc/caddy/conf.d/*.caddy

example.com {
    import app_upstream
}
```

For local testing, replace `example.com` with `:80`.

## Required Labels

```yaml
release.enable: "true"
release.provider: caddy
```

## Deploy

```sh
docker compose up -d
docker release app
```

## Optional Overrides

| Label | Default | Override when |
|---|---|---|
| `release.caddy.service` | auto-detected | multiple Caddy containers in the project |
| `release.caddy.config_dir` | `/shared/caddy-config` | volume mounted at a different path |

## Path Routing

Use `handle_path` to route by URL path. Caddy strips the prefix before forwarding.

```caddy
import /etc/caddy/conf.d/*.caddy

example.com {
    handle_path /app/* {
        import app_upstream
    }

    handle_path /api/* {
        import api_upstream
    }
}
```

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
- `docker-release` always writes a named snippet. You control where and how it is imported in your Caddyfile.

## Common Problems

| Problem | Fix |
|---|---|
| Caddy does not route to the app | Check `import /etc/caddy/conf.d/*.caddy` is at the top of your Caddyfile |
| Reload does not run | Set `release.caddy.service` to your Caddy Compose service name |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
