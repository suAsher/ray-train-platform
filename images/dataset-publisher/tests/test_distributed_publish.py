from __future__ import annotations

import hashlib
import json
import sys
import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher.distributed_publish import (  # noqa: E402
    build_partition_plan,
    run_plan,
)
from raytrain_publisher.cloud_publish import CloudPublishRequest  # noqa: E402
from raytrain_publisher.multimodal import (  # noqa: E402
    load_cloud_multimodal_index,
)


class _IndexStorage:
    def __init__(self, objects: dict[str, bytes]) -> None:
        self.objects = objects

    def get_index(self, key: str, *, maximum_bytes: int) -> bytes:
        payload = self.objects[key]
        if len(payload) > maximum_bytes:
            raise ValueError("index exceeds bound")
        return payload


class DistributedPublicationPlanTest(unittest.TestCase):
    def test_plan_is_stable_and_reuses_only_matching_verified_receipts(self) -> None:
        samples = (
            {"token": "sample-a", "source_key": "a.bin", "size": 10, "sha256": "a" * 64},
            {"token": "sample-b", "source_key": "b.bin", "size": 20, "sha256": "b" * 64},
        )
        first = build_partition_plan(samples=samples, partition_count=2)
        receipts = {item.ordinal: item.input_fingerprint for item in first}

        second = build_partition_plan(
            samples=samples,
            partition_count=2,
            verified_base_receipts=receipts,
        )
        self.assertTrue(all(item.reused for item in second))

        changed = build_partition_plan(
            samples=(samples[0], {**samples[1], "size": 21}),
            partition_count=2,
            verified_base_receipts=receipts,
        )
        self.assertTrue(any(not item.reused for item in changed))

    def test_loads_verified_sharded_multimodal_index(self) -> None:
        sample = self._multimodal_sample()
        part = self._canonical_json(
            {
                "format": "trusted-index-v3",
                "sample_schema_version": "s1h-multimodal-webdataset-v2",
                "class_count": 10,
                "cbgs_seed": 0,
                "samples": [sample],
            }
        )
        digest = hashlib.sha256(part).hexdigest()
        root = self._canonical_json(
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
        storage = _IndexStorage(
            {
                ".raytrain/trusted-index-v3.json": root,
                f".raytrain/trusted-index-v3.parts/sha256-{digest}.json": part,
            }
        )

        document = load_cloud_multimodal_index(
            storage=storage,
            source_root="ray-train/public/labeled",
            source_index=".raytrain/trusted-index-v3.json",
        )

        self.assertEqual(document.class_count, 10)
        self.assertEqual(document.cbgs_seed, 0)
        self.assertEqual(len(document.samples), 1)
        self.assertEqual(document.samples[0]["token"], sample["token"])
        self.assertEqual(
            document.samples[0]["schema_version"],
            "s1h-multimodal-webdataset-v2",
        )
        self.assertEqual(
            document.samples[0]["payloads"], sample["payloads"]
        )

        run_plan(
            CloudPublishRequest(
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
                output_dir=Path("/tmp/raytrain-test"),
            ),
            storage=storage,
            partition_count=8,
        )

    @staticmethod
    def _canonical_json(value: object) -> bytes:
        return json.dumps(
            value, ensure_ascii=False, separators=(",", ":"), sort_keys=True
        ).encode("utf-8")

    @staticmethod
    def _multimodal_sample() -> dict:
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
    unittest.main()
