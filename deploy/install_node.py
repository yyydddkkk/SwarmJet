#!/usr/bin/env python3
"""Install a SwarmJet Node from a private deployment bundle on its target host."""
import argparse
import json
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from urllib.parse import urlparse


HERE = Path(__file__).resolve().parent
IMAGE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,254}$")
NODE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$")
SAFE_ENV = re.compile(r"^[A-Za-z0-9_./:@-]+$")


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def read_config(path):
    config = json.loads(path.read_text())
    required = ("nodeId", "nodeSecret", "image", "dataDir", "advertiseUrl", "controlPlaneUrl")
    if any(not isinstance(config.get(key), str) or not config[key] for key in required):
        raise ValueError("部署包缺少必要配置")
    if not NODE_ID.fullmatch(config["nodeId"]) or not re.fullmatch(r"[0-9a-f]{64}", config["nodeSecret"]):
        raise ValueError("节点身份或注册凭据无效")
    if not IMAGE.fullmatch(config["image"]):
        raise ValueError("程序镜像地址无效")
    data = Path(config["dataDir"])
    if not data.is_absolute() or not SAFE_ENV.fullmatch(str(data)) or ".." in data.parts:
        raise ValueError("节点数据目录必须是安全的绝对路径")
    for key in ("advertiseUrl", "controlPlaneUrl"):
        url = urlparse(config[key])
        if url.scheme not in ("http", "https") or not url.hostname or url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
            raise ValueError(f"{key} 必须是无凭据的 HTTP(S) 入口地址")
        if not SAFE_ENV.fullmatch(config[key]):
            raise ValueError(f"{key} 含不支持的字符")
    capacity, port = config.get("capacity", 2), config.get("port", 8092)
    if type(capacity) is not int or not 1 <= capacity <= 128 or type(port) is not int or not 1 <= port <= 65535:
        raise ValueError("容量或端口无效")
    if config.get("listenHost", "0.0.0.0") not in ("0.0.0.0", "127.0.0.1"):
        raise ValueError("监听地址只支持内网接口或回环接口")
    if config.get("projectName", "swarmjet-" + config["nodeId"]) != "swarmjet-" + config["nodeId"]:
        raise ValueError("Compose 项目名无效")
    return config


def os_release():
    values = {}
    for line in Path("/etc/os-release").read_text().splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            values[key] = value.strip('"')
    return values


def docker_ready():
    if not shutil.which("docker"):
        return False
    return subprocess.run(["docker", "info"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0 and \
        subprocess.run(["docker", "compose", "version"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def install_docker():
    distro = os_release()
    system = distro.get("ID")
    if system not in ("ubuntu", "debian") or not distro.get("VERSION_CODENAME"):
        raise RuntimeError("自动安装 Docker 仅支持 Ubuntu/Debian；请先手动安装 Docker Engine 和 Compose 插件")
    existing = shutil.which("docker") is not None
    if existing and subprocess.run(["docker", "info"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0:
        run("systemctl", "start", "docker")
        if subprocess.run(["docker", "info"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0:
            raise RuntimeError("现有 Docker daemon 不可用；请先修复 Docker 服务")
    if existing and subprocess.run(["docker", "compose", "version"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        return
    run("apt-get", "update")
    run("apt-get", "install", "-y", "ca-certificates", "curl")
    keyring = Path("/etc/apt/keyrings")
    keyring.mkdir(parents=True, exist_ok=True, mode=0o755)
    key = keyring / "docker.asc"
    with urllib.request.urlopen(f"https://download.docker.com/linux/{system}/gpg", timeout=30) as response:
        key.write_bytes(response.read(1 << 20))
    key.chmod(0o644)
    arch = subprocess.check_output(["dpkg", "--print-architecture"], universal_newlines=True).strip()
    source = f"Types: deb\nURIs: https://download.docker.com/linux/{system}\nSuites: {distro['VERSION_CODENAME']}\nComponents: stable\nArchitectures: {arch}\nSigned-By: {key}\n"
    Path("/etc/apt/sources.list.d/docker.sources").write_text(source)
    run("apt-get", "update")
    packages = ["docker-compose-plugin"] if existing else ["docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"]
    run("apt-get", "install", "-y", *packages)
    if not existing:
        run("systemctl", "enable", "--now", "docker")
    if not docker_ready():
        raise RuntimeError("Docker 安装完成，但 daemon 或 Compose 插件不可用")


def probe_control_plane(config):
    req = urllib.request.Request(config["controlPlaneUrl"].rstrip("/") + "/healthz")
    with urllib.request.urlopen(req, timeout=8) as response:
        if response.status != 200:
            raise RuntimeError("节点无法访问控制面 /healthz")


def probe_host(config):
    env_file = HERE / "node.env"
    check_port = True
    if env_file.exists():
        settings = dict(line.split("=", 1) for line in env_file.read_text().splitlines() if "=" in line)
        if settings.get("NODE_ID") != config["nodeId"]:
            raise RuntimeError("部署目录已属于另一个 Node，不能覆盖")
        if settings.get("NODE_DATA_DIR") != config["dataDir"]:
            raise RuntimeError("已有 Node 的数据目录不能更改")
        check_port = settings.get("NODE_PORT") != str(config.get("port", 8092)) or settings.get("NODE_LISTEN_HOST") != config.get("listenHost", "0.0.0.0")
    if check_port:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
            try:
                listener.bind((config.get("listenHost", "0.0.0.0"), config.get("port", 8092)))
            except OSError as exc:
                raise RuntimeError("Node 监听端口已被占用") from exc
    data = Path(config["dataDir"])
    existing = data
    while not existing.exists():
        existing = existing.parent
    free = shutil.disk_usage(existing).free
    print(f"目标磁盘剩余 {free // (1024 ** 3)} GiB。")
    if free < 1024 ** 3:
        raise RuntimeError("目标磁盘剩余空间不足 1 GiB")


def write_private(path, content):
    fd, tmp = tempfile.mkstemp(prefix=".swarmjet-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            stream.write(content)
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def install(config):
    if os.geteuid() != 0:
        raise RuntimeError("安装需要 root 权限；请使用 sudo python3 install_node.py")
    if not docker_ready():
        install_docker()
    data = Path(config["dataDir"])
    data.mkdir(parents=True, exist_ok=True, mode=0o700)
    run("docker", "pull", config["image"])
    values = {
        "COMPOSE_PROJECT_NAME": config.get("projectName", "swarmjet-" + config["nodeId"]),
        "IMAGE": config["image"], "NODE_ID": config["nodeId"], "NODE_SECRET": config["nodeSecret"],
        "NODE_DATA_DIR": config["dataDir"], "NODE_ADVERTISE_URL": config["advertiseUrl"],
        "CONTROL_PLANE_URL": config["controlPlaneUrl"], "NODE_CAPACITY": str(config.get("capacity", 2)),
        "NODE_LISTEN_HOST": config.get("listenHost", "0.0.0.0"), "NODE_PORT": str(config.get("port", 8092)),
    }
    env_file = HERE / "node.env"
    write_private(env_file, "".join(f"{key}={value}\n" for key, value in values.items()))
    compose = ["docker", "compose", "--env-file", str(env_file), "-f", str(HERE / "compose.node.yaml")]
    run(*compose, "config", "--quiet")
    run(*compose, "up", "-d")
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        ps = subprocess.run(compose + ["ps", "--status", "running", "--quiet", "node"],
                            universal_newlines=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if ps.returncode == 0 and ps.stdout.strip():
            print("Node 容器已运行。请在 Web 的 Node 管理页确认在线、配置 Agent 镜像并启用接单。")
            return
        time.sleep(2)
    raise RuntimeError("Node 容器没有进入运行状态；请查看 docker compose logs node")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="只检查部署包、系统和网络，不修改主机")
    args = parser.parse_args()
    config = read_config(HERE / "node.json")
    if not (HERE / "compose.node.yaml").is_file():
        raise RuntimeError("部署包不完整")
    if platform.system() != "Linux":
        raise RuntimeError("Node 仅支持 Linux 主机")
    if not docker_ready():
        distro = os_release()
        if distro.get("ID") not in ("ubuntu", "debian"):
            raise RuntimeError("此系统缺少 Docker，且不支持自动安装")
        print("Docker Engine/Compose 尚未就绪，安装时将从 Docker 官方 apt 仓库安装。")
    probe_control_plane(config)
    probe_host(config)
    print("部署包和网络检查通过。")
    if not args.check:
        install(config)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError, urllib.error.URLError) as exc:
        print(f"Node 部署失败：{exc}", file=sys.stderr)
        sys.exit(1)
