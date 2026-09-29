#!/usr/bin/env python3
"""Host-local, resumable Compose upgrade runner. Never accepts shell commands."""
import argparse
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import time


IMAGE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/@-]{1,511}$")
NODE_IMAGE = re.compile(r"^[A-Za-z0-9._:-]+/swarmjet/platform@sha256:[0-9a-f]{64}$")
OPERATION = re.compile(r"^[0-9a-f]{32}$")
STEPS = ("preflight", "pin_previous", "quiesce", "backup", "stage", "migrate", "services", "doctor")
NODE_STEPS = ("preflight", "pin_previous", "backup", "stage", "quiesce", "services", "doctor")
COMPOSE_FILES = ("compose.yaml", "compose.builder.yaml", "compose.registry.yaml")
RELEASE_FILES = (*COMPOSE_FILES, "compose.node.yaml", "compose.build.yaml", "Dockerfile.updater", "upgrade_host.py", "install_node.py", "release-policy.json",
                 "temporal/schema.sh", "temporal/namespace.sh", "temporal/config_template.yaml",
                 "temporal/dynamicconfig.yaml", "temporal/postgres-init.sql", "registry/nginx.conf",
                 "observability/prometheus.yml", "observability/rabbitmq-enabled-plugins",
                 "observability/alerts.yml", "observability/alertmanager.yml", "observability/alerts_test.yml")
DEPENDENCY_SERVICES = frozenset(("postgres", "rabbitmq", "temporal-schema", "temporal", "temporal-namespace",
                                 "temporal-ui", "registry", "gateway", "buildkit"))
APP_IMAGE_SERVICES = frozenset(("api", "worker", "migrate", "agent-image", "builder", "local-node", "updater"))


def tree_size(path):
    total = 0
    for root, _dirs, files in os.walk(path):
        for filename in files:
            item = Path(root) / filename
            if not item.is_symlink():
                total += item.stat().st_size
    return total


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def checked_archive(path):
    with tarfile.open(path, "r:gz") as archive:
        if next(iter(archive), None) is None:
            raise RuntimeError("empty backup archive")
        for _member in archive:
            pass
    return sha256(path)


def declared_images(deploy):
    result = {}
    for filename in COMPOSE_FILES:
        service = None
        for line in (Path(deploy) / filename).read_text().splitlines():
            found = re.fullmatch(r"  ([a-z][a-z0-9-]*):", line)
            if found:
                service = found.group(1)
            image = re.fullmatch(r"    image:\s*([^#]+?)\s*", line)
            if service and image:
                result[service] = image.group(1).strip("'\"")
    return result


def dotenv(path):
    return dict(line.split("=", 1) for line in path.read_text().splitlines()
                if line and not line.startswith("#") and "=" in line)


def private_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_name("." + path.name + "." + secrets.token_hex(8))
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream, separators=(",", ":"))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
    finally:
        temp.unlink(missing_ok=True)


class Upgrade:
    def __init__(self, deploy_dir, state_dir, kind="control"):
        self.deploy = Path(deploy_dir).resolve()
        self.state = Path(state_dir).resolve()
        self.state.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.state.chmod(0o700)
        self.lock = threading.Lock()
        self.active = False
        if kind not in ("control", "node"):
            raise ValueError("invalid upgrade kind")
        self.kind = kind

    def path(self, operation_id):
        if not OPERATION.fullmatch(operation_id):
            raise ValueError("invalid operationId")
        return self.state / (operation_id + ".json")

    def load(self, operation_id):
        record = json.loads(self.path(operation_id).read_text())
        if record.get("kind") != self.kind:
            raise ValueError("operation kind mismatch")
        return record

    def save(self, record):
        record["updatedAt"] = int(time.time())
        private_json(self.path(record["operationId"]), record)

    def extract_target(self, image, stage, registry_address, registry_user, registry_password):
        if not image.startswith(registry_address + "/swarmjet/platform@sha256:"):
            raise ValueError("target image must be a digest in the configured registry")
        with tempfile.TemporaryDirectory(prefix="swarmjet-upgrade-login-") as docker_config:
            os.chmod(docker_config, 0o700)
            docker = ["docker", "--config", docker_config]
            subprocess.run(docker + ["login", registry_address, "--username", registry_user, "--password-stdin"],
                           input=registry_password + "\n", text=True, stdout=subprocess.DEVNULL, check=True)
            subprocess.run(docker + ["pull", image], check=True)
        image_id = subprocess.check_output(["docker", "image", "inspect", "--format", "{{.Id}}", image], text=True).strip()
        stage.mkdir(mode=0o700, parents=True, exist_ok=True)
        container = subprocess.check_output(["docker", "create", image_id], text=True).strip()
        try:
            subprocess.run(["docker", "cp", container + ":/opt/swarmjet-upgrade/.", str(stage)], check=True)
        finally:
            subprocess.run(["docker", "rm", "-f", container], check=True)
        for name in ("release.json", "release-policy.json"):
            if not (stage / name).is_file() or (stage / name).is_symlink():
                raise RuntimeError("target image lacks a regular release manifest: " + name)
        release = json.loads((stage / "release.json").read_text())
        policy = json.loads((stage / "release-policy.json").read_text())
        if (not isinstance(release.get("version"), str) or
                not re.fullmatch(r"v[A-Za-z0-9._-]{1,39}", release["version"]) or
                not re.fullmatch(r"[0-9a-f]{40}", release.get("commitSHA", "")) or
                release.get("upgradePackageVersion") != 1):
            raise RuntimeError("target image has an unsupported upgrade package")
        if policy.get("schemaVersion") != 1 or policy.get("updaterProtocolVersion") != 1 or not policy.get("supportsBootstrap"):
            raise RuntimeError("target release policy is incompatible with this updater")
        if (policy.get("minimumNodeProtocol") != 2 or policy.get("nodeDataFormat") != 1 or
                not isinstance(policy.get("dependencyImages"), dict) or policy.get("dependencyUpgradePaths") != {}):
            raise RuntimeError("target release declares an unsupported dependency upgrade path")
        if set(policy["dependencyImages"]) != DEPENDENCY_SERVICES:
            raise RuntimeError("target release policy omits required dependencies")
        for name in RELEASE_FILES:
            source = stage / name
            if (not source.is_file() or source.is_symlink() or
                    any(parent.is_symlink() for parent in source.parents if parent != stage and stage in parent.parents)):
                raise RuntimeError("target image lacks required deployment file: " + name)
        return image_id, release, policy

    def control_target(self, record, stage):
        registry = dotenv(self.deploy / ".env.registry")
        address = registry["REGISTRY_BIND_HOST"] + ":" + registry.get("REGISTRY_PORT", "5443")
        image_id, release, policy = self.extract_target(record["image"], stage, address,
                                                         registry["REGISTRY_PULL_USER"], registry["REGISTRY_PULL_PASSWORD"])
        current = declared_images(self.deploy)
        if (self.deploy / "compose.override.yaml").exists():
            resolved = json.loads(self.compose(record, "config", "--format", "json", capture=True))
            current.update({name: value["image"] for name, value in resolved["services"].items() if "image" in value})
        target = declared_images(stage)
        if set(target) - DEPENDENCY_SERVICES - APP_IMAGE_SERVICES:
            raise RuntimeError("target release adds an undeclared image service")
        for service, expected in policy["dependencyImages"].items():
            if not isinstance(expected, str) or target.get(service) != expected:
                raise RuntimeError("target dependency policy does not match Compose: " + service)
            if current.get(service) != expected:
                raise RuntimeError("dependency image change requires a separate migration path: " + service)
        record["targetImageId"] = image_id
        record["release"] = release
        record["dependencyImages"] = policy["dependencyImages"]
        self.save(record)
        return record

    def compose(self, record, *args, capture=False, output=None, old=False):
        if self.kind == "node":
            return self.node_compose(record, *args, capture=capture, output=output, old=old)
        argv = ["docker", "compose", "--env-file", str(self.deploy / ".env"),
                "--env-file", str(self.deploy / ".env.registry")]
        for name in COMPOSE_FILES:
            argv += ["-f", str(self.deploy / name)]
        override = self.deploy / "compose.override.yaml"
        if override.exists():
            argv += ["-f", str(override)]
        env = os.environ.copy()
        env["IMAGE"] = record.get("pinnedImage", record["previousImage"]) if old else record["image"]
        if output is not None:
            with open(output, "wb") as stream:
                subprocess.run(argv + list(args), cwd=self.deploy, env=env, stdout=stream, check=True)
            return None
        result = subprocess.run(argv + list(args), cwd=self.deploy, env=env,
                                capture_output=capture, text=capture, check=True)
        return result.stdout if capture else None

    def node_compose(self, record, *args, capture=False, output=None, old=False):
        argv = ["docker", "compose", "--env-file", str(self.deploy / "node.env"),
                "-f", str(self.deploy / "compose.node.yaml")]
        env = os.environ.copy()
        env["IMAGE"] = record.get("pinnedImage", record["previousImage"]) if old else record["image"]
        result = subprocess.run(argv + list(args), cwd=self.deploy, env=env,
                                capture_output=capture, text=capture, check=True)
        return result.stdout if capture else None

    def plan(self, image, agent_image=None):
        if not IMAGE.fullmatch(image) or (agent_image is not None and not IMAGE.fullmatch(agent_image)):
            raise ValueError("invalid image reference")
        if self.kind == "node":
            return self.node_plan(image, agent_image)
        if agent_image is not None:
            raise ValueError("Agent images are upgraded independently")
        if not re.search(r"@sha256:[0-9a-f]{64}$", image):
            raise ValueError("control image must be pinned by digest")
        if not (self.deploy / ".env").is_file() or not (self.deploy / ".env.registry").is_file():
            raise ValueError("deployment env files are missing")
        current = dotenv(self.deploy / ".env")
        operation_id = secrets.token_hex(16)
        backup = self.state / "backups" / operation_id
        data = Path(current.get("DATA_DIR", ""))
        if not data.is_absolute() or self.state == data or self.state.is_relative_to(data):
            raise ValueError("backup state directory must be outside DATA_DIR")
        record = {"operationId": operation_id, "status": "planned", "kind": "control",
                  "image": image, "previousImage": current.get("IMAGE", "swarmjet:local"),
                  "completed": [], "currentStep": None, "backupDir": str(backup),
                  "stageDir": str(self.state / "staging" / operation_id), "dataDir": str(data),
                  "needsRestore": False, "error": None}
        self.save(record)
        return record

    def node_plan(self, image, agent_image):
        if agent_image is not None:
            raise ValueError("agentImage is not supported for node upgrade")
        if not (self.deploy / "node.env").is_file() or not (self.deploy / "node.json").is_file():
            raise ValueError("node deployment files are missing")
        config = json.loads((self.deploy / "node.json").read_text())
        if not NODE_IMAGE.fullmatch(image) or not image.startswith(config["registryAddress"] + "/"):
            raise ValueError("node image must be a digest in its configured registry")
        current = dotenv(self.deploy / "node.env")
        operation_id = secrets.token_hex(16)
        record = {"operationId": operation_id, "status": "planned", "kind": "node",
                  "image": image, "previousImage": current["IMAGE"], "completed": [], "currentStep": None,
                  "backupDir": str(self.state / "backups" / operation_id), "needsRestore": False, "error": None}
        self.save(record)
        return record

    def preflight(self, image):
        record = self.plan(image)
        try:
            self.step(record, "preflight")
            return {"status": "preflighted", "release": record.get("release"),
                    "backupRequiredBytes": record.get("backupRequiredBytes"),
                    "backupFreeBytes": record.get("backupFreeBytes"),
                    "dependencyImages": record.get("dependencyImages")}
        finally:
            shutil.rmtree(record["backupDir"], ignore_errors=True)
            if record.get("stageDir"):
                shutil.rmtree(record["stageDir"], ignore_errors=True)
            self.path(record["operationId"]).unlink(missing_ok=True)

    def node_step(self, record, name):
        backup = Path(record["backupDir"])
        if name == "preflight":
            self.compose(record, "config", "--quiet")
            config = json.loads((self.deploy / "node.json").read_text())
            stage = backup / "staged-deploy"
            image_id, release, policy = self.extract_target(record["image"], stage, config["registryAddress"],
                                                             config["registryUser"], config["registryPassword"])
            record["targetImageId"] = image_id
            record["release"] = release
            record["minimumNodeProtocol"] = policy["minimumNodeProtocol"]
            self.save(record)
        elif name == "pin_previous":
            container = self.compose(record, "ps", "-q", "node", capture=True).strip()
            if not container:
                raise RuntimeError("node is not running")
            image_id = subprocess.check_output(["docker", "inspect", "--format", "{{.Image}}", container], text=True).strip()
            pin = "swarmjet-node-upgrade-prev:" + record["operationId"]
            subprocess.run(["docker", "image", "tag", image_id, pin], check=True)
            record["pinnedImage"] = pin
            self.save(record)
        elif name == "backup":
            backup.mkdir(mode=0o700, parents=True)
            for filename in ("node.env", "node.json", "compose.node.yaml", "registry-ca.crt", "install_node.py", "upgrade_host.py", "Dockerfile.updater"):
                shutil.copy2(self.deploy / filename, backup / filename)
        elif name == "stage":
            stage = backup / "staged-deploy"
            for filename in ("compose.node.yaml", "install_node.py", "upgrade_host.py", "Dockerfile.updater"):
                target = self.deploy / filename
                temp = target.with_name("." + filename + ".upgrade")
                shutil.copy2(stage / filename, temp)
                os.replace(temp, target)
            self.compose(record, "config", "--quiet")
        elif name == "quiesce":
            self.compose(record, "stop", "node")
        elif name == "services":
            self.compose(record, "up", "-d", "node")
        elif name == "doctor":
            if not self.compose(record, "ps", "--status", "running", "-q", "node", capture=True).strip():
                raise RuntimeError("node container is not running")
            config_path = self.deploy / "node.json"
            config = json.loads(config_path.read_text())
            config["image"] = record["image"]
            private_json(config_path, config)
            env_path = self.deploy / "node.env"
            lines = env_path.read_text().splitlines()
            lines = ["IMAGE=" + record["image"] if line.startswith("IMAGE=") else line for line in lines]
            temp = env_path.with_name(".node.env.upgrade")
            temp.write_text("\n".join(lines) + "\n")
            temp.chmod(0o600)
            os.replace(temp, env_path)
        else:
            raise ValueError("unknown node step")

    def step(self, record, name):
        if self.kind == "node":
            return self.node_step(record, name)
        backup = Path(record["backupDir"])
        if name == "preflight":
            self.compose(record, "config", "--quiet")
            self.compose(record, "ps", "-q", "postgres", capture=True)
            if not Path(record["dataDir"]).is_dir():
                raise RuntimeError("DATA_DIR does not exist")
            if Path(record["dataDir"]).stat().st_dev == self.state.stat().st_dev:
                raise RuntimeError("UPDATER_STATE_DIR must be on a separately mounted filesystem")
            pg_bytes = int(self.compose(record, "exec", "-T", "postgres", "psql", "-U", "swarmjet", "-d", "postgres", "-Atc",
                                        "SELECT COALESCE(sum(pg_database_size(oid)),0) FROM pg_database", capture=True).strip())
            pg_volume_bytes = 1024 * int(self.compose(record, "exec", "-T", "postgres", "du", "-sk", "/var/lib/postgresql", capture=True).split()[0])
            broker_bytes = 1024 * int(self.compose(record, "exec", "-T", "rabbitmq", "du", "-sk", "/var/lib/rabbitmq", capture=True).split()[0])
            registry_bytes = 1024 * int(self.compose(record, "exec", "-T", "registry", "du", "-sk", "/var/lib/registry", capture=True).split()[0])
            # docker cp creates a temporary tree beside each archive. Assume
            # incompressible data so a backup cannot run out of space mid-step.
            required = (tree_size(record["dataDir"]) + 2 * pg_bytes + 2 * pg_volume_bytes +
                        2 * broker_bytes + 2 * registry_bytes + (512 << 20))
            free = shutil.disk_usage(self.state).free
            if free < required:
                raise RuntimeError(f"backup disk lacks space: need {required} bytes, free {free} bytes")
            record["backupRequiredBytes"] = required
            record["backupFreeBytes"] = free
            self.save(record)
            self.control_target(record, Path(record["stageDir"]))
        elif name == "pin_previous":
            # Keep the exact running image, not a mutable tag that may be repulled.
            container = self.compose(record, "ps", "-q", "api", capture=True).strip()
            if not container:
                raise RuntimeError("api is not running")
            image_id = subprocess.check_output(["docker", "inspect", "--format", "{{.Image}}", container], text=True).strip()
            pin = "swarmjet-upgrade-prev:" + record["operationId"]
            subprocess.run(["docker", "image", "tag", image_id, pin], check=True)
            record["pinnedImage"] = pin
            self.save(record)
        elif name == "quiesce":
            self.compose(record, "stop", "api", "worker", "builder", "local-node", "temporal", "rabbitmq", "gateway", "registry")
        elif name == "backup":
            expected_parent = (self.state / "backups").resolve()
            if backup.parent.resolve() != expected_parent or backup.name != record["operationId"] or backup.is_symlink():
                raise RuntimeError("invalid backup operation directory")
            if backup.exists():
                shutil.rmtree(backup)
            backup.mkdir(mode=0o700, parents=True)
            if Path(record["dataDir"]).resolve().is_relative_to(backup.resolve()):
                raise RuntimeError("DATA_DIR cannot be within backup directory")
            for name in (".env", ".env.registry", *RELEASE_FILES, "compose.override.yaml"):
                source = self.deploy / name
                if source.exists():
                    (backup / name).parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(source, backup / name)
            # A retry may follow a crash after PostgreSQL was stopped below.
            self.compose(record, "up", "-d", "--wait", "postgres")
            self.compose(record, "exec", "-T", "postgres", "pg_dumpall", "-U", "swarmjet", output=backup / "postgres.sql")
            self.compose(record, "stop", "postgres")
            postgres = self.compose(record, "ps", "-a", "-q", "postgres", capture=True).strip()
            if not postgres:
                raise RuntimeError("postgres container is missing; backup is incomplete")
            postgres_data = backup / "postgres-data"
            postgres_data.mkdir(exist_ok=True)
            subprocess.run(["docker", "cp", "-a", postgres + ":/var/lib/postgresql/.", str(postgres_data)], check=True)
            with tarfile.open(backup / "postgres.tar.gz", "w:gz") as archive:
                archive.add(postgres_data, arcname="postgres")
            shutil.rmtree(postgres_data)
            with tarfile.open(backup / "data.tar.gz", "w:gz") as archive:
                archive.add(record["dataDir"], arcname="data")
            broker = self.compose(record, "ps", "-a", "-q", "rabbitmq", capture=True).strip()
            if not broker:
                raise RuntimeError("rabbitmq container is missing; backup is incomplete")
            rabbit = backup / "rabbitmq-data"
            rabbit.mkdir(exist_ok=True)
            subprocess.run(["docker", "cp", "-a", broker + ":/var/lib/rabbitmq/.", str(rabbit)], check=True)
            with tarfile.open(backup / "rabbitmq.tar.gz", "w:gz") as archive:
                archive.add(rabbit, arcname="rabbitmq")
            shutil.rmtree(rabbit)
            registry = self.compose(record, "ps", "-a", "-q", "registry", capture=True).strip()
            if not registry:
                raise RuntimeError("registry container is missing; backup is incomplete")
            image_store = backup / "registry-data"
            image_store.mkdir(exist_ok=True)
            subprocess.run(["docker", "cp", "-a", registry + ":/var/lib/registry/.", str(image_store)], check=True)
            with tarfile.open(backup / "registry.tar.gz", "w:gz") as archive:
                archive.add(image_store, arcname="registry")
            shutil.rmtree(image_store)
            pg_dump = backup / "postgres.sql"
            with pg_dump.open("rb") as stream:
                stream.seek(max(0, pg_dump.stat().st_size - 512))
                footer = stream.read()
            if pg_dump.stat().st_size == 0 or b"PostgreSQL database cluster dump complete" not in footer:
                raise RuntimeError("PostgreSQL backup is incomplete")
            hashes = {name: checked_archive(backup / name) for name in ("postgres.tar.gz", "data.tar.gz", "rabbitmq.tar.gz", "registry.tar.gz")}
            hashes["postgres.sql"] = sha256(pg_dump)
            private_json(backup / "manifest.json", {"operationId": record["operationId"], "dataDir": record["dataDir"],
                                                    "previousImage": record["previousImage"], "pinnedImage": record["pinnedImage"],
                                                    "sha256": hashes})
        elif name == "stage":
            stage = Path(record["stageDir"])
            if not stage.is_dir() or not record.get("targetImageId"):
                raise RuntimeError("target release was not preflighted")
            # Fixed reviewed file list; custom override and env files are preserved.
            for name in RELEASE_FILES:
                target = self.deploy / name
                target.parent.mkdir(parents=True, exist_ok=True)
                temp = target.with_name("." + target.name + ".upgrade")
                shutil.copy2(stage / name, temp)
                os.replace(temp, target)
            self.compose(record, "config", "--quiet")
        elif name == "migrate":
            self.compose(record, "up", "-d", "postgres", "rabbitmq")
            self.compose(record, "run", "--rm", "temporal-schema")
            self.compose(record, "up", "-d", "temporal")
            self.compose(record, "run", "--rm", "temporal-namespace")
            self.compose(record, "run", "--rm", "migrate")
        elif name == "services":
            self.compose(record, "up", "-d", "--wait", "api", "worker", "temporal-ui", "builder", "buildkit", "local-node", "registry", "gateway")
        elif name == "doctor":
            self.compose(record, "exec", "-T", "api", "swarmjet", "doctor")
            env_path = self.deploy / ".env"
            lines = env_path.read_text().splitlines()
            replacements = {"IMAGE": record["image"]}
            found = set()
            for index, line in enumerate(lines):
                key = line.split("=", 1)[0]
                if key in replacements:
                    lines[index] = key + "=" + replacements[key]
                    found.add(key)
            lines += [key + "=" + value for key, value in replacements.items() if key not in found]
            temp = env_path.with_name(".env.upgrade")
            temp.write_text("\n".join(lines) + "\n")
            temp.chmod(0o600)
            os.replace(temp, env_path)
        else:
            raise ValueError("unknown step")

    def apply(self, operation_id, reserved=False):
        with self.lock:
            if self.active and not reserved:
                raise RuntimeError("another upgrade is running")
            record = self.load(operation_id)
            if record["status"] in ("complete", "rolled_back"):
                if reserved:
                    self.active = False
                return record
            self.active = True
            record["status"] = "running"
            self.save(record)
        try:
            steps = STEPS if self.kind == "control" else NODE_STEPS
            for name in steps:
                if name in record["completed"]:
                    continue
                record["currentStep"] = name
                self.save(record)
                self.step(record, name)
                record["completed"].append(name)
                record["currentStep"] = None
                self.save(record)
            record["status"] = "complete"
            self.save(record)
            if record.get("stageDir"):
                shutil.rmtree(record["stageDir"], ignore_errors=True)
        except Exception as exc:
            record["status"] = "failed"
            record["error"] = str(exc)
            record["needsRestore"] = self.kind == "control" and ("migrate" in record["completed"] or record["currentStep"] in ("migrate", "services", "doctor"))
            self.save(record)
            if self.kind == "node" and ("stage" in record["completed"] or record.get("currentStep") == "stage"):
                try:
                    self.restore_node_files(record)
                except Exception as restore_error:
                    record["rollbackError"] = str(restore_error)
                    self.save(record)
            if not record["needsRestore"] and "quiesce" in record["completed"]:
                try:
                    self.rollback(operation_id, reserved=True)
                    record = self.load(operation_id)
                    record["status"] = "failed"
                    record["rolledBack"] = True
                    self.save(record)
                except Exception as rollback_error:
                    record["rollbackError"] = str(rollback_error)
                    self.save(record)
        finally:
            with self.lock:
                self.active = False
        return record

    def restore_node_files(self, record):
        backup = Path(record["backupDir"])
        for filename in ("node.env", "node.json", "compose.node.yaml", "install_node.py", "upgrade_host.py", "Dockerfile.updater"):
            source = backup / filename
            if source.exists():
                shutil.copy2(source, self.deploy / filename)

    def rollback(self, operation_id, reserved=False):
        with self.lock:
            if self.active and not reserved:
                raise RuntimeError("another upgrade is running")
            record = self.load(operation_id)
            if self.kind == "control" and ("migrate" in record["completed"] or record.get("needsRestore")):
                raise RuntimeError("needs_restore: database migration may have changed schema")
            self.active = True
        try:
            if "quiesce" in record["completed"]:
                if self.kind == "node":
                    self.restore_node_files(record)
                    self.compose(record, "up", "-d", "node", old=True)
                else:
                    if "stage" in record["completed"] or record.get("currentStep") == "stage":
                        for name in RELEASE_FILES:
                            source = Path(record["backupDir"]) / name
                            if source.exists():
                                target = self.deploy / name
                                target.parent.mkdir(parents=True, exist_ok=True)
                                shutil.copy2(source, target)
                    saved_env = Path(record["backupDir"]) / ".env"
                    if saved_env.exists():
                        shutil.copy2(saved_env, self.deploy / ".env")
                    self.compose(record, "up", "-d", "--wait", "postgres", "rabbitmq", "temporal", "api", "worker", "builder", "local-node", "registry", "gateway", old=True)
            if self.kind == "node" and "quiesce" not in record["completed"]:
                self.restore_node_files(record)
            record["status"] = "rolled_back"
            self.save(record)
            return record
        except Exception as exc:
            record["status"] = "failed"
            record["rollbackError"] = str(exc)
            self.save(record)
            raise
        finally:
            with self.lock:
                self.active = False


def serve(upgrade, secret, address):
    if len(secret) < 32:
        raise ValueError("UPDATER_SECRET must contain at least 32 characters")

    class Handler(BaseHTTPRequestHandler):
        def answer(self, status, value):
            body = json.dumps(value, separators=(",", ":")).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def authenticated(self):
            supplied = self.headers.get("Authorization", "")
            return hmac.compare_digest(supplied, "Bearer " + secret)

        def do_GET(self):
            if self.path == "/healthz":
                return self.answer(200, {"ok": True})
            if not self.authenticated():
                return self.answer(401, {"error": "unauthorized"})
            if self.path.startswith("/v1/status?operationId="):
                try:
                    return self.answer(200, upgrade.load(self.path.split("=", 1)[1]))
                except (ValueError, OSError):
                    return self.answer(404, {"error": "operation not found"})
            self.answer(404, {"error": "not found"})

        def do_POST(self):
            if not self.authenticated():
                return self.answer(401, {"error": "unauthorized"})
            try:
                length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                return self.answer(400, {"error": "invalid Content-Length"})
            if length > 4096 or length < 2:
                return self.answer(413, {"error": "invalid body size"})
            try:
                request = json.loads(self.rfile.read(length))
                if not isinstance(request, dict):
                    raise ValueError("body must be object")
                if self.path in ("/v1/plan", "/v1/preflight"):
                    if set(request) - {"image", "agentImage"}:
                        raise ValueError("unknown field")
                    if self.path == "/v1/preflight":
                        if request.get("agentImage") is not None:
                            raise ValueError("agentImage is not supported")
                        return self.answer(200, upgrade.preflight(request["image"]))
                    return self.answer(201, upgrade.plan(request["image"], request.get("agentImage")))
                if self.path in ("/v1/apply", "/v1/rollback"):
                    if set(request) != {"operationId"}:
                        raise ValueError("expected operationId")
                    operation_id = request["operationId"]
                    record = upgrade.load(operation_id)
                    if self.path == "/v1/rollback" and upgrade.kind == "control" and ("migrate" in record["completed"] or record.get("needsRestore")):
                        return self.answer(409, {"error": "needs_restore"})
                    target = upgrade.apply if self.path == "/v1/apply" else upgrade.rollback
                    with upgrade.lock:
                        if upgrade.active:
                            return self.answer(409, {"error": "upgrade already running"})
                        upgrade.active = True
                    def run():
                        target(operation_id, reserved=True)
                    threading.Thread(target=run, daemon=True).start()
                    return self.answer(202, {"operationId": operation_id, "status": "accepted"})
                return self.answer(404, {"error": "not found"})
            except (KeyError, ValueError, TypeError) as exc:
                return self.answer(400, {"error": str(exc)})
            except OSError:
                return self.answer(404, {"error": "operation not found"})
            except (RuntimeError, subprocess.CalledProcessError) as exc:
                return self.answer(409, {"error": str(exc)})

    # A container restart resumes the interrupted operation from its journal.
    for journal in sorted(upgrade.state.glob("*.json")):
        record = json.loads(journal.read_text())
        if record.get("status") == "running":
            with upgrade.lock:
                upgrade.active = True
            threading.Thread(target=upgrade.apply, args=(record["operationId"], True), daemon=True).start()
            break
    server = ThreadingHTTPServer((address, 8095), Handler)
    server.serve_forever()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deploy-dir", default=os.environ.get("UPDATER_DEPLOY_DIR", str(Path(__file__).resolve().parent)))
    parser.add_argument("--state-dir", default=os.environ.get("UPDATER_STATE_DIR", "/srv/swarmjet/upgrades"))
    parser.add_argument("--kind", choices=("control", "node"), default="control")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("plan", "preflight"):
        plan = sub.add_parser(name)
        plan.add_argument("--target-image", required=True)
        if name == "plan":
            plan.add_argument("--target-agent-image")
    for name in ("apply", "status", "rollback"):
        sub.add_parser(name).add_argument("--operation-id", required=True)
    sub.add_parser("serve")
    args = parser.parse_args()
    upgrade = Upgrade(args.deploy_dir, args.state_dir, args.kind)
    if args.command == "serve":
        return serve(upgrade, os.environ.get("UPDATER_SECRET", ""), os.environ.get("UPDATER_LISTEN", "0.0.0.0"))
    if args.command == "plan":
        result = upgrade.plan(args.target_image, args.target_agent_image)
    elif args.command == "preflight":
        result = upgrade.preflight(args.target_image)
    elif args.command == "apply":
        result = upgrade.apply(args.operation_id)
    elif args.command == "status":
        result = upgrade.load(args.operation_id)
    else:
        result = upgrade.rollback(args.operation_id)
    print(json.dumps(result, separators=(",", ":")))
    if result["status"] == "failed":
        return 1
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as exc:
        print(json.dumps({"error": str(exc)}), file=sys.stderr)
        sys.exit(1)
