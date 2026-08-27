"""Tests for the BEVFusion managed-Ray-Train source patch."""

from __future__ import annotations

import dataclasses
import os
from pathlib import Path
import sys
import unittest
from unittest import mock


sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "images" / "workspace"))

from ray_train_managed import (  # noqa: E402
    MANAGED_HOOK_HELPER,
    patch_managed_training_entrypoint,
)
from raytrain_runtime.entrypoint import PythonEntrypoint  # noqa: E402
from raytrain_runtime.managed_driver import _train_loop  # noqa: E402


SOURCE = '''import os
import torch
from mmcv.runner import init_dist


def main():
    cfg = Config(recursive_eval(configs), filename=args.config)
    distributed = args.launcher != "none"
    if distributed:
        init_dist(args.launcher, **cfg.dist_params)
    model = build_model(cfg.model)
    train_model(model, datasets, cfg, distributed=distributed)
'''


class RayTrainManagedPatchTest(unittest.TestCase):
    def setUp(self):
        self.patched = patch_managed_training_entrypoint(SOURCE)

    def test_adds_managed_hook_without_rewriting_training_loop(self):
        self.assertIn("configure_ray_train_managed_hook(cfg)", self.patched)
        self.assertEqual(self.patched.count("train_model(model, datasets, cfg"), 1)
        self.assertIn("custom_hooks", self.patched)
        self.assertIn('"priority": "VERY_HIGH"', self.patched)
        self.assertIn('"priority": "VERY_LOW"', self.patched)

    def test_guards_duplicate_process_group_initialization(self):
        self.assertIn(
            "if distributed and not torch.distributed.is_initialized():",
            self.patched,
        )
        self.assertEqual(self.patched.count("init_dist(args.launcher, **cfg.dist_params)"), 1)

    def test_is_idempotent_and_syntactically_valid(self):
        self.assertEqual(patch_managed_training_entrypoint(self.patched), self.patched)
        compile(self.patched, "westwell_train.py", "exec")

    def test_fails_closed_when_upstream_layout_changes(self):
        with self.assertRaisesRegex(ValueError, "distributed initialization"):
            patch_managed_training_entrypoint(SOURCE.replace("if distributed:\n", "if ready:\n"))

    def test_non_default_task9_policy_reaches_the_patched_hook(self):
        namespace = {"os": os}
        exec(MANAGED_HOOK_HELPER, namespace)

        class Config:
            log_config = {"interval": 13}
            custom_hooks = []

            def get(self, key, default=None):
                return getattr(self, key, default)

        config = Config()
        managed_loop_config = {
            "entrypoint": dataclasses.asdict(
                PythonEntrypoint("path", "train.py", ("train.py",))
            ),
            "checkpoint_every_epochs": 7,
            "keep_latest": 11,
            "keep_best": 2,
            "best_metric": "val/nds",
            "best_mode": "min",
            "job_id": "job-0123456789abcdef01234567",
            "parent_job_id": "",
            "storage_path": (
                "/mnt/data/output/.platform/ray-train/"
                "job-0123456789abcdef01234567"
            ),
        }

        def execute_patched_user_code(_entrypoint):
            namespace["configure_ray_train_managed_hook"](config)

        with mock.patch.dict(
            os.environ, {"PLATFORM_TRAINING_ENGINE": "ray-train"}, clear=True
        ):
            with mock.patch(
                "raytrain_runtime.managed_driver.execute",
                side_effect=execute_patched_user_code,
            ):
                _train_loop(managed_loop_config)

        self.assertEqual(
            config.custom_hooks,
            [
                {
                    "type": "RayTrainManagedRestoreHook",
                    "priority": "VERY_HIGH",
                },
                {
                    "type": "RayTrainManagedHook",
                    "interval": 13,
                    "checkpoint_every_epochs": 7,
                    "keep_latest": 11,
                    "keep_best": 2,
                    "best_metric": "val/nds",
                    "best_mode": "min",
                    "priority": "VERY_LOW",
                }
            ],
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
