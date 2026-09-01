"""Tests for deterministic bounded S1H acceptance indexes."""

from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest


SCRIPT_DIR = Path(__file__).resolve().parent
PUBLISHER_ROOT = Path(__file__).resolve().parents[3] / "images" / "dataset-publisher"
for path in (SCRIPT_DIR, PUBLISHER_ROOT):
    if str(path) not in sys.path:
        sys.path.insert(0, str(path))

from build_s1h_trusted_index import write_multimodal_index_bundle
from slice_s1h_trusted_index import select_samples


class SliceS1HTrustedIndexTest(unittest.TestCase):
    def test_selects_exact_deterministic_train_and_val_counts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "trusted-index-v3.json"
            samples = [self._sample(f"train-{index}", "train") for index in range(7)]
            samples.extend(self._sample(f"val-{index}", "val") for index in range(4))
            write_multimodal_index_bundle(
                samples,
                output=source,
                cbgs_seed=0,
                samples_per_part=3,
            )

            first, counts = select_samples(source, train_samples=4, val_samples=2)
            second, _ = select_samples(source, train_samples=4, val_samples=2)

        self.assertEqual(counts, {"train": 7, "val": 4})
        self.assertEqual(sum(sample["split"] == "train" for sample in first), 4)
        self.assertEqual(sum(sample["split"] == "val" for sample in first), 2)
        self.assertEqual(
            [sample["token"] for sample in first],
            [sample["token"] for sample in second],
        )

    def test_rejects_a_requested_slice_larger_than_the_source(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "trusted-index-v3.json"
            write_multimodal_index_bundle(
                [self._sample("train-0", "train"), self._sample("val-0", "val")],
                output=source,
                cbgs_seed=0,
            )
            with self.assertRaisesRegex(ValueError, "requested acceptance slice"):
                select_samples(source, train_samples=2, val_samples=1)

    @staticmethod
    def _sample(token: str, split: str) -> dict:
        identity = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]]
        return {
            "schema_version": "s1h-multimodal-webdataset-v2",
            "token": token,
            "scene": "scene-token",
            "split": split,
            "class_ids": [0],
            "timestamp": 1,
            "payloads": {
                "lidar": "site/lidar.bin",
                "sweeps": [],
                "cameras": {"CAM_FRONT": "site/front.jpg"},
            },
            "info": {
                "boxes": [[1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 0.1]],
                "labels": [0],
                "lidar_feature_count": 4,
                "lidar2ego_rotation": identity,
                "lidar2ego_translation": [0.0, 0.0, 0.0],
                "ego2global_rotation": identity,
                "ego2global_translation": [0.0, 0.0, 0.0],
                "sweeps": [],
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
