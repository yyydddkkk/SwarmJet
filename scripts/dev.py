#!/usr/bin/env python3
"""Linux native development supervisor; containers only provide dependencies."""
import fcntl
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlsplit, urlunsplit

ROOT = Path(__file__).resolve().parent.parent
STATE = ROOT / ".cache/dev"
PID = STATE / "supervisor.pid"
READY = STATE / "ready.json"
SCRIPT = Path(__file__).resolve()


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def environment():
    # Reuse Vite's dotenv parser: quoted values and .env.development overrides
    # work without evaluating shell code or printing credentials.
    result = run(
        ["node", "--input-type=module", "-e",
         'import {loadEnv} from "./web/node_modules/vite/dist/node/index.js";'
         'process.stdout.write(JSON.stringify(loadEnv("development",process.cwd(),"")))'],
        capture_output=True, text=True,
    )
    env = os.environ | json.loads(result.stdout)
    env.setdefault("DATA_DIR", str(ROOT / "data"))
    env.setdefault("TEMPORAL_NAMESPACE", "swarmjet")
    env.setdefault("TEMPORAL_ADDRESS", "temporal:7233")
    env.setdefault("DEV_WEB_PORT", "5273")
    env["PUBLIC_ORIGIN"] = "http://localhost:" + env["DEV_WEB_PORT"]
    env["LISTEN_ADDRESS"] = "127.0.0.1:" + env.get("DEV_API_PORT", "8180")
    env["DEV_API_URL"] = "http://" + env["LISTEN_ADDRESS"]
    env["RUNTIME_ADMIN_LISTEN"] = "127.0.0.1:" + env.get("DEV_RUNTIME_PORT", "8191")
    env["RUNTIME_ADMIN_URL"] = "http://" + env["RUNTIME_ADMIN_LISTEN"]
    env["SECURE_COOKIES"] = "false"
    env["SWARMJET_GIT_ASKPASS"] = str(ROOT / "scripts/askpass.sh")
    env.setdefault("GIT_AUTHOR_NAME", "swarmjet")
    env.setdefault("GIT_AUTHOR_EMAIL", "swarmjet@example.invalid")
    env.setdefault("GIT_COMMITTER_NAME", env["GIT_AUTHOR_NAME"])
    env.setdefault("GIT_COMMITTER_EMAIL", env["GIT_AUTHOR_EMAIL"])
    return env


def compose(env, *args, capture=False):
    return run(["docker", "compose", "-f", str(ROOT / "compose.yaml"), *args],
               env=env, capture_output=capture, text=True)


def service_ip(env, service):
    ids = compose(env, "ps", "-q", service, capture=True).stdout.split()
    if len(ids) != 1:
        raise RuntimeError(f"Existing {service} service is not running; start dependencies first")
    info = json.loads(run(["docker", "inspect", ids[0]], env=env,
                          capture_output=True, text=True).stdout)[0]
    networks = info["NetworkSettings"]["Networks"]
    name = env.get("COMPOSE_PROJECT_NAME", "swarmjet") + "_default"
    network = networks.get(name)
    if not network or not network.get("IPAddress"):
        raise RuntimeError(f"Cannot resolve {service}; set its host address in .env.development")
    return network["IPAddress"]


def resolve_dependencies(env):
    # On Linux, host processes can reach existing Compose bridge IPs directly.
    # Discover them on every start; do not hardcode IPs or recreate the database.
    db = urlsplit(env["DATABASE_URL"])
    if db.hostname == "postgres":
        authority = db.netloc.rsplit("@", 1)
        host = service_ip(env, "postgres") + ":" + str(db.port or 5432)
        netloc = authority[0] + "@" + host if len(authority) == 2 else host
        env["DATABASE_URL"] = urlunsplit(db._replace(netloc=netloc))
    if env["TEMPORAL_ADDRESS"].split(":")[0] == "temporal":
        env["TEMPORAL_ADDRESS"] = service_ip(env, "temporal") + ":7233"


def supervisor_pid():
    try:
        pid = int(PID.read_text())
        argv = (Path("/proc") / str(pid) / "cmdline").read_bytes().split(b"\0")
        if str(SCRIPT).encode() in argv and b"run" in argv:
            return pid
    except (OSError, ValueError):
        pass
    return None


def stop():
    pid = supervisor_pid()
    if not pid:
        print("Native development processes are stopped.")
        return
    os.kill(pid, signal.SIGTERM)
    for _ in range(100):
        if supervisor_pid() != pid:
            print("Native development processes stopped; dependencies/data retained.")
            return
        time.sleep(0.5)
    raise RuntimeError("Shutdown timed out; inspect .cache/dev logs before restarting")


def reachable(url):
    try:
        with urllib.request.urlopen(url, timeout=1) as response:
            return response.status == 200
    except OSError:
        return False


def supervise():
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (STATE / "lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        children = []
        stopping = False

        def requested_stop(*_):
            nonlocal stopping
            stopping = True

        signal.signal(signal.SIGTERM, requested_stop)
        signal.signal(signal.SIGINT, requested_stop)
        PID.write_text(str(os.getpid()))
        READY.unlink(missing_ok=True)
        try:
            env = environment()
            resolve_dependencies(env)
            go = env.get("GO_BINARY") or shutil.which("go", path=env.get("PATH"))
            if not go:
                raise RuntimeError("Install Go or set GO_BINARY in .env.development")
            env["PATH"] = str(Path(go).resolve().parent) + os.pathsep + env["PATH"]
            for tool in (env.get("META_COMMAND", "meta"), "git", "docker", "node"):
                if not shutil.which(tool, path=env["PATH"]):
                    raise RuntimeError(f"Missing required tool: {tool}")
            data = Path(env["DATA_DIR"]).resolve()
            env["DATA_DIR"] = str(data)
            data.mkdir(parents=True, exist_ok=True, mode=0o700)
            for path in [data, *data.iterdir()]:
                if path.is_dir() and not os.access(path, os.R_OK | os.W_OK | os.X_OK):
                    raise RuntimeError("DATA_DIR contains inaccessible directories; transfer ownership first")
            binary = STATE / "swarmjet"
            run([go, "build", "-o", str(binary), "./cmd/swarmjet"], env=env)
            run([str(binary), "doctor"], env=env)
            if stopping:
                return
            # Stop only the old app processes. Never run compose down or remove volumes.
            compose(env, "stop", "api", "worker")
            for port in (env["DEV_WEB_PORT"], env.get("DEV_API_PORT", "8180"),
                         env.get("DEV_RUNTIME_PORT", "8191")):
                with socket.socket() as probe:
                    probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                    probe.bind(("127.0.0.1", int(port)))
            commands = {
                "worker": [str(binary), "worker"],
                "api": [str(binary), "serve"],
                "web": ["node", str(ROOT / "web/node_modules/vite/bin/vite.js"),
                        "--host", "127.0.0.1", "--port", env["DEV_WEB_PORT"], "--strictPort"],
            }
            for name, command in commands.items():
                with (STATE / (name + ".log")).open("ab") as log:
                    child = subprocess.Popen(command, cwd=ROOT / "web" if name == "web" else ROOT,
                                             env=env, stdout=log, stderr=log, start_new_session=True)
                children.append((name, child))
            deadline = time.monotonic() + 45
            ready = False
            while not stopping:
                for name, child in children:
                    if child.poll() is not None:
                        raise RuntimeError(f"{name} exited ({child.returncode}); see .cache/dev/{name}.log")
                if not ready:
                    ready = (reachable(env["DEV_API_URL"] + "/healthz") and
                             reachable(env["PUBLIC_ORIGIN"]))
                    if ready:
                        READY.write_text(json.dumps({"url": env["PUBLIC_ORIGIN"],
                                                     "processes": {n: c.pid for n, c in children}}))
                    elif time.monotonic() > deadline:
                        raise RuntimeError("Startup health check timed out; see .cache/dev logs")
                time.sleep(0.5)
        finally:
            READY.unlink(missing_ok=True)
            for _, child in children:
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGTERM)
            deadline = time.monotonic() + 40
            for _, child in children:
                try:
                    child.wait(timeout=max(0.1, deadline - time.monotonic()))
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait()
            PID.unlink(missing_ok=True)


def start():
    if supervisor_pid():
        print("Already running. Use scripts/dev.py restart to rebuild the backend.")
        return
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    READY.unlink(missing_ok=True)
    with (STATE / "supervisor.log").open("ab") as log:
        child = subprocess.Popen([sys.executable, str(SCRIPT), "run"], cwd=ROOT,
                                 stdout=log, stderr=log, start_new_session=True)
    for _ in range(240):
        if child.poll() is not None:
            raise RuntimeError("Startup failed; see .cache/dev/supervisor.log (may contain private diagnostics)")
        if READY.exists():
            print("Native development ready: " + json.loads(READY.read_text())["url"])
            print("Logs: .cache/dev/{api,worker,web}.log")
            return
        time.sleep(0.5)
    child.terminate()
    raise RuntimeError("Startup timed out; supervisor is shutting down")


if __name__ == "__main__":
    os.umask(0o077)
    action = sys.argv[1] if len(sys.argv) == 2 else "help"
    try:
        if action == "run":
            supervise()
        elif action == "start":
            start()
        elif action == "stop":
            stop()
        elif action == "restart":
            stop()
            start()
        elif action == "status":
            print(READY.read_text() if supervisor_pid() and READY.exists() else "Stopped or starting")
        else:
            print("Usage: scripts/dev.py {start|stop|restart|status}")
            sys.exit(2)
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
