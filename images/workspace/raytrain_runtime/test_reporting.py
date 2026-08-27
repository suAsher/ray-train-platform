"""Tests for framework-neutral Ray Train reporting."""

from __future__ import annotations

import copy
import hashlib
import json
import math
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from raytrain_runtime.reporting import (  # noqa: E402
    finalize_checkpoint,
    report_metrics,
    sanitize_metrics,
    validate_checkpoint,
)


class _FakeCheckpoint:
    created = []

    @classmethod
    def from_directory(cls, path):
        checkpoint = {"directory": path}
        cls.created.append(checkpoint)
        return checkpoint


class _FakeContext:
    def __init__(self, rank):
        self._rank = rank

    def get_world_rank(self):
        return self._rank


class _FakeTrain:
    Checkpoint = _FakeCheckpoint

    def __init__(self, rank=0):
        self.context = _FakeContext(rank)
        self.reports = []

    def get_context(self):
        return self.context

    def report(self, metrics, *, checkpoint=None):
        self.reports.append({"metrics": metrics, "checkpoint": checkpoint})


class MetricSanitizationTest(unittest.TestCase):
    def test_sanitizes_into_an_immutable_copy_without_mutating_input(self):
        source = {"loss": 1, 7: 2.5, "nested": [1, 2], "enabled": True}
        before = copy.deepcopy(source)

        clean = sanitize_metrics(source)

        self.assertEqual(clean, {"loss": 1.0, "7": 2.5})
        self.assertEqual(source, before)
        self.assertIsNot(clean, source)

    def test_omits_non_scalar_and_non_finite_values_by_default(self):
        clean = sanitize_metrics(
            {"loss": 1.0, "nan": math.nan, "inf": math.inf, "text": "1", "list": [1]}
        )

        self.assertEqual(clean, {"loss": 1.0})

    def test_strict_mode_rejects_non_finite_metrics(self):
        with self.assertRaisesRegex(ValueError, "finite scalar"):
            sanitize_metrics({"loss": math.nan}, reject_invalid=True)


class CheckpointIntegrityTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "epoch-1"
        self.root.mkdir()
        (self.root / "model.pth").write_bytes(b"model")
        (self.root / "optimizer.pth").write_bytes(b"optimizer")

    def tearDown(self):
        self.temporary.cleanup()

    def test_finalizes_complete_manifest_with_relative_size_and_sha256(self):
        metadata = {"epoch": 1, "step": 100, "labels": {"dataset": "v1"}}
        snapshot = copy.deepcopy(metadata)

        manifest = finalize_checkpoint(self.root, metadata)
        on_disk = json.loads((self.root / "manifest.json").read_text(encoding="utf-8"))

        self.assertEqual(manifest, on_disk)
        self.assertTrue(on_disk["complete"])
        self.assertEqual(on_disk["metadata"], snapshot)
        self.assertEqual(metadata, snapshot)
        files = {item["path"]: item for item in on_disk["files"]}
        self.assertEqual(files["model.pth"]["size"], 5)
        self.assertEqual(files["model.pth"]["sha256"], hashlib.sha256(b"model").hexdigest())
        self.assertNotIn("manifest.json", files)
        self.assertEqual(validate_checkpoint(self.root), on_disk)

    def test_atomic_replace_failure_leaves_no_manifest_or_temporary_file(self):
        with mock.patch("raytrain_runtime.reporting.os.replace", side_effect=OSError("disk full")):
            with self.assertRaisesRegex(OSError, "disk full"):
                finalize_checkpoint(self.root, {"epoch": 1})

        self.assertFalse((self.root / "manifest.json").exists())
        self.assertEqual(list(self.root.glob(".manifest.*.tmp")), [])

    def test_rejects_incomplete_manifest(self):
        (self.root / "manifest.json").write_text(
            json.dumps({"complete": False, "metadata": {}, "files": []}),
            encoding="utf-8",
        )

        with self.assertRaisesRegex(ValueError, "complete"):
            validate_checkpoint(self.root)

    def test_rejects_manifest_when_file_was_changed_after_finalization(self):
        finalize_checkpoint(self.root, {"epoch": 1})
        (self.root / "model.pth").write_bytes(b"tampered")

        with self.assertRaisesRegex(ValueError, "integrity"):
            validate_checkpoint(self.root)

    def test_rejects_symlinks_that_escape_checkpoint(self):
        outside = Path(self.temporary.name) / "outside.pth"
        outside.write_bytes(b"outside")
        try:
            os.symlink(outside, self.root / "escape.pth")
        except (OSError, NotImplementedError):
            self.skipTest("symlinks are not supported")

        with self.assertRaisesRegex(ValueError, "symlink"):
            finalize_checkpoint(self.root, {"epoch": 1})


class ReportMetricsTest(unittest.TestCase):
    def setUp(self):
        _FakeCheckpoint.created.clear()
        self.temporary = tempfile.TemporaryDirectory()
        self.checkpoint = Path(self.temporary.name) / "epoch-1"
        self.checkpoint.mkdir()
        (self.checkpoint / "model.pth").write_bytes(b"model")
        finalize_checkpoint(self.checkpoint, {"epoch": 1, "step": 100})

    def tearDown(self):
        self.temporary.cleanup()

    def test_only_rank_zero_attaches_checkpoint_and_rank_calls_stay_equal(self):
        train = _FakeTrain()

        report_metrics({"loss": 1.0}, self.checkpoint, world_rank=0, train_api=train)
        report_metrics({"loss": 1.0}, self.checkpoint, world_rank=1, train_api=train)

        self.assertEqual(len(train.reports), 2)
        self.assertIsNotNone(train.reports[0]["checkpoint"])
        self.assertIsNone(train.reports[1]["checkpoint"])
        self.assertEqual(train.reports[0]["metrics"], train.reports[1]["metrics"])

    def test_uses_injected_context_rank_without_importing_ray(self):
        train = _FakeTrain(rank=1)

        report_metrics({"loss": 1}, self.checkpoint, train_api=train)

        self.assertIsNone(train.reports[0]["checkpoint"])
        self.assertEqual(_FakeCheckpoint.created, [])

    def test_rejects_incomplete_checkpoint_before_reporting(self):
        (self.checkpoint / "manifest.json").write_text(
            json.dumps({"complete": False, "metadata": {}, "files": []}),
            encoding="utf-8",
        )
        train = _FakeTrain()

        with self.assertRaisesRegex(ValueError, "complete"):
            report_metrics({"loss": 1}, self.checkpoint, world_rank=0, train_api=train)

        self.assertEqual(train.reports, [])

    def test_rejects_non_finite_metric_before_reporting(self):
        train = _FakeTrain()

        with self.assertRaisesRegex(ValueError, "finite scalar"):
            report_metrics({"loss": math.nan}, world_rank=0, train_api=train)

        self.assertEqual(train.reports, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
