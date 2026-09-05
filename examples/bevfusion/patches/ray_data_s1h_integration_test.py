"""Tests for wiring platform S1H Ray Data into legacy BEVFusion sources."""

from pathlib import Path
import os
import sys
import types
import unittest
from unittest import mock


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

    model = MMDistributedDataParallel(
        model.cuda(),
        device_ids=[torch.cuda.current_device()],
        broadcast_buffers=False,
    )
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
    def test_loader_seed_prefers_config_then_managed_environment_then_zero(self):
        from ray_data_s1h_integration import TRAIN_DATALOADER_HELPER

        scope = {"MMDistributedDataParallel": object, "os": os}
        exec(TRAIN_DATALOADER_HELPER, scope)
        scope["_platform_uses_native_streaming"] = lambda: True
        cfg = types.SimpleNamespace(
            seed=None,
            data=types.SimpleNamespace(samples_per_gpu=2, workers_per_gpu=0),
        )
        dataset = types.SimpleNamespace(pipeline=lambda sample: sample)
        for config_seed, managed_seed, expected in ((17, "29", 17), (0, "29", 0), (None, "29", 29), (None, None, 0)):
            with self.subTest(config_seed=config_seed, managed_seed=managed_seed):
                cfg.seed = config_seed
                environment = {"RAYTRAIN_DATASET_WORKER_SAMPLES": "4"}
                if managed_seed is not None:
                    environment["RAYTRAIN_DATASET_SHUFFLE_SEED"] = managed_seed
                builder = mock.Mock()
                with mock.patch.dict(os.environ, environment, clear=True), mock.patch.dict(
                    sys.modules,
                    {
                        "mmcv.parallel": types.SimpleNamespace(collate=mock.Mock()),
                        "ray_data_s1h": types.SimpleNamespace(build_bevfusion_train_dataloader=builder),
                    },
                ):
                    scope["_platform_build_train_dataloader"](dataset, cfg, True)
                self.assertEqual(builder.call_args.kwargs["shuffle_seed"], expected)

    def test_epoch_hook_reads_restored_runner_epoch_before_iteration(self):
        from ray_data_s1h_integration import TRAIN_DATALOADER_HELPER

        scope = {"MMDistributedDataParallel": object}
        exec(TRAIN_DATALOADER_HELPER, scope)
        scope["_platform_uses_native_streaming"] = lambda: True
        runner = types.SimpleNamespace(
            epoch=12, data_loader=mock.Mock(), register_hook=mock.Mock(),
        )
        with mock.patch.dict(sys.modules, {"mmcv.runner": types.SimpleNamespace(Hook=object)}):
            scope["_platform_register_data_epoch_hook"](runner)
        hook = runner.register_hook.call_args.args[0]
        hook.before_train_epoch(runner)
        runner.data_loader.set_epoch.assert_called_once_with(12)
        runner.epoch = 13
        hook.before_train_epoch(runner)
        runner.data_loader.set_epoch.assert_called_with(13)
        runner.register_hook.reset_mock()
        scope["_platform_uses_native_streaming"] = lambda: False
        scope["_platform_register_data_epoch_hook"](runner)
        runner.register_hook.assert_not_called()

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
        self.assertIn("RAYTRAIN_DATASET_LOCAL_SHUFFLE_BUFFER_SIZE", patched)
        self.assertIn('os.environ.get("RAYTRAIN_DATASET_SHUFFLE_SEED", "0")', patched)
        self.assertLess(
            patched.index("    _platform_register_data_epoch_hook(runner)"),
            patched.index("    runner.run("),
        )
        self.assertEqual(patched.count("build_dataloader("), 1)
        self.assertIn("legacy_builder=legacy_builder", patched)
        self.assertIn("class _PlatformMMDistributedDataParallel", patched)
        self.assertIn("def _platform_scatter_device_ids", patched)
        self.assertIn('torch.device("cuda", device_id)', patched)
        self.assertIn("def to_kwargs(self, inputs, kwargs, device_id):", patched)
        self.assertIn("def scatter(self, inputs, kwargs, device_ids):", patched)
        self.assertEqual(patched.count("scatter_kwargs("), 2)
        self.assertIn("def _platform_distributed_wrapper", patched)
        self.assertIn("model = _platform_distributed_wrapper()(", patched)
        self.assertNotIn("model = MMDistributedDataParallel(", patched)
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

    def test_pre_epoch_platform_archive_is_upgraded_idempotently(self):
        from ray_data_s1h_integration import _SHUFFLE_ARGUMENTS

        current = patch_s1h_train_api(TRAIN_API_SOURCE)
        epoch_start = current.index("def _platform_register_data_epoch_hook(runner):")
        epoch_end = current.index("def _platform_scatter_device_ids(device_ids):")
        previous = (current[:epoch_start] + current[epoch_end:]).replace(
            _SHUFFLE_ARGUMENTS, "", 1
        ).replace("    _platform_register_data_epoch_hook(runner)\n", "", 1)
        self.assertEqual(patch_s1h_train_api(previous), current)
        incomplete = current.replace("    _platform_register_data_epoch_hook(runner)\n", "", 1)
        with self.assertRaisesRegex(ValueError, "partially applied"):
            patch_s1h_train_api(incomplete)

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
