# Use your own proxy

Most people should start with a [bundled image](bundled-images.md). It runs the proxy and docker-release in one container.

Use the controller-only image, `malico/docker-release`, when:

- You already run a proxy and want to keep it.
- You want one controller for many projects on a server.
- You deploy workers that have no proxy.

The controller does not run a proxy. It updates the one you already have. Release checks, the preview address, and the preview link need a bundle, because they read the proxy's access log.

## Your proxy

Pick the guide for the proxy you run. Each one has a Compose file.

| Setup | Guide |
|---|---|
| nginx-proxy | [providers/nginx-proxy.md](providers/nginx-proxy.md) |
| Nginx | [providers/nginx.md](providers/nginx.md) |
| Caddy | [providers/caddy.md](providers/caddy.md) |
| Traefik | [providers/traefik.md](providers/traefik.md) |
| Angie | [providers/angie.md](providers/angie.md) |
| HAProxy | [providers/haproxy.md](providers/haproxy.md) |

## Many projects

To run one controller for every project on a server, see [Global mode](global.md).

## Workers

For services that get no web traffic, see [No proxy](providers/none.md).
