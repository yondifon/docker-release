"""Check blue-green release checks on a bundled proxy.

Usage (from the repo root):
    python3 tests/bundled/check-bluegreen.py nginx
    python3 tests/bundled/check-bluegreen.py traefik --scenario single

Each scenario starts its own stack, sends traffic through the proxy while it
releases a new version of `bg_app`, and checks what users saw:

  passing  the check command passes, the new version serves, the old one is
           removed only after the watch window
  failing  the check command fails: users never reach the new version, the
           old one is untouched, and the command's output is reported
  rollback the new version fails /signup after the switch: users go back to
           the old containers and no request reaches a stopped container
  single   the same with one container, where nothing else could take over
"""
import argparse
import dataclasses
import http.client
import json
import os
import re
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True

from bundle_checks import PORTS, ROOT, command

HOST = "check.localhost"
IMAGE = "docker-release-bg-app:local"
RULES = {"BG_FAIL_ON": "5xx", "BG_PATHS": "/signup", "BG_MAX_ERRORS": "1"}


def build_app(mode):
    command("docker", "build", "-q", "--build-arg", f"MODE={mode}", "-t", IMAGE, "tests/bundled/bluegreen-app")


@dataclasses.dataclass
class Sample:
    at: float
    path: str
    status: int
    version: str
    error: str = ""


class Traffic:
    """Requests `/` and `/signup` through the proxy on fresh and kept-alive connections."""

    def __init__(self, port):
        self.port = port
        self.samples = []
        self.stop_event = threading.Event()
        self.threads = [threading.Thread(target=self.run, args=(reuse,)) for reuse in (False, True)]

    def run(self, reuse):
        connection = None
        count = 0
        while not self.stop_event.is_set():
            path = "/signup" if count % 2 else "/"
            count += 1
            try:
                connection = connection or http.client.HTTPConnection("127.0.0.1", self.port, timeout=3)
                connection.request("GET", path, headers={"Host": HOST})
                response = connection.getresponse()
                body = response.read().decode()
                found = re.search(r"version=(\w+)", body)
                self.samples.append(Sample(time.monotonic(), path, response.status, found.group(1) if found else ""))
            except Exception as error:
                self.samples.append(Sample(time.monotonic(), path, 0, "", repr(error)))
                connection = None
            if not reuse and connection:
                connection.close()
                connection = None
            self.stop_event.wait(0.025)

    def __enter__(self):
        for thread in self.threads:
            thread.start()
        return self

    def __exit__(self, *_):
        self.stop_event.set()
        for thread in self.threads:
            thread.join()


class Stack:
    def __init__(self, proxy, labels):
        self.proxy = proxy
        self.env = {**labels}
        self.compose = ["docker", "compose", "-f", f"tests/{proxy}-bundled/docker-compose.yml", "-f", "tests/bundled/bluegreen.yml"]
        self.project = f"{proxy}-bundled"

    def run(self, *args, **kwargs):
        return subprocess.run(
            [*self.compose, *args], cwd=ROOT, env={**os.environ, **self.env}, capture_output=True, text=True, timeout=300, **kwargs,
        )

    def __enter__(self):
        build_app("ok")
        result = self.run("up", "-d", "--build", "--wait", "docker-release", "bg_app")
        assert result.returncode == 0, result.stdout + result.stderr
        result = self.run("create", "smoke")
        assert result.returncode == 0, result.stdout + result.stderr
        self.wait_until_serving()
        return self

    def wait_until_serving(self):
        """Wait for the proxy to route the app, and for the controller's startup config refreshes to end."""
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            try:
                connection = http.client.HTTPConnection("127.0.0.1", PORTS[self.proxy], timeout=3)
                connection.request("GET", "/", headers={"Host": HOST})
                if connection.getresponse().status == 200:
                    break
            except OSError:
                pass
            time.sleep(0.5)
        else:
            raise AssertionError("the proxy never served the app")
        time.sleep(5)

    def __exit__(self, *_):
        self.run("down", "-v", "--remove-orphans")

    def app_ids(self):
        return set(command(
            "docker", "ps", "-a", "--filter", f"label=com.docker.compose.project={self.project}",
            "--filter", "label=com.docker.compose.service=bg_app", "--format", "{{.ID}}",
        ).split())

    def release(self):
        started = time.monotonic()
        result = self.run("exec", "-T", "docker-release", "dr", "release", "bg_app")
        return result, time.monotonic() - started

    def status(self):
        return self.run("exec", "-T", "docker-release", "dr", "status", "bg_app").stdout


def count_app_containers(stack, stop, counts):
    while not stop.is_set():
        counts.append((time.monotonic(), len(stack.app_ids())))
        stop.wait(0.5)


def seconds_with_both_versions(counts):
    both = [at for at, count in counts if count > 1]
    return (max(both) - min(both) + 0.5) if both else 0.0


def scenario_passing(proxy):
    labels = {
        "BG_CHECK_SERVICE": "smoke",
        "BG_CHECK_COMMAND": 'echo "target=$RELEASE_TARGET host=$RELEASE_HOST"; wget -q -O- "$RELEASE_TARGET/signup" && test "$RELEASE_HOST" = check.localhost',
        "BG_WINDOW": "8s",
        **RULES,
    }
    with Stack(proxy, labels) as stack:
        old = stack.app_ids()
        counts, stop = [], threading.Event()
        counter = threading.Thread(target=count_app_containers, args=(stack, stop, counts))
        counter.start()
        with Traffic(PORTS[proxy]) as traffic:
            result, elapsed = stack.release()
        stop.set()
        counter.join()
        new = stack.app_ids()
        assert result.returncode == 0, result.stdout + result.stderr
        assert len(new) == 1 and new != old, (old, new)
        assert elapsed >= 8, f"the old version was removed before the 8s window ended ({elapsed:.1f}s)"
        assert seconds_with_both_versions(counts) >= 7, counts
        bad = [s for s in traffic.samples if s.status != 200 or s.version != "ok"]
        assert not bad, bad[:5]
        return {"requests": len(traffic.samples), "release_seconds": round(elapsed, 1)}


def scenario_failing(proxy):
    labels = {"BG_CHECK_COMMAND": 'wget -q -O- "$RELEASE_TARGET/signup"', "BG_CHECK_TIMEOUT": "30s"}
    with Stack(proxy, labels) as stack:
        old = stack.app_ids()
        build_app("bad")
        with Traffic(PORTS[proxy]) as traffic:
            result, _ = stack.release()
        assert result.returncode != 0, "the release should fail when the check fails"
        assert "release undone" in result.stderr and "server returned error" in result.stderr, result.stderr
        assert stack.app_ids() == old, "the old container must be untouched and the new one gone"
        bad = [s for s in traffic.samples if s.status != 200 or s.version != "ok"]
        assert not bad, f"users reached the new version or saw errors: {bad[:5]}"
        status = stack.status()
        assert "Last release failed" in status and "server returned error" in status, status
        return {"requests": len(traffic.samples)}


def rollback_scenario(proxy, replicas, max_gap):
    labels = {"BG_REPLICAS": str(replicas), "BG_WINDOW": "30s", **RULES}
    with Stack(proxy, labels) as stack:
        old = stack.app_ids()
        assert len(old) == replicas, old
        build_app("bad")
        with Traffic(PORTS[proxy]) as traffic:
            result, elapsed = stack.release()
            released_at = time.monotonic()
            time.sleep(3)
        assert result.returncode != 0, "the release should fail when green returns errors"
        assert "release undone" in result.stderr and "/signup returned 500" in result.stderr, result.stderr
        assert elapsed < 28, f"rolled back by the window ending, not by the errors ({elapsed:.1f}s)"
        assert stack.app_ids() == old, "the old containers must remain and the new ones be gone"

        broken = [s for s in traffic.samples if s.status == 500]
        assert len(broken) >= 2 and all(s.version == "bad" for s in broken), broken[:5]
        other = [s for s in traffic.samples if s.status not in (200, 500)]
        assert not other, f"requests failed outside the new version's own errors, e.g. served by a stopped container: {other[:5]}"
        after = [s for s in traffic.samples if s.at > released_at]
        assert after and all(s.status == 200 and s.version == "ok" for s in after), [s for s in after if s.status != 200][:5]

        gap = max(s.at for s in broken) - min(s.at for s in broken)
        assert gap <= max_gap, f"errors lasted {gap:.1f}s, the rollback should be instant"
        status = stack.status()
        assert "Last release failed" in status and "/signup returned 500" in status, status
        return {"requests": len(traffic.samples), "errors": len(broken), "error_seconds": round(gap, 2)}


SCENARIOS = {
    "passing": scenario_passing,
    "failing": scenario_failing,
    "rollback": lambda proxy: rollback_scenario(proxy, replicas=2, max_gap=8),
    "single": lambda proxy: rollback_scenario(proxy, replicas=1, max_gap=5),
}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("proxy", choices=sorted(PORTS))
    parser.add_argument("--scenario", choices=sorted(SCENARIOS), action="append")
    args = parser.parse_args()
    try:
        for name in args.scenario or SCENARIOS:
            print(json.dumps({"proxy": args.proxy, "scenario": name, **SCENARIOS[name](args.proxy)}), flush=True)
    finally:
        subprocess.run(["docker", "image", "rm", "-f", IMAGE], capture_output=True)


if __name__ == "__main__":
    main()
