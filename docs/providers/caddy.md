# Caddy Provider

Use this when your app runs behind Caddy.

Start with the bundle: Caddy and `docker-release` in one service, with routes generated from app labels. Run Caddy yourself only when you already do, or need to control its whole config.

## Bundled image

Use `malico/docker-release-caddy:<version>` in place of the image in the [quick start](../../README.md#quick-start). The [shared bundle setup](../bundled-images.md) lists the labels. You do not need `release.provider`, a Caddyfile, or a shared config volume.

HTTP works with the shared setup. To add automatic HTTPS, publish `443:443`, point your public hostname at the server, and allow inbound ports 80 and 443. Caddy obtains and renews certificates for `release.host`. HTTP remains available without a redirect. Mounted certificates and `release.tls.redirect` work as described in the shared setup.

Mount a named volume at `/var/lib/docker-release` ([Keep state](../bundled-images.md#keep-state)). Caddy stores certificates and its local CA under `/var/lib/docker-release/caddy`. Deleting the volume loses them as well as rollback history.

Do not publish Caddy's admin port, 2019.

For local names such as `linear.localhost`, Caddy uses its local CA. Export its root certificate from the test fixture and trust it only for that request:

```sh
docker compose -f tests/caddy-bundled/docker-compose.yml up --build -d --wait
mkdir -p .oga_tmp
docker compose -f tests/caddy-bundled/docker-compose.yml cp \
  docker-release:/var/lib/docker-release/caddy/pki/authorities/local/root.crt .oga_tmp/caddy-root.crt
curl --cacert .oga_tmp/caddy-root.crt \
  --resolve linear.localhost:8444:127.0.0.1 https://linear.localhost:8444/
docker compose -f tests/caddy-bundled/docker-compose.yml down -v
rm .oga_tmp/caddy-root.crt
```

For a custom Caddyfile, set `DR_PROXY_SKIP_CONFIG=true` and mount it at `/etc/caddy/Caddyfile:ro`. Keep the admin API at `127.0.0.1:2019` and serve HTTP 200 at `http://127.0.0.1/health`.

Import `/shared/caddy-config/*.caddy` globally. To keep hostless path labels working, import `/shared/caddy-config/*.location` inside your default HTTP site.

To write an app's routes yourself, omit its `release.host` and `release.path` labels. Import its `<service>_upstream` snippet where needed.

The bundle uses Caddy's default backend connection pool size. Set `release.caddy.keepalive` to override it, or `0` to turn backend keep-alive off.

## Separate Caddy service

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

## Required labels for a separate proxy

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

## Preview address

Set `release.preview.host` on a blue/green app to try the new version on its own host before users move; see [Preview address](../readme.md#preview-address).

In the bundle, the route is added for you, and Caddy gets the certificate for the preview host by itself, as it does for the main host.

With a separate Caddy, `docker-release` also writes a snippet `(app_preview_upstream)` into the same file as `(app_upstream)`. During a release it sends requests to the new containers only. The rest of the time it answers 404. Import it in a site block of its own:

```caddy
next.app.example.com {
    import app_preview_upstream
}
```

In the bundle you can also set `release.preview.token`, so one browser sees the new version on the normal host; see [Preview link](../readme.md#preview-link). With a separate Caddy, `release.preview.token` is refused.

## Notes

- Cookie affinity uses a generated cookie name like `_srr_a172cedcae`.
- With a separate Caddy service, `docker-release` writes a named snippet. You control where and how it is imported in your Caddyfile. The bundle also adds routes from the shared labels.

## Common Problems

| Problem | Fix |
|---|---|
| Caddy does not route to the app | Check `import /etc/caddy/conf.d/*.caddy` is at the top of your Caddyfile |
| Reload does not run | Set `release.caddy.service` to your Caddy Compose service name |
| Rollback state lost after `docker compose down` | Mount `/var/lib/docker-release` to a named volume |
| Bundled certificates lost after `docker compose down` | Mount `/var/lib/docker-release` to a named volume and keep it when recreating the bundle |
| Local HTTPS certificate is not trusted | Export Caddy's local CA root and pass it to your client; do not disable certificate checks |
