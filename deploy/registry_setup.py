#!/usr/bin/env python3
"""Create stable LAN registry credentials, TLS files, and Compose environment."""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess


def run(*args, input_text=None):
    return subprocess.run(args, input=input_text, text=True, check=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()


def setup(root: Path, host: str, port: int, env_file: Path, trust_docker=True):
    address = ipaddress.IPv4Address(host)
    if not address.is_private or address.is_loopback or not 1 <= port <= 65535:
        raise ValueError("仓库需要有效的局域网 IPv4 地址和端口")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(root, 0o700)
    secret_file = root / "secrets.json"
    if secret_file.exists():
        values = json.loads(secret_file.read_text())
        if values["host"] != host or values["port"] != port:
            raise ValueError(f"仓库地址已固定为 {values['host']}:{values['port']}；请固定局域网 IP 后再迁移配置")
    else:
        values = {"host": host, "port": port, "push_user": "builder",
                  "pull_user": "reader", "push_password": secrets.token_hex(24),
                  "pull_password": secrets.token_hex(24), "http_secret": secrets.token_hex(32)}
        secret_file.write_text(json.dumps(values) + "\n")
    os.chmod(secret_file, 0o600)

    tls = root / "tls"
    tls.mkdir(mode=0o700, exist_ok=True)
    os.chmod(tls, 0o755)
    ca, server = tls / "ca.crt", tls / "server.crt"
    if not ca.exists() or not server.exists():
        run("openssl", "req", "-x509", "-newkey", "rsa:3072", "-nodes", "-sha256",
            "-days", "3650", "-keyout", str(tls / "ca.key"), "-out", str(ca),
            "-subj", "/CN=SwarmJet LAN Registry CA",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign")
        run("openssl", "req", "-newkey", "rsa:3072", "-nodes", "-sha256",
            "-keyout", str(tls / "server.key"), "-out", str(tls / "server.csr"),
            "-subj", f"/CN={host}")
        ext = tls / "server.ext"
        ext.write_text(f"subjectAltName=IP:{host}\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n")
        run("openssl", "x509", "-req", "-in", str(tls / "server.csr"),
            "-CA", str(ca), "-CAkey", str(tls / "ca.key"), "-CAcreateserial",
            "-out", str(server), "-days", "825", "-sha256", "-extfile", str(ext))
        (tls / "server.csr").unlink()
        ext.unlink()
    for key in (tls / "ca.key", tls / "server.key"):
        os.chmod(key, 0o600)
    for cert in (ca, server):
        os.chmod(cert, 0o644)

    def htpasswd(user, password):
        return user + ":" + run("openssl", "passwd", "-apr1", "-stdin", input_text=password + "\n") + "\n"
    (root / "htpasswd").write_text(htpasswd(values["push_user"], values["push_password"]) +
                                    htpasswd(values["pull_user"], values["pull_password"]))
    (tls / "push.htpasswd").write_text(htpasswd(values["push_user"], values["push_password"]))
    os.chmod(root / "htpasswd", 0o644)
    os.chmod(tls / "push.htpasswd", 0o644)

    buildkit = root / "buildkitd.toml"
    base = (Path(__file__).resolve().parent / "buildkitd.toml").read_text()
    buildkit.write_text(base + f'\n[registry."{host}:{port}"]\n  ca = ["/etc/buildkit/certs/registry.crt"]\n')
    env_file.parent.mkdir(parents=True, exist_ok=True)
    env_file.write_text("\n".join(f"{key}={value}" for key, value in {
        "REGISTRY_BIND_HOST": host, "REGISTRY_PORT": port,
        "REGISTRY_TLS_DIR": tls.resolve(), "REGISTRY_AUTH_FILE": (root / "htpasswd").resolve(),
        "REGISTRY_HTTP_SECRET": values["http_secret"],
        "REGISTRY_BUILDKIT_CONFIG": buildkit.resolve(), "REGISTRY_CA_FILE": ca.resolve(),
        "REGISTRY_PUSH_USER": values["push_user"], "REGISTRY_PUSH_PASSWORD": values["push_password"],
        "REGISTRY_PULL_USER": values["pull_user"], "REGISTRY_PULL_PASSWORD": values["pull_password"],
    }.items()) + "\n")
    os.chmod(env_file, 0o600)

    if trust_docker:
        destination = Path("/etc/docker/certs.d") / f"{host}:{port}" / "ca.crt"
        if os.geteuid() == 0:
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ca, destination)
        else:
            run("docker", "run", "--rm", "--entrypoint", "sh",
                "-v", "/etc/docker/certs.d:/target", "-v", f"{tls.resolve()}:/source:ro",
                "registry:3", "-c", f"mkdir -p '/target/{host}:{port}' && cp /source/ca.crt '/target/{host}:{port}/ca.crt'")
    return values


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", type=int, default=5443)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--skip-docker-trust", action="store_true")
    args = parser.parse_args()
    setup(args.root, args.host, args.port, args.env_file, not args.skip_docker_trust)
    print(f"LAN registry prepared: {args.host}:{args.port}")


if __name__ == "__main__":
    main()
