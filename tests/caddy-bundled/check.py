import pathlib
import socket
import ssl
import sys
import urllib.parse

sys.dont_write_bytecode = True
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1] / "bundled"))

from bundle_checks import Bundle, ROOT, check_releases, check_routing, command


def listener_inodes(bundle):
    table = command("docker", "exec", bundle.controller, "cat", "/proc/net/tcp", "/proc/net/tcp6")
    inodes = set()
    for line in table.splitlines():
        fields = line.split()
        if len(fields) > 9 and fields[3] == "0A" and int(fields[1].split(":")[-1], 16) in [80, 443]:
            inodes.add(fields[9])
    assert len(inodes) == 2, inodes
    return inodes


def check_pending_connections(bundle):
    route = bundle.routes[0]
    connections = []
    try:
        for secure in [False, True]:
            pending = bundle.connection(route, secure)
            if secure:
                pending.sock = bundle.context.wrap_socket(
                    socket.create_connection(("127.0.0.1", bundle.tls_port), timeout=3), server_hostname=route.host,
                )
            else:
                pending.connect()
            connections.append((pending, secure))
            kept = bundle.connection(route, secure)
            bundle.check_response(route, "before-update", secure, kept)
            connections.append((kept, secure))
        file = "/shared/caddy-config/linear_app.upstreams"
        data = command("docker", "exec", bundle.controller, "cat", file).split("\n", 1)[1]
        endpoint = "http://127.0.0.1:2019/docker_release/upstreams?" + urllib.parse.urlencode({"file": file})
        command("docker", "exec", bundle.controller, "wget", "-qO-", "--header=Content-Type: application/json", "--post-data=" + data, endpoint)
        for connection, secure in connections:
            bundle.check_response(route, "after-update", secure, connection)
    finally:
        for connection, _ in connections:
            connection.close()
    print("PASS: pending and idle HTTP/HTTPS connections survive an upstream update", flush=True)


def main():
    scratch = ROOT / ".oga_tmp"
    scratch.mkdir(exist_ok=True)
    ca_path = scratch / "caddy-root.crt"
    bundle = Bundle("caddy")
    source = "docker-release:/var/lib/docker-release/caddy/pki/authorities/local/root.crt"
    command(*bundle.compose, "cp", source, str(ca_path))
    ca = ca_path.read_bytes()
    bundle.context = ssl.create_default_context(cafile=str(ca_path))
    check_routing(bundle)
    check_pending_connections(bundle)
    listeners = listener_inodes(bundle)
    check_releases(bundle)
    assert listener_inodes(bundle) == listeners, "app releases replaced Caddy's listening sockets"
    print("PASS: all app releases kept the same HTTP/HTTPS listening sockets", flush=True)
    command(*bundle.compose, "up", "-d", "--no-build", "--force-recreate", "--no-deps", "--wait", "docker-release")
    command(*bundle.compose, "cp", source, str(ca_path))
    assert ca_path.read_bytes() == ca, "recreating the bundle replaced the local CA"
    check_routing(bundle)
    print("PASS: bundle recreation keeps the local CA, trusted HTTPS, and all app routes", flush=True)


if __name__ == "__main__":
    main()
