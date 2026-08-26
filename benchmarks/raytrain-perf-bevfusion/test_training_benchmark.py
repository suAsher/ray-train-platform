import copy
import pickle
import sys
import tempfile
import unittest
from unittest import mock

from pathlib import Path

from training_benchmark import (
    map_explicit_recorded_path,
    load_subset,
    parse_args,
    resolve_files,
    rewrite_info_paths,
)


class RewriteInfoPathsTest(unittest.TestCase):
    def test_rewrites_lidar_sweeps_and_cameras_without_mutating_input(self):
        original = {
            "lidar_path": "/temp_data/site/sample/lidar.bin",
            "sweeps": [{"data_path": "/temp_data/site/sample/sweep.bin", "lag": 1}],
            "cams": {
                "front": {
                    "data_path": "/temp_data/site/sample/front.jpg",
                    "intrinsic": [1, 2, 3],
                }
            },
        }
        before = copy.deepcopy(original)
        mapping = {
            "/temp_data/site/sample/lidar.bin": "/mnt/cache/data/site/sample/lidar.bin",
            "/temp_data/site/sample/sweep.bin": "/mnt/cache2/data/site/sample/sweep.bin",
            "/temp_data/site/sample/front.jpg": "/mnt/cache/data/site/sample/front.jpg",
        }

        rewritten = rewrite_info_paths(original, mapping)

        self.assertEqual(original, before)
        self.assertEqual(rewritten["lidar_path"], mapping[original["lidar_path"]])
        self.assertEqual(rewritten["sweeps"][0]["data_path"], mapping[original["sweeps"][0]["data_path"]])
        self.assertEqual(rewritten["cams"]["front"]["data_path"], mapping[original["cams"]["front"]["data_path"]])
        self.assertEqual(rewritten["sweeps"][0]["lag"], 1)
        self.assertEqual(rewritten["cams"]["front"]["intrinsic"], [1, 2, 3])

    def test_accepts_training_batch_and_worker_overrides(self):
        argv = [
            "training_benchmark.py",
            "--config",
            "config.yaml",
            "--train",
            "train.pkl",
            "--val",
            "val.pkl",
            "--samples-per-gpu",
            "8",
            "--workers-per-gpu",
            "8",
            "--learning-rate",
            "0.0001",
        ]
        with mock.patch.object(sys, "argv", argv):
            args = parse_args()

        self.assertEqual(args.samples_per_gpu, 8)
        self.assertEqual(args.workers_per_gpu, 8)
        self.assertEqual(args.learning_rate, 0.0001)

    def test_maps_explicit_recorded_root_without_following_symlink_target(self):
        source_root = Path("/mnt/data/input")
        mapped = map_explicit_recorded_path(
            source_root,
            "/mnt/storage/me/temp_data/mxg128/sample.bin",
            "/mnt/storage/me",
        )

        self.assertEqual(mapped, source_root / "temp_data/mxg128/sample.bin")

    def test_rejects_recorded_path_outside_explicit_root(self):
        with self.assertRaises(ValueError):
            map_explicit_recorded_path(
                Path("/mnt/data/input"),
                "/mnt/storage/public/secret.bin",
                "/mnt/storage/me",
            )

    def test_explicit_mapping_does_not_scan_every_source_with_is_file(self):
        info = {"lidar_path": "/mnt/storage/me/temp_data/mxg128/sample.bin"}
        with mock.patch.object(
            Path,
            "is_file",
            side_effect=AssertionError("explicit mapping must not pre-stat FSX files"),
        ):
            resolved = resolve_files(
                Path("/mnt/data/input"), [info], "/mnt/storage/me"
            )

        self.assertEqual(
            resolved[info["lidar_path"]],
            Path("/mnt/data/input/temp_data/mxg128/sample.bin"),
        )

    def test_zero_limit_selects_all_infos(self):
        payload = {"infos": [{"token": "a"}, {"token": "b"}, {"token": "c"}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "infos.pkl"
            with path.open("wb") as stream:
                pickle.dump(payload, stream)

            subset_payload, infos = load_subset(path, 0)

        self.assertEqual(infos, payload["infos"])
        self.assertEqual(subset_payload["infos"], payload["infos"])


if __name__ == "__main__":
    unittest.main()
