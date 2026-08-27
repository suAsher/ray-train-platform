"""Tests for the BEVFusion managed-Ray-Train source patch."""

from __future__ import annotations

from pathlib import Path
import sys
import unittest


sys.path.insert(0, str(Path(__file__).resolve().parent))

from ray_train_managed import (  # noqa: E402
    patch_managed_training_entrypoint,
)


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


if __name__ == "__main__":
    unittest.main(verbosity=2)
