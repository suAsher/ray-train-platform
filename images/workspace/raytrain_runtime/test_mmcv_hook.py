"""Tests for the optional MMCV-to-Ray Train hook."""

from __future__ import annotations

import json
import math
import os
from pathlib import Path
import random
import sys
import tempfile
import types
import unittest
from unittest import mock


sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from raytrain_runtime.mmcv_hook import (  # noqa: E402
    MMCV_AVAILABLE,
    RayTrainManagedHook,
    RayTrainManagedRestoreHook,
    bounded_training_parameters,
    build_hook_config,
    build_restore_hook_config,
    extract_scalar_metrics,
    resolve_external_managed_checkpoint,
    restore_runner_checkpoint,
    write_runner_checkpoint,
)
from raytrain_runtime.reporting import finalize_checkpoint  # noqa: E402


class _LogBuffer:
    def __init__(self, output):
        self.output = output


class _Stateful:
    def __init__(self, state):
        self.state = state

    def state_dict(self):
        return dict(self.state)


class _LoadableStateful(_Stateful):
    def __init__(self, state):
        super().__init__(state)
        self.loaded = []

    def load_state_dict(self, state):
        self.state = dict(state)
        self.loaded.append(dict(state))


class _Runner:
    def __init__(self):
        self.iter = 0
        self.epoch = 0
        self.log_buffer = _LogBuffer({"loss": 1.25, "nested": [1], "nan": math.nan})
        self.model = _Stateful({"model": 1})
        self.optimizer = _Stateful({"optimizer": 2})
        self.lr_scheduler = _Stateful({"scheduler": 3})
        self.amp_scaler = _Stateful({"amp": 4})
        self.meta = {"seed": 7, "experiment": "bevfusion"}
        self.cfg = {f"parameter_{index}": index for index in range(200)}


class _ReadOnlyProgressRunner(_Runner):
    def __init__(self):
        self._epoch = 4
        self._iter = 321
        self.log_buffer = _LogBuffer({"loss": 1.25})
        self.model = _Stateful({"model": 1})
        self.optimizer = _Stateful({"optimizer": 2})
        self.lr_scheduler = _Stateful({"scheduler": 3})
        self.amp_scaler = _Stateful({"amp": 4})
        self.meta = {"seed": 7, "experiment": "bevfusion"}
        self.cfg = {"learning_rate": 0.001}

    @property
    def epoch(self):
        return self._epoch

    @property
    def iter(self):
        return self._iter


class _FakeCuda:
    def __init__(self):
        self.states = [b"cuda-0", b"cuda-1"]

    def is_available(self):
        return True

    def get_rng_state_all(self):
        return list(self.states)

    def set_rng_state_all(self, states):
        self.states = list(states)


class _FakeTorch:
    def __init__(self):
        self.cpu_state = b"cpu-rng"
        self.cuda = _FakeCuda()
        self.saved = {}

    def save(self, payload, path):
        self.saved[str(path)] = payload
        Path(path).write_bytes(b"torch-state")

    def load(self, path, **_kwargs):
        return self.saved.get(str(path), next(reversed(self.saved.values())))

    def get_rng_state(self):
        return self.cpu_state

    def set_rng_state(self, state):
        self.cpu_state = state


class _FakeNumpyRandom:
    def __init__(self):
        self.state = ("MT19937", [1, 2, 3], 0, 0, 0.0)

    def get_state(self):
        return self.state

    def set_state(self, state):
        self.state = state


class _FakeNumpy:
    def __init__(self):
        self.random = _FakeNumpyRandom()


class MetricHookTest(unittest.TestCase):
    def test_imports_without_mmcv_installed(self):
        self.assertIsInstance(MMCV_AVAILABLE, bool)
        self.assertTrue(callable(RayTrainManagedHook))
        self.assertTrue(callable(RayTrainManagedRestoreHook))

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

    def test_rank_zero_owns_one_mlflow_run_for_the_managed_runner(self):
        runner = _Runner()
        client = mock.Mock()
        hook = RayTrainManagedHook(
            checkpoint_every_epochs=0,
            rank_fn=lambda: 0,
            world_size_fn=lambda: 16,
            mlflow_start_fn=lambda parameters, rank, world_size: (
                client,
                True,
            ),
            mlflow_finish_fn=mock.Mock(),
        )

        hook.before_train_iter(runner)
        hook.before_train_iter(runner)
        hook.after_run(runner)

        self.assertTrue(hook._mlflow_started)
        hook._mlflow_finish.assert_called_once_with(client, owned=True, status="FINISHED")

    def test_nonzero_rank_does_not_start_mlflow(self):
        starter = mock.Mock(return_value=(None, False))
        hook = RayTrainManagedHook(
            checkpoint_every_epochs=0,
            rank_fn=lambda: 3,
            world_size_fn=lambda: 16,
            mlflow_start_fn=starter,
        )

        hook.before_train_iter(_Runner())

        starter.assert_called_once()
        self.assertTrue(hook._mlflow_started)

    def test_reports_process_local_streaming_metrics_without_internal_paths(self):
        from raytrain_runtime.data_metrics import (
            observe_data_metric,
            reset_data_metrics_for_tests,
        )

        reset_data_metrics_for_tests()
        self.addCleanup(reset_data_metrics_for_tests)
        observe_data_metric("dataset_source_read_seconds_total", 1.25)
        observe_data_metric("dataset_cache_hits_total", 7)
        reports = []
        runner = _Runner()
        runner.iter = 1
        hook = RayTrainManagedHook(
            interval=2,
            checkpoint_every_epochs=0,
            report_fn=lambda metrics, **kwargs: reports.append((metrics, kwargs)),
            rank_fn=lambda: 3,
        )

        hook.after_train_iter(runner)

        self.assertEqual(
            reports[0][0]["dataset_source_read_seconds_total"], 1.25
        )
        self.assertEqual(reports[0][0]["dataset_cache_hits_total"], 7.0)
        self.assertNotIn("dataset_root", reports[0][0])
        self.assertNotIn("manifest_path", reports[0][0])

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

    def test_restore_hook_config_is_separate_from_very_low_reporting(self):
        self.assertEqual(
            build_restore_hook_config(), {"type": "RayTrainManagedRestoreHook"}
        )
        self.assertNotIn("before_run", RayTrainManagedHook.__dict__)
        self.assertIn("before_run", RayTrainManagedRestoreHook.__dict__)

    def test_default_checkpoint_root_uses_output_contract_not_resume_input(self):
        with mock.patch.dict(
            os.environ,
            {
                "PLATFORM_CHECKPOINT_PATH": "/mnt/data/resume/checkpoint-7",
                "RAYTRAIN_CHECKPOINT_OUTPUT_PATH": "/mnt/data/output/job/checkpoints",
            },
            clear=True,
        ):
            hook = RayTrainManagedHook(
                checkpoint_every_epochs=0,
                rank_fn=lambda: 0,
                world_size_fn=lambda: 1,
                report_fn=lambda *_args, **_kwargs: None,
            )

        self.assertEqual(
            hook.checkpoint_root,
            Path("/mnt/data/output/job/checkpoints"),
        )

    def test_training_parameter_summary_redacts_secrets_and_normalizes_non_finite(self):
        runner = _Runner()
        runner.cfg = {
            "learning_rate": 0.001,
            "access_token": "must-not-leak",
            "nested": {"password": "must-not-leak", "loss_scale": math.inf},
        }

        summary = bounded_training_parameters(runner)

        self.assertEqual(summary["learning_rate"], 0.001)
        self.assertEqual(summary["access_token"], "<redacted>")
        self.assertEqual(summary["nested"]["password"], "<redacted>")
        self.assertEqual(summary["nested"]["loss_scale"], "<non-finite>")
        self.assertNotIn("must-not-leak", json.dumps(summary))


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

    @staticmethod
    def _restore_hook(state_loader):
        return RayTrainManagedRestoreHook(state_loader=state_loader)

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
        old = self.root / "checkpoint-epoch-000001-step-000000000010"
        old.mkdir()
        (old / "training_state.pth").write_bytes(b"old")
        finalize_checkpoint(old, {"epoch": 1, "step": 10})
        hook = self._hook(0, reports, keep_latest=0, keep_best=0)

        hook.after_train_epoch(self.runner)

        self.assertEqual(len(reports), 1)
        self.assertIsNone(reports[0][1]["checkpoint_dir"])
        self.assertFalse(old.exists())
        self.assertTrue((self.root / "retention-index.json").is_file())

    def test_missing_best_metric_keeps_unscored_latest_checkpoint_with_rank_parity(self):
        rank_zero_reports = []
        rank_one_reports = []
        self.runner.log_buffer.output = {"loss": 0.8}

        self._hook(0, rank_zero_reports).after_train_epoch(self.runner)
        self._hook(1, rank_one_reports).after_train_epoch(self.runner)

        self.assertEqual(len(rank_zero_reports), 1)
        self.assertEqual(len(rank_one_reports), 1)
        checkpoint = Path(rank_zero_reports[0][1]["checkpoint_dir"])
        self.assertTrue(checkpoint.is_dir())
        self.assertIsNone(rank_one_reports[0][1]["checkpoint_dir"])
        manifest = json.loads(
            (checkpoint / "manifest.json").read_text(encoding="utf-8")
        )
        self.assertNotIn("score", manifest["metadata"])
        index = json.loads(
            (self.root / "retention-index.json").read_text(encoding="utf-8")
        )
        self.assertEqual(
            [item["path"] for item in index["checkpoints"]], [checkpoint.name]
        )

    def test_missing_best_metric_without_latest_policy_reports_metrics_only(self):
        rank_zero_reports = []
        rank_one_reports = []
        self.runner.log_buffer.output = {"loss": 0.8}

        self._hook(0, rank_zero_reports, keep_latest=0).after_train_epoch(self.runner)
        self._hook(1, rank_one_reports, keep_latest=0).after_train_epoch(self.runner)

        self.assertEqual(len(rank_zero_reports), 1)
        self.assertEqual(len(rank_one_reports), 1)
        self.assertIsNone(rank_zero_reports[0][1]["checkpoint_dir"])
        self.assertIsNone(rank_one_reports[0][1]["checkpoint_dir"])

    def test_best_metric_may_exist_only_on_rank_zero_without_breaking_parity(self):
        rank_zero_reports = []
        rank_one_reports = []
        self.runner.log_buffer.output = {"loss": 0.8, "mAP": 0.6}
        self._hook(0, rank_zero_reports).after_train_epoch(self.runner)
        self.runner.log_buffer.output = {"loss": 0.8}

        self._hook(1, rank_one_reports).after_train_epoch(self.runner)

        self.assertEqual(len(rank_zero_reports), 1)
        self.assertEqual(len(rank_one_reports), 1)
        self.assertIsNotNone(rank_zero_reports[0][1]["checkpoint_dir"])
        self.assertIsNone(rank_one_reports[0][1]["checkpoint_dir"])

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

        self.assertEqual(len(reports), 1)
        self.assertIsNone(reports[0][1]["checkpoint_dir"])
        self.assertFalse(any(self.root.rglob("manifest.json")))

    def test_failed_staging_write_does_not_block_same_epoch_retry(self):
        reports = []
        attempts = 0
        self.runner.log_buffer.output = {"loss": 0.8, "mAP": 0.6}

        def fail_once(_runner, directory, _metadata):
            nonlocal attempts
            attempts += 1
            (directory / "training_state.pth").write_bytes(b"partial")
            if attempts == 1:
                raise OSError("transient write failure")

        hook = self._hook(0, reports, state_writer=fail_once)
        with self.assertRaisesRegex(OSError, "transient write failure"):
            hook.after_train_epoch(self.runner)

        final = self.root / "checkpoint-epoch-000002-step-000000000100"
        self.assertFalse(final.exists())

        hook.after_train_epoch(self.runner)

        self.assertEqual(attempts, 2)
        self.assertTrue((final / "manifest.json").is_file())
        self.assertEqual(reports[-1][1]["checkpoint_dir"], final)

    def test_checkpoint_is_finalized_in_hidden_staging_then_atomically_published(self):
        reports = []
        observed = {}
        final = self.root / "checkpoint-epoch-000002-step-000000000100"

        def observe_staging(_runner, directory, _metadata):
            observed["directory"] = directory
            observed["final_during_write"] = final.exists()
            (directory / "training_state.pth").write_bytes(b"state")

        self._hook(0, reports, state_writer=observe_staging).after_train_epoch(
            self.runner
        )

        self.assertTrue(observed["directory"].name.startswith(".checkpoint-staging-"))
        self.assertFalse(observed["final_during_write"])
        self.assertTrue((final / "manifest.json").is_file())
        self.assertFalse(observed["directory"].exists())
        self.assertEqual(reports[0][1]["checkpoint_dir"], final)

    def test_legacy_incomplete_final_directory_is_quarantined_before_retry(self):
        reports = []
        final = self.root / "checkpoint-epoch-000002-step-000000000100"
        final.mkdir()
        (final / "partial.pth").write_bytes(b"legacy-partial")

        self._hook(0, reports).after_train_epoch(self.runner)

        self.assertTrue((final / "manifest.json").is_file())
        quarantined = list(self.root.glob(".checkpoint-incomplete-*-*"))
        self.assertEqual(len(quarantined), 1)
        self.assertEqual(
            (quarantined[0] / "partial.pth").read_bytes(), b"legacy-partial"
        )
        self.assertEqual(reports[0][1]["checkpoint_dir"], final)

    def test_existing_complete_current_checkpoint_is_reused_without_rewrite(self):
        reports = []
        final = self.root / "checkpoint-epoch-000002-step-000000000100"
        final.mkdir()
        (final / "training_state.pth").write_bytes(b"complete")
        finalize_checkpoint(final, {"epoch": 2, "step": 100, "score": 0.6})
        writer = mock.Mock(side_effect=AssertionError("must not rewrite complete checkpoint"))

        self._hook(0, reports, state_writer=writer).after_train_epoch(self.runner)

        writer.assert_not_called()
        self.assertEqual(reports[0][1]["checkpoint_dir"], final)
        self.assertEqual((final / "training_state.pth").read_bytes(), b"complete")

    def test_report_failure_keeps_current_checkpoint_and_skips_retention(self):
        self.runner.log_buffer.output = {"loss": 0.8, "mAP": 0.6}

        def rejected_report(_metrics, **_kwargs):
            raise RuntimeError("Ray rejected checkpoint")

        hook = self._hook(0, [], report_fn=rejected_report)

        with self.assertRaisesRegex(RuntimeError, "Ray rejected checkpoint"):
            hook.after_train_epoch(self.runner)

        checkpoints = [path for path in self.root.iterdir() if path.is_dir()]
        self.assertEqual(len(checkpoints), 1)
        self.assertTrue((checkpoints[0] / "manifest.json").is_file())
        self.assertFalse((self.root / "retention-index.json").exists())

    def test_default_writer_captures_all_framework_state(self):
        fake_torch = _FakeTorch()
        fake_numpy = _FakeNumpy()
        metadata = {
            "epoch": 2,
            "step": 100,
            "data_version": "dataset-v7",
            "code_sha": "abc123",
            "image_digest": "sha256:image",
            "world_size": 16,
        }

        with mock.patch.dict(
            sys.modules, {"torch": fake_torch, "numpy": fake_numpy}
        ):
            output = write_runner_checkpoint(self.runner, self.root, metadata)

        captured = next(iter(fake_torch.saved.values()))
        self.assertEqual(output, self.root / "training_state.pth")
        self.assertEqual(captured["model"], {"model": 1})
        self.assertEqual(captured["optimizer"], {"optimizer": 2})
        self.assertEqual(captured["scheduler"], {"scheduler": 3})
        self.assertEqual(captured["amp"], {"amp": 4})
        self.assertEqual(captured["runner"]["epoch"], 1)
        self.assertEqual(captured["runner"]["iter"], 99)
        self.assertEqual(captured["runner"]["safe_meta"], self.runner.meta)
        self.assertEqual(captured["rng"]["torch_cpu"], b"cpu-rng")
        self.assertEqual(captured["rng"]["torch_cuda"], [b"cuda-0", b"cuda-1"])
        self.assertEqual(captured["rng"]["numpy"], fake_numpy.random.state)
        self.assertLessEqual(len(captured["training_parameters"]), 129)
        self.assertIn("__truncated__", captured["training_parameters"])
        for key, value in metadata.items():
            self.assertEqual(captured[key], value)

    def test_runner_metadata_is_bounded_redacted_and_preserves_safe_hook_messages(self):
        fake_torch = _FakeTorch()
        self.runner.meta = {
            "hook_msgs": {"last_ckpt": "epoch_4.pth"},
            "access_token": "must-not-leak",
            "nested": {"password": "must-not-leak"},
            "seed": 7,
            "experiment_name": "bevfusion",
            **{f"a_item_{index}": index for index in range(300)},
        }

        with mock.patch.dict(
            sys.modules, {"torch": fake_torch, "numpy": _FakeNumpy()}
        ):
            write_runner_checkpoint(
                self.runner, self.root, {"epoch": 2, "step": 100}
            )

        payload = next(iter(fake_torch.saved.values()))
        safe_meta = payload["runner"]["safe_meta"]
        self.assertEqual(
            safe_meta["hook_msgs"], {"last_ckpt": "epoch_4.pth"}
        )
        self.assertEqual(safe_meta["seed"], 7)
        self.assertEqual(safe_meta["experiment_name"], "bevfusion")
        self.assertNotIn("access_token", safe_meta)
        self.assertNotIn("nested", safe_meta)
        self.assertFalse(any(key.startswith("a_item_") for key in safe_meta))
        self.assertNotIn("must-not-leak", repr(payload))

    def test_non_mapping_runner_metadata_is_saved_as_empty_safe_state(self):
        fake_torch = _FakeTorch()
        self.runner.meta = ["not", "a", "mapping"]

        with mock.patch.dict(
            sys.modules, {"torch": fake_torch, "numpy": _FakeNumpy()}
        ):
            write_runner_checkpoint(
                self.runner, self.root, {"epoch": 2, "step": 100}
            )

        payload = next(iter(fake_torch.saved.values()))
        self.assertEqual(payload["runner"]["safe_meta"], {})

    def test_checkpoint_write_is_atomic_when_serializer_fails(self):
        class FailingTorch(_FakeTorch):
            def save(self, _payload, path):
                Path(path).write_bytes(b"partial")
                raise OSError("serializer failed")

        with mock.patch.dict(
            sys.modules, {"torch": FailingTorch(), "numpy": _FakeNumpy()}
        ):
            with self.assertRaisesRegex(OSError, "serializer failed"):
                write_runner_checkpoint(self.runner, self.root, {"epoch": 2, "step": 100})

        self.assertFalse((self.root / "training_state.pth").exists())
        self.assertEqual(list(self.root.glob(".training_state.*.tmp")), [])

    def test_resume_round_trip_restores_framework_runner_and_rng_state(self):
        fake_torch = _FakeTorch()
        fake_numpy = _FakeNumpy()
        runner = _Runner()
        runner.model = _LoadableStateful({"model": 1})
        runner.optimizer = _LoadableStateful({"optimizer": 2})
        runner.lr_scheduler = _LoadableStateful({"scheduler": 3})
        runner.amp_scaler = _LoadableStateful({"amp": 4})
        runner.epoch = 4
        runner.iter = 321
        expected_meta = dict(runner.meta)
        random.seed(7)
        expected_python_rng = random.getstate()

        with mock.patch.dict(
            sys.modules, {"torch": fake_torch, "numpy": fake_numpy}
        ):
            output = write_runner_checkpoint(
                runner, self.root, {"epoch": 5, "step": 322}
            )
            finalize_checkpoint(self.root, {"epoch": 5, "step": 322})
            runner.model.state = {"model": 99}
            runner.optimizer.state = {"optimizer": 99}
            runner.lr_scheduler.state = {"scheduler": 99}
            runner.amp_scaler.state = {"amp": 99}
            runner.epoch = 0
            runner.iter = 0
            runner.meta = {"mutated": True}
            random.seed(99)
            fake_torch.cpu_state = b"changed-cpu"
            fake_torch.cuda.states = [b"changed-cuda"]
            fake_numpy.random.state = ("changed", [], 0, 0, 0.0)

            restored = restore_runner_checkpoint(runner, self.root)

        self.assertEqual(restored, output)
        self.assertEqual(runner.model.state, {"model": 1})
        self.assertEqual(runner.optimizer.state, {"optimizer": 2})
        self.assertEqual(runner.lr_scheduler.state, {"scheduler": 3})
        self.assertEqual(runner.amp_scaler.state, {"amp": 4})
        self.assertEqual((runner.epoch, runner.iter), (4, 321))
        self.assertEqual(runner.meta, expected_meta)
        self.assertEqual(random.getstate(), expected_python_rng)
        self.assertEqual(fake_torch.cpu_state, b"cpu-rng")
        self.assertEqual(fake_torch.cuda.states, [b"cuda-0", b"cuda-1"])
        self.assertEqual(
            fake_numpy.random.state,
            ("MT19937", [1, 2, 3], 0, 0, 0.0),
        )

    def test_restore_updates_mmcv_read_only_progress_backing_fields(self):
        fake_torch = _FakeTorch()
        fake_numpy = _FakeNumpy()
        runner = _ReadOnlyProgressRunner()
        runner.model = _LoadableStateful({"model": 1})
        runner.optimizer = _LoadableStateful({"optimizer": 2})
        runner.lr_scheduler = _LoadableStateful({"scheduler": 3})
        runner.amp_scaler = _LoadableStateful({"amp": 4})

        with mock.patch.dict(
            sys.modules, {"torch": fake_torch, "numpy": fake_numpy}
        ):
            write_runner_checkpoint(runner, self.root, {"epoch": 5, "step": 322})
            finalize_checkpoint(self.root, {"epoch": 5, "step": 322})
            runner._epoch = 0
            runner._iter = 0
            restore_runner_checkpoint(runner, self.root)

        self.assertEqual((runner.epoch, runner.iter), (4, 321))

    def test_invalid_resume_state_fails_before_mutating_runner(self):
        fake_torch = _FakeTorch()
        runner = _Runner()
        runner.model = _LoadableStateful({"model": 1})
        state_path = self.root / "training_state.pth"
        state_path.write_bytes(b"invalid")
        fake_torch.saved[str(state_path)] = {
            "format_version": 1,
            "model": {"model": 2},
            "runner": {"epoch": 2, "iter": 10, "safe_meta": {}},
            # Deliberately missing required RNG state.
        }
        finalize_checkpoint(self.root, {"epoch": 3, "step": 11})

        with mock.patch.dict(
            sys.modules, {"torch": fake_torch, "numpy": _FakeNumpy()}
        ):
            with self.assertRaisesRegex(ValueError, "RNG"):
                restore_runner_checkpoint(runner, self.root)

        self.assertEqual(runner.model.state, {"model": 1})
        self.assertEqual(runner.model.loaded, [])

    def test_before_run_prefers_ray_handoff_and_validates_before_loading(self):
        ray_resume = self.root / "ray-resume"
        ray_resume.mkdir()
        (ray_resume / "training_state.pth").write_bytes(b"state")
        finalize_checkpoint(ray_resume, {"epoch": 2, "step": 20})
        restored = []
        hook = self._restore_hook(
            lambda runner, path: restored.append((runner, Path(path)))
        )

        with mock.patch.dict(
            os.environ,
            {
                "RAYTRAIN_RESUME_CHECKPOINT_PATH": str(ray_resume),
                "PLATFORM_CHECKPOINT_PATH": "/backend/resume/input",
            },
            clear=True,
        ):
            hook.before_run(self.runner)

        self.assertEqual(restored, [(self.runner, ray_resume)])

    def test_before_run_uses_backend_resume_input_when_ray_has_no_checkpoint(self):
        backend_resume = self.root / "backend-resume"
        backend_resume.mkdir()
        (backend_resume / "training_state.pth").write_bytes(b"state")
        finalize_checkpoint(backend_resume, {"epoch": 2, "step": 20})
        restored = []
        hook = self._restore_hook(
            lambda runner, path: restored.append((runner, Path(path)))
        )

        with mock.patch.dict(
            os.environ,
            {"PLATFORM_CHECKPOINT_PATH": str(backend_resume)},
            clear=True,
        ):
            hook.before_run(self.runner)

        self.assertEqual(restored, [(self.runner, backend_resume)])

    def test_external_previous_run_root_prefers_retention_index(self):
        previous_run = self.root / "previous-run"
        checkpoints = previous_run / "checkpoints"
        checkpoints.mkdir(parents=True)
        indexed = checkpoints / "checkpoint-epoch-000001-step-000000000010"
        indexed.mkdir()
        (indexed / "training_state.pth").write_bytes(b"indexed")
        finalize_checkpoint(indexed, {"epoch": 1, "step": 10})
        unindexed_newer = checkpoints / "checkpoint-epoch-000009-step-000000000090"
        unindexed_newer.mkdir()
        (unindexed_newer / "training_state.pth").write_bytes(b"newer")
        finalize_checkpoint(unindexed_newer, {"epoch": 9, "step": 90})
        (checkpoints / "retention-index.json").write_text(
            json.dumps(
                {
                    "version": 1,
                    "complete": True,
                    "checkpoints": [
                        {"path": indexed.name, "epoch": 1, "step": 10}
                    ],
                }
            ),
            encoding="utf-8",
        )

        resolved = resolve_external_managed_checkpoint(previous_run)

        self.assertEqual(resolved, indexed.resolve())

    def test_external_previous_run_platform_job_root_resolves_latest_complete(self):
        previous_run = self.root / "previous-run"
        checkpoints = (
            previous_run
            / ".platform"
            / "ray-train"
            / "job-0123456789abcdef01234567"
            / "checkpoints"
        )
        checkpoints.mkdir(parents=True)
        older = checkpoints / "checkpoint-epoch-000001-step-000000000010"
        newer = checkpoints / "checkpoint-epoch-000002-step-000000000020"
        for checkpoint, epoch in ((older, 1), (newer, 2)):
            checkpoint.mkdir()
            (checkpoint / "training_state.pth").write_bytes(str(epoch).encode())
            finalize_checkpoint(checkpoint, {"epoch": epoch, "step": epoch * 10})

        resolved = resolve_external_managed_checkpoint(previous_run)

        self.assertEqual(resolved, newer.resolve())

    def test_external_legacy_resume_without_manifest_skips_managed_restore(self):
        legacy = self.root / "legacy"
        legacy.mkdir()
        (legacy / "epoch_7.pth").write_bytes(b"legacy")
        loader = mock.Mock()
        hook = self._restore_hook(loader)

        with mock.patch.dict(
            os.environ, {"PLATFORM_CHECKPOINT_PATH": str(legacy)}, clear=True
        ):
            hook.before_run(self.runner)

        loader.assert_not_called()

    def test_external_resolver_ignores_symlink_escape(self):
        previous_run = self.root / "previous-run"
        checkpoints = previous_run / "checkpoints"
        checkpoints.mkdir(parents=True)
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "training_state.pth").write_bytes(b"outside")
        finalize_checkpoint(outside, {"epoch": 99, "step": 990})
        try:
            os.symlink(outside, checkpoints / "checkpoint-epoch-99")
        except (OSError, NotImplementedError):
            self.skipTest("symlinks are not supported")

        self.assertIsNone(resolve_external_managed_checkpoint(previous_run))

    def test_external_resolver_bounds_managed_job_root_scan(self):
        managed_root = self.root / "previous-run" / ".platform" / "ray-train"
        (managed_root / "job-a").mkdir(parents=True)
        (managed_root / "job-b").mkdir()

        with self.assertRaisesRegex(ValueError, "bounded"):
            resolve_external_managed_checkpoint(
                self.root / "previous-run", max_job_roots=1
            )

    def test_ray_resume_handoff_remains_strict_for_corrupt_checkpoint(self):
        corrupt = self.root / "ray-corrupt"
        corrupt.mkdir()
        (corrupt / "training_state.pth").write_bytes(b"corrupt")
        loader = mock.Mock()
        hook = self._restore_hook(loader)

        with mock.patch.dict(
            os.environ,
            {"RAYTRAIN_RESUME_CHECKPOINT_PATH": str(corrupt)},
            clear=True,
        ):
            with self.assertRaisesRegex(ValueError, "complete manifest"):
                hook.before_run(self.runner)

        loader.assert_not_called()


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

        self.assertTrue(module.MMCV_AVAILABLE)
        self.assertEqual(len(registered), 2)
        self.assertEqual(
            [item.__name__ for item in registered],
            ["RayTrainManagedRestoreHook", "RayTrainManagedHook"],
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
