import concurrent.futures
import http.client
import json
import pathlib
import socket
import ssl
import subprocess
import threading


ROOT = pathlib.Path(__file__).resolve().parents[2]
COMPOSE = ["docker", "compose", "-f", "tests/caddy-bundled/docker-compose.yml"]
ROUTES = {
    "linear_app": ("linear.localhost", "/linear/", b"Name: linear"),
    "canary_app": ("canary.localhost", "/canary/", b"Name: canary"),
    "blue_green_app": ("bluegreen.localhost", "/bluegreen/", b"Name: blue_green"),
    "path_app": ("localhost", "/path/", b"Name: linear"),
}


def command(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True, timeout=120).strip()


def request(host, path, expected, context=None):
    if context is None:
        connection = http.client.HTTPConnection("127.0.0.1", 8091, timeout=3)
    else:
        connection = http.client.HTTPSConnection(host, 8444, timeout=3, context=context)
        connection.sock = context.wrap_socket(
            socket.create_connection(("127.0.0.1", 8444), timeout=3), server_hostname=host,
        )
    try:
        connection.request("GET", path, headers={"Host": host, "Connection": "close"})
        response = connection.getresponse()
        body = response.read()
        assert response.status == 200, (host, path, response.status, body)
        assert expected in body, (host, path, body)
        if path != "/health":
            assert b"GET / HTTP/1.1" in body, ("path was not stripped", body)
    finally:
        connection.close()


def container_ids(service):
    return command(
        "docker", "ps", "--filter", "label=com.docker.compose.project=caddy-bundled",
        "--filter", f"label=com.docker.compose.service={service}", "--format", "{{.ID}}",
    ).splitlines()


def main():
    scratch = ROOT / ".oga_tmp"
    scratch.mkdir(exist_ok=True)
    ca_path = scratch / "caddy-root.crt"
    command(*COMPOSE, "cp", "docker-release:/var/lib/docker-release/caddy/pki/authorities/local/root.crt", str(ca_path))
    ca = ca_path.read_bytes()
    context = ssl.create_default_context(cafile=str(ca_path))
    for host, path, expected in ROUTES.values():
        request(host, path, expected)
        if host != "localhost":
            request(host, path, expected, context)
    request("127.0.0.1", "/health", b"ok")
    print("PASS: all four apps return HTTP 200 with stripped paths; all three hosts return trusted HTTPS 200", flush=True)

    stop = threading.Event()

    def traffic(service, route, secure):
        requests, failures = 0, []
        while not stop.is_set():
            requests += 1
            try:
                request(*route, context=context if secure else None)
            except Exception as error:
                failures.append(str(error))
            stop.wait(0.025)
        return {"service": service, "scheme": "https" if secure else "http", "requests": requests, "failures": failures}

    with concurrent.futures.ThreadPoolExecutor(max_workers=7) as pool:
        monitors = [pool.submit(traffic, service, route, False) for service, route in ROUTES.items()]
        monitors += [pool.submit(traffic, service, route, True) for service, route in ROUTES.items() if route[0] != "localhost"]
        try:
            for service in ROUTES:
                old = container_ids(service)
                assert len(old) == 1, (service, old)
                output = command(*COMPOSE, "exec", "-T", "docker-release", "dr", "release", service)
                current = container_ids(service)
                assert len(current) == 1 and current != old, (service, old, current, output)
        finally:
            stop.set()
        results = [monitor.result() for monitor in monitors]
    print(json.dumps({"traffic": results}), flush=True)
    assert all(not result["failures"] for result in results), results

    command(*COMPOSE, "up", "-d", "--no-build", "--force-recreate", "--no-deps", "--wait", "docker-release")
    command(*COMPOSE, "cp", "docker-release:/var/lib/docker-release/caddy/pki/authorities/local/root.crt", str(ca_path))
    assert ca_path.read_bytes() == ca, "recreating the bundle replaced the local CA"
    for host, path, expected in ROUTES.values():
        request(host, path, expected)
        if host != "localhost":
            request(host, path, expected, context)
    print("PASS: bundle recreation keeps the local CA, trusted HTTPS, and all app routes", flush=True)


if __name__ == "__main__":
    main()
