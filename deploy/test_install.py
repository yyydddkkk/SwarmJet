"""Run with python3 -m unittest discover -s deploy -p 'test_install.py'."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("installer", HERE / "install.py")
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)
REGISTRY_SPEC = importlib.util.spec_from_file_location("registry_setup", HERE / "registry_setup.py")
registry_setup = importlib.util.module_from_spec(REGISTRY_SPEC)
REGISTRY_SPEC.loader.exec_module(registry_setup)


class InstallTests(unittest.TestCase):
    def test_registry_bootstrap_is_stable_and_private(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "registry"
            env_file = Path(directory) / ".env.registry"
            first = registry_setup.setup(root, "10.0.0.20", 5443, env_file, False)
            second = registry_setup.setup(root, "10.0.0.20", 5443, env_file, False)
            self.assertEqual(first, second)
            self.assertNotEqual(first["push_password"], first["pull_password"])
            self.assertEqual((root / "secrets.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(env_file.stat().st_mode & 0o777, 0o600)
            self.assertIn("10.0.0.20:5443", (root / "buildkitd.toml").read_text())

    def test_generated_secrets_permissions_and_rerun(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            installer.initialize(path, HERE / ".env.example", "/srv/test", "opencode,codex")
            original = path.read_bytes()
            values = dict(line.split("=", 1) for line in original.decode().splitlines()
                          if line and not line.startswith("#"))
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            secrets = [values[key] for key in ("POSTGRES_PASSWORD", "RABBITMQ_PASSWORD", "ADMIN_PASSWORD",
                                               "RUNTIME_ADMIN_SECRET", "SWARMJET_MASTER_KEY")]
            self.assertEqual(len(set(secrets)), 5)
            self.assertTrue(all(len(secret) >= 48 for secret in secrets))
            self.assertIn(values["POSTGRES_PASSWORD"], values["DATABASE_URL"])
            installer.initialize(path, HERE / ".env.example", "/different", "opencode")
            self.assertEqual(path.read_bytes(), original)

    def test_existing_env_gets_broker_secret_once(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text("POSTGRES_PASSWORD=existing\n")
            installer.initialize(path, HERE / ".env.example", "/srv/test", "opencode")
            first = path.read_text()
            self.assertEqual(first.count("RABBITMQ_PASSWORD="), 1)
            self.assertIn("POSTGRES_PASSWORD=existing", first)
            installer.initialize(path, HERE / ".env.example", "/srv/test", "opencode")
            self.assertEqual(path.read_text(), first)

    def exercise(self, fail_build=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".env.example").write_bytes((HERE / ".env.example").read_bytes())
            (root / "compose.override.yaml").touch()
            calls = []

            def run(argv, **kwargs):
                calls.append(argv)
                output = ""
                if argv[1:3] == ["context", "inspect"]:
                    output = json.dumps([{"Endpoints": {"docker": {"Host": "unix:///var/run/docker.sock"}}}])
                if "config" in argv:
                    values = dict(line.split("=", 1) for line in (root / ".env").read_text().splitlines()
                                  if line and not line.startswith("#"))
                    output = json.dumps({"services": {"api": {"environment": values},
                                                       "worker": {"environment": values}}})
                if fail_build and argv[-1] == "build":
                    raise subprocess.CalledProcessError(1, argv)
                return subprocess.CompletedProcess(argv, 0, output)

            def registry_setup(registry_root, host, env_file):
                env_file.write_text("REGISTRY_BIND_HOST=" + host + "\nREGISTRY_PORT=5443\n")

            with patch.object(installer, "__file__", str(root / "install.py")), \
                 patch.object(installer.shutil, "which", return_value="/usr/bin/docker"), \
                 patch.object(installer.subprocess, "run", side_effect=run), \
                 patch.object(installer, "lan_host", return_value="10.0.0.20"), \
                 patch.object(installer, "setup_registry", side_effect=registry_setup), \
                 patch.dict(installer.os.environ, {"DOCKER_HOST": "unix:///var/run/docker.sock"}), \
                 patch("sys.argv", ["install.py", "--data-dir", str(root / "data")]), \
                 contextlib.redirect_stdout(io.StringIO()):
                if fail_build:
                    with self.assertRaises(subprocess.CalledProcessError):
                        installer.main()
                    self.assertFalse(any("up" in call for call in calls))
                    self.assertTrue((root / ".env").exists())
                else:
                    installer.main()
                    self.assertTrue((root / "data").is_dir())
                    self.assertEqual(calls[-3][-3:], ["up", "-d", "--wait"])
                    self.assertEqual(calls[-2][-2:], ["swarmjet", "doctor"])
                    self.assertIn(str(root / "compose.override.yaml"), calls[-1])
                self.assertFalse(any("down" in call for call in calls))

    def test_start_and_health_check(self):
        self.exercise()

    def test_build_failure_does_not_start_services(self):
        self.exercise(fail_build=True)


if __name__ == "__main__":
    unittest.main()
