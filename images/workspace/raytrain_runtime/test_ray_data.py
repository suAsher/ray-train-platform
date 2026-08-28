from __future__ import annotations

import dataclasses
import pathlib
import sys
import types
import unittest
from unittest import mock


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))

from raytrain_runtime.managed_driver import build_trainer, parse_driver_config  # noqa: E402
from raytrain_runtime.ray_data import (  # noqa: E402
    DatasetConfig,
    build_dataset,
    worker_iterator,
)
from raytrain_runtime.test_managed_driver import (  # noqa: E402
    BASE_ENV,
    FAKE_RAY,
    driver_argv,
)


class DatasetConfigTest(unittest.TestCase):
    def test_config_is_frozen_and_accepts_stable_mounted_dataset(self):
        config = DatasetConfig(format="images", uri="/mnt/data/input/shards/train")
        self.assertEqual(config.uri, "/mnt/data/input/shards/train")
        with self.assertRaises(dataclasses.FrozenInstanceError):
            config.uri = "/mnt/data/input/other"  # type: ignore[misc]

    def test_rejects_unsupported_formats_and_unsafe_uris(self):
        cases = (
            ("pkl", "/mnt/data/input/bevfusion/train.pkl"),
            ("parquet", "tos://access:secret@bucket/train.parquet"),
            ("images", "/mnt/data/input/../storage/team/private"),
            ("images", "/mnt/storage/public/images"),
            ("images", "/mnt/data/input/images\nsecret"),
            ("images", "/mnt/data/input/images\u0085secret"),
        )
        for dataset_format, uri in cases:
            with self.subTest(format=dataset_format, uri=uri):
                with self.assertRaises(ValueError):
                    DatasetConfig(format=dataset_format, uri=uri)


class AdapterTest(unittest.TestCase):
    def test_build_dataset_dispatches_only_registered_formats(self):
        fake_data = types.ModuleType("ray.data")
        fake_data.read_parquet = mock.Mock(return_value="parquet-dataset")
        fake_data.read_images = mock.Mock(return_value="image-dataset")
        fake_ray = types.ModuleType("ray")
        fake_ray.data = fake_data

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.data": fake_data}):
            parquet = build_dataset(DatasetConfig("parquet", "/mnt/data/input/table"))
            images = build_dataset(DatasetConfig("images", "/mnt/data/input/images"))

        self.assertEqual(parquet, "parquet-dataset")
        self.assertEqual(images, "image-dataset")
        fake_data.read_parquet.assert_called_once_with("/mnt/data/input/table")
        fake_data.read_images.assert_called_once_with(
            "/mnt/data/input/images", include_paths=True
        )

    def test_worker_iterator_uses_named_train_shard_and_prefetches_two_batches(self):
        shard = mock.Mock()
        batches = object()
        shard.iter_torch_batches.return_value = batches
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            result = worker_iterator()

        self.assertIs(result, batches)
        fake_train.get_dataset_shard.assert_called_once_with("train")
        shard.iter_torch_batches.assert_called_once_with(prefetch_batches=2)

    def test_worker_iterator_errors_when_named_shard_is_missing(self):
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=None)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train
        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            with self.assertRaisesRegex(RuntimeError, "'train'.*unavailable"):
                worker_iterator("train")


class ManagedDriverDataModeTest(unittest.TestCase):
    def test_mount_and_cache_modes_never_import_ray_data(self):
        for mode in ("mount", "cache"):
            with self.subTest(mode=mode):
                config = parse_driver_config(driver_argv("--data-mode", mode), environ=BASE_ENV)
                with mock.patch(
                    "raytrain_runtime.managed_driver._load_ray_data_dataset",
                    side_effect=AssertionError("ray.data must stay lazy"),
                    create=True,
                ):
                    trainer = build_trainer(config, ray_components=FAKE_RAY)
                self.assertNotIn("datasets", trainer.kwargs)

    def test_ray_data_mode_passes_named_train_dataset(self):
        config = parse_driver_config(
            driver_argv(
                "--data-mode",
                "ray-data",
                "--dataset-format",
                "images",
                "--dataset-uri",
                "/mnt/data/input/images",
            ),
            environ=BASE_ENV,
        )
        dataset = object()
        with mock.patch(
            "raytrain_runtime.managed_driver._load_ray_data_dataset",
            return_value=dataset,
            create=True,
        ) as load:
            trainer = build_trainer(config, ray_components=FAKE_RAY)

        self.assertEqual(trainer.kwargs["datasets"], {"train": dataset})
        load.assert_called_once_with(config.dataset)


if __name__ == "__main__":
    unittest.main()
