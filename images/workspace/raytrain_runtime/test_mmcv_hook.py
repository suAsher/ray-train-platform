"""Tests for the optional MMCV-to-Ray Train hook."""

from __future__ import annotations

import json
import math
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest import mock


sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from raytrain_runtime.mmcv_hook import (  # noqa: E402
    MMCV_AVAILABLE,
    RayTrainManagedHook,
    build_hook_config,
    extract_scalar_metrics,
    write_runner_checkpoint,
)


class _LogBuffer:
    def __init__(self, output):
        self.output = output


class _Stateful:
    def __init__(self, state):
        self.state = state

    def state_dict(self):
        return dict(self.state)


class _Runner:
    def __init__(self):
        self.iter = 0
        self.epoch = 0
        self.log_buffer = _LogBuffer({"loss": 1.25, "nested": [1], "nan": math.nan})
        self.model = _Stateful({"model": 1})
        self.optimizer = _Stateful({"optimizer": 2})
        self.lr_scheduler = _Stateful({"scheduler": 3})
        self.amp_scaler = _Stateful({"amp": 4})


class MetricHookTest(unittest.TestCase):
    def test_imports_without_mmcv_installed(self):
        self.assertIsInstance(MMCV_AVAILABLE, bool)
        self.assertTrue(callable(RayTrainManagedHook))

    def test_extracts_only_finite_scalars_without_mutating_runner_output(self):
        output = {"loss": 1, "mAP": 0.5, "nested": [1], "nan": math.nan}
        before = dict(output)

        clean = extract_scalar_metrics(output)

        self.assertEqual(clean, {"loss": 1.0, "mAP": 0.5})
        self.assertEqual(output, before)

    def test_reports_at_interval_on_every_rank(self):
        reports = []
        runner = _Runner()
        runner.iter = 1
        hook = RayTrainManagedHook(
            interval=2,
            checkpoint_every_epochs=0,
            report_fn=lambda metrics, **kwargs: reports.append((metrics, kwargs)),
            rank_fn=lambda: 7,
        )

        hook.after_train_iter(runner)

        self.assertEqual(len(reports), 1)
        self.assertEqual(reports[0][0]["loss"], 1.25)
        self.assertEqual(reports[0][0]["step"], 2.0)
        self.assertEqual(reports[0][1]["world_rank"], 7)

    def test_does_not_create_or_initialize_a_process_group(self):
        source = Path(sys.modules[RayTrainManagedHook.__module__].__file__).read_text(encoding="utf-8")

        self.assertNotIn("init_process_group", source)
        self.assertNotIn("init_dist(", source)

    def test_build_hook_config_consumes_task9_policy_deterministically(self):
        config = build_hook_config(
            interval=5,
            checkpoint_every_epochs=2,
            keep_latest=4,
            keep_best=2,
            best_metric="mAP",
            best_mode="max",
        )

        self.assertEqual(
            config,
            {
                "type": "RayTrainManagedHook",
                "interval": 5,
                "checkpoint_every_epochs": 2,
                "keep_latest": 4,
                "keep_best": 2,
                "best_metric": "mAP",
                "best_mode": "max",
            },
        )


class CheckpointHookTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.runner = _Runner()
        self.runner.epoch = 1
        self.runner.iter = 99

    def tearDown(self):
        self.temporary.cleanup()

    @staticmethod
    def _writer(runner, directory, metadata):
        del runner
        (directory / "training_state.pth").write_bytes(b"state")
        (directory / "captured.json").write_text(json.dumps(metadata), encoding="utf-8")

    def _hook(self, rank, reports, **overrides):
        options = {
            "interval": 50,
            "checkpoint_every_epochs": 2,
            "keep_latest": 3,
            "keep_best": 1,
            "best_metric": "mAP",
            "best_mode": "max",
            "checkpoint_root": self.root,
            "data_version": "dataset-v7",
            "code_sha": "abc123",
            "image_digest": "sha256:image",
            "world_size_fn": lambda: 16,
            "rank_fn": lambda: rank,
            "state_writer": self._writer,
            "report_fn": lambda metrics, **kwargs: reports.append((metrics, kwargs)),
        }
        options.update(overrides)
        return RayTrainManagedHook(**options)

    def test_checkpoint_epoch_reports_on_all_ranks_but_only_rank_zero_attaches(self):
        rank_zero_reports = []
        rank_one_reports = []
        self.runner.log_buffer.output = {"loss": 0.8, "mAP": 0.6}

        self._hook(0, rank_zero_reports).after_train_epoch(self.runner)
        self._hook(1, rank_one_reports).after_train_epoch(self.runner)

        self.assertEqual(len(rank_zero_reports), 1)
        self.assertEqual(len(rank_one_reports), 1)
        checkpoint = rank_zero_reports[0][1]["checkpoint_dir"]
        self.assertIsNotNone(checkpoint)
        self.assertIsNone(rank_one_reports[0][1]["checkpoint_dir"])
        manifest = json.loads((Path(checkpoint) / "manifest.json").read_text(encoding="utf-8"))
        self.assertTrue(manifest["complete"])
        metadata = manifest["metadata"]
        self.assertEqual(metadata["epoch"], 2)
        self.assertEqual(metadata["step"], 100)
        self.assertEqual(metadata["data_version"], "dataset-v7")
        self.assertEqual(metadata["code_sha"], "abc123")
        self.assertEqual(metadata["image_digest"], "sha256:image")
        self.assertEqual(metadata["world_size"], 16)
        self.assertIn("training_state.pth", {item["path"] for item in manifest["files"]})

    def test_checkpoint_policy_zero_retention_disables_checkpoint_but_not_report(self):
        reports = []
        hook = self._hook(0, reports, keep_latest=0, keep_best=0)

        hook.after_train_epoch(self.runner)

        self.assertEqual(len(reports), 1)
        self.assertIsNone(reports[0][1]["checkpoint_dir"])
        self.assertEqual(list(self.root.iterdir()), [])

    def test_best_metric_is_required_when_best_retention_is_enabled(self):
        reports = []
        self.runner.log_buffer.output = {"loss": 0.8}
        hook = self._hook(0, reports)

        with self.assertRaisesRegex(ValueError, "best metric"):
            hook.after_train_epoch(self.runner)

        self.assertEqual(reports, [])

    def test_default_best_retention_does_not_require_an_unconfigured_metric(self):
        reports = []
        self.runner.log_buffer.output = {"loss": 0.8}
        hook = self._hook(0, reports, best_metric="")

        hook.after_train_epoch(self.runner)

        self.assertEqual(len(reports), 1)
        self.assertIsNotNone(reports[0][1]["checkpoint_dir"])

    def test_state_writer_failure_never_reports_incomplete_checkpoint(self):
        reports = []
        self.runner.log_buffer.output = {"loss": 0.8, "mAP": 0.6}

        def fail_writer(_runner, directory, _metadata):
            (directory / "partial.pth").write_bytes(b"partial")
            raise OSError("checkpoint write failed")

        hook = self._hook(0, reports, state_writer=fail_writer)

        with self.assertRaisesRegex(OSError, "checkpoint write failed"):
            hook.after_train_epoch(self.runner)

        self.assertEqual(reports, [])
        self.assertFalse(any(self.root.rglob("manifest.json")))

    def test_default_writer_captures_all_framework_state(self):
        captured = {}

        def save(payload, path):
            captured.update(payload)
            Path(path).write_bytes(b"torch-state")

        fake_torch = types.SimpleNamespace(save=save)
        metadata = {
            "epoch": 2,
            "step": 100,
            "data_version": "dataset-v7",
            "code_sha": "abc123",
            "image_digest": "sha256:image",
            "world_size": 16,
        }

        with mock.patch.dict(sys.modules, {"torch": fake_torch}):
            output = write_runner_checkpoint(self.runner, self.root, metadata)

        self.assertEqual(output, self.root / "training_state.pth")
        self.assertEqual(captured["model"], {"model": 1})
        self.assertEqual(captured["optimizer"], {"optimizer": 2})
        self.assertEqual(captured["scheduler"], {"scheduler": 3})
        self.assertEqual(captured["amp"], {"amp": 4})
        for key, value in metadata.items():
            self.assertEqual(captured[key], value)


class OptionalRegistrationTest(unittest.TestCase):
    def test_registers_with_mmcv_registry_only_when_mmcv_is_available(self):
        registered = []

        class Registry:
            def register_module(self):
                return lambda cls: registered.append(cls) or cls

        fake_mmcv = types.ModuleType("mmcv")
        fake_runner = types.ModuleType("mmcv.runner")
        fake_hooks = types.ModuleType("mmcv.runner.hooks")
        fake_runner.HOOKS = Registry()
        fake_runner.Hook = object
        fake_hooks.HOOKS = fake_runner.HOOKS
        fake_hooks.Hook = object
        module_path = Path(sys.modules[RayTrainManagedHook.__module__].__file__)
        module_name = "raytrain_runtime._mmcv_hook_registration_test"

        with mock.patch.dict(
            sys.modules,
            {"mmcv": fake_mmcv, "mmcv.runner": fake_runner, "mmcv.runner.hooks": fake_hooks},
        ):
            spec = __import__("importlib.util").util.spec_from_file_location(module_name, module_path)
            module = __import__("importlib.util").util.module_from_spec(spec)
            sys.modules[module_name] = module
            try:
                spec.loader.exec_module(module)
            finally:
                sys.modules.pop(module_name, None)

        self.assertEqual(len(registered), 1)
        self.assertEqual(registered[0].__name__, "RayTrainManagedHook")


if __name__ == "__main__":
    unittest.main(verbosity=2)
