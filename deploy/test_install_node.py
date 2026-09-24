"""Focused validation of the private Node deployment bundle contract."""
import importlib.util
import json
from pathlib import Path
import socket
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("install_node", Path(__file__).with_name("install_node.py"))
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


class NodeInstallerTests(unittest.TestCase):
    def config(self):
        return {
            "nodeId": "remote1", "nodeSecret": "a" * 64,
            "image": "10.0.0.20:5443/swarmjet/platform@sha256:" + "b" * 64,
            "dataDir": "/srv/swarmjet/node-data", "advertiseUrl": "http://10.0.0.21:8092",
            "controlPlaneUrl": "http://10.0.0.20:8180", "registryAddress": "10.0.0.20:5443",
            "registryUser": "reader", "registryPassword": "private-password",
            "capacity": 2, "port": 8092, "listenHost": "0.0.0.0",
        }

    def read(self, value):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "node.json"
            path.write_text(json.dumps(value))
            return installer.read_config(path)

    def test_accepts_pinned_builtin_registry_image(self):
        self.assertEqual(self.read(self.config())["nodeId"], "remote1")

    def test_rejects_wrong_registry_and_unsafe_env_values(self):
        for key, value in [
            ("image", "other.example/swarmjet/platform@sha256:" + "b" * 64),
            ("dataDir", "/srv/swarmjet/../other"),
            ("advertiseUrl", "http://user:password@10.0.0.21:8092"),
            ("controlPlaneUrl", "http://10.0.0.20:8180/other"),
            ("nodeSecret", "short"),
        ]:
            with self.subTest(key=key):
                config = self.config()
                config[key] = value
                with self.assertRaises(ValueError):
                    self.read(config)

    def test_private_file_is_replaced_with_0600_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "node.env"
            path.write_text("old")
            installer.write_private(path, "new")
            self.assertEqual(path.read_text(), "new")
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_preflight_rejects_an_occupied_node_port(self):
        with tempfile.TemporaryDirectory() as directory, socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            config = self.config()
            config["listenHost"] = "127.0.0.1"
            config["port"] = listener.getsockname()[1]
            config["dataDir"] = directory
            with patch.object(installer, "HERE", Path(directory)):
                with self.assertRaisesRegex(RuntimeError, "端口已被占用"):
                    installer.probe_host(config)

    def test_preflight_preserves_existing_node_data_dir(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.config()
            config["dataDir"] = directory
            (Path(directory) / "node.env").write_text("NODE_ID=remote1\nNODE_DATA_DIR=/srv/old-data\nNODE_PORT=8092\nNODE_LISTEN_HOST=0.0.0.0\n")
            with patch.object(installer, "HERE", Path(directory)):
                with self.assertRaisesRegex(RuntimeError, "数据目录不能更改"):
                    installer.probe_host(config)


if __name__ == "__main__":
    unittest.main()
