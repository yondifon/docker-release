# Bundle checks

Run commands from the repo root. The Angie fixture uses HTTP port 8090; Nginx uses 8089. Both use the same app image, labels, and checks.

```sh
docker compose -f tests/angie-bundled/docker-compose.yml up --build -d --wait
python3 tests/bundled/check-routing.py angie
python3 tests/bundled/check-reload.py angie
python3 tests/bundled/check-shutdown.py angie
docker compose -f tests/angie-bundled/docker-compose.yml down -v
```

For Nginx, replace `angie` with `nginx` in each command. The reload check pauses the proxy master for 12 seconds while workers keep serving requests, releases `linear_app`, and checks that its old container survives until the reload is accepted. The shutdown check stops the controller with an incomplete HTTP request open and requires exit 0 within 15 seconds.

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

The check trusts only the supplied certificate and verifies HTTP 308 to HTTPS, certificate validation, and HTTPS 200. The mount path contains `/nginx/` so both bundles must preserve certificate paths unchanged.

Run the reload check on the HTTP fixture, not the TLS overlay: it expects HTTP 200, not a redirect. Set `BUNDLE_TLS_PORT=8444` on Compose commands and use `--tls-port 8444` to test both TLS stacks at once.

After checking both bundles, remove the local images with `docker image rm docker-release-angie:local docker-release-nginx:local docker-release-test-app:local`. Delete the certificates and other scratch files in `.oga_tmp/` when done.
