"""Tests for wiring platform S1H Ray Data into legacy BEVFusion sources."""

from pathlib import Path
import sys
import unittest


sys.path.insert(0, str(Path(__file__).resolve().parent))

from ray_data_s1h_integration import (  # noqa: E402
    patch_s1h_train_api,
    patch_s1h_training_entrypoint,
)


TRAIN_API_SOURCE = '''import torch
from mmcv.parallel import MMDistributedDataParallel
from mmdet.datasets import build_dataloader


def train_model(
    model,
    dataset,
    cfg,
    distributed=False,
    validate=False,
    timestamp=None,
):
    dataset = dataset if isinstance(dataset, (list, tuple)) else [dataset]

    data_loaders = [
        build_dataloader(
            ds,
            cfg.data.samples_per_gpu,
            cfg.data.workers_per_gpu,
            None,
            dist=distributed,
            seed=cfg.seed,
        )
        for ds in dataset
    ]
    runner = build_runner(cfg.runner)
    if hasattr(runner, "set_dataset"):
        runner.set_dataset(dataset)
    runner.run(data_loaders, [("train", 1)])
'''


ENTRYPOINT_SOURCE = '''import os
from mmdet3d.datasets import build_dataset


def main():
    datasets = [build_dataset(cfg.data.train)]
    train_model(
        model,
        datasets,
        cfg,
        distributed=distributed,
        validate=True,
        timestamp=timestamp,
    )
'''


class RayDataS1HIntegrationPatchTest(unittest.TestCase):
    def test_train_api_selects_platform_loader_only_for_streaming(self):
        patched = patch_s1h_train_api(TRAIN_API_SOURCE)

        self.assertIn("def _platform_build_train_dataloader", patched)
        self.assertIn(
            'os.environ.get("PLATFORM_TRAINING_ENGINE") == "ray-train"',
            patched,
        )
        self.assertIn(
            'os.environ.get("PLATFORM_DATA_MODE") == "streaming"',
            patched,
        )
        self.assertIn("if not _platform_uses_native_streaming():", patched)
        self.assertIn("build_bevfusion_train_dataloader", patched)
        self.assertIn("RAYTRAIN_DATASET_WORKER_SAMPLES", patched)
        self.assertIn("RAYTRAIN_DATASET_PREFETCH_BATCHES", patched)
        self.assertEqual(patched.count("build_dataloader("), 1)
        self.assertIn("legacy_builder=legacy_builder", patched)
        compile(patched, "mmdet3d/apis/train.py", "exec")

    def test_entrypoint_uses_proxy_and_skips_legacy_validation_only_for_streaming(self):
        patched = patch_s1h_training_entrypoint(ENTRYPOINT_SOURCE)

        self.assertIn("build_streaming_dataset_proxy", patched)
        self.assertIn("datasets = [build_streaming_dataset_proxy(cfg.data.train)]", patched)
        self.assertIn("datasets = [build_dataset(cfg.data.train)]", patched)
        self.assertIn(
            "validate=not _platform_uses_native_streaming(),",
            patched,
        )
        self.assertIn("if _platform_uses_native_streaming():", patched)
        compile(patched, "tools/westwell_train.py", "exec")

    def test_both_transforms_are_idempotent(self):
        train_api = patch_s1h_train_api(TRAIN_API_SOURCE)
        entrypoint = patch_s1h_training_entrypoint(ENTRYPOINT_SOURCE)

        self.assertEqual(patch_s1h_train_api(train_api), train_api)
        self.assertEqual(patch_s1h_training_entrypoint(entrypoint), entrypoint)

    def test_upstream_layout_changes_fail_closed(self):
        with self.assertRaisesRegex(ValueError, "data loader"):
            patch_s1h_train_api(TRAIN_API_SOURCE.replace("data_loaders = [", "loaders = ["))
        with self.assertRaisesRegex(ValueError, "dataset selection"):
            patch_s1h_training_entrypoint(
                ENTRYPOINT_SOURCE.replace(
                    "datasets = [build_dataset(cfg.data.train)]",
                    "datasets = make_datasets(cfg)",
                )
            )


if __name__ == "__main__":
    unittest.main(verbosity=2)
