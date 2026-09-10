import hashlib
import json
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

    def test_incremental_inventory_reuses_unchanged_content_and_reports_tombstones(self):
        from tempfile import TemporaryDirectory

        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source" / "QP_NuScene" / "labeled"
            source.mkdir(parents=True)
            frame = source / "frame.bin"
            frame.write_bytes(b"frame")
            stat = frame.stat()
            digest = hashlib.sha256(b"frame").hexdigest()
            previous = {
                "schema": 1,
                "entries": [
                    {
                        "relativePath": "frame.bin",
                        "sizeBytes": stat.st_size,
                        "modifiedAtNs": stat.st_mtime_ns,
                        "modifiedAt": worker._timestamp(stat.st_mtime_ns),
                        "sha256": digest,
                        "objectKey": f"ray-train/platform/idc-raw/sha256/{digest[:2]}/{digest}",
                    },
                    {
                        "relativePath": "removed.bin",
                        "sizeBytes": 7,
                        "modifiedAtNs": stat.st_mtime_ns,
                        "modifiedAt": worker._timestamp(stat.st_mtime_ns),
                        "sha256": "a" * 64,
                        "objectKey": "ray-train/platform/idc-raw/sha256/aa/" + "a" * 64,
                    },
                ],
            }
            config = root / "config"
            config.write_text("configured")
            commands: list[list[str]] = []

            def run_tosutil(arguments: list[str]) -> None:
                commands.append(arguments)
                if arguments[1] == "cp" and arguments[2].startswith("tos://") and arguments[3].endswith("previous-inventory.json"):
                    Path(arguments[3]).write_text(json.dumps(previous))

            callbacks: list[tuple[str, str, dict[str, object]]] = []
            with patch.object(worker, "_run_tosutil", run_tosutil), patch.object(
                worker, "_post_callback", lambda url, token, payload: callbacks.append((url, token, payload))
            ), patch.object(worker, "_sha256_file", side_effect=AssertionError("unchanged file was hashed")):
                receipt = worker.run([
                    "--run-id", "run-2", "--source-root", str(root / "source"), "--source-relative-path", "QP_NuScene/labeled",
                    "--mirror-prefix", "ray-train/platform/idc-mirror/labeled", "--internal-prefix", "ray-train/platform",
                    "--bucket", "training-data", "--tosutil-config", str(config), "--work-dir", str(root / "work"),
                    "--previous-inventory-key", "ray-train/platform/idc-inventories/run-1/" + "b" * 64 + ".json",
                    "--callback-url", "http://backend/internal", "--callback-token", "worker-token",
                ])

            self.assertEqual(receipt["newObjectCount"], 0)
            self.assertEqual(receipt["changedObjectCount"], 0)
            self.assertEqual(receipt["reusedObjectCount"], 1)
            self.assertEqual(receipt["tombstonedObjectCount"], 1)
            immutable_uploads = [command for command in commands if "/idc-raw/sha256/" in " ".join(command)]
            self.assertEqual(immutable_uploads, [])
            self.assertEqual(callbacks[-1][2]["reusedObjectCount"], 1)

    def test_main_reports_sanitized_failure_to_control_plane(self):
        with patch.object(worker, "run", side_effect=worker.SyncError("secret path")), patch.object(
            worker, "_failure_callback_from_argv", return_value=("http://backend/internal/failed", "token", "run-3")
        ), patch.object(worker, "_post_callback") as callback:
            self.assertEqual(worker.main(), 1)
        callback.assert_called_once_with(
            "http://backend/internal/failed", "token", {"runId": "run-3", "failureReason": "IDC sync worker failed"}
        )
