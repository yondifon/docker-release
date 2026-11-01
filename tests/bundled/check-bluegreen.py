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
  preview  while the check runs, the preview host serves only the new version
           and the main host only the old one; after the switch the preview
           host answers 404 and the main host serves the new version
  preview_failed
           a failing check also takes the preview host away
  preview_link
           `release.preview.token: auto`: while the check runs, a browser with
           the cookie, or a script with the header, reaches the new version on
           the normal host and everyone else stays on the old one; the link
           sets the cookie and keeps the rest of the query; after the switch
           the token does nothing
  preview_link_failed
           the same, and a failing check takes the token away too
  preview_link_host
           a fixed token together with a preview host: the preview host also
           needs the token
  preview_link_tls
           the same over HTTPS with a test certificate: the cookie from the
           link is Secure there, and not on plain HTTP
"""
import argparse
import dataclasses
import http.client
import json
import os
import re
import shutil
import socket
import ssl
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
    def __init__(self, proxy, labels, tls=False):
        self.proxy = proxy
        self.env = {**labels}
        self.compose = ["docker", "compose", "-f", f"tests/{proxy}-bundled/docker-compose.yml", "-f", "tests/bundled/bluegreen.yml"]
        if tls:
            self.env["PWD"] = str(ROOT)
            self.compose += ["-f", "tests/bundled/bluegreen-tls.yml"]
        self.project = f"{proxy}-bundled"

    def run(self, *args, **kwargs):
        return subprocess.run(
            [*self.compose, *args], cwd=ROOT, env={**os.environ, **self.env}, capture_output=True, text=True, timeout=300, **kwargs,
        )

    def __enter__(self):
        build_app("ok")
        try:
            result = self.run("up", "-d", "--build", "--wait", "docker-release", "bg_app")
            assert result.returncode == 0, result.stdout + result.stderr
            result = self.run("create", "smoke")
            assert result.returncode == 0, result.stdout + result.stderr
            self.wait_until_serving()
        except BaseException:
            self.__exit__()
            raise
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


PREVIEW_HOST = "next.check.localhost"


def fetch(port, host, path="/"):
    """Returns (status, version) for one request through the proxy."""
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
    try:
        connection.request("GET", path, headers={"Host": host})
        response = connection.getresponse()
        found = re.search(r"version=(\w+)", response.read().decode())
        return response.status, found.group(1) if found else ""
    finally:
        connection.close()


def run_release_in_background(stack):
    outcome = {}

    def target():
        outcome["result"], outcome["elapsed"] = stack.release()

    thread = threading.Thread(target=target)
    thread.start()
    return thread, outcome


def wait_for_preview(port):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            if fetch(port, PREVIEW_HOST)[0] == 200:
                return
        except OSError:
            pass
        time.sleep(0.25)
    raise AssertionError("the preview host never started serving the new version")


def preview_scenario(proxy, check_command, passes):
    labels = {"BG_PREVIEW_HOST": PREVIEW_HOST, "BG_CHECK_COMMAND": check_command, "BG_CHECK_TIMEOUT": "60s"}
    port = PORTS[proxy]
    with Stack(proxy, labels) as stack:
        old = stack.app_ids()
        assert fetch(port, PREVIEW_HOST)[0] == 404, "the preview host must not answer before a release"
        build_app("bad")
        thread, outcome = run_release_in_background(stack)
        wait_for_preview(port)
        # The check sleeps while we look, so both hosts are watched during it.
        during = []
        for _ in range(20):
            during.append((fetch(port, PREVIEW_HOST), fetch(port, HOST)))
            time.sleep(0.1)
        thread.join()
        result = outcome["result"]
        assert all(preview == (200, "bad") for preview, _ in during), during
        assert all(main == (200, "ok") for _, main in during), during
        after_preview = fetch(port, PREVIEW_HOST)
        after_main = fetch(port, HOST)
        assert after_preview[0] == 404, f"the preview host should be gone, got {after_preview}"
        if passes:
            assert result.returncode == 0, result.stdout + result.stderr
            assert after_main == (200, "bad"), f"users should be on the new version, got {after_main}"
            assert stack.app_ids() != old
        else:
            assert result.returncode != 0 and "release undone" in result.stderr, result.stderr
            assert after_main == (200, "ok"), f"users should stay on the old version, got {after_main}"
            assert stack.app_ids() == old
        return {"samples": len(during), "after_preview": after_preview[0], "after_main": after_main[1]}


LINK_WAIT = 14
TLS_PORT = 8447
CERTS = ROOT / ".oga_tmp" / "certs"
FIXED_TOKEN = "fixed-token-for-the-live-check"


class TLSConnection(http.client.HTTPConnection):
    """Reaches the proxy over HTTPS as `check.localhost`, and again after the server closes it."""

    def __init__(self, port, context):
        super().__init__("127.0.0.1", port, timeout=3)
        self.context = context

    def connect(self):
        raw = socket.create_connection((self.host, self.port), self.timeout)
        self.sock = self.context.wrap_socket(raw, server_hostname=HOST)


def preview_cookies(response):
    """The `Set-Cookie` values for the preview cookie. A proxy may set cookies of its own."""
    return [value for value in response.msg.get_all("Set-Cookie", []) if value.startswith("release_preview=")]


def open_connection(port, tls=None):
    """A connection to the proxy; `tls` is the SSL context that trusts the test certificate."""
    if tls:
        return TLSConnection(port, tls)
    return http.client.HTTPConnection("127.0.0.1", port, timeout=3)


def request(port, host, path="/", headers=None, connection=None, tls=None):
    """One request through the proxy, without following redirects.

    Returns (status, version, response). Pass a connection to keep it alive.
    """
    own = connection is None
    connection = connection or open_connection(port, tls)
    try:
        connection.request("GET", path, headers={"Host": host, **(headers or {})})
        response = connection.getresponse()
        body = response.read().decode()
        found = re.search(r"version=(\w+)", body)
        response.body = body
        return response.status, found.group(1) if found else "", response
    finally:
        if own:
            connection.close()


def wait_for_link(stack, port, fixed):
    """The token, once `dr status` shows it and the proxy sends it to the new version."""
    deadline = time.monotonic() + 90
    token = fixed
    while time.monotonic() < deadline:
        if not token:
            found = re.search(r"Preview:\s+\S*\?release-preview=(\w+)", stack.status())
            token = found.group(1) if found else None
        if token:
            try:
                if request(port, HOST, headers={"X-Release-Preview": token})[1] == "bad":
                    return token
            except OSError:
                pass
        time.sleep(0.5)
    raise AssertionError("the preview link never started sending requests to the new version")


def check_link_while_green_waits(port, token, preview_host, tls=None):
    def get(host, path="/", headers=None, connection=None):
        return request(port, host, path, headers, connection, tls)

    cookie = f"release_preview={token}"
    checks = {
        "no token": (get(HOST), 200, "ok"),
        "header": (get(HOST, headers={"X-Release-Preview": token}), 200, "bad"),
        "wrong header": (get(HOST, headers={"X-Release-Preview": token + "x"}), 200, "ok"),
        "cookie among others": (get(HOST, headers={"Cookie": f"a=1; {cookie}; b=2"}), 200, "bad"),
        "wrong cookie": (get(HOST, headers={"Cookie": cookie + "x"}), 200, "ok"),
        "token on a path the new version breaks": (get(HOST, "/signup", {"Cookie": cookie}), 500, "bad"),
        "wrong token in the link": (get(HOST, "/?x=1&release-preview=wrong"), 200, "ok"),
    }
    for name, (got, status, version) in checks.items():
        assert (got[0], got[1]) == (status, version), f"{name}: want {(status, version)}, got {got[:2]}"
    assert "query=x=1&release-preview=wrong" in checks["wrong token in the link"][0][2].body, "a wrong token must reach the app untouched"
    for name in ("no token", "wrong header", "wrong token in the link"):
        assert not preview_cookies(checks[name][0][2]), f"{name}: no preview cookie may be set"

    status, _, response = get(HOST, f"/?release-preview={token}")
    assert len(preview_cookies(response)) == 1, response.msg.get_all("Set-Cookie")
    set_cookie = preview_cookies(response)[0]
    assert status == 302 and response.getheader("Location") == "/", (status, response.getheader("Location"))
    assert set_cookie.startswith(cookie) and "HttpOnly" in set_cookie and "SameSite=Lax" in set_cookie and "Path=/" in set_cookie, set_cookie
    assert ("Secure" in set_cookie) == bool(tls), f"the cookie is Secure on HTTPS only: {set_cookie}"
    assert "version=" not in response.body, "the link must not reach the app"
    got = get(HOST, headers={"Cookie": set_cookie.split(";")[0]})
    assert got[:2] == (200, "bad"), f"the cookie from the link should lead to the new version, got {got[:2]}"

    for query, location in {
        f"release-preview={token}&b=2&c=3": "/signup?b=2&c=3",
        f"a=1&release-preview={token}": "/signup?a=1",
        f"a=1&release-preview={token}&b=2": "/signup?a=1&b=2",
        f"a=1&b=%20x%26y&release-preview={token}&c=3": "/signup?a=1&b=%20x%26y&c=3",
        f"release-preview={token}": "/signup",
    }.items():
        status, _, response = get(HOST, f"/signup?{query}")
        assert status == 302 and response.getheader("Location") == location, f"{query}: got {status} {response.getheader('Location')}, want {location}"

    # One kept-alive connection must follow each request, not stay where it started.
    connection = open_connection(port, tls)
    try:
        seen = [get(HOST, headers=h, connection=connection)[1]
                for h in ({"X-Release-Preview": token}, {}, {"X-Release-Preview": token}, {})]
    finally:
        connection.close()
    assert seen == ["bad", "ok", "bad", "ok"], seen
    if not tls:
        # Some proxies answer an unknown host on HTTPS with their first site, token or not.
        assert get("unknown.localhost", headers={"X-Release-Preview": token})[0] == 404, "an unknown host must stay a 404"

    if preview_host:
        assert get(PREVIEW_HOST)[0] == 404, "the preview host needs the token"
        assert get(PREVIEW_HOST, headers={"X-Release-Preview": token + "x"})[0] == 404
        for headers in ({"X-Release-Preview": token}, {"Cookie": cookie}):
            got = get(PREVIEW_HOST, headers=headers)
            assert got[:2] == (200, "bad"), f"{headers}: got {got[:2]}"
        status, _, response = get(PREVIEW_HOST, f"/signup?a=1&release-preview={token}")
        assert status == 302 and response.getheader("Location") == "/signup?a=1" and preview_cookies(response)[0].startswith(cookie)


def check_link_after_release(port, token, passes):
    """The token does nothing once the release is over: the app gets the link as it was sent."""
    expected = "bad" if passes else "ok"
    for name, headers in {"cookie": {"Cookie": f"release_preview={token}"}, "header": {"X-Release-Preview": token}}.items():
        got = request(port, HOST, headers=headers)
        assert got[:2] == (200, expected), f"{name} after the release: want {(200, expected)}, got {got[:2]}"
    got = request(port, HOST, f"/?a=1&release-preview={token}")
    assert got[:2] == (200, expected) and not preview_cookies(got[2]), f"the link must not be answered by the proxy any more: {got[:2]}"
    assert f"query=a=1&release-preview={token}" in got[2].body, got[2].body


def trusted_context():
    """An SSL context that trusts only the test certificate."""
    return ssl.create_default_context(cafile=str(CERTS / "cert.pem"))


def make_certificate():
    CERTS.mkdir(parents=True, exist_ok=True)
    command(
        "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", f"/CN={HOST}",
        "-addext", f"subjectAltName=DNS:{HOST},DNS:{PREVIEW_HOST},IP:127.0.0.1",
        "-keyout", str(CERTS / "key.pem"), "-out", str(CERTS / "cert.pem"),
    )


def preview_link_scenario(proxy, passes, token, preview_host=False, tls=False):
    wait = f'test -n "$RELEASE_PREVIEW_TOKEN" || exit 3; sleep {LINK_WAIT}'
    labels = {"BG_PREVIEW_TOKEN": token, "BG_CHECK_COMMAND": wait if passes else wait + "; exit 1", "BG_CHECK_TIMEOUT": "60s"}
    if preview_host:
        labels["BG_PREVIEW_HOST"] = PREVIEW_HOST
    port = PORTS[proxy]
    if tls:
        make_certificate()
    with Stack(proxy, labels, tls) as stack:
        old = stack.app_ids()
        before = request(port, HOST, "/?release-preview=anything")
        assert before[:2] == (200, "ok") and not preview_cookies(before[2]), "no link works before a release"
        build_app("bad")
        thread, outcome = run_release_in_background(stack)
        live = wait_for_link(stack, port, token if token != "auto" else None)
        if token == "auto":
            assert len(live) >= 32 and "Preview:" in stack.status()
        check_link_while_green_waits(port, live, preview_host)
        if tls:
            check_link_while_green_waits(TLS_PORT, live, preview_host, trusted_context())
        thread.join()
        result = outcome["result"]
        if passes:
            assert result.returncode == 0, result.stdout + result.stderr
            assert stack.app_ids() != old
        else:
            assert result.returncode != 0 and "release undone" in result.stderr and "exit code 1" in result.stderr, result.stderr
            assert stack.app_ids() == old
        status = stack.status()
        assert "release-preview" not in status and "Preview:" not in status, f"the link must go away with the release: {status}"
        check_link_after_release(port, live, passes)
        if token == "auto":
            logs = stack.run("logs", "--no-log-prefix").stdout
            assert live not in logs, "the token must not be written to the logs"
        return {"checked_for_seconds": LINK_WAIT}


SCENARIOS = {
    "preview": lambda proxy: preview_scenario(
        proxy, 'test "$RELEASE_PREVIEW_URL" = http://next.check.localhost && sleep 8', passes=True),
    "preview_failed": lambda proxy: preview_scenario(proxy, "sleep 8; exit 1", passes=False),
    "preview_link": lambda proxy: preview_link_scenario(proxy, passes=True, token="auto"),
    "preview_link_failed": lambda proxy: preview_link_scenario(proxy, passes=False, token="auto"),
    "preview_link_host": lambda proxy: preview_link_scenario(proxy, passes=True, token=FIXED_TOKEN, preview_host=True),
    "preview_link_tls": lambda proxy: preview_link_scenario(proxy, passes=True, token="auto", preview_host=True, tls=True),
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
        shutil.rmtree(CERTS, ignore_errors=True)


if __name__ == "__main__":
    main()
