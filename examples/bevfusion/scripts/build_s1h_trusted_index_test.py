"""Contract tests for the S1H PKL to trusted-index adapter."""

from __future__ import annotations

import sys
from pathlib import Path
import tempfile
import unittest

import numpy as np


SCRIPT_DIR = Path(__file__).resolve().parent
PUBLISHER_ROOT = Path(__file__).resolve().parents[3] / "images" / "dataset-publisher"
for path in (SCRIPT_DIR, PUBLISHER_ROOT):
    if str(path) not in sys.path:
        sys.path.insert(0, str(path))


class BuildS1HTrustedIndexTest(unittest.TestCase):
    def setUp(self) -> None:
        try:
            import build_s1h_trusted_index as adapter
        except ModuleNotFoundError as error:
            raise AssertionError("S1H trusted-index adapter is not implemented") from error
        self.adapter = adapter

    def test_converts_lidar_only_info_with_exact_s1h_class_order(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "cnfzhjyg" / "package" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            info = self._info(
                lidar,
                gt_names=np.array(["Car", "igv.w_container", "unknown"]),
                gt_boxes=np.array(
                    [
                        [1, 2, 3, 4, 5, 6, 0.1],
                        [7, 8, 9, 10, 11, 12, 0.2],
                        [13, 14, 15, 16, 17, 18, 0.3],
                    ],
                    dtype=np.float32,
                ),
            )

            samples = self.adapter.convert_infos(
                [info], split="train", source_root=source_root
            )

        self.assertEqual(len(samples), 1)
        sample = samples[0]
        self.assertEqual(sample["token"], "sample-token")
        self.assertEqual(sample["scene"], "scene-token")
        self.assertEqual(sample["split"], "train")
        self.assertEqual(sample["class_ids"], [0, 9])
        self.assertEqual(
            sample["lidar_path"],
            "cnfzhjyg/package/samples/LIDAR_TOP/1.bin",
        )
        self.assertEqual(sample["point_columns"], 4)
        self.assertEqual(sample["info"]["labels"], [0, 9])
        self.assertEqual(len(sample["info"]["boxes"]), 2)

    def test_serializes_output_with_the_publisher_restricted_contract(self):
        from raytrain_publisher.pack import load_trusted_index_document

        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "scene" / "samples" / "LIDAR_TOP" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            samples = self.adapter.convert_infos(
                [self._info(lidar)], split="val", source_root=source_root
            )
            payload = self.adapter.dump_index(samples, cbgs_seed=17)

        document = load_trusted_index_document(payload)
        self.assertEqual(document.class_count, 10)
        self.assertEqual(document.cbgs_seed, 17)
        self.assertEqual(document.samples[0]["split"], "val")

    def test_rejects_path_escape_duplicate_token_and_invalid_boxes(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            outside = source_root.parent / "outside.bin"
            outside.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            with self.assertRaisesRegex(ValueError, "source root"):
                self.adapter.convert_infos(
                    [self._info(outside)], split="train", source_root=source_root
                )

            lidar = source_root / "site" / "scene" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            duplicate = self._info(lidar)
            with self.assertRaisesRegex(ValueError, "duplicate token"):
                self.adapter.merge_splits(
                    train_infos=[duplicate],
                    val_infos=[dict(duplicate)],
                    source_root=source_root,
                )

            invalid = self._info(lidar)
            invalid["gt_boxes"] = np.array(
                [[1, 2, 3, 4, 5, 6, np.nan]], dtype=np.float32
            )
            with self.assertRaisesRegex(ValueError, "finite"):
                self.adapter.convert_infos(
                    [invalid], split="train", source_root=source_root
                )

    def test_requires_explicit_scene_token_and_supported_split(self):
        with tempfile.TemporaryDirectory() as directory:
            source_root = Path(directory)
            lidar = source_root / "site" / "scene" / "1.bin"
            lidar.parent.mkdir(parents=True)
            lidar.write_bytes(np.arange(8, dtype=np.float32).tobytes())
            missing_scene = self._info(lidar)
            del missing_scene["scene_token"]
            with self.assertRaisesRegex(ValueError, "scene_token"):
                self.adapter.convert_infos(
                    [missing_scene], split="train", source_root=source_root
                )
            with self.assertRaisesRegex(ValueError, "split"):
                self.adapter.convert_infos(
                    [self._info(lidar)], split="dev", source_root=source_root
                )

    @staticmethod
    def _info(
        lidar: Path,
        *,
        gt_names: np.ndarray | None = None,
        gt_boxes: np.ndarray | None = None,
    ) -> dict[str, object]:
        return {
            "token": "sample-token",
            "scene_token": "scene-token",
            "timestamp": 123456,
            "lidar_path": str(lidar),
            "gt_names": gt_names if gt_names is not None else np.array(["Car"]),
            "gt_boxes": gt_boxes
            if gt_boxes is not None
            else np.array([[1, 2, 3, 4, 5, 6, 0.1]], dtype=np.float32),
        }


if __name__ == "__main__":
    unittest.main(verbosity=2)
