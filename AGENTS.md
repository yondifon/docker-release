# AGENTS.md

Agent context for docker-release. Read this before making any changes.

---

## What this project is

`docker-release` is a zero-downtime deployment controller for Docker Compose. It runs as a sidecar container in a Compose stack, watches Docker events, and orchestrates rolling updates, blue/green, and canary releases by writing upstream config to a shared volume that a reverse proxy (Nginx, Angie, Traefik) hot-reloads.

It does **not** proxy traffic. It manages containers and generates proxy config files.

---

## Architecture

```
host machine
└── docker compose stack
    ├── docker-release   ← controller (this project); mounts docker.sock + shared volume
    ├── nginx/angie      ← proxy; mounts shared volume (read-only)
    └── app              ← managed service; labelled with release.*
```

**Flow:** user triggers `docker release app` on host → host plugin proxies to `docker exec <cid> dr release app` → controller runs `Release()` → strategy executes → provider writes config → proxy reloads.

---

## Repository layout

```
cmd/docker-release/main.go       CLI entrypoint (binary: dr)
internal/
  config/labels.go               Label parsing → ServiceConfig struct
  config/project.go              Compose project auto-detection
  controller/controller.go       Core: Watch, Release, Rollback, Status
  provider/                      nginx, angie, traefik, nginx-proxy, noop
  strategy/                      linear, blue-green, canary
  state/                         Deployment state persistence (JSON)
  docker/                        Docker API client wrapper
  monitor/                       Docker event monitor
  rollback/                      Rollback helpers
scripts/
  docker-release                 Host CLI plugin (self-contained shell script)
tests/
  nginx/                         Test compose stack for nginx provider
  angie/                         Test compose stack for angie provider
  nginx-proxy/                   Test compose stack for nginx-proxy provider
  traefik/                       Test compose stack for traefik provider
  app.Dockerfile                 Shared app image used in all test stacks
Dockerfile                       Builds the controller image (binary: /usr/local/bin/dr)
Makefile                         dev, test
docs/
  readme.md                      Full reference (strategies, providers, labels)
  providers/nginx.md             Nginx provider quickstart
```

---

## The binary

- Built from `./cmd/docker-release/` → binary named `dr`
- Installed at `/usr/local/bin/dr` inside the container image
- `ENTRYPOINT ["dr"]`, `CMD ["watch"]` — container starts the watcher by default
- No other scripts or tools are bundled in the image

### CLI commands (inside container)

```
dr <service> [--force]    Deploy (short form; implicit release)
dr release <service>      Deploy explicitly
dr rollback <service>     Roll back
dr status [service]       Show deployment state
dr watch                  Start the event watcher (default container command)
dr version                Print version
```

The `default:` branch in `main.go` treats any unrecognised first argument as a service name → runs `Release`. Reserved words: `watch`, `release`, `rollback`, `status`, `version`, `help`.

---

## The host CLI plugin

`scripts/docker-release` is a self-contained POSIX shell script. It:

1. Responds to `docker-cli-plugin-metadata` for Docker plugin discovery
2. Parses an optional `-f <compose-file>` flag for explicit compose file targeting
3. Auto-detects the Compose project (priority: `COMPOSE_PROJECT_NAME` env → `name:` field in compose file → cwd basename)
4. Finds the running controller container via labels: `com.docker.compose.project=<project>` + `org.opencontainers.image.title=docker-release`
5. Proxies all remaining args via `docker exec <cid> dr "$@"`

Install (no repo clone needed):

```sh
curl -fsSL https://raw.githubusercontent.com/yondifon/docker-release/main/scripts/docker-release \
  | sudo tee ~/.docker/cli-plugins/docker-release >/dev/null \
  && sudo chmod +x ~/.docker/cli-plugins/docker-release
```

Contributors: `make dev` symlinks it instead.

### Usage on host

```sh
docker release app                        # deploy
docker release app --force
docker release -f ./infra/compose.yml app # explicit compose file
docker release rollback app
docker release status
```

---

## Key internal packages

### `internal/config`

- `ParseLabels(labels map[string]string) (*ServiceConfig, error)` — reads all `release.*` labels into a typed struct
- `DetectProject(ctx, dockerClient) (string, error)` — 4-step fallback: own container label → `COMPOSE_PROJECT_NAME` → compose file `name:` → cwd basename

`ServiceConfig` fields of note:

| Field | Label | Default |
|---|---|---|
| `Provider` | `release.provider` | `nginx-proxy` |
| `Strategy` | `release.strategy` | `linear` |
| `HealthCheckTimeout` | `release.health_check_timeout` | `60s` |
| `DrainTimeout` | `release.drain_timeout` | `10s` |
| `NginxService` | `release.nginx.service` | auto-detected by image |
| `NginxConfigDir` | `release.nginx.config_dir` | `/shared/nginx-config` (nginx) or `/shared/nginx-tmpl` (nginx-proxy) |
| `AngieService` | `release.angie.service` | auto-detected by image |
| `AngieConfigDir` | `release.angie.config_dir` | `/shared/angie-config` |
| `TraefikConfigDir` | `release.traefik.config_dir` | `/shared/traefik-config` |
| `CaddyService` | `release.caddy.service` | auto-detected by image |
| `CaddyConfigDir` | `release.caddy.config_dir` | `/shared/caddy-config` |
| `CaddyPath` | `release.caddy.path` | `` (named-snippet mode) |
| `HAProxyService` | `release.haproxy.service` | auto-detected by image |
| `HAProxyConfigDir` | `release.haproxy.config_dir` | `/shared/haproxy-config` |
| `UpstreamName` | `release.upstream` | service name |
| `Affinity` | `release.affinity` | `ip` |
| `BlueGreen.SoakTime` | `release.bg.soak_time` | `5m` |
| `BlueGreen.GreenWeight` | `release.bg.green_weight` | `50` |
| `Canary.StartPercentage` | `release.canary.start_percentage` | `10` |
| `Canary.Step` | `release.canary.step` | `20` |
| `Canary.Interval` | `release.canary.interval` | `2m` |

### `internal/provider`

Interface — each provider implements:
- `GenerateConfig(state UpstreamState) error` — write/reload proxy config
- `Drain(containerAddr string) error` — remove one server from upstream
- `SetTrafficSplit(old, new []Server) error` — canary weight update

Providers: `nginx`, `angie`, `caddy`, `traefik`, `haproxy`, `nginxproxy`, `noop`

**Caddy default:** no `release.caddy.path` generates a named snippet like `(api_upstream)`. Users import generated files globally, then use `import api_upstream` inside their own site/route blocks so headers, auth, rate limits, and other directives stay in their Caddyfile. Set `release.caddy.path` only when docker-release should generate a full `handle_path` block.

### `internal/strategy`

Interface — each strategy implements:
- `Deploy(ctx, Deployment) error`
- `Rollback(ctx, Deployment) error`

Strategies: `linear`, `bluegreen`, `canary`

### `internal/controller`

Public methods on `*Controller`:
- `Watch(ctx)` — starts Docker event loop
- `Release(ctx, service, force)` — triggers deploy
- `Rollback(ctx, service)` — triggers rollback
- `Status(ctx, service)` — prints state
- `WaitDeployments()` — blocks until in-flight deploys settle

### `internal/state`

Persists `DeploymentState` to `/var/lib/docker-release/<project>/<service>.json`. Tracks status (`idle`, `in_progress`), active/previous container sets, strategy, timestamps.

### `internal/server`

Optional HTTP API (port 9080) + web dashboard (port 9081), both off by default. Env vars:

| Env var | Default | Purpose |
|---------|---------|---------|
| `DR_EXPOSE_API` | off | `1`/`true`/`yes` enables the REST API |
| `DR_EXPOSE_WEB` | off | enables the web dashboard |
| `DR_BIND_ADDR` | `0.0.0.0` | listen address |
| `DR_API_PORT` | `9080` | API port (1–65535; invalid values fall back with a logged warning) |
| `DR_WEB_PORT` | `9081` | web port |
| `DR_API_TOKEN` | — | when set, mutating endpoints (`POST .../cancel`) require `Authorization: Bearer <token>` (constant-time compare). Unset logs a startup warning and leaves them open — only safe on trusted networks |

Routes: `GET /api/health`, `GET /api/services`, `GET /api/services/{service}`, `POST /api/services/{service}/cancel`, `GET /api/deployments/{id}`, `POST /api/deployments/{id}/cancel`. Unknown or invalid service names return 404.

---

## Provider mechanics

All providers follow the same pattern:
1. Controller writes upstream config to a shared Docker volume
2. Proxy reads that volume (mounted read-only) and hot-reloads

**Session affinity mapping** — all strategies enable sticky sessions by default (`release.affinity: ip`). Each provider implements it differently:

| Affinity | Nginx (OSS) | Angie | Traefik | nginx-proxy |
|----------|------------|-------|---------|-------------|
| `cookie` | `ip_hash` (no sticky cookie in OSS) | `sticky cookie _srr_<hash> path=/;` | `sticky.cookie.name: _srr_<hash>` | `ip_hash` |
| `ip` | `ip_hash` | `ip_hash` | `strategy: hrw` | `ip_hash` |
| `""` (off) | `least_conn` | `least_conn` | RoundRobin (no sticky) | `least_conn` |

**Nginx / Angie:** generates `<service>_upstream.conf` in the shared dir, sends `nginx -s reload` / `angie -s reload` to the proxy container via `docker exec`.

**Traefik:** generates `<service>-router.yml` dynamic config in the shared dir; Traefik watches via file provider.

**nginx-proxy:** generates from `nginx.tmpl`; jwilder/nginx-proxy auto-reloads on volume changes.

**noop:** no config written; orchestration only (health checks, wait, drain timers still run).

---

## Deployment strategies

All strategies enable session affinity by default (`release.affinity: ip`). This ensures users stay on the same backend during a deployment. Set `release.affinity: ""` to disable, or `release.affinity: cookie` for cookie-based sticky sessions.

### Linear
Replaces containers one at a time: start new → wait healthy → add to upstream → mark old as draining → wait drain timeout → remove old. Repeats per replica.

### Blue/Green
Spins up full replacement set → all healthy → atomic upstream switch → soak period → remove old. Rollback during soak: switch back instantly.

### Canary
Start subset of new containers → route `start_percentage` traffic → monitor for `interval` → increase by `step` → repeat until 100% → drain old. Auto-rollback if canary fails health checks.

---

## Compose label required to manage a service

```yaml
labels:
  release.enable: "true"               # required — opts this service in
  release.provider: nginx              # required — nginx | angie | traefik | nginx-proxy | none
```

Everything else has defaults. See `internal/config/labels.go:ParseLabels` for all defaults.

---

## Container label used to find the controller

The controller image sets:

```
org.opencontainers.image.title=docker-release
```

The host plugin finds it by combining this with the Compose project label:

```sh
docker ps \
  --filter "label=com.docker.compose.project=<project>" \
  --filter "label=org.opencontainers.image.title=docker-release"
```

---

## Rules for agents

- **Never add scripts or host tools to the Docker image.** The image contains only the `dr` binary. Host-side tooling lives in `scripts/` and is installed separately.
- **`scripts/docker-release` must stay self-contained.** No sourcing external files. Users install it via a single `curl | tee` — it cannot depend on anything else in the repo.
- **`scripts/docker-release` is the only host command.** There is no `dr` standalone binary for the host. On the host: `docker release`. Inside the container: `dr`.
- **Do not add subcommands to the binary without updating the reserved-word list** in `main.go`'s `default:` branch comment and `printUsage()`.
- **Provider config is written to a shared volume, never to the proxy container's filesystem directly.** The proxy mounts the volume read-only.
- **State is persisted to `/var/lib/docker-release`.** Mount a Docker volume there in production to survive container restarts.
- **Test stacks live in `tests/`.** Each provider has its own compose stack. `tests/app.Dockerfile` builds the shared dummy app used across all test stacks.
- **Run `go test ./...` after any Go change.** All packages have tests; keep them green.
- **`entrypoint.sh` in the repo root is dead code** — the Dockerfile does not use it. Do not reference or restore it without first understanding why it was bypassed.

---

## Known gaps / in-flight

- `traefik` and `nginx-proxy` providers are marked draft — functional but not hardened
- No automated integration tests (test stacks in `tests/` are manual)
- `entrypoint.sh` is orphaned (Dockerfile uses `ENTRYPOINT ["dr"]` directly)
