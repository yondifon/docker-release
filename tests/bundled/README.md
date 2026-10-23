# Bundle checks

Run commands from the repo root. Nginx uses HTTP port 8089; Angie uses 8090; Caddy uses 8091 and HTTPS port 8444; HAProxy uses 8092; Traefik uses 8093. All five use the same app image and shared labels.

```sh
docker compose -f tests/angie-bundled/docker-compose.yml up --build -d --wait
python3 tests/bundled/check-routing.py angie
python3 tests/bundled/check-reload.py angie
python3 tests/bundled/check-shutdown.py angie
docker compose -f tests/angie-bundled/docker-compose.yml down -v
```

For Nginx, replace `angie` with `nginx` in each command. The reload check pauses the proxy master for 12 seconds while workers keep serving requests and checks that the old `linear_app` container survives until the reload is accepted. It then checks canary and blue/green releases too. The shutdown check stops the controller with an incomplete HTTP request open and requires exit 0 within 15 seconds.

For HAProxy, replace `angie` with `haproxy` in each command. Its fixture also checks a hostless path route. App releases use the Runtime API without reloading the proxy, so the paused-master check does not apply. HAProxy must confirm backend changes before the old app container stops.

For Traefik, replace `angie` with `traefik` in each command. Its fixture also checks a hostless path route. The paused-master check does not apply: Traefik serves requests and watches file changes in one process. The bundle confirms the applied routes, services, and middleware through Traefik's loopback-only API before old app containers stop.

For Caddy, run its checks instead of the paused-master check. Caddy serves requests and applies config in one process:

```sh
docker compose -f tests/caddy-bundled/docker-compose.yml up --build -d --wait
python3 tests/caddy-bundled/check.py
python3 tests/bundled/check-shutdown.py caddy
docker compose -f tests/caddy-bundled/docker-compose.yml down -v
```

The Caddy check covers stripped paths, HTTP and trusted HTTPS requests during all three release strategies, and certificate storage across bundle recreation. It writes the test CA to `.oga_tmp/caddy-root.crt`; remove that file after checking.

All traffic checks use both new and kept-alive connections. Every successful response must contain the requested app's exact name and the stripped request path, including its query string. Wrong-app bodies count as failures and are also counted separately. Unknown hosts and paths must return 404 without an app body. The JSON result reports requests, failures, and wrong-backend counts per proxy and per traffic stream.

Failed requests are not retried. The Caddy check also checks that pending and idle connections survive an upstream update, and that app releases keep the same HTTP and HTTPS listening sockets.

## TLS

Create a one-day local certificate; no account or public DNS is needed:

```sh
mkdir -p .oga_tmp/certs
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=linear.localhost' \
  -addext 'subjectAltName=DNS:linear.localhost,IP:127.0.0.1' \
  -keyout .oga_tmp/certs/key.pem -out .oga_tmp/certs/cert.pem
docker compose -f tests/angie-bundled/docker-compose.yml \
  -f tests/bundled/tls.yml up --build -d --wait
python3 tests/bundled/check-routing.py angie --tls-port 8443 --ca .oga_tmp/certs/cert.pem
python3 tests/bundled/check-shutdown.py angie
docker compose -f tests/angie-bundled/docker-compose.yml \
  -f tests/bundled/tls.yml down -v
```

The check trusts only the supplied certificate and verifies HTTP 308 to HTTPS, certificate validation, and HTTPS 200. The mount path contains `/nginx/` so every bundle must preserve certificate paths unchanged.

The same TLS commands work with `nginx`, `haproxy`, or `traefik` in place of `angie`. All five bundles read the certificate and key separately; you do not need to combine them into one PEM file.

To check HTTP and HTTPS traffic through every HAProxy release strategy, include all three app names in the certificate's `subjectAltName`: `DNS:linear.localhost,DNS:canary.localhost,DNS:bluegreen.localhost,IP:127.0.0.1`. Then run:

```sh
docker compose -f tests/haproxy-bundled/docker-compose.yml \
  -f tests/haproxy-bundled/tls.yml up -d --wait
python3 tests/haproxy-bundled/check.py
python3 tests/bundled/check-shutdown.py haproxy
docker compose -f tests/haproxy-bundled/docker-compose.yml \
  -f tests/haproxy-bundled/tls.yml down -v
```

This also checks in-flight requests, listening sockets, and recovery after a rejected certificate.

For Traefik HTTP and HTTPS traffic during all three release strategies, use the same three-name certificate and run:

```sh
docker compose -f tests/traefik-bundled/docker-compose.yml \
  -f tests/traefik-bundled/tls.yml up --build -d --wait
python3 tests/bundled/check-reload.py traefik --tls-port 8446 --ca .oga_tmp/certs/cert.pem
python3 tests/bundled/check-shutdown.py traefik
docker compose -f tests/traefik-bundled/docker-compose.yml \
  -f tests/traefik-bundled/tls.yml down -v
```

The shared redirect overlay, `tests/bundled/tls.yml`, is only for the routing check: the reload check expects HTTP 200, not a redirect. The HAProxy and Traefik overlays above keep HTTP available while also checking HTTPS. Set `BUNDLE_TLS_PORT=8444` on Compose commands and use `--tls-port 8444` to choose another port for the shared redirect overlay.

After checking, remove the local bundle and app images you built. For Caddy: `docker image rm docker-release-caddy:local docker-release-test-app:local`. Delete the certificates and other scratch files in `.oga_tmp/` when done.
