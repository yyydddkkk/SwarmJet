#!/usr/bin/env python3
"""Publish the SwarmJet application image to the built-in LAN registry."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


HERE = Path(__file__).resolve().parent
TAG = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$")
DIGEST = re.compile(r"digest: (sha256:[0-9a-f]{64})\b")


def dotenv(path):
    return dict(line.split("=", 1) for line in path.read_text().splitlines()
                if line and not line.startswith("#") and "=" in line)


def check_native_dev():
    ready = HERE.parent / ".cache/dev/ready.json"
    try:
        processes = json.loads(ready.read_text())["processes"]
        for service in ("api", "worker"):
            os.kill(int(processes[service]), 0)
    except (OSError, KeyError, TypeError, ValueError) as exc:
        raise RuntimeError("本机原生开发的 API/Worker 未运行；请先执行 scripts/dev start") from exc


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True, help="Release tag, for example v1.2.3 or 20260924")
    parser.add_argument("--source-image", help="Local application image; defaults to deploy/.env IMAGE")
    parser.add_argument("--dev", action="store_true", help="Use the native development control plane and its built-in registry")
    parser.add_argument("--build", action="store_true", help="Build the application image from this checkout before publishing; requires --dev")
    args = parser.parse_args()
    if not TAG.fullmatch(args.tag):
        parser.error("invalid image tag")
    if args.build and not args.dev:
        parser.error("--build 仅用于 --dev；容器版应先通过正常部署流程更新 API/Worker")
    env = dotenv(HERE / ".env")
    registry = dotenv(HERE.parent / ".cache/dev/.env.registry" if args.dev else HERE / ".env.registry")
    source = args.source_image or env.get("IMAGE", "swarmjet:local")
    address = registry["REGISTRY_BIND_HOST"] + ":" + registry.get("REGISTRY_PORT", "5443")
    destination = f"{address}/swarmjet/platform:{args.tag}"
    if args.dev:
        check_native_dev()
        if args.build:
            subprocess.run(["docker", "build", "-f", "deploy/Dockerfile", "-t", source, "."], cwd=HERE.parent, check=True)
    source_id = subprocess.check_output(["docker", "image", "inspect", "--format", "{{.Id}}", source], universal_newlines=True).strip()
    if not args.dev:
        project = env.get("COMPOSE_PROJECT_NAME", "swarmjet")
        for service in ("api", "worker"):
            running = subprocess.check_output(["docker", "ps", "--quiet", "--filter", f"label=com.docker.compose.project={project}",
                                               "--filter", f"label=com.docker.compose.service={service}"], universal_newlines=True).strip().splitlines()
            if len(running) != 1:
                raise RuntimeError(f"未找到唯一运行中的控制面 {service} 容器；请在控制面主机执行")
            active_id = subprocess.check_output(["docker", "inspect", "--format", "{{.Image}}", running[0]], universal_newlines=True).strip()
            if active_id != source_id:
                raise RuntimeError(f"本地程序镜像与运行中的 {service} 版本不同；请先确认部署版本")
    with tempfile.TemporaryDirectory(prefix="swarmjet-publish-") as config:
        os.chmod(config, 0o700)
        docker = ["docker", "--config", config]
        login = subprocess.run(docker + ["login", address, "--username", registry["REGISTRY_PUSH_USER"], "--password-stdin"],
                               input=registry["REGISTRY_PUSH_PASSWORD"] + "\n", universal_newlines=True,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if login.returncode:
            raise RuntimeError("内置仓库登录失败；请检查 Docker CA 和推送凭据")
        subprocess.run(docker + ["image", "inspect", source], check=True, stdout=subprocess.DEVNULL)
        subprocess.run(docker + ["tag", source, destination], check=True)
        pushed = subprocess.run(docker + ["push", destination], universal_newlines=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if pushed.returncode:
            raise RuntimeError("程序镜像推送失败；检查仓库连接与权限")
        match = DIGEST.search(pushed.stdout)
        if not match:
            raise RuntimeError("仓库没有返回镜像摘要，无法生成固定版本引用")
        print(f"程序镜像已发布：{address}/swarmjet/platform@{match.group(1)}")
        print("在 Node 管理页下载部署包时选择此版本。")


if __name__ == "__main__":
    try:
        main()
    except (OSError, KeyError, RuntimeError, subprocess.CalledProcessError) as exc:
        print(f"发布失败：{exc}", file=sys.stderr)
        sys.exit(1)
