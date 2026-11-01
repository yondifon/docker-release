"""Check that a release gives the new container what `docker compose up` would.

Usage (from the repo root):
    python3 tests/upgrade/check-upgrade.py mounted
    python3 tests/upgrade/check-upgrade.py fallback
    python3 tests/upgrade/check-upgrade.py host
    python3 tests/upgrade/check-upgrade.py host-options

Host modes invoke the host plugin without a project mount. `host-options`
also uses an explicit env file and an extra overlay. All modes finish with
an unreachable proxy check.
"""
import argparse
import http.client
import json
import os
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
HOST_TMP = HERE / ".oga_tmp"


class CheckFailed(AssertionError):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailed(message)


def run(*args, timeout=120, check=True, cwd=ROOT, env=None):
    result = subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
    if check and result.returncode != 0:
        raise CheckFailed(f"{' '.join(args)} failed ({result.returncode}):\n{result.stdout}{result.stderr}")
    return result


class Stack:
    def __init__(self, mode, unreachable=False):
        self.mode = mode
        self.host = mode in ("host", "host-options")
        self.files = ["tests/upgrade/docker-compose.yml"]
        if mode == "mounted":
            self.files.append("tests/upgrade/mounted.yml")
        if self.host:
            self.files.append("tests/upgrade/host.yml")
        if mode == "host-options":
            self.files.append("tests/upgrade/host-options.yml")
        if unreachable:
            self.files.append("tests/upgrade/unreachable.yml")

    def compose(self, *args, **kwargs):
        # The overlay mounts `.:${PWD}`, as in the docs, so PWD must be the project directory.
        command = ["env", f"PWD={HERE}", "docker", "compose"]
        if self.mode == "host-options":
            command += ["--env-file", str(HOST_TMP / "explicit.env")]
        for file in self.files:
            command += ["-f", file]
        return run(*command, *args, **kwargs)

    def release(self):
        if self.host:
            # An unrelated cwd makes `-f` and Compose's saved paths necessary.
            environment = os.environ.copy()
            for key in ("COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_ENV_FILES",
                        "FROM_COMPOSE", "TAG", "OVERLAY_VALUE"):
                environment.pop(key, None)
            environment["PWD"] = str(HOST_TMP)
            return run(str(ROOT / "scripts" / "docker-release"),
                       "-f", str(HERE / "docker-compose.yml"), "app",
                       cwd=HOST_TMP, env=environment, timeout=300, check=False)
        return self.compose("exec", "-T", "docker-release", "dr", "release", "app", timeout=300, check=False)

    def check_controller_mounts(self):
        controller = self.compose("ps", "-q", "docker-release").stdout.strip()
        mounts = inspect(controller)["Mounts"]
        binds = [mount for mount in mounts if mount["Type"] == "bind"]
        require(len(binds) == 1 and binds[0]["Destination"] == "/var/run/docker.sock",
                f"controller must have only the Docker socket bind, not project files: {mounts}")
        pass_(f"{self.mode}: controller has no project bind mount")

    def write_env(self, value, tag="latest"):
        text = f"FROM_COMPOSE={value}\nTAG={tag}\n"
        path = HERE / ".env"
        if self.mode == "host-options":
            path = HOST_TMP / "explicit.env"
            text += f"OVERLAY_VALUE=overlay-{value}\n"
        path.write_text(text)

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
        result = stack.release()
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

    if stack.host:
        check_host_model(stack, new, expect_compose or "one")

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


def check_host_model(stack, container, revision):
    config = container["Config"]
    labels = config["Labels"]
    expected_files = {str(ROOT / file) for file in stack.files}
    actual_files = set(labels.get("com.docker.compose.project.config_files", "").split(","))
    require(actual_files == expected_files,
            f"replacement lost original Compose file paths: {actual_files}, wanted {expected_files}")
    require(labels.get("com.docker.compose.project.working_dir") == str(HERE),
            f"replacement lost original Compose working directory: {labels}")
    for value in ("HOST_LITERAL=literal $cash and $HOME", "ENV_FILE_LITERAL=env $cash and ${HOME}"):
        require(value in config["Env"], f"host model changed a literal dollar value: {config['Env']}")
    require(config["Labels"].get("upgrade.revision") == revision,
            f"updated Compose label was not applied: {config['Labels']}")
    if stack.mode == "host-options":
        require(f"OVERLAY_ONLY=overlay-{revision}" in config["Env"],
                f"explicit env file or overlay was not applied: {config['Env']}")
        require(labels.get("com.docker.compose.project.environment_file") == str(HOST_TMP / "explicit.env"),
                f"replacement lost explicit Compose env file path: {labels}")
    alias = f"app-{revision}"
    endpoint = container["NetworkSettings"]["Networks"][f"{PROJECT}_backend"]
    require(alias in endpoint["Aliases"], f"updated network alias {alias} was not applied: {endpoint}")
    response = stack.compose("exec", "-T", "client", "wget", "-qO-", "-T", "3", f"http://{alias}/")
    require(f"host={config['Hostname']}" in response.stdout,
            f"updated alias does not reach the replacement: {response.stdout}")
    marker = run("docker", "exec", container["Id"], "cat", "/host-files/marker").stdout.strip()
    require(marker == "host-path-ok", f"relative bind path with dollars or spaces was not preserved: {marker!r}")
    pass_(f"{stack.mode}: dollars, env file, bind path, updated label and network alias applied")


def rejected_host_config(stack):
    before = app_containers()
    require(len(before) == 1, f"expected one app before invalid config, got {before}")
    original = inspect(before[0])
    env_file = HOST_TMP / "explicit.env" if stack.mode == "host-options" else HERE / ".env"
    contents = env_file.read_text()
    try:
        env_file.write_text('FROM_COMPOSE="unterminated\n')
        result = stack.release()
        require(result.returncode != 0,
                f"host release accepted malformed env file:\n{result.stdout}{result.stderr}")
        require(app_containers(running_only=False) == before,
                "invalid host config replaced the old app or left an extra container")
        current = inspect(before[0])
        require(current["State"]["StartedAt"] == original["State"]["StartedAt"],
                "invalid host config restarted the old app")
        require(current["Config"] == original["Config"] and current["Image"] == original["Image"],
                "invalid host config changed the old app")
        wait_for_body(f"host={original['Config']['Hostname']}", "after rejecting invalid host config")
        pass_(f"{stack.mode}: malformed env file rejected; original app still serves, no extras")
    finally:
        env_file.write_text(contents)


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


def prepare(mode):
    (HERE / ".env").unlink(missing_ok=True)
    (HERE / "bind").mkdir(exist_ok=True)
    (HERE / "bind" / "marker").write_text("bind-ok\n")
    if mode in ("host", "host-options"):
        HOST_TMP.mkdir(exist_ok=True)
        (HOST_TMP / "host $ files").mkdir(exist_ok=True)
        (HOST_TMP / "host $ files" / "marker").write_text("host-path-ok\n")
        (HOST_TMP / "app values.env").write_text("ENV_FILE_LITERAL='env $cash and ${HOME}'\n")
    if mode == "host-options":
        (HERE / ".env").write_text("FROM_COMPOSE=wrong-default\nTAG=wrong-default\n")
        (HOST_TMP / "explicit.env").write_text("FROM_COMPOSE=one\nTAG=latest\nOVERLAY_VALUE=overlay-one\n")


def cleanup():
    for file in ("explicit.env", "app values.env", "host $ files/marker"):
        (HOST_TMP / file).unlink(missing_ok=True)
    if (HOST_TMP / "host $ files").exists():
        (HOST_TMP / "host $ files").rmdir()
    if HOST_TMP.exists():
        HOST_TMP.rmdir()
    (HERE / ".env").unlink(missing_ok=True)
    (HERE / "bind" / "marker").unlink(missing_ok=True)
    try:
        (HERE / "bind").rmdir()
    except OSError:
        pass


def upgrade_scenario(mode, build_stack):
    stack = Stack(mode)
    prepare(mode)
    build_app("1", "latest")
    try:
        seed_volume(stack)
        stack.compose(*(["up", "--build"] if build_stack else ["up"]), "-d", "--wait", timeout=600)
        if mode != "mounted":
            stack.check_controller_mounts()
        wait_for_body("image=1", "before the first release")

        # Same tag, new image: the common case of rebuilding `app:latest`.
        build_app("2", "latest")
        outputs = [release(stack, "2", "latest")]

        if mode != "fallback":
            stack.write_env("two")
            outputs.append(release(stack, "2", "latest", expect_compose="two"))

            build_app("3", "v3")
            stack.write_env("two", "v3")
            outputs.append(release(stack, "3", "v3", expect_compose="two"))

        if stack.host:
            rejected_host_config(stack)

        logged = COMPOSE_MISSING in "".join(outputs)
        if mode != "fallback":
            require(not logged, f"{mode}: release fell back to container config instead of the Compose model")
        else:
            require(logged, f"`dr release` did not log `{COMPOSE_MISSING}` in fallback mode")
        pass_(f"{mode}: `dr release` {'logs' if mode == 'fallback' else 'does not log'} `{COMPOSE_MISSING}`")
    except BaseException:
        diagnostics(stack)
        raise
    finally:
        teardown(stack)


def unreachable_scenario(mode):
    stack = Stack(mode, unreachable=True)
    prepare(mode)
    build_app("1", "latest")
    try:
        seed_volume(stack)
        stack.compose("up", "-d", "--wait", timeout=600)
        if mode != "mounted":
            stack.check_controller_mounts()
        build_app("2", "latest")
        before = app_containers()
        require(len(before) == 1, f"expected one app container, got {before}")
        started = inspect(before[0])["State"]["StartedAt"]

        result = stack.release()
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
    parser.add_argument("mode", choices=["mounted", "fallback", "host", "host-options"])
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
