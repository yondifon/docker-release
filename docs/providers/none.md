# No Proxy Provider

Use this for workers, jobs, and any service that does not receive web traffic.

`docker-release` starts new containers, waits for health checks, then stops old ones. It writes no proxy config.

## When to Use This

- Background workers
- Queue consumers
- Scheduled jobs
- Sidecars

Do not use this for services that need canary or blue/green traffic splitting — those strategies require a proxy.

## Compose Example

```yaml
services:
  docker-release:
    image: malico/docker-release:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    healthcheck:
      test: ["CMD", "dr", "healthcheck"]
      interval: 5s
      retries: 10

  worker:
    image: your-registry/worker:latest
    labels:
      release.enable: "true"
      release.provider: none
    healthcheck:
      test: ["CMD", "my-worker", "--health"]
      interval: 10s
      timeout: 5s
      retries: 3
```

## Required Labels

```yaml
release.enable: "true"
release.provider: none
```

## Deploy

```sh
docker compose up -d
docker release worker
```

## Strategy

Only `linear` is supported. `canary` and `blue-green` require a proxy to split traffic.

```yaml
release.drain_timeout: 5s
release.health_check_timeout: 60s
```

## Health Check

Every worker needs a health check. `docker-release` waits for `healthy` before it stops old containers.

```yaml
healthcheck:
  test: ["CMD", "my-worker", "--health"]
  interval: 10s
  timeout: 5s
  retries: 3
```

Use whatever check proves the worker is ready — a flag, a file, a TCP connection to an internal port.

## Cron Job Example

```yaml
services:
  job:
    image: your-registry/job:latest
    labels:
      release.enable: "true"
      release.provider: none
    healthcheck:
      test: ["CMD", "job", "--ready"]
      interval: 10s
      timeout: 5s
      retries: 3
```

## Common Problems

| Problem | Fix |
|---|---|
| Canary or blue-green rejected | Switch to `linear`, or use a proxy provider |
| Deploy waits forever | Check the health check — `docker-release` waits until `healthy` |
| Rollback state lost on restart | Mount `/var/lib/docker-release` to a named volume |
