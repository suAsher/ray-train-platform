from __future__ import annotations

import hashlib
import importlib.util
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher.cloud_publish import CloudPublishRequest  # noqa: E402
from raytrain_publisher.distributed_publish import (  # noqa: E402
    build_multimodal_reference_manifest_rows,
    run_finalize,
    run_pack,
)
from raytrain_publisher.tos_storage import TOSStorageError  # noqa: E402


HAS_PYARROW = importlib.util.find_spec("pyarrow") is not None


class _Storage:
    def __init__(
        self,
        *,
        indexes: dict[str, bytes],
        sources: dict[str, bytes],
    ) -> None:
        self.indexes = indexes
        self.sources = sources
        self.objects: dict[str, bytes] = {}

    def get_index(self, key: str, *, maximum_bytes: int) -> bytes:
        payload = self.indexes[key]
        if len(payload) > maximum_bytes:
            raise TOSStorageError("index read failed")
        return payload

    def head_source(self, key: str) -> SimpleNamespace:
        payload = self.sources[key]
        return SimpleNamespace(
            size=len(payload), sha256=hashlib.sha256(payload).hexdigest()
        )

    def download_file(
        self, key: str, destination: str | Path, *, maximum_bytes: int
    ) -> SimpleNamespace:
        payload = self.sources[key]
        if len(payload) > maximum_bytes:
            raise TOSStorageError("source download failed")
        target = Path(destination)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(payload)
        return SimpleNamespace(
            size=len(payload), sha256=hashlib.sha256(payload).hexdigest()
        )

    def put_immutable(
        self,
        key: str,
        content: bytes | io.BufferedReader,
        *,
        sha256: str,
        maximum_bytes: int,
        size: int | None = None,
        content_type: str = "application/octet-stream",
    ) -> None:
        del content_type
        payload = content if isinstance(content, bytes) else content.read()
        if len(payload) > maximum_bytes or (size is not None and len(payload) != size):
            raise TOSStorageError("immutable write failed")
        if hashlib.sha256(payload).hexdigest() != sha256:
            raise TOSStorageError("immutable write failed")
        if key in self.objects and self.objects[key] != payload:
            raise TOSStorageError("immutable object conflict")
        self.objects[key] = payload

    def verify_immutable(
        self, key: str, *, expected_size: int, expected_sha256: str
    ) -> None:
        payload = self.objects[key]
        if len(payload) != expected_size or hashlib.sha256(payload).hexdigest() != expected_sha256:
            raise TOSStorageError("immutable verify failed")

    def get_immutable(self, key: str, *, maximum_bytes: int) -> bytes:
        try:
            payload = self.objects[key]
        except KeyError:
            raise TOSStorageError("immutable read failed") from None
        if len(payload) > maximum_bytes:
            raise TOSStorageError("immutable read failed")
        return payload


class MultimodalDistributedPublicationTest(unittest.TestCase):
    def test_pack_writes_content_addressed_tar_and_recoverable_receipt(self) -> None:
        sample = self._sample()
        indexes = self._index_bundle(sample)
        sources = {
            "site/lidar/sample.bin": b"lidar-payload",
            "site/sweeps/previous.bin": b"sweep-payload",
            "site/cameras/front.jpg": b"camera-payload",
        }
        storage = _Storage(indexes=indexes, sources=sources)
        with tempfile.TemporaryDirectory() as directory:
            run_pack(
                self._request(Path(directory)),
                storage=storage,
                partition_count=1,
                ordinal=0,
            )

        shard_keys = [key for key in storage.objects if key.endswith(".tar")]
        self.assertEqual(len(shard_keys), 1)
        shard_payload = storage.objects[shard_keys[0]]
        self.assertIn(hashlib.sha256(shard_payload).hexdigest(), shard_keys[0])
        receipts = [
            json.loads(payload)
            for key, payload in storage.objects.items()
            if "/publication/receipts/" in key
        ]
        self.assertEqual(len(receipts), 1)
        self.assertEqual(receipts[0]["locators"][0]["token"], "sample-token")
        self.assertEqual(receipts[0]["locators"][0]["shard_path"], shard_keys[0])
        self.assertEqual(receipts[0]["locators"][0]["shard_sha256"], hashlib.sha256(shard_payload).hexdigest())
        self.assertEqual(
            {source["key"] for source in receipts[0]["sources"]}, set(sources)
        )
        rows = build_multimodal_reference_manifest_rows(
            [sample],
            {row["token"]: row for row in receipts[0]["locators"]},
        )
        self.assertEqual(rows[0]["ordinal"], 0)
        self.assertEqual(rows[0]["metadata_member"], "sample-token/metadata.json")

        if HAS_PYARROW:
            with tempfile.TemporaryDirectory() as finalize_directory:
                result = run_finalize(
                    self._request(Path(finalize_directory)),
                    storage=storage,
                    partition_count=1,
                )
            self.assertEqual(result["receipt"]["schema_version"], "s1h-multimodal-webdataset-v2")
            self.assertIn("dataset-test/manifests/version-test.parquet", storage.objects)

    @staticmethod
    def _request(output_dir: Path) -> CloudPublishRequest:
        return CloudPublishRequest(
            run_id="publication-test",
            dataset_id="dataset-test",
            dataset_version_id="version-test",
            version="20260901T000000Z+test",
            schema_version="s1h-multimodal-webdataset-v2",
            source_bucket="source-bucket",
            target_bucket="target-bucket",
            tos_endpoint="tos-cn-shanghai.ivolces.com",
            tos_region="cn-shanghai",
            source_root="ray-train/public/labeled",
            source_index=".raytrain/trusted-index-v3.json",
            internal_prefix="ray-train/platform/datasets",
            output_dir=output_dir,
        )

    @classmethod
    def _index_bundle(cls, sample: dict) -> dict[str, bytes]:
        part = cls._json(
            {
                "format": "trusted-index-v3",
                "sample_schema_version": "s1h-multimodal-webdataset-v2",
                "class_count": 10,
                "cbgs_seed": 0,
                "samples": [sample],
            }
        )
        digest = hashlib.sha256(part).hexdigest()
        root = cls._json(
            {
                "format": "trusted-index-sharded-v2",
                "sample_schema_version": "s1h-multimodal-webdataset-v2",
                "class_count": 10,
                "cbgs_seed": 0,
                "sample_count": 1,
                "parts": [
                    {
                        "key": f"trusted-index-v3.parts/sha256-{digest}.json",
                        "sha256": digest,
                        "sample_count": 1,
                    }
                ],
            }
        )
        return {
            ".raytrain/trusted-index-v3.json": root,
            f".raytrain/trusted-index-v3.parts/sha256-{digest}.json": part,
        }

    @staticmethod
    def _json(value: object) -> bytes:
        return json.dumps(value, separators=(",", ":"), sort_keys=True).encode()

    @staticmethod
    def _sample() -> dict:
        identity = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]]
        return {
            "schema_version": "s1h-multimodal-webdataset-v2",
            "token": "sample-token",
            "scene": "scene-token",
            "split": "train",
            "class_ids": [0],
            "timestamp": 123456,
            "payloads": {
                "lidar": "site/lidar/sample.bin",
                "sweeps": ["site/sweeps/previous.bin"],
                "cameras": {"CAM_FRONT": "site/cameras/front.jpg"},
            },
            "info": {
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.1]],
                "labels": [0],
                "lidar_feature_count": 4,
                "lidar2ego_rotation": identity,
                "lidar2ego_translation": [0.0, 0.0, 0.0],
                "ego2global_rotation": identity,
                "ego2global_translation": [0.0, 0.0, 0.0],
                "sweeps": [
                    {
                        "timestamp": 123400,
                        "sensor2lidar_rotation": identity,
                        "sensor2lidar_translation": [0.0, 0.0, 0.0],
                    }
                ],
                "cams": {
                    "CAM_FRONT": {
                        "sensor2lidar_rotation": identity,
                        "sensor2lidar_translation": [0.0, 0.0, 0.0],
                        "sensor2ego_rotation": identity,
                        "sensor2ego_translation": [0.0, 0.0, 0.0],
                        "camera_intrinsics": identity,
                    }
                },
            },
        }


if __name__ == "__main__":
    unittest.main(verbosity=2)
