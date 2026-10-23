import concurrent.futures
import dataclasses
import http.client
import json
import pathlib
import socket
import subprocess
import threading
import time


ROOT = pathlib.Path(__file__).resolve().parents[2]
PORTS = {"nginx": 8089, "angie": 8090, "caddy": 8091}


@dataclasses.dataclass(frozen=True)
class Route:
    service: str
    host: str
    prefix: str
    name: str


ROUTES = [
    Route("linear_app", "linear.localhost", "/linear/", "linear"),
    Route("canary_app", "canary.localhost", "/canary/", "canary"),
    Route("blue_green_app", "bluegreen.localhost", "/bluegreen/", "blue_green"),
]


def command(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True, timeout=120).strip()


class WrongBackend(AssertionError):
    pass


class Bundle:
    def __init__(self, proxy, context=None, tls_port=8444):
        self.proxy = proxy
        self.context = context
        self.tls_port = tls_port
        self.routes = list(ROUTES)
        if proxy == "caddy":
            self.routes.append(Route("path_app", "localhost", "/path/", "path"))
        self.compose = ["docker", "compose", "-f", f"tests/{proxy}-bundled/docker-compose.yml"]
        self.controller = f"{proxy}-bundled-docker-release-1"

    def connection(self, route, secure):
        if secure:
            return http.client.HTTPSConnection(route.host, self.tls_port, timeout=3, context=self.context)
        return http.client.HTTPConnection("127.0.0.1", PORTS[self.proxy], timeout=3)

    def exchange(self, route, path, secure=False, connection=None, host=None):
        connection = connection or self.connection(route, secure)
        if secure and connection.sock is None:
            connection.sock = self.context.wrap_socket(
                socket.create_connection(("127.0.0.1", self.tls_port), timeout=3),
                server_hostname=route.host,
            )
        connection.request("GET", path, headers={"Host": host or route.host})
        response = connection.getresponse()
        body = response.read()
        return connection, response.status, body, dict(response.getheaders())

    def check_response(self, route, suffix, secure=False, connection=None):
        connection, status, body, _ = self.exchange(route, route.prefix + suffix, secure, connection)
        assert status == 200, (route.service, status, body)
        lines = body.decode().splitlines()
        if f"Name: {route.name}" not in lines:
            raise WrongBackend((route.service, "wrong app body", body))
        assert f"GET /{suffix} HTTP/1.1" in lines, (route.service, "path was not stripped", body)
        return connection

    def container_ids(self, service):
        return command(
            "docker", "ps", "--filter", f"label=com.docker.compose.project={self.proxy}-bundled",
            "--filter", f"label=com.docker.compose.service={service}", "--format", "{{.ID}}",
        ).splitlines()


def check_routing(bundle, tls_routes=None, redirect=False):
    for route in bundle.routes:
        for suffix in ["", "probe?bundle=route-check"]:
            if redirect and route.service == "linear_app":
                connection, status, _, headers = bundle.exchange(route, route.prefix + suffix)
                connection.close()
                assert status == 308, (route.service, status)
                assert headers["Location"] == f"https://{route.host}{route.prefix}{suffix}", headers
            else:
                bundle.check_response(route, suffix).close()
    tls_routes = tls_routes if tls_routes is not None else bundle.routes[:3]
    if bundle.context:
        for route in tls_routes:
            for suffix in ["", "probe?bundle=tls-check"]:
                bundle.check_response(route, suffix, True).close()
    negatives = [(bundle.routes[0], "/linear/", "unknown.localhost")]
    negatives += [(route, "/not-a-route/", route.host) for route in bundle.routes]
    for route, path, host in negatives:
        connection, status, body, _ = bundle.exchange(route, path, host=host)
        connection.close()
        assert status == 404 and b"Name: " not in body, ("unknown route reached an app", host, path, status, body)
    if bundle.context:
        for route in tls_routes:
            connection, status, body, _ = bundle.exchange(route, "/not-a-route/", True)
            connection.close()
            assert status == 404 and b"Name: " not in body, ("unknown TLS path reached an app", route.host, status, body)
    print(f"PASS: {bundle.proxy} app bodies, stripped paths, unknown hosts, and unknown paths", flush=True)


def check_releases(bundle):
    stop = threading.Event()

    def traffic(route, secure, reuse):
        requests, wrong_backend, failures = 0, 0, []
        connection = None
        try:
            while not stop.is_set():
                requests += 1
                suffix = "probe?bundle=traffic" if requests % 2 else ""
                try:
                    connection = connection or bundle.connection(route, secure)
                    bundle.check_response(route, suffix, secure, connection)
                except Exception as error:
                    wrong_backend += isinstance(error, WrongBackend)
                    failures.append({"time": time.time(), "error": str(error)})
                    connection.close()
                    connection = None
                if not reuse and connection:
                    connection.close()
                    connection = None
                stop.wait(0.025)
        finally:
            if connection:
                connection.close()
        return {
            "service": route.service, "scheme": "https" if secure else "http",
            "connections": "keep-alive" if reuse else "fresh", "requests": requests,
            "failures": failures, "wrong_backend": wrong_backend,
        }

    streams = [(route, False, reuse) for route in bundle.routes for reuse in [False, True]]
    if bundle.context:
        streams += [(route, True, reuse) for route in bundle.routes[:3] for reuse in [False, True]]
    retained = None
    release_error = None
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(streams)) as pool:
        monitors = [pool.submit(traffic, *stream) for stream in streams]
        try:
            for route in bundle.routes:
                old = bundle.container_ids(route.service)
                assert len(old) == 1, (route.service, old)
                if route.service == "linear_app" and bundle.proxy != "caddy":
                    master = command("docker", "exec", bundle.controller, "cat", f"/run/{bundle.proxy}/{bundle.proxy}.pid")
                    process = None
                    command("docker", "exec", bundle.controller, "kill", "-STOP", master)
                    try:
                        process = subprocess.Popen(
                            bundle.compose + ["exec", "-T", "docker-release", "dr", "release", route.service],
                            cwd=ROOT,
                        )
                        time.sleep(12)
                        retained = set(old).issubset(bundle.container_ids(route.service))
                    finally:
                        command("docker", "exec", bundle.controller, "kill", "-CONT", master)
                        if process:
                            process.wait(timeout=90)
                    assert process.returncode == 0, process.returncode
                    assert retained, f"{bundle.proxy} retired old backend before accepting config"
                else:
                    command(*bundle.compose, "exec", "-T", "docker-release", "dr", "release", route.service)
                current = bundle.container_ids(route.service)
                assert len(current) == 1 and current != old, (route.service, old, current)
        except Exception as error:
            release_error = error
        finally:
            stop.set()
        samples = [monitor.result() for monitor in monitors]
    summary = {
        "proxy": bundle.proxy, "requests": sum(sample["requests"] for sample in samples),
        "failures": sum(len(sample["failures"]) for sample in samples),
        "wrong_backend": sum(sample["wrong_backend"] for sample in samples),
        "old_retained_before_reload": retained, "streams": samples,
    }
    print(json.dumps(summary), flush=True)
    if release_error:
        raise release_error
    assert summary["failures"] == 0 and summary["wrong_backend"] == 0, summary
    return summary
