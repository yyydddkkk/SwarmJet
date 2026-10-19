#!/usr/bin/env python3
"""Bootstrap a single-host source deployment using the existing Compose files."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys


def initialize(path, example, data_dir, agent_clis):
    if path.exists():
        print("保留已有 .env，不修改密码、密钥或配置。", flush=True)
        return
    password = secrets.token_hex(24)
    values = {
        "POSTGRES_PASSWORD": password,
        "DATABASE_URL": f"postgres://swarmjet:{password}@postgres:5432/swarmjet?sslmode=disable",
        "ADMIN_PASSWORD": secrets.token_hex(24),
        "RUNTIME_ADMIN_SECRET": secrets.token_hex(32),
        "NODE_SECRET": secrets.token_hex(32),
        "SWARMJET_MASTER_KEY": secrets.token_hex(32),
        "DATA_DIR": data_dir,
        "AGENT_CLIS": agent_clis,
        "INIT_VERIFY_IMAGE": "swarmjet-agent:local",
    }
    lines = []
    for line in example.read_text().splitlines():
        key = line.split("=", 1)[0]
        lines.append(f"{key}={values[key]}" if key in values else line)
    # Publish a complete file atomically, without overwriting another installer.
    temporary = path.with_name(f".env.install-{secrets.token_hex(8)}")
    try:
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write("\n".join(lines) + "\n")
        os.link(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)
    print("已生成 .env（权限 0600），请备份其中的主密钥。", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data-dir", default="/srv/swarmjet/data", help="首次生成配置时的数据目录")
    parser.add_argument("--agent-clis", default="opencode", help="首次安装的 CLI，如 opencode,codex,claude")
    parser.add_argument("--cells", action="store_true", help="叠加 compose.cells.yaml，在 serve 中启用 Forager Cells（挂载 Docker socket，宿主 root 权限）")
    args = parser.parse_args()
    if not Path(args.data_dir).is_absolute() or any(c in args.data_dir for c in "\r\n$#'\"\\ \t"):
        parser.error("--data-dir 必须是无空白或 dotenv 特殊字符的绝对路径")
    clis = args.agent_clis.split(",")
    if "opencode" not in clis or not set(clis) <= {"opencode", "codex", "claude", "dst", "agy"}:
        parser.error("--agent-clis 必须包含 opencode，可选 codex,claude,dst,agy")
    directory = Path(__file__).resolve().parent
    if not shutil.which("docker"):
        raise RuntimeError("请先安装 Docker Engine 和 Compose 插件：https://docs.docker.com/engine/install/")

    def run(argv, capture=False):
        return subprocess.run(argv, cwd=directory, check=True, text=True,
                              stdout=subprocess.PIPE if capture else None).stdout

    run(["docker", "compose", "version"])
    run(["docker", "info"], capture=True)
    # Bind mounts and the socket in this deployment belong to this host.
    context = json.loads(run(["docker", "context", "inspect"], capture=True))[0]
    endpoint = os.environ.get("DOCKER_HOST") or context["Endpoints"]["docker"]["Host"]
    if endpoint != "unix:///var/run/docker.sock":
        raise RuntimeError("此部署需要本机 /var/run/docker.sock，请使用本机 Docker context。")
    initialize(directory / ".env", directory / ".env.example", args.data_dir, args.agent_clis)
    compose = ["docker", "compose", "--env-file", str(directory / ".env"),
               "-f", "compose.yaml", "-f", "compose.build.yaml", "-f", "compose.local-node.yaml"]
    if args.cells:
        compose += ["-f", "compose.cells.yaml"]
    override = directory / "compose.override.yaml"
    if override.exists():
        compose += ["-f", str(override)]
    # Let Compose parse dotenv; never execute it as shell code or print secrets.
    config = json.loads(run(compose + ["--profile", "tools", "config", "--format", "json"], capture=True))
    services = config["services"]
    api_env = services["api"]["environment"]
    if api_env.get("EXECUTION_MODE", "local") != "local":
        raise RuntimeError("已有配置不是 local 模式，请使用对应的分布式部署流程。")
    for key in ("ADMIN_PASSWORD", "SWARMJET_MASTER_KEY", "RUNTIME_ADMIN_SECRET"):
        value = api_env.get(key, "")
        if not value or value.startswith("replace-with-"):
            raise RuntimeError(f"已有 .env 的 {key} 未配置；请填写后重试，脚本不会覆盖已有配置。")
    if not services["worker"]["environment"].get("INIT_VERIFY_IMAGE"):
        raise RuntimeError("请先在 .env 配置 INIT_VERIFY_IMAGE，例如 swarmjet-agent:local。")
    data = Path(services["worker"]["environment"]["DATA_DIR"])
    if not data.is_absolute():
        raise RuntimeError("DATA_DIR 必须为绝对路径。")
    data.mkdir(mode=0o700, parents=True, exist_ok=True)
    if args.cells:
        print("已叠加 compose.cells.yaml：serve 将挂载 Docker socket（等同宿主 root 权限）。", flush=True)
    print("构建应用与 Agent 镜像……", flush=True)
    run(compose + ["--profile", "tools", "build"])
    print("初始化数据库、Temporal 并启动服务……", flush=True)
    run(compose + ["up", "-d", "--wait"])
    run(compose + ["exec", "-T", "api", "swarmjet", "doctor"])
    run(compose + ["ps"])
    print(f"\n部署完成：{api_env['PUBLIC_ORIGIN']}")
    print(f"管理员账号：{api_env['ADMIN_USERNAME']}；密码保存在 {directory / '.env'} 的 ADMIN_PASSWORD。")
    print("默认仅监听本机。远程访问：ssh -N -L 8180:127.0.0.1:8180 用户名@服务器")
    print("本机 Node 默认使用刚构建的 swarmjet-agent:local；可在 Node 管理中改为 GHCR 上的 Agent 镜像。")
    print("登录后启用 Agent、配置模型认证并添加 GitLab 项目；项目初始化验证通过后才能接单。")


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError, ValueError, KeyError) as exc:
        print(f"部署未完成：{exc}\n配置与数据已保留，修复后可重新运行。", file=sys.stderr)
        sys.exit(1)
