from __future__ import annotations

import hashlib
import io
import json
import pathlib
import sys
import tarfile
import tempfile
import unittest


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))


class S1HWebDatasetResolverTest(unittest.TestCase):
    def test_resolves_one_batch_through_digest_cache_and_cleans_staging(self):
        from raytrain_runtime.data_metrics import (
            reset_data_metrics_for_tests,
            snapshot_data_metrics,
        )
        from raytrain_runtime.s1h_webdataset import WebDatasetBatchResolver

        reset_data_metrics_for_tests()
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            dataset_root = root / "datasets"
            cache_roots = (root / "nvme1", root / "nvme2")
            staging_root = root / "staging"
            for path in (*cache_roots, staging_root):
                path.mkdir(parents=True)
            payload = self._tar_payload()
            digest = hashlib.sha256(payload).hexdigest()
            shard = (
                dataset_root
                / "dataset-s1h"
                / "shards"
                / f"sha256-{digest}.tar"
            )
            shard.parent.mkdir(parents=True)
            shard.write_bytes(payload)
            ref = self._ref(digest=digest, size=len(payload))
            resolver = WebDatasetBatchResolver(
                dataset_root=dataset_root,
                dataset_id="dataset-s1h",
                cache_policy="bounded",
                cache_roots=cache_roots,
                staging_root=staging_root,
            )

            with resolver.resolve_batch([ref]) as samples:
                self.assertEqual(len(samples), 1)
                sample = samples[0]
                lidar = pathlib.Path(sample["payload_paths"]["lidar"])
                camera = pathlib.Path(sample["payload_paths"]["cameras"]["CAM_FRONT"])
                sweep = pathlib.Path(sample["payload_paths"]["sweeps"][0])
                self.assertEqual(lidar.read_bytes(), b"lidar")
                self.assertEqual(camera.read_bytes(), b"camera")
                self.assertEqual(sweep.read_bytes(), b"sweep")
                self.assertEqual(sample["info"]["labels"], [0])
                materialized = lidar.parent.parent
                self.assertTrue(materialized.exists())
            self.assertFalse(materialized.exists())

            with resolver.resolve_batch([ref]) as samples:
                self.assertEqual(samples[0]["token"], "sample-token")
            metrics = resolver.cache_metrics()
            self.assertEqual(metrics["download"], 1)
            self.assertEqual(metrics["hit"], 1)
            observed = snapshot_data_metrics()
            self.assertEqual(observed["dataset_shard_reads_total"], 2)
            self.assertEqual(observed["dataset_source_reads_total"], 1)
            self.assertEqual(observed["dataset_cache_reads_total"], 2)
            self.assertEqual(observed["dataset_cache_downloads_total"], 1)
            self.assertEqual(observed["dataset_cache_hits_total"], 1)
            self.assertEqual(observed["dataset_source_bytes_total"], len(payload))
            self.assertEqual(
                observed["dataset_cache_bytes_read_total"], 2 * len(payload)
            )

    @classmethod
    def _tar_payload(cls) -> bytes:
        metadata = {
            "token": "sample-token",
            "scene": "scene-token",
            "split": "train",
            "class_ids": [0],
            "timestamp": 123456,
            "source_digest": "b" * 64,
            "payload_members": {
                "lidar": "sample-token/lidar.bin",
                "sweeps": ["sample-token/sweeps/000.bin"],
                "cameras": {"CAM_FRONT": "sample-token/cameras/CAM_FRONT.jpg"},
            },
            "info": {"labels": [0], "boxes": [[1.0] * 7]},
        }
        entries = {
            "sample-token/cameras/CAM_FRONT.jpg": b"camera",
            "sample-token/lidar.bin": b"lidar",
            "sample-token/metadata.json": json.dumps(metadata).encode(),
            "sample-token/sweeps/000.bin": b"sweep",
        }
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w:") as archive:
            for name, content in sorted(entries.items()):
                info = tarfile.TarInfo(name)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
        return output.getvalue()

    @staticmethod
    def _ref(*, digest: str, size: int) -> dict:
        return {
            "ordinal": 0,
            "token": "sample-token",
            "scene": "scene-token",
            "class_ids": [0],
            "timestamp": 123456,
            "source_digest": "b" * 64,
            "split": "train",
            "shard_path": f"dataset-s1h/shards/sha256-{digest}.tar",
            "shard_sha256": digest,
            "shard_size": size,
            "metadata_member": "sample-token/metadata.json",
        }


if __name__ == "__main__":
    unittest.main(verbosity=2)
