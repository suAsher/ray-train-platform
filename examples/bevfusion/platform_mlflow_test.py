#!/usr/bin/env python3
"""Tests for the BEVFusion rank-zero MLflow bridge."""

from __future__ import annotations

import importlib.util
import io
import logging
import os
from pathlib import Path
import sys
import types
import unittest
from unittest import mock


MODULE_PATH = Path(__file__).with_name("patches") / "platform_mlflow.py"
PATCH_PATH = Path(__file__).with_name("patches") / "0002-platform-mlflow.patch"


def load_module():
    spec = importlib.util.spec_from_file_location("platform_mlflow", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


class FakeLogConfig:
    def __init__(self) -> None:
        self.interval = 7
        self.hooks = [{"type": "TextLoggerHook"}]

    def get(self, name: str, default=None):
        return getattr(self, name, default)


class FakeConfig(dict):
    def __init__(self) -> None:
        super().__init__(
            seed=42,
            optimizer={"type": "AdamW", "lr": 0.001},
            runner={"max_epochs": 3},
        )
        self.log_config = FakeLogConfig()
        self.filename = "/workspace/configs/train.yaml"

    def __getattr__(self, name: str):
        return self[name]


class PlatformMLflowTest(unittest.TestCase):
    def setUp(self) -> None:
        self.training_logger = logging.getLogger("mmdet3d")
        self.root_logger = logging.getLogger()
        for logger in (self.training_logger, self.root_logger):
            for attr in ("handlers", "level", "propagate", "disabled"):
                saved = getattr(logger, attr)
                self.addCleanup(setattr, logger, attr, saved)
            logger.handlers = []
            logger.disabled = False
        self.training_logger.setLevel(logging.INFO)
        self.training_logger.propagate = True

    def fake_mlflow(self):
        client = types.ModuleType("mlflow")
        for name in ("set_tracking_uri", "set_experiment", "start_run", "log_params"):
            setattr(client, name, mock.Mock())
        return client

    def test_late_mlflow_root_handler_does_not_duplicate_console_or_file(self):
        console, file_log, root_output = io.StringIO(), io.StringIO(), io.StringIO()
        handlers = [logging.StreamHandler(console), logging.StreamHandler(file_log)]
        self.training_logger.handlers = handlers
        client = self.fake_mlflow()
        config = FakeConfig()
        with mock.patch.dict(os.environ, self.platform_environment(), clear=True), mock.patch.dict(
            sys.modules, {"mlflow": client}
        ):
            load_module().start_platform_mlflow(config, rank=0, world_size=8)
        # MlflowLoggerHook imports mlflow.pytorch AFTER start_platform_mlflow.
        # MLflow 2.17.2's Lightning module configures the root logger here.
        logging.basicConfig(level=logging.ERROR, stream=root_output)
        root_handlers = list(self.root_logger.handlers)
        self.training_logger.info("Epoch [1][50/100] loss: 0.5")
        logging.getLogger("another-library").error("unrelated error")
        self.assertEqual(console.getvalue().count("Epoch"), 1)
        self.assertEqual(file_log.getvalue().count("Epoch"), 1)
        self.assertNotIn("Epoch", root_output.getvalue())
        self.assertIn("unrelated error", root_output.getvalue())
        self.assertEqual(self.training_logger.handlers, handlers)
        self.assertEqual(self.root_logger.handlers, root_handlers)
        client.log_params.assert_called_once()
        self.assertEqual([hook["type"] for hook in config.log_config.hooks],
                         ["TextLoggerHook", "MlflowLoggerHook"])

    def test_root_only_logging_remains_visible(self):
        for handlers in ([], [logging.NullHandler()]):
            with self.subTest(handlers=handlers):
                self.training_logger.handlers = handlers
                self.training_logger.propagate = True
                output = io.StringIO()
                self.root_logger.handlers = [logging.StreamHandler(output)]
                with mock.patch.dict(os.environ, self.platform_environment(), clear=True), mock.patch.dict(
                    sys.modules, {"mlflow": self.fake_mlflow()}
                ):
                    load_module().start_platform_mlflow(FakeConfig(), rank=0, world_size=1)
                self.training_logger.info("visible training message")
                self.assertIn("visible training message", output.getvalue())

    def test_disabled_tracking_and_nonzero_rank_preserve_logging(self):
        for environment, rank in (({}, 0), (self.platform_environment(), 1)):
            with self.subTest(rank=rank), mock.patch.dict(os.environ, environment, clear=True):
                self.training_logger.handlers = [logging.StreamHandler(io.StringIO())]
                self.assertIsNone(load_module().start_platform_mlflow(FakeConfig(), rank, 8))
                self.assertTrue(self.training_logger.propagate)

    def platform_environment(self) -> dict[str, str]:
        return {
            "MLFLOW_TRACKING_URI": "http://mlflow-ingest.mlflow-system.svc:5000",
            "MLFLOW_EXPERIMENT_NAME": "raytrain-local",
            "MLFLOW_RUN_NAME": "job-test",
            "RAYTRAIN_JOB_ID": "job-test",
            "RAYTRAIN_TENANT_ID": "local",
            "RAYTRAIN_SUBMITTER_USER_ID": "user-test",
            "RAYTRAIN_MLFLOW_PROVENANCE": "signed-provenance",
            "RAYTRAIN_CLUSTER_ATTEMPT": "2",
            "PLATFORM_DATA_MODE": "streaming",
            "PLATFORM_DATASET_ID": "labeled-full",
            "PLATFORM_DATASET_VERSION_ID": "version-20260830",
            "PLATFORM_DATASET_CACHE_POLICY": "bounded",
            "PLATFORM_RAY_VERSION": "2.58.0",
            "PLATFORM_DATASET_MANIFEST_PATH": "/private/platform/manifest.parquet",
        }

    def test_rank_zero_starts_owned_run_and_installs_scalar_logger(self) -> None:
        module = load_module()
        fake_mlflow = types.ModuleType("mlflow")
        fake_mlflow.set_tracking_uri = mock.Mock()
        fake_mlflow.set_experiment = mock.Mock()
        fake_mlflow.start_run = mock.Mock(return_value=object())
        fake_mlflow.log_params = mock.Mock()
        config = FakeConfig()

        with mock.patch.dict(os.environ, self.platform_environment(), clear=True), mock.patch.dict(
            sys.modules, {"mlflow": fake_mlflow}
        ):
            client = module.start_platform_mlflow(config, rank=0, world_size=16)

        self.assertIs(client, fake_mlflow)
        fake_mlflow.start_run.assert_called_once_with(
            run_name="job-test",
            tags={
                "platform.job_id": "job-test",
                "platform.tenant_id": "local",
                "platform.submitter_user_id": "user-test",
                "platform.provenance": "signed-provenance",
                "platform.cluster_attempt": "2",
                "platform.data_mode": "streaming",
                "platform.dataset_id": "labeled-full",
                "platform.dataset_version_id": "version-20260830",
                "platform.dataset_cache_policy": "bounded",
                "platform.ray_version": "2.58.0",
            },
        )
        fake_mlflow.log_params.assert_called_once_with(
            {
                "config_file": "train.yaml",
                "learning_rate": 0.001,
                "max_epochs": 3,
                "optimizer": "AdamW",
                "seed": 42,
                "world_size": 16,
                "data_mode": "streaming",
                "dataset_id": "labeled-full",
                "dataset_version_id": "version-20260830",
                "dataset_cache_policy": "bounded",
                "ray_version": "2.58.0",
            }
        )
        recorded = str(fake_mlflow.start_run.call_args) + str(
            fake_mlflow.log_params.call_args
        )
        self.assertNotIn("manifest.parquet", recorded)
        self.assertNotIn("/private", recorded)
        self.assertEqual(
            config.log_config.hooks[-1],
            {
                "type": "MlflowLoggerHook",
                "log_model": False,
                "interval": 7,
                "ignore_last": False,
                "reset_flag": False,
                "by_epoch": True,
            },
        )

    def test_nonzero_rank_does_not_create_an_mlflow_run(self) -> None:
        module = load_module()
        with mock.patch.dict(os.environ, self.platform_environment(), clear=True):
            self.assertIsNone(module.start_platform_mlflow(FakeConfig(), rank=1, world_size=16))

    def test_partial_platform_contract_fails_before_training(self) -> None:
        module = load_module()
        fake_mlflow = types.ModuleType("mlflow")
        fake_mlflow.set_tracking_uri = mock.Mock()
        fake_mlflow.set_experiment = mock.Mock()
        with mock.patch.dict(
            os.environ, {"MLFLOW_TRACKING_URI": "http://mlflow"}, clear=True
        ), mock.patch.dict(sys.modules, {"mlflow": fake_mlflow}):
            with self.assertRaisesRegex(RuntimeError, "MLFLOW_EXPERIMENT_NAME"):
                module.start_platform_mlflow(FakeConfig(), rank=0, world_size=16)

    def test_patch_starts_run_after_model_initialization(self) -> None:
        patch = PATCH_PATH.read_text(encoding="utf-8")
        model_ready = patch.index('logger.info(f"Model:\\n{model}")')
        start_run = patch.index("platform_mlflow = start_platform_mlflow")
        train_guard = patch.index("+    try:")
        self.assertLess(model_ready, start_run)
        self.assertLess(start_run, train_guard)


if __name__ == "__main__":
    unittest.main()
