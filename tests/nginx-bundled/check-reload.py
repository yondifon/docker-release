import concurrent.futures
import http.client
import json
import pathlib
import subprocess
import threading
import time


def main():
    root = pathlib.Path(__file__).resolve().parents[2]
    controller = "nginx-bundled-docker-release-1"
    compose = ["docker", "compose", "-f", "tests/nginx-bundled/docker-compose.yml"]

    def command(*args):
        return subprocess.check_output(args, cwd=root, text=True).strip()

    old = command(
        "docker", "ps", "--filter", "label=com.docker.compose.project=nginx-bundled",
        "--filter", "label=com.docker.compose.service=linear_app", "--format", "{{.ID}}",
    )
    assert old and "\n" not in old, old
    master = command("docker", "exec", controller, "cat", "/run/nginx/nginx.pid")
    stop = threading.Event()

    def traffic():
        requests, failures = 0, []
        while not stop.is_set():
            connection = http.client.HTTPConnection("127.0.0.1", 8089, timeout=2)
            requests += 1
            try:
                connection.request("GET", "/linear/", headers={"Host": "linear.localhost", "Connection": "close"})
                response = connection.getresponse()
                response.read()
                if response.status != 200:
                    failures.append(response.status)
            except Exception as error:
                failures.append(str(error))
            finally:
                connection.close()
            stop.wait(0.025)
        return {"requests": requests, "failures": failures}

    with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
        monitor = pool.submit(traffic)
        process = None
        paused = False
        try:
            command("docker", "exec", controller, "kill", "-STOP", master)
            paused = True
            process = subprocess.Popen(
                compose + ["exec", "-T", "docker-release", "dr", "release", "linear_app"],
                cwd=root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            time.sleep(12)
            retained = bool(command("docker", "ps", "--filter", f"id={old}", "--format", "{{.ID}}"))
        finally:
            if paused:
                command("docker", "exec", controller, "kill", "-CONT", master)
            if process is not None:
                process.wait(timeout=90)
            stop.set()
        sample = monitor.result()
    print(json.dumps({"old_retained_before_reload": retained, "traffic": sample}), flush=True)
    assert process.returncode == 0, process.returncode
    assert retained, "old backend was retired before nginx accepted the reload"
    assert not sample["failures"], sample
    current = command(
        "docker", "ps", "--filter", "label=com.docker.compose.project=nginx-bundled",
        "--filter", "label=com.docker.compose.service=linear_app", "--format", "{{.ID}}",
    )
    assert current != old and current and "\n" not in current, current
    print("PASS: nginx accepted the reload before the old backend was retired", flush=True)


if __name__ == "__main__":
    main()
