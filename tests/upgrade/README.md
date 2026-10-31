# Upgrade check

Checks that a release gives the new container what `docker compose up` would. Run it from the repo root. It uses port 8094 and removes its stack, `.env`, `bind/` and `upgrade-app` images when done.

```sh
python3 tests/upgrade/check-upgrade.py mounted
python3 tests/upgrade/check-upgrade.py fallback
```

Add `--no-build` to reuse an existing `docker-release-angie:local` image.

`mounted` gives the controller the project directory with one extra line, `- .:${PWD}:ro` in `mounted.yml`. The path inside the controller must match the host path, so Compose can read the files. `fallback` leaves that line out.

Each release rebuilds the app under the same tag while requests flow through the proxy. The check requires:

- No failed request during the release, and 30 good ones after it.
- A new container on the new image. The old one is gone.
- The new image's `ENV`, `CMD`, labels and `HEALTHCHECK`. A compose `environment` value still wins over the image on the same key.
- The named volume and the read-only bind mount carried over.
- Both networks joined, with the `app` alias, so `client` reaches `http://app/` after the old container is gone.
- A hostname of its own.
- `deployment complete` in the controller log.

In `mounted` mode it also changes `.env` (`FROM_COMPOSE`, then `TAG`) and requires the next release to use it. In `fallback` mode compose file changes are ignored, and the controller must log `compose files are not mounted`.

Last, an `unreachable` stack puts the app on a network the proxy is not on. The release must fail with `not reachable by the proxy` and leave the old container running.
