import argparse
import json
import pathlib
import socket
import subprocess
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("proxy", choices=["nginx", "angie"], nargs="?", default="nginx")
    proxy = parser.parse_args().proxy
    root = pathlib.Path(__file__).resolve().parents[2]
    port = 8089 if proxy == "nginx" else 8090
    connection = socket.create_connection(("127.0.0.1", port), timeout=2)
    connection.sendall(b"G")
    time.sleep(1)
    started = time.monotonic()
    try:
        subprocess.run(
            ["docker", "compose", "-f", f"tests/{proxy}-bundled/docker-compose.yml",
             "stop", "-t", "90", "docker-release"],
            cwd=root, check=True, timeout=95,
        )
        elapsed = time.monotonic() - started
        output = subprocess.check_output(
            ["docker", "inspect", f"{proxy}-bundled-docker-release-1"], text=True,
        )
        state = json.loads(output)[0]["State"]
        assert state["ExitCode"] == 0, state
        assert elapsed < 15, f"idle connection held shutdown for {elapsed:.2f}s"
        print(f"PASS: incomplete headers did not force-kill {proxy}: exit=0, stop={elapsed:.2f}s")
    finally:
        connection.close()


if __name__ == "__main__":
    main()
