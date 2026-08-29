from __future__ import annotations

import dataclasses
import pathlib
import sys
import tempfile
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
    stage_binary_dataset,
    worker_iterator,
)
from raytrain_runtime.test_managed_driver import (  # noqa: E402
    BASE_ENV,
    CapturedConfig,
    FAKE_RAY,
    driver_argv,
)


class DatasetConfigTest(unittest.TestCase):
    def test_config_is_frozen_and_accepts_stable_mounted_dataset(self):
        config = DatasetConfig(format="images", uri="/mnt/data/input/shards/train")
        self.assertEqual(config.uri, "/mnt/data/input/shards/train")
        with self.assertRaises(dataclasses.FrozenInstanceError):
            config.uri = "/mnt/data/input/other"  # type: ignore[misc]

    def test_file_staging_accepts_the_selected_input_root(self):
        config = DatasetConfig(format="files", uri="/mnt/data/input")
        self.assertEqual(config.uri, "/mnt/data/input")

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
        fake_data.read_binary_files = mock.Mock(return_value="binary-dataset")
        fake_ray = types.ModuleType("ray")
        fake_ray.data = fake_data

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.data": fake_data}):
            parquet = build_dataset(DatasetConfig("parquet", "/mnt/data/input/table"))
            images = build_dataset(DatasetConfig("images", "/mnt/data/input/images"))
            files = build_dataset(DatasetConfig("files", "/mnt/data/input"))

        self.assertEqual(parquet, "parquet-dataset")
        self.assertEqual(images, "image-dataset")
        self.assertEqual(files, "binary-dataset")
        fake_data.read_parquet.assert_called_once_with("/mnt/data/input/table")
        fake_data.read_images.assert_called_once_with(
            "/mnt/data/input/images", include_paths=True
        )
        fake_data.read_binary_files.assert_called_once_with(
            "/mnt/data/input", include_paths=True
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

    def test_binary_dataset_stages_across_both_cache_roots_and_reuses_ready_view(self):
        class Iterator:
            def iter_rows(self):
                return iter((
                    {"path": "/selected/a.bin", "bytes": b"a"},
                    {"path": "/selected/nested/b.bin", "bytes": b"bb"},
                ))

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            cache1, cache2 = root / "cache1", root / "cache2"
            result = stage_binary_dataset(
                Iterator(),
                source_root="/selected",
                cache_paths=f"{cache1}:{cache2}",
                copy_workers=2,
            )
            self.assertFalse(result.reused)
            self.assertEqual((result.files, result.bytes), (2, 3))
            self.assertEqual((result.path / "a.bin").read_bytes(), b"a")
            self.assertEqual((result.path / "nested/b.bin").read_bytes(), b"bb")

            reused = stage_binary_dataset(
                Iterator(),
                source_root="/selected",
                cache_paths=f"{cache1}:{cache2}",
                copy_workers=2,
            )
            self.assertTrue(reused.reused)
            self.assertEqual((reused.files, reused.bytes), (2, 3))

    def test_binary_dataset_rejects_a_path_outside_selected_input(self):
        iterator = mock.Mock()
        iterator.iter_rows.return_value = iter((
            {"path": "/other/secret.bin", "bytes": b"secret"},
        ))
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "escaped selected input"):
                stage_binary_dataset(
                    iterator,
                    source_root="/selected",
                    cache_paths=str(pathlib.Path(temporary) / "cache"),
                )


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

    def test_ray_data_stage_replicates_binary_dataset_to_each_node_local_rank_zero(self):
        config = parse_driver_config(
            driver_argv(
                "--data-mode",
                "ray-data-stage",
                "--dataset-format",
                "files",
                "--dataset-uri",
                "/mnt/data/input",
            ),
            environ=BASE_ENV,
        )
        dataset = object()
        fake_ray = types.SimpleNamespace(**vars(FAKE_RAY), DataConfig=CapturedConfig)
        with mock.patch(
            "raytrain_runtime.managed_driver._load_ray_data_dataset",
            return_value=dataset,
            create=True,
        ):
            trainer = build_trainer(config, ray_components=fake_ray)

        self.assertEqual(trainer.kwargs["datasets"], {"train": dataset})
        self.assertEqual(
            trainer.kwargs["dataset_config"].kwargs["datasets_to_split"], []
        )
        self.assertEqual(
            trainer.kwargs["train_loop_config"]["data_mode"], "ray-data-stage"
        )


if __name__ == "__main__":
    unittest.main()
