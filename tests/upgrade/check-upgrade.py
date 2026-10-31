"""Check that a release gives the new container what `docker compose up` would.

Usage (from the repo root):
    python3 tests/upgrade/check-upgrade.py mounted
    python3 tests/upgrade/check-upgrade.py fallback

`mounted` gives the controller the project directory, so it can run Docker
Compose itself. `fallback` does not, so the controller copies the old container
and refreshes it from the new image. Compose file changes only apply in
`mounted` mode. Each mode then runs an `unreachable` stack, where the proxy
shares no network with the app and the release must fail without touching the
old container.
"""
import argparse
import http.client
import json
import pathlib
import subprocess
import threading
import time


ROOT = pathlib.Path(__file__).resolve().parents[2]
HERE = ROOT / "tests" / "upgrade"
PORT = 8094
HOST = "app.localhost"
PROJECT = "upgrade"
COMPOSE_MISSING = "compose files are not mounted"
BUILT_IMAGES = []


class CheckFailed(AssertionError):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailed(message)


def run(*args, timeout=120, check=True):
    result = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode != 0:
        raise CheckFailed(f"{' '.join(args)} failed ({result.returncode}):\n{result.stdout}{result.stderr}")
    return result


class Stack:
    def __init__(self, mounted, unreachable=False):
        self.files = ["tests/upgrade/docker-compose.yml"]
        if mounted:
            self.files.append("tests/upgrade/mounted.yml")
        if unreachable:
            self.files.append("tests/upgrade/unreachable.yml")

    def compose(self, *args, **kwargs):
        # The overlay mounts `.:${PWD}`, as in the docs, so PWD must be the project directory.
        command = ["env", f"PWD={HERE}", "docker", "compose"]
        for file in self.files:
            command += ["-f", file]
        return run(*command, *args, **kwargs)

    def logs(self):
        return self.compose("logs", "--no-color", "docker-release", timeout=60).stdout


def build_app(version, tag):
    """Builds `upgrade-app:<tag>` with the version baked into ENV, LABEL, CMD and HEALTHCHECK."""
    healthcheck = "# no healthcheck in v1"
    if version != "1":
        healthcheck = (
            f"HEALTHCHECK --interval={version}s --timeout=2s --retries=10 "
            f'CMD ["wget", "-qO-", "http://127.0.0.1/?v={version}"]'
        )
    text = (HERE / "app.Dockerfile").read_text()
    text = text.replace("{{VERSION}}", version).replace("{{HEALTHCHECK}}", healthcheck)
    result = subprocess.run(
        ["docker", "build", "-q", "-f", "-", "-t", f"upgrade-app:{tag}", str(HERE)],
        cwd=ROOT, input=text, capture_output=True, text=True, timeout=180,
    )
    require(result.returncode == 0, f"building upgrade-app:{tag} failed:\n{result.stdout}{result.stderr}")
    BUILT_IMAGES.append(result.stdout.strip())


def app_containers(running_only=True):
    args = ["docker", "ps", "-q" if running_only else "-aq",
            "--filter", f"label=com.docker.compose.project={PROJECT}",
            "--filter", "label=com.docker.compose.service=app"]
    return sorted(run(*args).stdout.split())


def inspect(target):
    return json.loads(run("docker", "inspect", target).stdout)[0]


def image_config(tag):
    return json.loads(run("docker", "image", "inspect", f"upgrade-app:{tag}").stdout)[0]


def fetch():
    conn = http.client.HTTPConnection("127.0.0.1", PORT, timeout=5)
    try:
        conn.request("GET", "/", headers={"Host": HOST})
        resp = conn.getresponse()
        return resp.status, resp.read().decode(errors="replace").strip()
    finally:
        conn.close()


class Traffic(threading.Thread):
    """Requests the app through the proxy on fresh connections until stopped."""

    def __init__(self):
        super().__init__(daemon=True)
        self.stop_event = threading.Event()
        self.ok = 0
        self.failures = []

    def run(self):
        while not self.stop_event.is_set():
            try:
                status, body = fetch()
                if status == 200:
                    self.ok += 1
                else:
                    self.failures.append(f"HTTP {status}: {body[:80]}")
            except Exception as exc:
                self.failures.append(f"{type(exc).__name__}: {exc}")
            time.sleep(0.05)

    def finish(self):
        self.stop_event.set()
        self.join(timeout=10)


def wait_for_body(expected, what):
    deadline = time.monotonic() + 60
    last = None
    while time.monotonic() < deadline:
        try:
            status, body = fetch()
            last = f"HTTP {status}: {body[:120]}"
            if status == 200 and expected in body:
                return body
        except Exception as exc:
            last = f"{type(exc).__name__}: {exc}"
        time.sleep(1)
    raise CheckFailed(f"{what}: the proxy never served {expected!r} (last: {last})")


def pass_(message):
    print(f"PASS: {message}", flush=True)


def release(stack, version, tag, expect_compose=None, expect_replaced=True):
    """Runs `dr release app` under traffic, then checks the new container."""
    old_ids = app_containers()
    require(len(old_ids) == 1, f"expected one running app container before the release, got {old_ids}")
    old = inspect(old_ids[0])
    old_version = next(e.split("=", 1)[1] for e in old["Config"]["Env"] if e.startswith("APP_VERSION="))
    wait_for_body(f"image={old_version}", "before the release")

    traffic = Traffic()
    traffic.start()
    try:
        result = stack.compose("exec", "-T", "docker-release", "dr", "release", "app", timeout=300, check=False)
    finally:
        traffic.finish()
    output = result.stdout + result.stderr
    require(result.returncode == 0, f"`dr release app` exited {result.returncode}:\n{output}")
    require(traffic.ok > 0, "no request reached the app during the release")
    require(not traffic.failures,
            f"{len(traffic.failures)} of {traffic.ok + len(traffic.failures)} requests failed during the release "
            f"(a 504 means the proxy was given an address on a network it is not on): {traffic.failures[:3]}")
    pass_(f"v{version}: {traffic.ok} requests through the proxy during the release, none failed")

    # `dr release` runs the deployment itself, so its log is the command's output.
    require("deployment complete" in output, f"`dr release` did not log `deployment complete`:\n{output}")
    require("deployment failed" not in output, f"`dr release` logged `deployment failed`:\n{output}")
    pass_(f"v{version}: `dr release` logged `deployment complete`")

    new_ids = app_containers()
    require(len(new_ids) == 1, f"expected one running app container after the release, got {new_ids}")
    require(not (expect_replaced and new_ids[0] == old_ids[0]), "the old app container was not replaced")
    require(len(app_containers(running_only=False)) == 1, "a stopped or extra app container was left behind")
    new = inspect(new_ids[0])
    image = image_config(tag)
    require(new["Image"] == image["Id"],
            f"the new container runs image {new['Image'][:19]}, not the current upgrade-app:{tag} ({image['Id'][:19]})")
    pass_(f"v{version}: old container replaced by one on the current upgrade-app:{tag} image")

    for _ in range(30):
        status, body = fetch()
        require(status == 200, f"proxy answered {status} after the release: {body[:80]}")
        for part in (f"image={version}", "data=data-ok", "bind=bind-ok", f"cmd={version}", f"host={new['Config']['Hostname']}"):
            require(part in body, f"proxy body is missing {part!r}: {body}")
        if expect_compose:
            require(f"compose={expect_compose}" in body, f"compose env was not applied; wanted compose={expect_compose}: {body}")
    pass_(f"v{version}: 30 requests served by the new container with volume and bind data")

    mounts = {m["Destination"]: m for m in new["Mounts"]}
    require(mounts.get("/data", {}).get("Type") == "volume", f"named volume /data was not carried over: {new['Mounts']}")
    require(mounts.get("/bind", {}).get("Type") == "bind" and not mounts["/bind"]["RW"],
            f"read-only bind mount /bind was not carried over: {new['Mounts']}")
    pass_(f"v{version}: named volume and read-only bind mount carried over")

    config = new["Config"]
    require(f"APP_VERSION={version}" in config["Env"], f"image ENV was lost: Env={config['Env']}")
    require(f"APP_VERSION={old_version}" not in config["Env"] or old_version == version,
            f"old image ENV APP_VERSION={old_version} is still set: {config['Env']}")
    require("SHARED_KEY=compose" in config["Env"],
            f"the compose value must win over the image value on the same key: Env={config['Env']}")
    require(config["Labels"].get("app.version") == version, f"image label app.version was lost: {config['Labels']}")
    require(config["Labels"].get("release.enable") == "true" and config["Labels"].get("com.docker.compose.service") == "app",
            f"release or compose labels were lost: {config['Labels']}")
    require(config["Cmd"] == image["Config"]["Cmd"] and f"cmd={version}" in " ".join(config["Cmd"]),
            f"CMD is not the new image's CMD: {config['Cmd']}")
    require(config.get("Healthcheck") == image["Config"].get("Healthcheck"),
            f"HEALTHCHECK is not the new image's: {config.get('Healthcheck')} vs {image['Config'].get('Healthcheck')}")
    pass_(f"v{version}: image ENV, CMD, labels and HEALTHCHECK are the new image's; compose env still wins")

    networks = new["NetworkSettings"]["Networks"]
    require({f"{PROJECT}_default", f"{PROJECT}_backend"} <= set(networks), f"the container did not join both networks: {list(networks)}")
    for name, endpoint in networks.items():
        require("app" in (endpoint.get("Aliases") or []), f"network {name} has no alias `app`: {endpoint.get('Aliases')}")
    pass_(f"v{version}: joined both networks with the `app` alias")

    require(old["Id"] not in app_containers(running_only=False), "the old container still exists")
    by_name = stack.compose("exec", "-T", "client", "wget", "-qO-", "-T", "3", "http://app/", check=False)
    require(by_name.returncode == 0 and f"image={version}" in by_name.stdout,
            f"`client` cannot reach http://app/ after the old container is gone: {by_name.stdout}{by_name.stderr}")
    pass_(f"v{version}: client reaches the app by service name")

    hostname = config["Hostname"]
    require(hostname != old["Config"]["Hostname"], f"the new container reuses the old hostname {hostname}")
    require(hostname == new["Id"][:12], f"hostname {hostname} is not the container's own ID {new['Id'][:12]}")
    pass_(f"v{version}: hostname is the new container's own")
    return output


def seed_volume(stack):
    """Creates the stack's containers and volume, then writes the volume marker."""
    stack.compose("create", "app", timeout=120)
    run("docker", "run", "--rm", "-v", f"{PROJECT}_appdata:/data", "busybox", "sh", "-c", "echo data-ok > /data/marker")


def teardown(stack):
    stack.compose("down", "-v", "--remove-orphans", timeout=180, check=False)


def diagnostics(stack):
    print("--- docker ps -a", flush=True)
    print(run("docker", "ps", "-a", "--format", "{{.Names}}\t{{.Image}}\t{{.Status}}", check=False).stdout, flush=True)
    print("--- controller log (tail)", flush=True)
    logs = stack.compose("logs", "--no-color", "--tail", "80", "docker-release", check=False, timeout=60)
    print(logs.stdout + logs.stderr, flush=True)


def prepare():
    (HERE / ".env").unlink(missing_ok=True)
    (HERE / "bind").mkdir(exist_ok=True)
    (HERE / "bind" / "marker").write_text("bind-ok\n")


def cleanup():
    (HERE / ".env").unlink(missing_ok=True)
    (HERE / "bind" / "marker").unlink(missing_ok=True)
    try:
        (HERE / "bind").rmdir()
    except OSError:
        pass


def upgrade_scenario(mode, build_stack):
    stack = Stack(mounted=mode == "mounted")
    prepare()
    build_app("1", "latest")
    try:
        seed_volume(stack)
        stack.compose(*(["up", "--build"] if build_stack else ["up"]), "-d", "--wait", timeout=600)
        wait_for_body("image=1", "before the first release")

        # Same tag, new image: the common case of rebuilding `app:latest`.
        build_app("2", "latest")
        outputs = [release(stack, "2", "latest")]

        if mode == "mounted":
            # The controller only sees files on disk, not the shell's environment.
            (HERE / ".env").write_text("FROM_COMPOSE=two\n")
            outputs.append(release(stack, "2", "latest", expect_compose="two"))

            build_app("3", "v3")
            (HERE / ".env").write_text("FROM_COMPOSE=two\nTAG=v3\n")
            outputs.append(release(stack, "3", "v3", expect_compose="two"))

        logged = COMPOSE_MISSING in "".join(outputs)
        if mode == "mounted":
            require(not logged, "`dr release` says compose files are not mounted, but they are")
        else:
            require(logged, f"`dr release` did not log `{COMPOSE_MISSING}` in fallback mode")
        pass_(f"{mode}: `dr release` {'does not log' if mode == 'mounted' else 'logs'} `{COMPOSE_MISSING}`")
    except BaseException:
        diagnostics(stack)
        raise
    finally:
        teardown(stack)


def unreachable_scenario(mode):
    stack = Stack(mounted=mode == "mounted", unreachable=True)
    prepare()
    build_app("1", "latest")
    try:
        seed_volume(stack)
        stack.compose("up", "-d", "--wait", timeout=600)
        build_app("2", "latest")
        before = app_containers()
        require(len(before) == 1, f"expected one app container, got {before}")
        started = inspect(before[0])["State"]["StartedAt"]

        result = stack.compose("exec", "-T", "docker-release", "dr", "release", "app", timeout=300, check=False)
        output = result.stdout + result.stderr
        require(result.returncode != 0, f"`dr release app` succeeded although the proxy cannot reach the app:\n{output}")
        require("not reachable by the proxy" in output, f"the error does not say the container is not reachable by the proxy:\n{output}")
        after = app_containers()
        require(after == before, f"the old app container changed: {before} -> {after}")
        require(inspect(before[0])["State"]["StartedAt"] == started, "the old app container was restarted")
        require(app_containers(running_only=False) == before, "the failed release left an extra app container behind")
        pass_(f"{mode}: unreachable app fails the release and the old container keeps running")
    except BaseException:
        diagnostics(stack)
        raise
    finally:
        teardown(stack)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["mounted", "fallback"])
    parser.add_argument("--no-build", action="store_true", help="reuse the existing docker-release-angie:local image")
    args = parser.parse_args()
    try:
        upgrade_scenario(args.mode, build_stack=not args.no_build)
        unreachable_scenario(args.mode)
    except CheckFailed as exc:
        print(f"FAIL: {exc}", flush=True)
        raise SystemExit(1)
    finally:
        cleanup()
        for image in BUILT_IMAGES:
            run("docker", "image", "rm", "-f", image, check=False)
    print(f"PASS: all {args.mode} upgrade checks", flush=True)


if __name__ == "__main__":
    main()
