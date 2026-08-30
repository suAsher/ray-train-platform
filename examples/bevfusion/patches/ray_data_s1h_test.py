"""Contract tests for the minimal BEVFusion S1H Ray Data adapter."""

from __future__ import annotations

import importlib
import inspect
from pathlib import Path
import sys
import unittest
from unittest import mock


PATCH_DIR = Path(__file__).resolve().parent
RUNTIME_PARENT = Path(__file__).resolve().parents[3] / "images" / "workspace"
for path in (PATCH_DIR, RUNTIME_PARENT):
    if str(path) not in sys.path:
        sys.path.insert(0, str(path))


def _adapter_module():
    try:
        return importlib.import_module("ray_data_s1h")
    except ModuleNotFoundError as error:
        raise AssertionError(
            "the BEVFusion S1H Ray Data patch is not implemented"
        ) from error


class RayDataS1HPatchTest(unittest.TestCase):
    def test_streaming_loader_accepts_exact_count_and_keeps_per_gpu_batch(self):
        adapter = _adapter_module()
        pipeline = mock.Mock(side_effect=lambda sample: {**sample, "augmented": True})
        collate = mock.Mock(
            side_effect=lambda samples, samples_per_gpu: {
                "tokens": [sample["token"] for sample in samples],
                "samples_per_gpu": samples_per_gpu,
            }
        )
        worker_batches = mock.Mock(
            return_value=iter(
                (
                    [{"token": "token-a"}, {"token": "token-b"}],
                    [{"token": "token-c"}],
                )
            )
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(
                    side_effect=AssertionError("legacy loader used")
                ),
                pipeline=pipeline,
                collate_fn=collate,
                samples_per_gpu=2,
                worker_sample_count=3,
                prefetch_batches=3,
            )
            produced = list(loader)

        self.assertEqual(len(loader), 2)
        self.assertIsNone(loader.sampler)
        self.assertIsNone(loader.batch_sampler)
        self.assertEqual(loader.samples_per_gpu, 2)
        self.assertEqual(
            produced,
            [
                {"tokens": ["token-a", "token-b"], "samples_per_gpu": 2},
                {"tokens": ["token-c"], "samples_per_gpu": 2},
            ],
        )
        worker_batches.assert_called_once_with(
            name="train",
            samples_per_gpu=2,
            prefetch_batches=3,
            pipeline=pipeline,
        )
        self.assertEqual(collate.call_count, 2)

    def test_streaming_loader_reports_a_redacted_short_shard_at_end(self):
        adapter = _adapter_module()
        private_token = "tos://access-key:secret@internal/private.parquet"
        collate = mock.Mock(side_effect=lambda samples, samples_per_gpu: samples)
        worker_batches = mock.Mock(
            return_value=iter(([{"token": private_token}],))
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=collate,
                samples_per_gpu=2,
                worker_sample_count=2,
            )
            batches = iter(loader)
            self.assertEqual(next(batches), [{"token": private_token}])
            with self.assertRaisesRegex(
                RuntimeError, "ended before declared sample count"
            ) as caught:
                next(batches)

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access-key", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)
        self.assertEqual(collate.call_count, 1)

    def test_streaming_loader_rejects_an_overfull_batch_before_collate_or_yield(self):
        adapter = _adapter_module()
        private_token = "tos://access-key:secret@internal/private.parquet"
        collate = mock.Mock(side_effect=lambda samples, samples_per_gpu: samples)
        worker_batches = mock.Mock(
            return_value=iter(
                (
                    [{"token": "token-a"}],
                    [{"token": "token-b"}, {"token": private_token}],
                )
            )
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=collate,
                samples_per_gpu=2,
                worker_sample_count=2,
            )
            batches = iter(loader)
            self.assertEqual(next(batches), [{"token": "token-a"}])
            with self.assertRaisesRegex(
                RuntimeError, "exceeded declared sample count"
            ) as caught:
                next(batches)

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access-key", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)
        self.assertEqual(collate.call_count, 1)

    def test_legacy_modes_including_ray_data_keep_the_existing_loader_unchanged(self):
        adapter = _adapter_module()
        sentinel = object()
        for data_mode in ("mount", "cache", "ray-data", "ray-data-stage"):
            with self.subTest(data_mode=data_mode):
                legacy_builder = mock.Mock(return_value=sentinel)
                with mock.patch(
                    "raytrain_runtime.ray_data.worker_s1h_batches",
                    side_effect=AssertionError("legacy mode touched a Ray shard"),
                ):
                    loader = adapter.build_bevfusion_train_dataloader(
                        data_mode=data_mode,
                        legacy_builder=legacy_builder,
                        pipeline=mock.Mock(),
                        collate_fn=mock.Mock(),
                        samples_per_gpu=4,
                        worker_sample_count=100,
                    )

                self.assertIs(loader, sentinel)
                legacy_builder.assert_called_once_with()

    def test_streaming_loader_never_constructs_a_distributed_sampler(self):
        adapter = _adapter_module()
        loader = adapter.build_bevfusion_train_dataloader(
            data_mode="streaming",
            legacy_builder=mock.Mock(side_effect=AssertionError("legacy loader used")),
            pipeline=lambda sample: sample,
            collate_fn=lambda samples, samples_per_gpu: samples,
            samples_per_gpu=1,
            worker_sample_count=1,
        )

        self.assertIsNone(loader.sampler)
        self.assertIsNone(loader.batch_sampler)
        self.assertNotIn("DistributedSampler", inspect.getsource(adapter))

    def test_streaming_loader_forwards_the_platform_batch_resolver(self):
        adapter = _adapter_module()
        batch_resolver = mock.Mock()
        worker_batches = mock.Mock(
            return_value=iter(
                (
                    [{"token": "token-a"}, {"token": "token-b"}],
                    [{"token": "token-c"}, {"token": "token-d"}],
                )
            )
        )

        with mock.patch(
            "raytrain_runtime.ray_data.worker_s1h_batches",
            worker_batches,
        ):
            loader = adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=2,
                worker_sample_count=4,
                batch_resolver=batch_resolver,
            )
            list(loader)

        worker_batches.assert_called_once_with(
            name="train",
            samples_per_gpu=2,
            prefetch_batches=2,
            pipeline=loader.pipeline,
            batch_resolver=batch_resolver,
        )

    def test_adapter_uses_only_shard_and_non_location_provenance(self):
        adapter = _adapter_module()
        source = inspect.getsource(adapter).lower()

        self.assertIn("worker_s1h_batches", source)
        self.assertNotIn("tos://", source)
        self.assertNotIn("/mnt/storage", source)
        self.assertNotIn("internal manifest", source)
        self.assertNotIn("manifest_uri", source)

    def test_invalid_mode_or_provenance_fails_before_reading_a_shard(self):
        adapter = _adapter_module()
        with self.assertRaisesRegex(ValueError, "data_mode"):
            adapter.build_bevfusion_train_dataloader(
                data_mode="implicit-auto",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=1,
                worker_sample_count=1,
            )
        with self.assertRaisesRegex(ValueError, "worker_sample_count"):
            adapter.build_bevfusion_train_dataloader(
                data_mode="streaming",
                legacy_builder=mock.Mock(),
                pipeline=lambda sample: sample,
                collate_fn=lambda samples, samples_per_gpu: samples,
                samples_per_gpu=1,
                worker_sample_count=0,
            )


if __name__ == "__main__":
    unittest.main(verbosity=2)
