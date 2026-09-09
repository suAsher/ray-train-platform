import hashlib
from pathlib import Path
from unittest import TestCase
from unittest.mock import patch

from idc_sync import worker


class WorkerTest(TestCase):
    def test_canonical_inventory_is_stable_and_content_addressed(self):
        with self.subTest("worker receipt"):
            from tempfile import TemporaryDirectory

            with TemporaryDirectory() as temporary:
                root = Path(temporary)
                source = root / "source" / "QP_NuScene" / "labeled"
                source.mkdir(parents=True)
                (source / "frame.bin").write_bytes(b"frame")
                config = root / "config"
                config.write_text("configured")
                commands: list[list[str]] = []
                callbacks: list[tuple[str, str, dict[str, object]]] = []
                with patch.object(worker, "_run_tosutil", commands.append), patch.object(worker, "_post_callback", lambda url, token, payload: callbacks.append((url, token, payload))):
                    receipt = worker.run([
                        "--run-id", "run-1", "--source-root", str(root / "source"), "--source-relative-path", "QP_NuScene/labeled",
                        "--mirror-prefix", "ray-train/platform/idc-mirror/labeled", "--internal-prefix", "ray-train/platform",
                        "--bucket", "training-data", "--tosutil-config", str(config), "--work-dir", str(root / "work"),
                        "--callback-url", "http://backend/internal", "--callback-token", "worker-token",
                    ])
                self.assertEqual(receipt["sourceObjectCount"], 1)
                entry = receipt["entries"][0]
                self.assertEqual(entry["sha256"], hashlib.sha256(b"frame").hexdigest())
                self.assertTrue(entry["objectKey"].endswith("/" + entry["sha256"]))
                self.assertEqual(receipt["inventorySha256"], hashlib.sha256((root / "work" / "inventory.json").read_bytes()).hexdigest())
                self.assertEqual(commands[0][1:5], ["cp", str(source) + "/", "tos://training-data/ray-train/platform/idc-mirror/labeled/", "-r"])
                self.assertEqual(callbacks[-1][0], "http://backend/internal/complete")

    def test_worker_rejects_unsafe_source_paths(self):
        from tempfile import TemporaryDirectory

        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            config = root / "config"
            config.write_text("configured")
            for source_path in ["/etc", "../outside", "QP_NuScene/../labeled"]:
                with self.subTest(source_path=source_path), self.assertRaises(worker.SyncError):
                    worker.run([
                        "--run-id", "run-1", "--source-root", str(root), "--source-relative-path", source_path,
                        "--mirror-prefix", "mirror", "--internal-prefix", "internal", "--bucket", "training-data",
                        "--tosutil-config", str(config), "--work-dir", str(root / "work"),
                        "--callback-url", "http://backend/internal", "--callback-token", "worker-token",
                    ])
