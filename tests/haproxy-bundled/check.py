import concurrent.futures
import pathlib
import socket
import ssl
import subprocess
import sys
import time

sys.dont_write_bytecode = True
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "bundled"))

from bundle_checks import Bundle, ROOT, check_releases, check_routing, command


def listener_inodes(bundle):
    table = command("docker", "exec", bundle.controller, "cat", "/proc/net/tcp", "/proc/net/tcp6")
    return {
        fields[9] for line in table.splitlines()
        if len(fields := line.split()) > 9 and fields[3] == "0A"
        and int(fields[1].split(":")[-1], 16) in [80, 443]
    }


def check_in_flight_requests(bundle, apply):
    route = bundle.routes[0]
    old = bundle.container_ids(route.service)[0]
    hostname = command("docker", "inspect", "--format", "{{.Config.Hostname}}", old)
    connections = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        pending = []
        try:
            for secure in [False, True]:
                connection = bundle.connection(route, secure)
                if secure:
                    connection.sock = bundle.context.wrap_socket(
                        socket.create_connection(("127.0.0.1", bundle.tls_port), timeout=10),
                        server_hostname=route.host,
                    )
                connection.timeout = 10
                connections.append(connection)
                pending.append(pool.submit(bundle.exchange, route, route.prefix + "probe?wait=4s", secure, connection))
            time.sleep(0.5)
            apply()
            for response in pending:
                _, status, body, _ = response.result()
                assert status == 200, (status, body)
                lines = body.decode().splitlines()
                assert f"Hostname: {hostname}" in lines, body
                assert f"Name: {route.name}" in lines, body
                assert "GET /probe?wait=4s HTTP/1.1" in lines, body
        finally:
            for connection in connections:
                connection.close()


def check_open_requests(bundle):
    old = bundle.container_ids("linear_app")
    check_in_flight_requests(bundle, lambda: command(
        *bundle.compose, "exec", "-T", "docker-release", "dr", "release", "linear_app",
    ))
    assert bundle.container_ids("linear_app") != old, old
    print("PASS: in-flight HTTP and HTTPS requests finish during an app replacement", flush=True)


def check_graceful_reload(bundle):
    workers = set(worker_pids(bundle))
    master = command("docker", "exec", bundle.controller, "cat", "/run/docker-release/haproxy.started")
    check_in_flight_requests(bundle, lambda: command("docker", "exec", bundle.controller, "kill", "-USR2", master))
    assert set(worker_pids(bundle)) - workers, "master-worker reload did not start a new worker"
    print("PASS: in-flight HTTP and HTTPS requests finish through a seamless master-worker reload", flush=True)


def check_rejected_certificate(bundle, certificate):
    path = "/shared/haproxy-config/linear_app.cfg"
    working = command("docker", "exec", bundle.controller, "cat", path)
    old = set(bundle.container_ids("linear_app"))
    cert = certificate.read_bytes()
    try:
        certificate.write_text("invalid certificate\n")
        result = subprocess.run(
            bundle.compose + ["exec", "-T", "docker-release", "dr", "release", "linear_app"],
            cwd=ROOT, text=True, capture_output=True, timeout=60,
        )
        assert "deployment failed" in result.stderr and "haproxy rejected config" in result.stderr, result
        assert command("docker", "exec", bundle.controller, "cat", path) == working, "rejected backend config remains on disk"
        assert old.issubset(bundle.container_ids("linear_app")), "rejected config retired the old app"
    finally:
        certificate.write_bytes(cert)
    command(*bundle.compose, "exec", "-T", "docker-release", "dr", "release", "path_app")
    check_routing(bundle)
    print(f"PASS: rejected certificate keeps the old app and config, and the next app release works (release exit={result.returncode})", flush=True)


def worker_pids(bundle):
    master = command("docker", "exec", bundle.controller, "cat", "/run/docker-release/haproxy.started")
    return command("docker", "exec", bundle.controller, "cat", f"/proc/{master}/task/{master}/children").split()


def main():
    certificate = ROOT / ".oga_tmp/certs/cert.pem"
    bundle = Bundle("haproxy", ssl.create_default_context(cafile=str(certificate)), 8445)
    check_routing(bundle)
    listeners = listener_inodes(bundle)
    assert len(listeners) == 2, listeners
    workers = worker_pids(bundle)
    assert len(workers) == 1, workers
    check_open_requests(bundle)
    check_releases(bundle)
    assert listener_inodes(bundle) == listeners, "app releases replaced HAProxy's listening sockets"
    assert worker_pids(bundle) == workers, "app releases reloaded HAProxy instead of updating its backends"
    print("PASS: HTTP and HTTPS listening sockets and the same worker survive every app release", flush=True)
    check_graceful_reload(bundle)
    check_rejected_certificate(bundle, certificate)


if __name__ == "__main__":
    main()
