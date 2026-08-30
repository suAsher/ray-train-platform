#!/usr/bin/env python3
"""Tests for the BEVFusion rank-zero MLflow bridge."""

from __future__ import annotations

import importlib.util
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
