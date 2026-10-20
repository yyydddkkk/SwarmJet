#!/usr/bin/env python3
"""Install, upgrade and operate a single-host SwarmJet deployment.

From a repository checkout:

    python3 deploy/install.py

On a fresh machine (no checkout; pulls the published images):

    curl -fsSL https://raw.githubusercontent.com/yyydddkkk/SwarmJet/main/deploy/install.py | python3 -

Set SWARMJET_REF (or --ref) to download the Compose files from a branch or
tag other than main, for example `SWARMJET_REF=dev` before the first release.

The script only needs Python 3 (standard library) and Docker with the Compose
plugin. It never prints generated passwords unless --show-password is passed.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import shlex
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

REPOSITORY = "yyydddkkk/SwarmJet"
REGISTRY = "ghcr.io/yyydddkkk/swarmjet"
RAW_BASE = f"https://raw.githubusercontent.com/{REPOSITORY}"
DEFAULT_VERSION = "latest"
DEFAULT_REF = "main"
TUNNEL_URL = "http://tunnel:2000"
DEFAULT_PORT = 8180
DEFAULT_PROJECT = "swarmjet"
MIN_DOCKER = (24, 0)
MIN_COMPOSE = (2, 20)
COMMANDS = ("install", "upgrade", "status", "connect", "logs", "stop", "uninstall")

# omp auth broker/gateway state lives under DATA_DIR/omp. install.py creates
# both bearer token files (0600) before the services start, and preserves them
# on later runs so upgrading does not invalidate a running broker.
LLM_CONFIG_SUBDIR = "omp"
LLM_TOKEN_FILES = ("auth-broker.token", "auth-gateway.token")

# Host-side files used by a standalone installation. Repository installs use
# the checkout in place.
SOURCES = (
    ("deploy/compose.yaml", "compose.yaml"),
    ("deploy/compose.cells.yaml", "compose.cells.yaml"),
    ("deploy/install.py", "install.py"),
    ("deploy/observability/prometheus.yml", "observability/prometheus.yml"),
    ("deploy/observability/alerts.yml", "observability/alerts.yml"),
    ("deploy/observability/alertmanager.yml", "observability/alertmanager.yml"),
)

# Order and comments for the generated .env. Every key is optional to Docker
# Compose, but the first six are required by the application.
ENV_ORDER = (
    ("COMPOSE_PROJECT_NAME", "Compose project name; also the network prefix."),
    ("POSTGRES_PASSWORD", "Generated database password."),
    ("DATABASE_URL", "Database URL; must contain POSTGRES_PASSWORD."),
    ("ADMIN_USERNAME", "The single administrator account."),
    ("ADMIN_PASSWORD", "Generated administrator password (at least 12 characters)."),
    ("SWARMJET_MASTER_KEY", "32-byte hex key encrypting GitHub App credentials. Back it up."),
    ("DATA_DIR", "Absolute host path; the same path is mounted in the api container."),
    ("IMAGE", "Platform image."),
    ("AGENT_IMAGE", "Agent image used by Forager Cells."),
    ("OMP_VERSION", "Pinned omp release shared by the Agent, broker and gateway."),
    ("AGENT_CLIS", "Optional extra CLIs baked into a locally built Agent image."),
    ("PUBLIC_ORIGIN", "Console origin used for cookies and the GitHub App redirect."),
    ("LISTEN_HOST", "Host interface the console binds to."),
    ("HTTP_PORT", "Host port of the console."),
    ("SECURE_COOKIES", "Set true when the console is served over HTTPS."),
    ("SWARMJET_CELLS", "1 when the compose.cells.yaml overlay is active."),
    ("SWARMJET_DEFAULT_MODEL", "Default model used by Forager Cells."),
    ("SWARMJET_CELL_MCP_URL", "MCP URL reachable from inside a Cell."),
    ("SWARMJET_CELL_LLM_URL", "Model proxy URL; empty derives it from the MCP URL."),
    ("SWARMJET_CELL_NETWORK", "Docker network joined by Cells; empty means <project>_default."),
    ("SWARMJET_LLM_GATEWAY_URL", "Internal auth-gateway URL; compose.cells.yaml supplies it."),
    ("SWARMJET_AUTH_BROKER_URL", "Internal auth-broker URL; compose.cells.yaml supplies it."),
    ("GITHUB_APP_ID", "Optional manual GitHub App fallback (env wins over the database)."),
    ("GITHUB_APP_PRIVATE_KEY_FILE", "Optional container-visible PEM path."),
    ("GITHUB_WEBHOOK_SECRET", "Optional manual webhook secret."),
    ("SWARMJET_TUNNEL_URL", "Tunnel metrics URL; set only when the tunnel profile runs."),
    ("TUNNEL_METRICS_PORT", "Loopback port for the tunnel metrics endpoint."),
    ("PROMETHEUS_PORT", "Loopback port for the optional Prometheus UI."),
    ("ALERTMANAGER_PORT", "Loopback port for the optional Alertmanager UI."),
)

PLACEHOLDERS = ("replace-with-", "change-me", "changeme")


class InstallError(RuntimeError):
    """A user-facing deployment error."""


def say(message: str = "") -> None:
    print(message, flush=True)


def warn(message: str) -> None:
    print(f"警告：{message}", file=sys.stderr, flush=True)


def run(argv, cwd=None, capture=False, check=True, timeout=None):
    """Run a command without a shell; never log generated secrets."""
    try:
        result = subprocess.run(
            argv,
            cwd=str(cwd) if cwd else None,
            text=True,
            check=False,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
            timeout=timeout,
        )
    except FileNotFoundError as exc:
        raise InstallError(f"命令不存在：{argv[0]}") from exc
    except subprocess.TimeoutExpired as exc:
        raise InstallError(f"命令超时：{' '.join(argv)}") from exc
    if check and result.returncode != 0:
        detail = ((result.stderr or "") + (result.stdout or "")).strip()
        message = f"命令失败（{result.returncode}）：{' '.join(argv)}"
        if detail:
            message = f"{message}\n{detail}"
        raise InstallError(message)
    return result


def script_dir() -> Path:
    file = globals().get("__file__", "")
    if not file or file in ("-", "<stdin>"):
        return Path.cwd().resolve()
    return Path(file).resolve().parent


def is_repo_directory(directory: Path) -> bool:
    directory = Path(directory)
    return (directory / "compose.yaml").is_file() and (directory.parent / "go.mod").is_file()


def resolve_directory(args) -> Path:
    if getattr(args, "dir", None):
        return Path(args.dir).expanduser().resolve()
    here = script_dir()
    if is_repo_directory(here):
        return here
    return (Path.home() / "swarmjet").resolve()


def parse_version(text: str):
    numbers = re.findall(r"\d+", text or "")
    if not numbers:
        return ()
    return tuple(int(part) for part in numbers[:3])


def check_docker() -> None:
    if not shutil.which("docker"):
        raise InstallError(
            "未找到 Docker。请先安装 Docker Engine 和 Compose 插件：https://docs.docker.com/engine/install/"
        )
    compose = run(["docker", "compose", "version", "--short"], capture=True, check=False)
    if compose.returncode != 0:
        raise InstallError("未找到 Docker Compose 插件；请安装 docker-compose-plugin。")
    if parse_version(compose.stdout) < MIN_COMPOSE:
        raise InstallError(f"Docker Compose 版本过低（{compose.stdout.strip()}）；至少需要 2.20。")
    info = run(["docker", "info"], capture=True, check=False)
    if info.returncode != 0:
        detail = (info.stderr or info.stdout or "").strip()
        raise InstallError("无法连接 Docker daemon；请确认 Docker 正在运行并且当前用户有权限。" + (f"\n{detail}" if detail else ""))
    version = run(["docker", "version", "--format", "{{.Server.Version}}"], capture=True, check=False)
    if version.returncode != 0 or parse_version(version.stdout) < MIN_DOCKER:
        raise InstallError(f"Docker 版本过低（{(version.stdout or '?').strip()}）；至少需要 24.0。")


def _missing(value) -> bool:
    if value is None:
        return True
    text = str(value).strip()
    if text == "":
        return True
    lowered = text.lower()
    return any(lowered.startswith(prefix) for prefix in PLACEHOLDERS)


def read_env(path: Path) -> dict:
    values = {}
    try:
        lines = Path(path).read_text().splitlines()
    except FileNotFoundError:
        return values
    for raw in lines:
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        if key.startswith("export "):
            key = key[len("export ") :].strip()
        values[key] = value.strip().strip('"').strip("'")
    return values


def atomic_write(path: Path, text: str, mode: int = 0o600) -> None:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".env.", dir=str(path.parent))
    try:
        with os.fdopen(descriptor, "w") as stream:
            stream.write(text)
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def write_env(path: Path, values: dict) -> None:
    """Write .env atomically, preserving comments and unknown keys."""
    path = Path(path)
    if path.exists():
        lines = path.read_text().splitlines()
        present = set()
        output = []
        for line in lines:
            stripped = line.strip()
            if stripped and not stripped.startswith("#") and "=" in stripped:
                key = stripped.split("=", 1)[0].strip()
                if key.startswith("export "):
                    key = key[len("export ") :].strip()
                if key in values:
                    output.append(f"{key}={values.get(key, '')}")
                    present.add(key)
                    continue
            output.append(line)
        missing = [(key, comment) for key, comment in ENV_ORDER if key not in present]
        if missing:
            output.append("")
            output.append("# Added by install.py")
            for key, comment in missing:
                if comment:
                    output.append(f"# {comment}")
                output.append(f"{key}={values.get(key, '')}")
    else:
        output = [
            "# SwarmJet deployment configuration; generated by deploy/install.py.",
            "# Keep this file mode 0600. Back up SWARMJET_MASTER_KEY separately.",
        ]
        for key, comment in ENV_ORDER:
            if comment:
                output.append(f"# {comment}")
            output.append(f"{key}={values[key]}")
    atomic_write(path, "\n".join(output).rstrip() + "\n", 0o600)


def ensure_env_mode(path: Path) -> None:
    if not path.exists():
        return
    mode = path.stat().st_mode & 0o777
    if mode & 0o077:
        os.chmod(path, 0o600)
        warn(f"{path} 对同组或其他用户可读，已收紧为 0600。")


def ensure_llm_tokens(data_dir: str) -> Path:
    """Create the broker/gateway bearer tokens under DATA_DIR/omp.

    Re-running install or upgrade must not rotate a token that a running
    service already loaded, so existing non-empty files are kept and only
    their permissions are tightened. No token is ever printed.
    """
    validate_data_dir(data_dir)
    config_dir = Path(data_dir) / LLM_CONFIG_SUBDIR
    try:
        config_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    except OSError as exc:
        raise InstallError(f"无法创建模型凭证配置目录 {config_dir}：{exc}") from exc
    if not config_dir.is_dir():
        raise InstallError(f"模型凭证配置路径不是目录：{config_dir}")
    if (config_dir.stat().st_mode & 0o077) != 0:
        try:
            os.chmod(config_dir, 0o700)
        except OSError as exc:
            raise InstallError(f"无法收紧 {config_dir} 的权限：{exc}") from exc
    for name in LLM_TOKEN_FILES:
        path = config_dir / name
        if path.exists():
            try:
                value = path.read_text().strip()
            except OSError as exc:
                raise InstallError(f"无法读取 {path}：{exc}") from exc
            if value:
                if (path.stat().st_mode & 0o077) != 0:
                    os.chmod(path, 0o600)
                    warn(f"{path} 对同组或其他用户可读，已收紧为 0600。")
                continue
        atomic_write(path, secrets.token_hex(32), 0o600)
    return config_dir


def image_values(source: str, version: str):
    if source == "ghcr":
        return f"{REGISTRY}:{version}", f"{REGISTRY}/agent:{version}"
    return "swarmjet:local", "swarmjet-agent:local"


def tag_from_image(image: str) -> str:
    if not image or "@" in image:
        return ""
    if ":" in image.rsplit("/", 1)[-1]:
        return image.rsplit(":", 1)[-1]
    return ""


def build_values(
    existing: dict,
    *,
    data_dir,
    port,
    listen_host,
    public_origin,
    cells,
    tunnel,
    image,
    agent_image,
    admin_username,
    overrides=None,
) -> dict:
    overrides = overrides or {}
    values = {}
    for key, _ in ENV_ORDER:
        if key in overrides:
            values[key] = overrides[key]
        elif key in existing and not _missing(existing[key]):
            values[key] = existing[key]

    # Round 4 replaced OpenCode with omp, which the Agent image always
    # installs. Older .env files listed opencode here, which the image build
    # now rejects, so drop it on upgrade.
    if "AGENT_CLIS" in values:
        kept = [c.strip() for c in values["AGENT_CLIS"].split(",") if c.strip() and c.strip() != "opencode"]
        values["AGENT_CLIS"] = ",".join(kept)

    password = values.get("POSTGRES_PASSWORD") or secrets.token_hex(24)
    values["POSTGRES_PASSWORD"] = password
    if _missing(values.get("DATABASE_URL")) or "replace-with-" in values.get("DATABASE_URL", ""):
        values["DATABASE_URL"] = (
            f"postgres://swarmjet:{password}@postgres:5432/swarmjet?sslmode=disable"
        )
    if _missing(values.get("ADMIN_PASSWORD")):
        values["ADMIN_PASSWORD"] = secrets.token_hex(24)
    if _missing(values.get("SWARMJET_MASTER_KEY")):
        values["SWARMJET_MASTER_KEY"] = secrets.token_hex(32)
    defaults = {
        "COMPOSE_PROJECT_NAME": DEFAULT_PROJECT,
        "ADMIN_USERNAME": admin_username,
        "DATA_DIR": str(data_dir),
        "IMAGE": image,
        "AGENT_IMAGE": agent_image,
        "OMP_VERSION": "18.12.2",
        "AGENT_CLIS": "",
        "PUBLIC_ORIGIN": public_origin,
        "LISTEN_HOST": listen_host,
        "HTTP_PORT": str(port),
        "SECURE_COOKIES": "false",
        "SWARMJET_CELLS": "1" if cells else "0",
        "SWARMJET_DEFAULT_MODEL": "deepseek/deepseek-chat",
        "SWARMJET_CELL_MCP_URL": "http://swarmjet:8080/mcp",
        "SWARMJET_CELL_LLM_URL": "",
        "SWARMJET_CELL_NETWORK": "",
        "SWARMJET_LLM_GATEWAY_URL": "",
        "SWARMJET_AUTH_BROKER_URL": "",
        "GITHUB_APP_ID": "",
        "GITHUB_APP_PRIVATE_KEY_FILE": "",
        "GITHUB_WEBHOOK_SECRET": "",
        "SWARMJET_TUNNEL_URL": TUNNEL_URL if tunnel else "",
        "TUNNEL_METRICS_PORT": "2000",
        "PROMETHEUS_PORT": "9090",
        "ALERTMANAGER_PORT": "9093",
    }
    for key, fallback in defaults.items():
        if _missing(values.get(key)):
            values[key] = fallback
    return values


def ask_text(prompt: str, default, yes: bool, cast=str):
    if yes or not sys.stdin.isatty():
        return default
    answer = input(f"{prompt} [{default}]: ").strip()
    if not answer:
        return default
    try:
        return cast(answer)
    except ValueError as exc:
        raise InstallError(f"无效输入：{answer}") from exc


def ask_yes(prompt: str, default: bool, yes: bool) -> bool:
    if yes or not sys.stdin.isatty():
        return default
    suffix = "Y/n" if default else "y/N"
    answer = input(f"{prompt} [{suffix}]: ").strip().lower()
    if not answer:
        return default
    if answer in ("y", "yes"):
        return True
    if answer in ("n", "no"):
        return False
    raise InstallError(f"无效输入：{answer}")


def ask_choice(prompt: str, choices, default: str, yes: bool) -> str:
    if yes or not sys.stdin.isatty():
        return default
    answer = input(f"{prompt} ({'/'.join(choices)}) [{default}]: ").strip().lower() or default
    if answer not in choices:
        raise InstallError(f"无效选择：{answer}")
    return answer


def validate_data_dir(value: str) -> None:
    if not value:
        raise InstallError("DATA_DIR 不能为空。")
    if not os.path.isabs(value):
        raise InstallError("DATA_DIR 必须是绝对路径。")
    if re.search(r"[\s$#'\"\\]", value):
        raise InstallError("DATA_DIR 不能包含空白、引号、反斜杠或 $#' 等 dotenv 特殊字符。")


def ensure_data_dir(value: str) -> None:
    validate_data_dir(value)
    path = Path(value)
    try:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    except OSError as exc:
        raise InstallError(f"无法创建数据目录 {path}：{exc}") from exc
    if not path.is_dir():
        raise InstallError(f"DATA_DIR 不是目录：{path}")
    if not os.access(path, os.W_OK):
        raise InstallError(f"数据目录不可写：{path}（当前用户 {os.getuid()}）")


def port_available(host: str, port: int) -> bool:
    family = socket.AF_INET6 if ":" in host else socket.AF_INET
    with socket.socket(family, socket.SOCK_STREAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            sock.bind((host, port))
        except OSError:
            return False
    return True


def stack_running(project: str) -> bool:
    result = run(["docker", "ps", "--format", "{{.Names}}"], capture=True, check=False)
    names = (result.stdout or "").split()
    return any(name.startswith(f"{project}-") or name == project for name in names)


def volume_exists(name: str) -> bool:
    result = run(["docker", "volume", "inspect", name], capture=True, check=False)
    return result.returncode == 0


def compose_files(directory: Path, values: dict):
    files = [directory / "compose.yaml"]
    if values.get("SWARMJET_CELLS") == "1":
        files.append(directory / "compose.cells.yaml")
    return files


def compose_base(directory: Path, values: dict, extra_profiles=(), build: bool = False):
    argv = [
        "docker",
        "compose",
        "--project-directory",
        str(directory),
        "--env-file",
        str(directory / ".env"),
    ]
    for path in compose_files(directory, values):
        argv += ["-f", str(path)]
    if build:
        argv += ["-f", str(directory / "compose.build.yaml")]
    profiles = []
    if values.get("SWARMJET_TUNNEL_URL"):
        profiles.append("tunnel")
    profiles.extend(extra_profiles)
    for profile in dict.fromkeys(profiles):
        argv += ["--profile", profile]
    return argv


def download(url: str, destination: Path) -> None:
    say(f"下载 {url}")
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with urllib.request.urlopen(url, timeout=30) as response:
        payload = response.read()
    temporary = destination.with_name(f".{destination.name}.tmp")
    temporary.write_bytes(payload)
    os.chmod(temporary, 0o644)
    os.replace(temporary, destination)


def is_release_ref(ref: str) -> bool:
    return bool(re.match(r"^v\d", ref or ""))


def resolve_ref(explicit, version=None) -> str:
    """Pick the Git ref used for raw files: --ref, SWARMJET_REF, then main."""
    ref = (explicit or os.environ.get("SWARMJET_REF", "")).strip()
    if ref:
        return ref
    if version and version != DEFAULT_VERSION:
        return version
    return DEFAULT_REF


def is_quick_tunnel(value: str) -> bool:
    return ".trycloudflare.com" in (value or "")


def local_origin(port) -> str:
    return f"http://localhost:{port}"


def console_origin(existing: str, port) -> str:
    """Keep a configured origin, but never a quick-tunnel address.

    The tunnel only carries GitHub webhooks. Its address changes whenever the
    tunnel restarts, and the console rejects logins whose Origin differs from
    PUBLIC_ORIGIN, so a quick-tunnel origin would lock the administrator out.
    Older installers wrote it; repair it to the local console address.
    """
    if existing and not is_quick_tunnel(existing):
        return existing
    return local_origin(port)


def ensure_sources(directory: Path, ref: str, overwrite: bool = False) -> None:
    ref = ref or DEFAULT_REF
    for remote, local in SOURCES:
        target = directory / local
        if target.exists() and not overwrite:
            continue
        download(f"{RAW_BASE}/{ref}/{remote}", target)


def copy_sources(source: Path, destination: Path) -> None:
    for _, local in SOURCES:
        origin = source / local
        target = destination / local
        if not origin.exists():
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(origin, target)


def prepare_sources(directory: Path, ref: str, repo: bool) -> None:
    if repo:
        return
    here = script_dir()
    if here != directory and is_repo_directory(here):
        copy_sources(here, directory)
    ensure_sources(directory, ref)


def wait_for_tunnel(values: dict, timeout: float = 30.0):
    port = values.get("TUNNEL_METRICS_PORT") or "2000"
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/quicktunnel", timeout=2) as response:
                data = json.load(response)
            host = data.get("hostname") or data.get("url")
            if host:
                return host if str(host).startswith("https://") else f"https://{host}"
        except Exception:
            time.sleep(1)
            continue
        time.sleep(1)
    return None


def warn_legacy(project: str) -> None:
    names = run(["docker", "ps", "-a", "--format", "{{.Names}}"], capture=True, check=False)
    containers = [
        name
        for name in (names.stdout or "").split()
        if name.startswith(f"{project}-")
        and any(tag in name for tag in ("temporal", "worker", "node", "mihomo"))
    ]
    volumes_result = run(["docker", "volume", "ls", "--format", "{{.Name}}"], capture=True, check=False)
    volumes = [
        name
        for name in (volumes_result.stdout or "").split()
        if name.startswith(f"{project}_")
        and any(tag in name for tag in ("temporal", "mihomo", "node"))
    ]
    if containers or volumes:
        warn("检测到上一轮部署留下的容器或数据卷；安装脚本不会自动删除它们。")
        for name in containers:
            warn(f"  容器：{name}")
        for name in volumes:
            warn(f"  数据卷：{name}")
        warn("确认不再需要后，用 `docker rm` / `docker volume rm` 手工清理。")


def self_hint() -> str:
    name = sys.argv[0]
    if not name or name in ("-",):
        return "install.py"
    return name


def print_summary(
    values: dict,
    env_path: Path,
    directory: Path,
    *,
    action: str,
    show_password: bool = False,
    tunnel_url=None,
) -> None:
    say()
    say(f"SwarmJet {action}完成。")
    say(f"控制台：{values.get('PUBLIC_ORIGIN') or local_origin(values.get('HTTP_PORT'))}")
    say(f"管理员：{values.get('ADMIN_USERNAME')}")
    if show_password:
        say(f"密码：{values.get('ADMIN_PASSWORD')}")
    else:
        say(f"密码：保存在 {env_path} 的 ADMIN_PASSWORD（加 --show-password 可显示）")
    say("下一步：打开控制台的“接入”页，一键创建 GitHub App；再为 Claude Code / Codex 创建连接。")
    say(f"数据目录：{values.get('DATA_DIR')}；SWARMJET_MASTER_KEY 请单独备份。")
    if tunnel_url:
        say(f"Webhook 隧道：{tunnel_url}（只用于接收 GitHub webhook，平台会自动同步到 GitHub App；临时地址，重启后可能变化。请用上面的控制台地址登录）")
    say(f"管理命令：python3 {self_hint()} status --dir {directory}")


def install(args) -> int:
    check_docker()
    directory = resolve_directory(args)
    repo = is_repo_directory(script_dir()) and (
        args.dir is None or Path(args.dir).expanduser().resolve() == script_dir()
    )
    if not repo and args.image_source == "repo":
        raise InstallError("当前不在仓库中运行，无法本地构建；请改用 --image-source ghcr。")
    if directory != script_dir():
        directory.mkdir(parents=True, exist_ok=True)
    env_path = directory / ".env"
    existing = read_env(env_path)
    if existing:
        say(f"检测到已有配置 {env_path}；已有的值不会被覆盖。")

    default_data = existing.get("DATA_DIR") or str(Path.home() / "swarmjet" / "data")
    data_dir = args.data_dir or ask_text("数据目录", default_data, args.yes)
    default_port = int(existing.get("HTTP_PORT") or DEFAULT_PORT) if str(existing.get("HTTP_PORT") or DEFAULT_PORT).isdigit() else DEFAULT_PORT
    port = args.port or ask_text("控制台端口", default_port, args.yes, int)
    listen_host = args.listen_host or existing.get("LISTEN_HOST") or "127.0.0.1"

    if args.cells is not None:
        cells = args.cells
    elif existing.get("SWARMJET_CELLS") in ("0", "1"):
        cells = existing["SWARMJET_CELLS"] == "1"
    else:
        cells = ask_yes("启用 Forager Cell？（会挂载宿主 Docker socket，等同 root 权限）", True, args.yes)

    if args.tunnel is not None:
        tunnel = args.tunnel
    elif existing.get("SWARMJET_TUNNEL_URL"):
        tunnel = True
    else:
        tunnel = ask_yes(
            "启用 Cloudflare webhook 隧道？（服务器有公网域名时选否，并填写 PUBLIC_ORIGIN）",
            True,
            args.yes,
        )

    if args.image_source:
        source = args.image_source
    elif str(existing.get("IMAGE", "")).startswith("ghcr.io/"):
        source = "ghcr"
    elif existing.get("IMAGE"):
        source = "repo"
    else:
        source = "repo" if repo else "ghcr"
    source = ask_choice("镜像来源", ("repo", "ghcr"), source, args.yes)
    if source == "repo" and not repo:
        raise InstallError("当前不在仓库中运行，无法本地构建；请改用 --image-source ghcr。")

    ref = resolve_ref(args.ref, args.version)
    version = args.version
    if source == "ghcr":
        version = version or (ref if is_release_ref(ref) else "") or tag_from_image(existing.get("IMAGE", "")) or DEFAULT_VERSION
        version = ask_text("镜像版本", version, args.yes)
    image, agent_image = image_values(source, version or DEFAULT_VERSION)
    explicit_images = bool(args.image or args.agent_image or args.image_source or args.version)
    if not explicit_images and not _missing(existing.get("IMAGE")):
        image = existing["IMAGE"]
        agent_image = existing.get("AGENT_IMAGE") or agent_image
    if args.image:
        image = args.image
    if args.agent_image:
        agent_image = args.agent_image

    admin_username = args.admin_username or existing.get("ADMIN_USERNAME") or "admin"
    if args.public_origin:
        public_origin = args.public_origin
    elif existing.get("PUBLIC_ORIGIN"):
        public_origin = console_origin(existing["PUBLIC_ORIGIN"], port)
    elif tunnel:
        # The tunnel is for webhooks only; the console stays on this host.
        public_origin = local_origin(port)
    else:
        public_origin = ask_text("控制台公开地址", local_origin(port), args.yes)

    overrides = {}
    if args.data_dir:
        overrides["DATA_DIR"] = args.data_dir
    if args.port:
        overrides["HTTP_PORT"] = str(args.port)
    if args.listen_host:
        overrides["LISTEN_HOST"] = args.listen_host
    if args.project_name:
        overrides["COMPOSE_PROJECT_NAME"] = args.project_name
    if args.public_origin:
        overrides["PUBLIC_ORIGIN"] = args.public_origin
    elif is_quick_tunnel(existing.get("PUBLIC_ORIGIN", "")):
        # Existing values win in build_values, so the repair must be explicit.
        overrides["PUBLIC_ORIGIN"] = public_origin
    if args.admin_username:
        overrides["ADMIN_USERNAME"] = args.admin_username
    if args.cells is not None:
        overrides["SWARMJET_CELLS"] = "1" if args.cells else "0"
    if args.tunnel is not None:
        overrides["SWARMJET_TUNNEL_URL"] = TUNNEL_URL if args.tunnel else ""
    if explicit_images:
        overrides["IMAGE"] = image
        overrides["AGENT_IMAGE"] = agent_image

    values = build_values(
        existing,
        data_dir=data_dir,
        port=port,
        listen_host=listen_host,
        public_origin=public_origin,
        cells=cells,
        tunnel=tunnel,
        image=image,
        agent_image=agent_image,
        admin_username=admin_username,
        overrides=overrides,
    )

    ensure_data_dir(values["DATA_DIR"])
    if values.get("SWARMJET_CELLS") == "1":
        ensure_llm_tokens(values["DATA_DIR"])
        say("模型凭证服务 Token 已就绪（DATA_DIR/omp，权限 0600）。")
    ensure_env_mode(env_path)
    project = values.get("COMPOSE_PROJECT_NAME") or DEFAULT_PROJECT
    if not existing and volume_exists(f"{project}_postgres-data"):
        raise InstallError(
            f"检测到已有 PostgreSQL 数据卷 {project}_postgres-data，但当前没有 .env；"
            "新生成的数据库密码无法打开它。请恢复原 .env，或用 --project-name 换一个项目名（不会删除旧数据卷）。"
        )
    if not port_available(values.get("LISTEN_HOST", "127.0.0.1"), int(values["HTTP_PORT"])):
        if not stack_running(project):
            raise InstallError(
                f"端口 {values['LISTEN_HOST']}:{values['HTTP_PORT']} 已被占用；请用 --port 换一个端口。"
            )
    write_env(env_path, values)
    say(f"配置已写入 {env_path}（权限 0600）。")
    prepare_sources(directory, ref, repo)

    if source == "repo":
        say("构建平台与 Agent 镜像……")
        run(compose_base(directory, values, extra_profiles=("tools",), build=True) + ["build"])
    else:
        say(f"拉取镜像 {values['IMAGE']} / {values['AGENT_IMAGE']}……")
        run(compose_base(directory, values) + ["pull"])

    tunnel_url = None
    if values.get("SWARMJET_TUNNEL_URL"):
        say("启动 Cloudflare 隧道并读取公网地址……")
        try:
            run(compose_base(directory, values) + ["up", "-d", "--no-deps", "tunnel"])
            tunnel_url = wait_for_tunnel(values)
        except InstallError as exc:
            warn(f"隧道未能启动，将改用本机控制台地址：{exc}")
            values["SWARMJET_TUNNEL_URL"] = ""
            tunnel = False
            write_env(env_path, values)
        else:
            # The tunnel address is only used for GitHub webhooks; the platform
            # syncs it to the GitHub App itself. PUBLIC_ORIGIN stays local.
            if tunnel_url:
                say(f"隧道地址为 {tunnel_url}（仅用于接收 GitHub webhook）。")

    say("启动服务并等待健康检查……")
    run(compose_base(directory, values) + ["up", "-d", "--wait", "--wait-timeout", "300"])
    warn_legacy(project)
    print_summary(
        values,
        env_path,
        directory,
        action="安装",
        show_password=args.show_password,
        tunnel_url=tunnel_url,
    )
    return 0


def upgrade(args) -> int:
    check_docker()
    directory = resolve_directory(args)
    env_path = directory / ".env"
    if not env_path.exists():
        raise InstallError(f"未找到 {env_path}；请先运行 install。")
    values = read_env(env_path)
    if is_quick_tunnel(values.get("PUBLIC_ORIGIN", "")):
        values["PUBLIC_ORIGIN"] = console_origin(values["PUBLIC_ORIGIN"], values.get("HTTP_PORT", DEFAULT_PORT))
        say(f"PUBLIC_ORIGIN 曾被设为临时隧道地址，已改回 {values['PUBLIC_ORIGIN']}。")
    repo = is_repo_directory(script_dir()) and directory == script_dir().resolve()
    source = args.image_source or ("repo" if repo else "ghcr")
    if source == "repo" and not repo:
        raise InstallError("当前不在仓库中运行，无法本地构建；请改用 --image-source ghcr。")
    if source == "ghcr":
        ref = resolve_ref(args.ref, args.version)
        version = args.version or (ref if is_release_ref(ref) else "") or tag_from_image(values.get("IMAGE", "")) or DEFAULT_VERSION
        if not repo:
            ensure_sources(directory, ref, overwrite=True)
        values["IMAGE"] = f"{REGISTRY}:{version}"
        values["AGENT_IMAGE"] = f"{REGISTRY}/agent:{version}"
    else:
        values["IMAGE"] = "swarmjet:local"
        values["AGENT_IMAGE"] = "swarmjet-agent:local"
    write_env(env_path, values)
    if values.get("SWARMJET_CELLS") == "1" and values.get("DATA_DIR"):
        ensure_llm_tokens(values["DATA_DIR"])
        say("模型凭证服务 Token 已就绪（DATA_DIR/omp，权限 0600）。")

    if source == "repo":
        say("重新构建平台与 Agent 镜像……")
        run(compose_base(directory, values, extra_profiles=("tools",), build=True) + ["build"])
    else:
        say("拉取新镜像……")
        run(compose_base(directory, values) + ["pull"])
    say("更新服务并等待健康检查……")
    run(compose_base(directory, values) + ["up", "-d", "--wait", "--wait-timeout", "300"])
    warn_legacy(values.get("COMPOSE_PROJECT_NAME") or DEFAULT_PROJECT)
    print_summary(values, env_path, directory, action="升级", show_password=args.show_password)
    return 0


def status(args) -> int:
    directory = resolve_directory(args)
    env_path = directory / ".env"
    if not env_path.exists():
        raise InstallError(f"未找到 {env_path}；请先运行 install。")
    values = read_env(env_path)
    compose = compose_base(directory, values)
    run(compose + ["ps"])
    identifiers = run(compose + ["ps", "-q", "api"], capture=True, check=False).stdout.split()
    if identifiers:
        health = run(
            ["docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", identifiers[0]],
            capture=True,
            check=False,
        )
        say(f"api 健康：{(health.stdout or 'unknown').strip()}")
    say(f"控制台：{values.get('PUBLIC_ORIGIN') or local_origin(values.get('HTTP_PORT'))}")
    if values.get("SWARMJET_TUNNEL_URL"):
        say("隧道：已启用（公网地址可在控制台“接入”页查看）")
    return 0


def logs(args) -> int:
    directory = resolve_directory(args)
    values = read_env(directory / ".env")
    argv = compose_base(directory, values) + ["logs", "--tail", str(args.tail)]
    if args.follow:
        argv.append("-f")
    argv.extend(args.service)
    run(argv)
    return 0


def stop(args) -> int:
    directory = resolve_directory(args)
    values = read_env(directory / ".env")
    run(compose_base(directory, values) + ["stop"])
    say("服务已停止，容器、数据卷和 DATA_DIR 都保留。")
    return 0


def _safe_remove_data_dir(raw: str, directory: Path) -> None:
    path = Path(raw).expanduser().resolve()
    home = Path.home().resolve()
    if len(path.parts) < 3 or path == Path("/"):
        raise InstallError(f"拒绝删除过短或根路径：{path}")
    if path == home or path in home.parents:
        raise InstallError(f"拒绝删除主目录或其上级：{path}")
    if path == directory.resolve():
        raise InstallError(f"拒绝删除安装目录本身：{path}")
    if path.exists():
        shutil.rmtree(path)
        say(f"已删除数据目录：{path}")


def uninstall(args) -> int:
    directory = resolve_directory(args)
    env_path = directory / ".env"
    if not env_path.exists() and not (directory / "compose.yaml").exists():
        raise InstallError(f"未找到部署配置：{directory}")
    values = read_env(env_path)
    compose = compose_base(directory, values)
    if not args.purge:
        run(compose + ["down"])
        say("已删除容器和网络；数据卷、DATA_DIR 与 .env 保留。")
        say("需要彻底删除时重新运行：uninstall --purge")
        return 0

    data_dir = values.get("DATA_DIR", "")
    say("--purge 会删除 PostgreSQL 数据卷")
    if data_dir:
        say(f"以及数据目录 {data_dir}")
    if not args.yes:
        if not sys.stdin.isatty():
            raise InstallError("非交互环境删除数据需要显式加 --yes。")
        answer = input('这会永久删除数据。输入 delete 确认：').strip()
        if answer != "delete":
            say("已取消；没有删除任何数据。")
            return 0
    run(compose + ["down", "-v"])
    if data_dir:
        _safe_remove_data_dir(data_dir, directory)
    say("已卸载。.env 保留在 " + str(env_path) + "，确认不再需要后可手工删除。")
    return 0


def claude_command(output: str) -> str:
    for line in (output or "").splitlines():
        line = line.strip()
        if line.startswith("claude mcp add"):
            return line
    return ""


def local_mcp_url(values: dict) -> str:
    """MCP endpoint of this install as reachable from the host.

    swarmjet connect runs inside the api container, where it cannot know the
    published host port, so the installer passes it explicitly.
    """
    host = (values.get("LISTEN_HOST") or "127.0.0.1").strip()
    if host in ("", "0.0.0.0", "::", "[::]"):
        host = "127.0.0.1"
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    port = values.get("HTTP_PORT") or DEFAULT_PORT
    return f"http://{host}:{port}/mcp"


def connect(args) -> int:
    directory = resolve_directory(args)
    values = read_env(directory / ".env")
    if not values:
        raise InstallError(f"未找到 {directory / '.env'}；请先运行 install。")
    argv = compose_base(directory, values) + [
        "exec",
        "-T",
        "api",
        "swarmjet",
        "connect",
        "--client",
        args.client,
    ]
    if args.name:
        argv += ["--name", args.name]
    argv += ["--url", args.url or local_mcp_url(values)]
    result = run(argv, capture=True)
    if result.stdout:
        say(result.stdout.rstrip())
    if result.stderr:
        print(result.stderr.rstrip(), file=sys.stderr)
    if args.client == "claude":
        command = claude_command(result.stdout)
        if command and shutil.which("claude"):
            if ask_yes("检测到本机 claude 命令，是否直接执行 claude mcp add？", True, args.yes):
                run(shlex.split(command))
        elif not command:
            warn("未能在命令输出中找到 claude mcp add；请手动复制上面的配置。")
    return 0


def add_bool(parser, name: str, destination: str, help_text: str) -> None:
    group = parser.add_mutually_exclusive_group()
    group.add_argument(f"--{name}", dest=destination, action="store_true", help=help_text)
    group.add_argument(f"--no-{name}", dest=destination, action="store_false")
    parser.set_defaults(**{destination: None})


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="install.py",
        description="Install, upgrade and operate a single-host SwarmJet deployment.",
    )
    subparsers = parser.add_subparsers(dest="command")

    install_parser = subparsers.add_parser("install", help="安装并启动（默认命令）")
    install_parser.add_argument("--dir", help="安装目录；仓库内默认为 deploy/，另装默认为 ~/swarmjet")
    install_parser.add_argument("--data-dir", help="DATA_DIR 绝对路径")
    install_parser.add_argument("--port", type=int, help="控制台端口")
    install_parser.add_argument("--listen-host", help="控制台监听地址")
    install_parser.add_argument("--public-origin", help="PUBLIC_ORIGIN，例如 https://swarmjet.example.com")
    install_parser.add_argument("--admin-username", help="管理员用户名")
    install_parser.add_argument("--project-name", help="Compose 项目名，默认 swarmjet；用于避开旧数据卷")
    install_parser.add_argument("--image-source", choices=("repo", "ghcr"), help="本地构建或拉取 GHCR 镜像")
    install_parser.add_argument("--version", help="GHCR 镜像版本，默认 latest")
    install_parser.add_argument(
        "--ref",
        help="下载 Compose 文件的 Git 分支或标签，默认 main；也可用 SWARMJET_REF",
    )
    install_parser.add_argument("--image", help="覆盖平台镜像")
    install_parser.add_argument("--agent-image", help="覆盖 Agent 镜像")
    add_bool(install_parser, "cells", "cells", "启用 Forager Cell（挂 Docker socket）")
    add_bool(install_parser, "tunnel", "tunnel", "启用 Cloudflare webhook 隧道")
    install_parser.add_argument("--yes", "-y", action="store_true", help="全部使用默认值，不提问")
    install_parser.add_argument("--show-password", action="store_true", help="在终端显示管理员密码")
    install_parser.set_defaults(func=install)

    upgrade_parser = subparsers.add_parser("upgrade", help="重新构建或拉取镜像并更新服务")
    upgrade_parser.add_argument("--dir")
    upgrade_parser.add_argument("--image-source", choices=("repo", "ghcr"))
    upgrade_parser.add_argument("--version")
    upgrade_parser.add_argument(
        "--ref",
        help="下载 Compose 文件的 Git 分支或标签，默认 main；也可用 SWARMJET_REF",
    )
    upgrade_parser.add_argument("--yes", "-y", action="store_true")
    upgrade_parser.add_argument("--show-password", action="store_true")
    upgrade_parser.set_defaults(func=upgrade)

    status_parser = subparsers.add_parser("status", help="查看服务和健康状态")
    status_parser.add_argument("--dir")
    status_parser.set_defaults(func=status)

    connect_parser = subparsers.add_parser("connect", help="为本地 Agent 生成 MCP 配置")
    connect_parser.add_argument("--dir")
    connect_parser.add_argument("--client", choices=("claude", "codex", "json"), default="claude")
    connect_parser.add_argument("--name")
    connect_parser.add_argument("--url")
    connect_parser.add_argument("--yes", "-y", action="store_true")
    connect_parser.set_defaults(func=connect)

    logs_parser = subparsers.add_parser("logs", help="查看服务日志")
    logs_parser.add_argument("--dir")
    logs_parser.add_argument("--tail", type=int, default=200)
    logs_parser.add_argument("--follow", "-f", action="store_true")
    logs_parser.add_argument("service", nargs="*")
    logs_parser.set_defaults(func=logs)

    stop_parser = subparsers.add_parser("stop", help="停止服务，保留数据")
    stop_parser.add_argument("--dir")
    stop_parser.set_defaults(func=stop)

    uninstall_parser = subparsers.add_parser("uninstall", help="删除容器；默认保留数据")
    uninstall_parser.add_argument("--dir")
    uninstall_parser.add_argument("--purge", action="store_true", help="同时删除数据卷和 DATA_DIR")
    uninstall_parser.add_argument("--yes", "-y", action="store_true")
    uninstall_parser.set_defaults(func=uninstall)

    return parser


def normalize_argv(arguments) -> list:
    arguments = list(arguments)
    if arguments[:1] in (["-h"], ["--help"]):
        return arguments
    if not arguments or arguments[0] not in COMMANDS:
        return ["install"] + arguments
    return arguments


def main(argv=None) -> int:
    parser = build_parser()
    arguments = normalize_argv(sys.argv[1:] if argv is None else argv)
    args = parser.parse_args(arguments)
    try:
        return args.func(args)
    except InstallError as exc:
        print(f"失败：{exc}", file=sys.stderr)
        return 1
    except (OSError, subprocess.SubprocessError, ValueError) as exc:
        print(f"失败：{exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
