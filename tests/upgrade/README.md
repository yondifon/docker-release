# Upgrade check

Checks that a release gives the new container what `docker compose up` would. Run modes one at a time from the repo root. They share port 8094 and project `upgrade`. Each removes its stack, `.env`, `bind/`, `.oga_tmp/` fixtures and `upgrade-app` images when done.

```sh
python3 tests/upgrade/check-upgrade.py mounted
python3 tests/upgrade/check-upgrade.py fallback
python3 tests/upgrade/check-upgrade.py host
python3 tests/upgrade/check-upgrade.py host-options
```

Add `--no-build` to reuse an existing `docker-release-angie:local` image.

`mounted` gives the controller the project directory with one extra line, `- .:${PWD}:ro` in `mounted.yml`. The path inside the controller must match the host path, so Compose can read the files. `fallback` leaves that line out.

`host` invokes the actual `scripts/docker-release -f <absolute-compose-file> app` entrypoint from another directory, without installing the Docker CLI plugin. The controller has only the Docker socket bind, not the project directory. `host-options` does the same with an explicit `--env-file` and an extra overlay used to start the stack; the plugin must recover those settings from Compose labels. Its default `.env` contains deliberately wrong values.

Each release rebuilds the app under the same tag while requests flow through the proxy. The check requires:

- No failed request during the release, and 30 good ones after it.
- A new container on the new image. The old one is gone.
- The new image's `ENV`, `CMD`, labels and `HEALTHCHECK`. A compose `environment` value still wins over the image on the same key.
- The named volume and the read-only bind mount carried over.
- Both networks joined, with the `app` alias, so `client` reaches `http://app/` after the old container is gone.
- A hostname of its own.
- `deployment complete` in the controller log.

In `mounted` and `host` modes it also changes `.env` (`FROM_COMPOSE`, then `TAG`) and requires the next release to use it. `host-options` changes its explicit env file instead. Both host modes check literal dollar signs in environment values, a service env file, a relative bind path with spaces and a dollar sign, updated labels, and access through an updated network alias.

In `fallback` mode Compose file changes are ignored, and the controller must log `compose files are not mounted`.

Both host modes repeat releases using the replacement's original Compose file metadata. A malformed env file must reject a release without restarting or replacing the app, leaving extra containers, or stopping traffic.

Last, an `unreachable` stack puts the app on a network the proxy is not on. The release must fail with `not reachable by the proxy` and leave the old container running.
