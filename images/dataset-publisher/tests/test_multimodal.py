from __future__ import annotations

import hashlib
import io
import json
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT))

from raytrain_publisher.multimodal import pack_multimodal_shard  # noqa: E402


class MultimodalShardTest(unittest.TestCase):
    def test_packs_sensor_payloads_into_a_stable_path_free_webdataset_tar(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            payloads = {
                "site/lidar/sample.bin": b"lidar-bytes",
                "site/sweeps/previous.bin": b"sweep-bytes",
                "site/cameras/front.jpg": b"front-camera",
            }
            for relative, content in payloads.items():
                destination = root / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(content)

            first = pack_multimodal_shard([self._sample()], input_root=root)
            second = pack_multimodal_shard([self._sample()], input_root=root)

        self.assertEqual(first.payload, second.payload)
        self.assertEqual(first.sha256, hashlib.sha256(first.payload).hexdigest())
        self.assertEqual(len(first.members), 1)
        self.assertNotIn(str(root), first.payload.decode("latin1"))
        with tarfile.open(fileobj=io.BytesIO(first.payload), mode="r:") as archive:
            self.assertEqual(
                archive.getnames(),
                [
                    "sample-token/cameras/CAM_FRONT.jpg",
                    "sample-token/lidar.bin",
                    "sample-token/metadata.json",
                    "sample-token/sweeps/000.bin",
                ],
            )
            metadata = json.loads(
                archive.extractfile("sample-token/metadata.json").read()
            )
        self.assertEqual(metadata["token"], "sample-token")
        self.assertEqual(metadata["payload_members"]["lidar"], "sample-token/lidar.bin")
        self.assertNotIn("site/lidar/sample.bin", json.dumps(metadata, sort_keys=True))
        self.assertEqual(first.members[0]["source_digest"], metadata["source_digest"])

    def test_rejects_path_escape_and_unexpected_sensor_descriptors(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sample = self._sample()
            sample["payloads"] = {**sample["payloads"], "cameras": {"CAM_FRONT": "../escape.jpg"}}
            with self.assertRaisesRegex(ValueError, "camera payload"):
                pack_multimodal_shard([sample], input_root=root)

            extra = self._sample()
            extra["payloads"] = {**extra["payloads"], "radar": "site/radar.bin"}
            with self.assertRaisesRegex(ValueError, "payload fields"):
                pack_multimodal_shard([extra], input_root=root)

    @staticmethod
    def _sample() -> dict:
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
                "lidar2ego_rotation": [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]],
                "lidar2ego_translation": [0.0, 0.0, 0.0],
                "ego2global_rotation": [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]],
                "ego2global_translation": [0.0, 0.0, 0.0],
                "sweeps": [{"timestamp": 123400, "sensor2lidar_rotation": [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]], "sensor2lidar_translation": [0.0, 0.0, 0.0]}],
                "cams": {"CAM_FRONT": {"sensor2lidar_rotation": [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]], "sensor2lidar_translation": [0.0, 0.0, 0.0], "sensor2ego_rotation": [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]], "sensor2ego_translation": [0.0, 0.0, 0.0], "camera_intrinsics": [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]]}},
            },
        }


if __name__ == "__main__":
    unittest.main(verbosity=2)
