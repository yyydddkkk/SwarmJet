"""Run with `python3 -m unittest discover -s deploy -p 'test_*.py'`.

Everything here is mocked: the tests never start Docker and never touch the
network.
"""
import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("installer", HERE / "install.py")
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


def completed(argv, stdout=""):
    return subprocess.CompletedProcess(argv, 0, stdout=stdout, stderr="")


class FakeTTY(io.StringIO):
    def isatty(self):
        return True


def make_repo(root: Path) -> Path:
    (root / "go.mod").write_text("module example.invalid/swarmjet\n")
    deploy = root / "deploy"
    deploy.mkdir()
    (deploy / "compose.yaml").write_text("services: {}\n")
    (deploy / "compose.cells.yaml").write_text("services: {}\n")
    (deploy / "compose.build.yaml").write_text("services: {}\n")
    (deploy / "install.py").write_text("# placeholder\n")
    (deploy / "Dockerfile").write_text("FROM scratch\n")
    observability = deploy / "observability"
    observability.mkdir()
    for name in ("prometheus.yml", "alerts.yml", "alertmanager.yml"):
        (observability / name).write_text("")
    return deploy


def make_values(**overrides):
    options = dict(
        data_dir="/tmp/swarmjet-test-data",
        port=8180,
        listen_host="127.0.0.1",
        public_origin="http://localhost:8180",
        cells=True,
        tunnel=False,
        image="swarmjet:local",
        agent_image="swarmjet-agent:local",
        admin_username="admin",
    )
    options.update(overrides)
    return installer.build_values({}, **options)


class EnvironmentTests(unittest.TestCase):
    def test_generated_secrets_and_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            values = make_values()
            installer.write_env(path, values)
            parsed = installer.read_env(path)

            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            secrets = [parsed[key] for key in ("POSTGRES_PASSWORD", "ADMIN_PASSWORD", "SWARMJET_MASTER_KEY")]
            self.assertEqual(len(set(secrets)), 3)
            self.assertEqual(len(parsed["SWARMJET_MASTER_KEY"]), 64)
            self.assertGreaterEqual(len(parsed["ADMIN_PASSWORD"]), 24)
            self.assertIn(parsed["POSTGRES_PASSWORD"], parsed["DATABASE_URL"])
            self.assertEqual(parsed["SWARMJET_CELLS"], "1")
            self.assertEqual(parsed["SWARMJET_TUNNEL_URL"], "")

    def test_existing_values_and_comments_are_kept(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text(
                "POSTGRES_PASSWORD=existing-password\n"
                "ADMIN_PASSWORD=existing-admin-password\n"
                "# keep me\n"
                "CUSTOM_KEY=1\n"
            )
            existing = installer.read_env(path)
            values = installer.build_values(
                existing,
                data_dir="/tmp/data",
                port=8180,
                listen_host="127.0.0.1",
                public_origin="http://localhost:8180",
                cells=False,
                tunnel=False,
                image="swarmjet:local",
                agent_image="swarmjet-agent:local",
                admin_username="admin",
            )
            installer.write_env(path, values)
            text = path.read_text()

            self.assertIn("POSTGRES_PASSWORD=existing-password", text)
            self.assertIn("ADMIN_PASSWORD=existing-admin-password", text)
            self.assertIn("# keep me", text)
            self.assertIn("CUSTOM_KEY=1", text)
            self.assertIn("existing-password", installer.read_env(path)["DATABASE_URL"])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertGreaterEqual(len(installer.read_env(path)["SWARMJET_MASTER_KEY"]), 64)

    def test_placeholders_are_replaced(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text(
                "POSTGRES_PASSWORD=replace-with-a-random-hex-password\n"
                "DATABASE_URL=postgres://swarmjet:replace-with-a-random-hex-password@postgres:5432/swarmjet?sslmode=disable\n"
            )
            values = installer.build_values(
                installer.read_env(path),
                data_dir="/tmp/data",
                port=8180,
                listen_host="127.0.0.1",
                public_origin="http://localhost:8180",
                cells=False,
                tunnel=False,
                image="swarmjet:local",
                agent_image="swarmjet-agent:local",
                admin_username="admin",
            )
            self.assertNotEqual(values["POSTGRES_PASSWORD"], "replace-with-a-random-hex-password")
            self.assertIn(values["POSTGRES_PASSWORD"], values["DATABASE_URL"])

    def test_data_dir_validation(self):
        with self.assertRaises(installer.InstallError):
            installer.validate_data_dir("relative/path")
        with self.assertRaises(installer.InstallError):
            installer.validate_data_dir("/tmp/has space")
        with self.assertRaises(installer.InstallError):
            installer.validate_data_dir("/tmp/$weird")

    def test_compose_arguments_include_cells_and_tunnel(self):
        values = {"SWARMJET_CELLS": "1", "SWARMJET_TUNNEL_URL": "http://tunnel:2000"}
        files = [path.name for path in installer.compose_files(Path("/srv/deploy"), values)]
        self.assertEqual(files, ["compose.yaml", "compose.cells.yaml"])
        argv = installer.compose_base(Path("/srv/deploy"), values)
        self.assertIn(str(Path("/srv/deploy/compose.cells.yaml")), argv)
        self.assertEqual(argv.count("--profile"), 1)
        self.assertIn("tunnel", argv)
        self.assertIn(str(Path("/srv/deploy/.env")), argv)


class ArgumentTests(unittest.TestCase):
    def test_default_command(self):
        self.assertEqual(installer.normalize_argv([]), ["install"])
        self.assertEqual(installer.normalize_argv(["--port", "9000"]), ["install", "--port", "9000"])
        self.assertEqual(installer.normalize_argv(["status"]), ["status"])

    def test_subcommands(self):
        parser = installer.build_parser()
        self.assertIs(parser.parse_args(["status"]).func, installer.status)
        install = parser.parse_args(["install", "--yes", "--no-cells", "--tunnel", "--ref", "dev", "--project-name", "swarmjet-e2e"])
        self.assertTrue(install.yes)
        self.assertFalse(install.cells)
        self.assertTrue(install.tunnel)
        self.assertEqual(install.ref, "dev")
        self.assertEqual(install.project_name, "swarmjet-e2e")
        connect = parser.parse_args(["connect", "--client", "codex"])
        self.assertEqual(connect.client, "codex")
        uninstall = parser.parse_args(["uninstall", "--purge", "--yes"])
        self.assertTrue(uninstall.purge)
        self.assertTrue(uninstall.yes)

    def test_repository_and_image_detection(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            self.assertTrue(installer.is_repo_directory(deploy))
            self.assertFalse(installer.is_repo_directory(root))
        self.assertEqual(
            installer.image_values("ghcr", "v1.2.3"),
            ("ghcr.io/yyydddkkk/swarmjet:v1.2.3", "ghcr.io/yyydddkkk/swarmjet/agent:v1.2.3"),
        )
        self.assertEqual(installer.image_values("repo", "latest"), ("swarmjet:local", "swarmjet-agent:local"))
        self.assertEqual(installer.tag_from_image("ghcr.io/yyydddkkk/swarmjet:v1.2.3"), "v1.2.3")

    def test_ref_resolution_and_download_url(self):
        with patch.dict(installer.os.environ, {}, clear=True):
            self.assertEqual(installer.resolve_ref(None), "main")
            self.assertEqual(installer.resolve_ref(None, "v1.2.3"), "v1.2.3")
            self.assertFalse(installer.is_release_ref("dev"))
            self.assertTrue(installer.is_release_ref("v1.2.3"))
        with patch.dict(installer.os.environ, {"SWARMJET_REF": "dev"}, clear=True):
            self.assertEqual(installer.resolve_ref(None), "dev")
            self.assertEqual(installer.resolve_ref("v1.2.3"), "v1.2.3")
            self.assertEqual(installer.resolve_ref(None, "v1.2.3"), "dev")
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(installer, "download") as download:
                installer.ensure_sources(Path(directory), "dev")
            urls = [call.args[0] for call in download.call_args_list]
            self.assertTrue(any("/dev/deploy/compose.yaml" in url for url in urls))
            self.assertTrue(any("/dev/deploy/install.py" in url for url in urls))


class ConnectTests(unittest.TestCase):
    def test_local_mcp_url_uses_installed_port_and_host(self):
        self.assertEqual(installer.local_mcp_url({"HTTP_PORT": "8190"}), "http://127.0.0.1:8190/mcp")
        self.assertEqual(installer.local_mcp_url({"HTTP_PORT": "9000", "LISTEN_HOST": "0.0.0.0"}), "http://127.0.0.1:9000/mcp")
        self.assertEqual(installer.local_mcp_url({"HTTP_PORT": "9000", "LISTEN_HOST": "10.0.0.5"}), "http://10.0.0.5:9000/mcp")
        self.assertEqual(installer.local_mcp_url({}), f"http://127.0.0.1:{installer.DEFAULT_PORT}/mcp")

    def test_connect_passes_the_installed_mcp_url(self):
        with tempfile.TemporaryDirectory() as directory:
            deploy = Path(directory)
            (deploy / ".env").write_text("COMPOSE_PROJECT_NAME=sj-test\nHTTP_PORT=8190\n")
            calls = []
            def run(argv, **kwargs):
                calls.append(list(argv))
                return completed(argv)
            with patch.object(installer, "run", side_effect=run), \
                 contextlib.redirect_stdout(io.StringIO()):
                code = installer.main(["connect", "--dir", str(deploy), "--client", "json"])
            self.assertEqual(code, 0)
            argv = calls[-1]
            self.assertIn("--url", argv)
            self.assertEqual(argv[argv.index("--url") + 1], "http://127.0.0.1:8190/mcp")


class InstallFlowTests(unittest.TestCase):
    def fake_run(self, calls):
        def run(argv, **kwargs):
            calls.append(list(argv))
            return completed(argv)
        return run

    def test_noninteractive_repository_install_builds_and_starts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            data = root / "data"
            calls = []
            with patch.object(installer, "script_dir", return_value=deploy), \
                 patch.object(installer, "check_docker"), \
                 patch.object(installer, "run", side_effect=self.fake_run(calls)), \
                 patch.object(installer, "port_available", return_value=True), \
                 patch.object(installer, "volume_exists", return_value=False), \
                 patch.object(installer, "stack_running", return_value=False), \
                 patch.object(installer, "warn_legacy"), \
                 contextlib.redirect_stdout(io.StringIO()):
                code = installer.main([
                    "install", "--dir", str(deploy), "--data-dir", str(data),
                    "--port", "18180", "--image-source", "repo", "--yes",
                    "--no-cells", "--no-tunnel",
                ])
            self.assertEqual(code, 0)
            self.assertTrue((deploy / ".env").exists())
            self.assertEqual((deploy / ".env").stat().st_mode & 0o777, 0o600)
            self.assertTrue(data.is_dir())
            parsed = installer.read_env(deploy / ".env")
            self.assertEqual(parsed["DATA_DIR"], str(data))
            self.assertEqual(parsed["HTTP_PORT"], "18180")
            self.assertEqual(parsed["SWARMJET_CELLS"], "0")
            self.assertEqual(parsed["SWARMJET_TUNNEL_URL"], "")

            build_calls = [call for call in calls if "build" in call]
            self.assertEqual(len(build_calls), 1)
            self.assertIn("--profile", build_calls[0])
            self.assertIn("tools", build_calls[0])
            self.assertTrue(any("up" in call and "--wait" in call for call in calls))
            self.assertFalse(any("pull" in call for call in calls))
            self.assertFalse(any("--no-deps" in call for call in calls))

    def run_install(self, deploy, data, extra, existing_env=None):
        if existing_env is not None:
            (deploy / ".env").write_text(existing_env)
            (deploy / ".env").chmod(0o600)
        calls = []
        with patch.object(installer, "script_dir", return_value=deploy), \
             patch.object(installer, "check_docker"), \
             patch.object(installer, "run", side_effect=self.fake_run(calls)), \
             patch.object(installer, "port_available", return_value=True), \
             patch.object(installer, "volume_exists", return_value=False), \
             patch.object(installer, "stack_running", return_value=False), \
             patch.object(installer, "warn_legacy"), \
             patch.object(installer, "wait_for_tunnel", return_value="https://abc-def.trycloudflare.com"), \
             contextlib.redirect_stdout(io.StringIO()) as out:
            code = installer.main([
                "install", "--dir", str(deploy), "--data-dir", str(data),
                "--port", "18190", "--image-source", "repo", "--yes", "--no-cells",
            ] + extra)
        self.assertEqual(code, 0)
        return installer.read_env(deploy / ".env"), out.getvalue()

    def test_tunnel_install_keeps_local_console_origin(self):
        # The tunnel only carries webhooks. A quick-tunnel PUBLIC_ORIGIN would
        # reject local logins and break once the tunnel address changes.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            parsed, output = self.run_install(deploy, root / "data", ["--tunnel"])
            self.assertEqual(parsed["PUBLIC_ORIGIN"], "http://localhost:18190")
            self.assertEqual(parsed["SWARMJET_TUNNEL_URL"], "http://tunnel:2000")
            self.assertIn("控制台：http://localhost:18190", output)
            self.assertIn("Webhook 隧道：https://abc-def.trycloudflare.com", output)

    def test_install_repairs_quick_tunnel_origin_from_older_installer(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            parsed, _ = self.run_install(
                deploy, root / "data", ["--tunnel"],
                existing_env="PUBLIC_ORIGIN=https://old-name.trycloudflare.com\nHTTP_PORT=18190\n",
            )
            self.assertEqual(parsed["PUBLIC_ORIGIN"], "http://localhost:18190")

    def test_install_keeps_explicit_public_origin_with_tunnel(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            parsed, _ = self.run_install(
                deploy, root / "data", ["--tunnel", "--public-origin", "https://swarmjet.example.com"],
            )
            self.assertEqual(parsed["PUBLIC_ORIGIN"], "https://swarmjet.example.com")

    def test_noninteractive_standalone_install_pulls_images(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            standalone = root / "standalone"
            standalone.mkdir()
            # Reuse the source manifests but pretend we are outside a checkout;
            # the observability files exist, so no download is needed.
            for _, local in installer.SOURCES:
                source = deploy / local
                target = standalone / local
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(source.read_text())
            empty = root / "not-a-checkout"
            empty.mkdir()
            calls = []
            with patch.object(installer, "script_dir", return_value=empty), \
                 patch.object(installer, "check_docker"), \
                 patch.object(installer, "run", side_effect=self.fake_run(calls)), \
                 patch.object(installer, "port_available", return_value=True), \
                 patch.object(installer, "volume_exists", return_value=False), \
                 patch.object(installer, "stack_running", return_value=False), \
                 patch.object(installer, "warn_legacy"), \
                 patch.object(installer, "download") as download, \
                 contextlib.redirect_stdout(io.StringIO()):
                code = installer.main([
                    "install", "--dir", str(standalone), "--data-dir", str(root / "data"),
                    "--port", "18180", "--image-source", "ghcr", "--version", "v1.2.3",
                    "--yes", "--no-cells", "--no-tunnel",
                ])
            self.assertEqual(code, 0)
            download.assert_not_called()
            parsed = installer.read_env(standalone / ".env")
            self.assertEqual(parsed["IMAGE"], "ghcr.io/yyydddkkk/swarmjet:v1.2.3")
            self.assertEqual(parsed["AGENT_IMAGE"], "ghcr.io/yyydddkkk/swarmjet/agent:v1.2.3")
            self.assertTrue(any("pull" in call for call in calls))
            self.assertFalse(any("build" in call for call in calls))

    def test_existing_postgres_volume_without_env_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deploy = make_repo(root)
            calls = []
            with patch.object(installer, "script_dir", return_value=deploy), \
                 patch.object(installer, "check_docker"), \
                 patch.object(installer, "run", side_effect=self.fake_run(calls)), \
                 patch.object(installer, "volume_exists", return_value=True), \
                 patch.object(installer, "port_available", return_value=True), \
                 patch.object(installer, "stack_running", return_value=False), \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                code = installer.main([
                    "install", "--dir", str(deploy), "--data-dir", str(root / "data"),
                    "--yes", "--no-cells", "--no-tunnel", "--image-source", "repo",
                ])
            self.assertEqual(code, 1)
            self.assertFalse((deploy / ".env").exists())
            self.assertFalse(any("build" in call or "up" in call for call in calls))


class UninstallTests(unittest.TestCase):
    def prepare(self, root: Path) -> Path:
        data = root / "data"
        data.mkdir()
        (data / "keep.txt").write_text("x")
        (root / ".env").write_text(
            "COMPOSE_PROJECT_NAME=swarmjet\n"
            f"DATA_DIR={data}\n"
            "SWARMJET_CELLS=0\n"
            "SWARMJET_TUNNEL_URL=\n"
        )
        (root / "compose.yaml").write_text("services: {}\n")
        return data

    def test_uninstall_keeps_data_by_default(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.prepare(root)
            calls = []
            def run(argv, **kwargs):
                calls.append(list(argv))
                return completed(argv)
            with patch.object(installer, "run", side_effect=run), contextlib.redirect_stdout(io.StringIO()):
                code = installer.main(["uninstall", "--dir", str(root)])
            self.assertEqual(code, 0)
            self.assertTrue(any("down" in call for call in calls))
            self.assertFalse(any("-v" in call for call in calls))
            self.assertTrue((data / "keep.txt").exists())

    def test_purge_requires_confirmation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.prepare(root)
            calls = []
            def run(argv, **kwargs):
                calls.append(list(argv))
                return completed(argv)
            with patch.object(installer, "run", side_effect=run), \
                 patch.object(installer.sys, "stdin", FakeTTY()), \
                 patch("builtins.input", return_value="no"), \
                 contextlib.redirect_stdout(io.StringIO()):
                code = installer.main(["uninstall", "--dir", str(root), "--purge"])
            self.assertEqual(code, 0)
            self.assertEqual(calls, [])
            self.assertTrue((data / "keep.txt").exists())

    def test_purge_after_confirmation_removes_volumes_and_data(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.prepare(root)
            calls = []
            def run(argv, **kwargs):
                calls.append(list(argv))
                return completed(argv)
            with patch.object(installer, "run", side_effect=run), \
                 patch.object(installer.sys, "stdin", FakeTTY()), \
                 patch("builtins.input", return_value="delete"), \
                 contextlib.redirect_stdout(io.StringIO()):
                code = installer.main(["uninstall", "--dir", str(root), "--purge"])
            self.assertEqual(code, 0)
            self.assertTrue(any("down" in call and "-v" in call for call in calls))
            self.assertFalse(data.exists())
            self.assertTrue((root / ".env").exists())

    def test_purge_with_yes_is_noninteractive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.prepare(root)
            calls = []
            def run(argv, **kwargs):
                calls.append(list(argv))
                return completed(argv)
            with patch.object(installer, "run", side_effect=run), \
                 patch("builtins.input", side_effect=AssertionError("must not prompt")), \
                 contextlib.redirect_stdout(io.StringIO()):
                code = installer.main(["uninstall", "--dir", str(root), "--purge", "--yes"])
            self.assertEqual(code, 0)
            self.assertTrue(any("down" in call and "-v" in call for call in calls))
            self.assertFalse(data.exists())

    def test_purge_without_tty_requires_yes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.prepare(root)
            calls = []
            def run(argv, **kwargs):
                calls.append(list(argv))
                return completed(argv)
            with patch.object(installer, "run", side_effect=run), \
                 patch.object(installer.sys, "stdin", io.StringIO()), \
                 contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                code = installer.main(["uninstall", "--dir", str(root), "--purge"])
            self.assertEqual(code, 1)
            self.assertEqual(calls, [])
            self.assertTrue((data / "keep.txt").exists())


if __name__ == "__main__":
    unittest.main()
