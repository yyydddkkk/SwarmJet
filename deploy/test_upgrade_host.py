"""Dry-run contract tests; no Docker or production files are touched."""
import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("upgrade_host", Path(__file__).with_name("upgrade_host.py"))
upgrade_host = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(upgrade_host)
IMAGE = "registry.example/swarmjet/platform@sha256:" + "a" * 64


class UpgradeHostTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.deploy = self.root / "deploy"
        self.deploy.mkdir()
        self.state = self.root / "backups"

    def test_control_plan_and_migration_guard(self):
        data = self.root / "data"
        data.mkdir()
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\nIMAGE=old:v1\nAGENT_IMAGE=old-agent:v1\n")
        (self.deploy / ".env.registry").write_text("REGISTRY_BIND_HOST=127.0.0.1\n")
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        plan = runner.plan(IMAGE)
        self.assertEqual(plan["status"], "planned")
        self.assertEqual(runner.load(plan["operationId"])["previousImage"], "old:v1")
        self.assertEqual(runner.path(plan["operationId"]).stat().st_mode & 0o777, 0o600)
        plan["completed"] = ["preflight", "pin_previous", "quiesce", "backup", "pull", "migrate"]
        runner.save(plan)
        with self.assertRaisesRegex(RuntimeError, "needs_restore"):
            runner.rollback(plan["operationId"])

    def test_control_resume_runs_only_remaining_steps(self):
        data = self.root / "data"
        data.mkdir()
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\nIMAGE=old:v1\n")
        (self.deploy / ".env.registry").touch()
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        plan = runner.plan(IMAGE)
        plan["completed"] = list(upgrade_host.STEPS[:-2])
        runner.save(plan)
        calls = []
        with patch.object(runner, "step", side_effect=lambda _record, name: calls.append(name)):
            result = runner.apply(plan["operationId"])
        self.assertEqual(calls, ["services", "doctor"])
        self.assertEqual(result["status"], "complete")

    def test_node_plan_and_rollback(self):
        image = "registry.example/swarmjet/platform@sha256:" + "a" * 64
        (self.deploy / "node.env").write_text("IMAGE=old:v1\nNODE_ID=one\n")
        (self.deploy / "node.json").write_text(json.dumps({"registryAddress": "registry.example"}))
        runner = upgrade_host.Upgrade(self.deploy, self.state, "node")
        plan = runner.plan(image)
        self.assertEqual(plan["kind"], "node")
        plan["completed"] = ["preflight", "pin_previous", "backup", "pull", "quiesce", "services", "doctor"]
        backup = Path(plan["backupDir"])
        backup.mkdir(parents=True)
        (backup / "node.env").write_text("IMAGE=old:v1\nNODE_ID=one\n")
        (backup / "node.json").write_text(json.dumps({"registryAddress": "registry.example", "image": "old:v1"}))
        runner.save(plan)
        with patch.object(runner, "compose") as compose:
            result = runner.rollback(plan["operationId"])
        self.assertEqual(result["status"], "rolled_back")
        self.assertEqual(compose.call_args.kwargs["old"], True)
        self.assertIn("IMAGE=old:v1", (self.deploy / "node.env").read_text())

    def test_rejects_backup_inside_data_and_untrusted_operation_id(self):
        data = self.root / "data"
        data.mkdir()
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\n")
        (self.deploy / ".env.registry").touch()
        runner = upgrade_host.Upgrade(self.deploy, data / "upgrades")
        with self.assertRaisesRegex(ValueError, "outside DATA_DIR"):
            runner.plan(IMAGE)
        with self.assertRaises(ValueError):
            runner.load("../../etc/passwd")

    def test_dependency_change_is_blocked_before_quiesce(self):
        data = self.root / "data"
        data.mkdir()
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\nIMAGE=old:v1\n")
        (self.deploy / ".env.registry").write_text("REGISTRY_BIND_HOST=registry.example\nREGISTRY_PULL_USER=reader\nREGISTRY_PULL_PASSWORD=secret\n")
        policy = json.loads(Path(__file__).with_name("release-policy.json").read_text())
        current = {**policy["dependencyImages"]}
        current["postgres"] = "postgres:17-old"
        target = policy["dependencyImages"]
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        record = runner.plan(IMAGE)
        stage = Path(record["backupDir"]) / "staged-deploy"
        for directory, images in ((self.deploy, current), (stage, target)):
            directory.mkdir(parents=True, exist_ok=True)
            lines = ["services:"]
            for service, image in images.items():
                lines += [f"  {service}:", f"    image: {image}"]
            (directory / "compose.yaml").write_text("\n".join(lines) + "\n")
            for filename in ("compose.builder.yaml", "compose.registry.yaml"):
                (directory / filename).write_text("services:\n")
        def extract(_image, destination, *_credentials):
            self.assertEqual(destination, stage)
            return "sha256:target", {"version": "v2", "commitSHA": "a" * 40}, policy
        with patch.object(runner, "extract_target", side_effect=extract):
            with self.assertRaisesRegex(RuntimeError, "dependency image change"):
                runner.control_target(record, stage)

    def test_node_steps_stage_before_stop(self):
        self.assertLess(upgrade_host.NODE_STEPS.index("stage"), upgrade_host.NODE_STEPS.index("quiesce"))
        self.assertLess(upgrade_host.STEPS.index("preflight"), upgrade_host.STEPS.index("quiesce"))

    def test_preflight_requires_a_separate_backup_filesystem(self):
        data = self.root / "data"
        data.mkdir()
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\n")
        (self.deploy / ".env.registry").touch()
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        record = runner.plan(IMAGE)
        with patch.object(runner, "compose", return_value="container"):
            with self.assertRaisesRegex(RuntimeError, "separately mounted"):
                runner.step(record, "preflight")

    def test_backup_failure_before_env_copy_restores_old_services(self):
        data = self.root / "data"
        data.mkdir()
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\nIMAGE=old:v1\n")
        (self.deploy / ".env.registry").touch()
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        record = runner.plan(IMAGE)
        record["completed"] = ["preflight", "pin_previous", "quiesce"]
        record["pinnedImage"] = "swarmjet-upgrade-prev:" + record["operationId"]
        runner.save(record)
        with patch.object(runner, "step", side_effect=RuntimeError("backup disk failed")), \
             patch.object(runner, "compose") as compose:
            result = runner.apply(record["operationId"])
        self.assertEqual(result["status"], "failed")
        self.assertTrue(result["rolledBack"])
        self.assertNotIn("backup", result["completed"])
        self.assertNotIn("rollbackError", result)
        self.assertEqual(compose.call_args.args[1:3], ("up", "-d"))
        self.assertEqual((self.deploy / ".env").read_text(), f"DATA_DIR={data}\nIMAGE=old:v1\n")

    def test_incomplete_backup_clears_partial_files_and_has_no_manifest(self):
        data = self.root / "data"
        data.mkdir()
        (data / "work.txt").write_text("work")
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\nIMAGE=old:v1\n")
        (self.deploy / ".env.registry").touch()
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        record = runner.plan(IMAGE)
        record["pinnedImage"] = "swarmjet-upgrade-prev:" + record["operationId"]
        backup = Path(record["backupDir"])
        backup.mkdir(parents=True)
        (backup / "partial.flag").write_text("old incomplete attempt")
        (backup / "manifest.json").write_text("stale")

        def fake_compose(_record, *args, **kwargs):
            if kwargs.get("output"):
                Path(kwargs["output"]).write_bytes(b"-- PostgreSQL database cluster dump complete\n")
            if args[:3] == ("ps", "-a", "-q"):
                return {"postgres": "pg", "rabbitmq": ""}.get(args[3], "")
            return None

        def fake_docker(argv, **_kwargs):
            if argv[:3] == ["docker", "cp", "-a"]:
                (Path(argv[-1]) / "sample").write_text("backup")

        with patch.object(runner, "compose", side_effect=fake_compose), \
             patch.object(upgrade_host.subprocess, "run", side_effect=fake_docker):
            with self.assertRaisesRegex(RuntimeError, "rabbitmq container is missing"):
                runner.step(record, "backup")
        self.assertFalse((backup / "partial.flag").exists())
        self.assertFalse((backup / "manifest.json").exists())
        self.assertNotIn("backup", record["completed"])

    def test_backup_includes_stopped_postgres_volume_with_checksum(self):
        data = self.root / "data"
        data.mkdir()
        (data / "work.txt").write_text("work")
        (self.deploy / ".env").write_text(f"DATA_DIR={data}\nIMAGE=old:v1\n")
        (self.deploy / ".env.registry").touch()
        runner = upgrade_host.Upgrade(self.deploy, self.state)
        record = runner.plan(IMAGE)
        record["pinnedImage"] = "swarmjet-upgrade-prev:" + record["operationId"]
        events = []

        def fake_compose(_record, *args, **kwargs):
            events.append(args)
            if kwargs.get("output"):
                Path(kwargs["output"]).write_bytes(b"-- PostgreSQL database cluster dump complete\n")
            if args[:3] == ("ps", "-a", "-q"):
                return {"postgres": "pg", "rabbitmq": "mq", "registry": "reg"}[args[3]]
            return None

        def fake_docker(argv, **_kwargs):
            if argv[:3] == ["docker", "cp", "-a"]:
                (Path(argv[-1]) / "sample").write_text("backup")

        with patch.object(runner, "compose", side_effect=fake_compose), \
             patch.object(upgrade_host.subprocess, "run", side_effect=fake_docker):
            runner.step(record, "backup")
        backup = Path(record["backupDir"])
        manifest = json.loads((backup / "manifest.json").read_text())
        self.assertIn("postgres.tar.gz", manifest["sha256"])
        self.assertEqual(manifest["sha256"]["postgres.tar.gz"],
                         hashlib.sha256((backup / "postgres.tar.gz").read_bytes()).hexdigest())
        self.assertLess(events.index(("exec", "-T", "postgres", "pg_dumpall", "-U", "swarmjet")),
                        events.index(("stop", "postgres")))
        self.assertLess(events.index(("stop", "postgres")), events.index(("ps", "-a", "-q", "postgres")))


if __name__ == "__main__":
    unittest.main()
