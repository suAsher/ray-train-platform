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
    RETENTION_INDEX_NAME,
    finalize_checkpoint,
    report_metrics,
    retain_checkpoints,
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

    def test_rejects_non_finite_manifest_metadata_without_publishing(self):
        with self.assertRaisesRegex(ValueError, "JSON compliant"):
            finalize_checkpoint(self.root, {"epoch": 1, "score": math.nan})

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

        self.assertEqual(
            train.reports, [{"metrics": {"loss": 1.0}, "checkpoint": None}]
        )

    def test_corrupt_checkpoint_preparation_preserves_rank_report_parity(self):
        (self.checkpoint / "model.pth").write_bytes(b"corrupt")
        train = _FakeTrain()

        report_metrics({"loss": 1}, self.checkpoint, world_rank=1, train_api=train)
        with self.assertRaisesRegex(ValueError, "integrity"):
            report_metrics(
                {"loss": 1}, self.checkpoint, world_rank=0, train_api=train
            )

        self.assertEqual(len(train.reports), 2)
        self.assertTrue(all(item["checkpoint"] is None for item in train.reports))

    def test_checkpoint_factory_failure_reports_then_reraises_on_rank_zero(self):
        class RejectingCheckpoint:
            @classmethod
            def from_directory(cls, _path):
                raise RuntimeError("checkpoint factory failed")

        train = _FakeTrain()
        train.Checkpoint = RejectingCheckpoint

        with self.assertRaisesRegex(RuntimeError, "checkpoint factory failed"):
            report_metrics(
                {"loss": 1}, self.checkpoint, world_rank=0, train_api=train
            )

        self.assertEqual(
            train.reports, [{"metrics": {"loss": 1.0}, "checkpoint": None}]
        )

    def test_report_failure_takes_precedence_over_checkpoint_preparation_failure(self):
        (self.checkpoint / "manifest.json").write_text("{}", encoding="utf-8")

        class RejectingTrain(_FakeTrain):
            def report(self, metrics, *, checkpoint=None):
                super().report(metrics, checkpoint=checkpoint)
                raise RuntimeError("report failed")

        train = RejectingTrain()

        with self.assertRaisesRegex(RuntimeError, "report failed"):
            report_metrics(
                {"loss": 1}, self.checkpoint, world_rank=0, train_api=train
            )

        self.assertEqual(len(train.reports), 1)
        self.assertIsNone(train.reports[0]["checkpoint"])


class CheckpointRetentionTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "checkpoints"
        self.root.mkdir()

    def tearDown(self):
        self.temporary.cleanup()

    def _checkpoint(self, epoch, score=None, *, complete=True):
        checkpoint = self.root / f"checkpoint-epoch-{epoch:06d}-step-{epoch * 10:012d}"
        checkpoint.mkdir()
        (checkpoint / "training_state.pth").write_bytes(f"state-{epoch}".encode())
        metadata = {"epoch": epoch, "step": epoch * 10}
        if score is not None:
            metadata.update({"score": score, "score_metric": "mAP"})
        if complete:
            finalize_checkpoint(checkpoint, metadata)
        return checkpoint

    def _retained_names(self):
        index = json.loads((self.root / RETENTION_INDEX_NAME).read_text(encoding="utf-8"))
        self.assertTrue(index["complete"])
        return [item["path"] for item in index["checkpoints"]]

    def test_preserves_union_of_latest_and_best_max(self):
        first = self._checkpoint(1, 0.9)
        second = self._checkpoint(2, 0.2)
        third = self._checkpoint(3, 0.3)
        current = self._checkpoint(4, 0.4)

        retained = retain_checkpoints(
            self.root,
            current,
            keep_latest=2,
            keep_best=1,
            best_mode="max",
        )

        self.assertEqual(
            {item["path"] for item in retained["checkpoints"]},
            {first.name, third.name, current.name},
        )
        self.assertFalse(second.exists())
        self.assertEqual(
            set(self._retained_names()),
            {first.name, third.name, current.name},
        )

    def test_min_mode_and_score_ties_are_deterministic(self):
        oldest = self._checkpoint(1, 0.1)
        middle = self._checkpoint(2, 0.1)
        newest = self._checkpoint(3, 0.1)

        retained = retain_checkpoints(
            self.root,
            newest,
            keep_latest=0,
            keep_best=2,
            best_mode="min",
        )

        self.assertEqual(
            [item["path"] for item in retained["checkpoints"]],
            [middle.name, newest.name],
        )
        self.assertFalse(oldest.exists())

    def test_missing_score_can_be_latest_but_not_best(self):
        best = self._checkpoint(1, 0.8)
        scored_latest = self._checkpoint(2, 0.4)
        current = self._checkpoint(3)

        retained = retain_checkpoints(
            self.root,
            current,
            keep_latest=2,
            keep_best=1,
            best_mode="max",
        )

        self.assertEqual(
            {item["path"] for item in retained["checkpoints"]},
            {best.name, scored_latest.name, current.name},
        )

    def test_zero_policy_removes_only_complete_accepted_checkpoints(self):
        current = self._checkpoint(1, 0.5)
        incomplete = self._checkpoint(2, complete=False)

        retained = retain_checkpoints(
            self.root,
            current,
            keep_latest=0,
            keep_best=0,
            best_mode="max",
        )

        self.assertEqual(retained["checkpoints"], [])
        self.assertFalse(current.exists())
        self.assertTrue(incomplete.exists())

    def test_atomic_index_failure_does_not_prune_any_checkpoint(self):
        old = self._checkpoint(1, 0.1)
        current = self._checkpoint(2, 0.2)

        with mock.patch("raytrain_runtime.reporting.os.replace", side_effect=OSError("disk full")):
            with self.assertRaisesRegex(OSError, "disk full"):
                retain_checkpoints(
                    self.root,
                    current,
                    keep_latest=1,
                    keep_best=0,
                    best_mode="max",
                )

        self.assertTrue(old.exists())
        self.assertTrue(current.exists())
        self.assertFalse((self.root / RETENTION_INDEX_NAME).exists())

    def test_policy_can_be_enforced_after_metrics_only_report_without_current(self):
        old = self._checkpoint(1, 0.1)
        newest = self._checkpoint(2, 0.2)

        retained = retain_checkpoints(
            self.root,
            None,
            keep_latest=1,
            keep_best=0,
            best_mode="max",
        )

        self.assertEqual(
            [item["path"] for item in retained["checkpoints"]], [newest.name]
        )
        self.assertFalse(old.exists())
        self.assertTrue(newest.exists())

    def test_hidden_staging_and_quarantine_directories_are_ignored(self):
        current = self._checkpoint(2, 0.2)
        hidden = self.root / ".checkpoint-staging-or-quarantine"
        hidden.mkdir()
        (hidden / "training_state.pth").write_bytes(b"hidden")
        finalize_checkpoint(hidden, {"epoch": 99, "step": 990, "score": 9.9})

        retained = retain_checkpoints(
            self.root,
            current,
            keep_latest=1,
            keep_best=1,
            best_mode="max",
        )

        self.assertEqual(
            [item["path"] for item in retained["checkpoints"]], [current.name]
        )
        self.assertTrue(hidden.exists())

    def test_rejects_non_finite_metric_before_reporting(self):
        train = _FakeTrain()

        with self.assertRaisesRegex(ValueError, "finite scalar"):
            report_metrics({"loss": math.nan}, world_rank=0, train_api=train)

        self.assertEqual(train.reports, [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
