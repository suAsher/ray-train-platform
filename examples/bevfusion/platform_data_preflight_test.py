"""Tests for the standalone BEVFusion public-data preflight."""

import os
import io
import pickle
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from platform_data_preflight import (  # noqa: E402
    parse_args,
    main,
    resolve_annotation_path,
    resolver_module_dir,
    split_sample_limit,
)


class SampleLimitTest(unittest.TestCase):
    def test_accepts_different_train_and_validation_limits(self):
        args = parse_args(
            [
                "--train",
                "train.pkl",
                "--val",
                "val.pkl",
                "--max-train",
                "1024",
                "--max-val",
                "512",
            ]
        )
        self.assertEqual(split_sample_limit(args, "train"), 1024)
        self.assertEqual(split_sample_limit(args, "val"), 512)

    def test_legacy_samples_value_remains_the_default_for_both_splits(self):
        args = parse_args(
            ["--train", "train.pkl", "--val", "val.pkl", "--samples", "64"]
        )
        self.assertEqual(split_sample_limit(args, "train"), 64)
        self.assertEqual(split_sample_limit(args, "val"), 64)

    def test_rejects_non_positive_split_limits(self):
        args = parse_args(
            ["--train", "train.pkl", "--val", "val.pkl", "--max-val", "0"]
        )
        with self.assertRaisesRegex(ValueError, "positive"):
            split_sample_limit(args, "val")


class PathBoundaryTest(unittest.TestCase):
    def test_rejects_absolute_annotation_paths(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaisesRegex(ValueError, "relative"):
                resolve_annotation_path(Path(root), "/tmp/train.pkl")

    def test_rejects_parent_traversal_outside_the_selected_data_root(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaisesRegex(ValueError, "inside"):
                resolve_annotation_path(Path(root), "../train.pkl")

    def test_resolves_a_relative_annotation_inside_the_data_root(self):
        with tempfile.TemporaryDirectory() as root:
            expected = Path(root, "annotations", "train.pkl").resolve()
            self.assertEqual(
                resolve_annotation_path(Path(root), "annotations/train.pkl"),
                expected,
            )

    def test_finds_the_example_resolver_when_run_from_this_repository(self):
        module_dir = resolver_module_dir(Path(__file__).resolve().with_name("platform_data_preflight.py"))
        self.assertEqual(module_dir.name, "patches")
        self.assertTrue((module_dir / "platform_paths.py").is_file())

    def test_main_runs_from_the_example_location_and_checks_real_files(self):
        with tempfile.TemporaryDirectory() as root:
            root_path = Path(root)
            sample = root_path / "sample.bin"
            sample.write_bytes(b"\x00")
            payload = {"infos": [{"lidar_path": "/legacy/sample.bin"}]}
            for name in ("train.pkl", "val.pkl"):
                with (root_path / name).open("wb") as stream:
                    pickle.dump(payload, stream)

            argv = [
                "platform_data_preflight.py",
                "--train",
                "train.pkl",
                "--val",
                "val.pkl",
                "--samples",
                "1",
            ]
            output = io.StringIO()
            with mock.patch.dict(os.environ, {"PLATFORM_DATASET_PATH": root}, clear=False):
                with mock.patch.object(sys, "argv", argv), redirect_stdout(output):
                    self.assertEqual(main(), 0)
            self.assertIn("PATH_CHECK checked=2 missing=0", output.getvalue())
            self.assertIn("BEVFUSION_PLATFORM_DATA_PREFLIGHT_OK", output.getvalue())


if __name__ == "__main__":
    unittest.main(verbosity=2)
