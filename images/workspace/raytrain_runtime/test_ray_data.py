from __future__ import annotations

import dataclasses
import contextlib
import os
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock

import numpy as np


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))

from raytrain_runtime.managed_driver import build_trainer, parse_driver_config  # noqa: E402
from raytrain_runtime import ray_data as ray_data_module  # noqa: E402
from raytrain_runtime import s1h_parquet as s1h_parquet_module  # noqa: E402
from raytrain_runtime.ray_data import (  # noqa: E402
    DatasetConfig,
    build_dataset,
    stage_binary_dataset,
    worker_iterator,
    worker_s1h_webdataset_batches,
)
from raytrain_runtime.s1h_webdataset import WEB_DATASET_REF_COLUMNS  # noqa: E402
from raytrain_runtime.test_s1h_dataset import _row  # noqa: E402
from raytrain_runtime.test_managed_driver import (  # noqa: E402
    BASE_ENV,
    CapturedConfig,
    FAKE_RAY,
    driver_argv,
)


REF_FIELDS = (
    "ordinal",
    "token",
    "class_ids",
    "source_digest",
    "split",
    "shard_path",
    "row_index",
)


def _sample_ref(row, index):
    return {
        "ordinal": index,
        "token": row["token"],
        "class_ids": row["class_ids"],
        "source_digest": row["source_digest"],
        "split": "train",
        "shard_path": f"dataset-s1h/shards/sha256-{'a' * 64}.parquet",
        "row_index": index,
    }


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

    def test_unsupported_format_error_does_not_echo_the_user_value(self):
        private_format = "tos://access:secret@internal/format"

        with self.assertRaisesRegex(ValueError, "unsupported Ray Data format") as caught:
            DatasetConfig(format=private_format, uri="/mnt/data/input")
        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)

    def test_webdataset_worker_materializes_only_current_ray_batch(self):
        ref = {
            "ordinal": 0,
            "token": "sample-token",
            "scene": "scene-token",
            "class_ids": [0],
            "timestamp": 123,
            "source_digest": "b" * 64,
            "split": "train",
            "shard_path": f"dataset-s1h/shards/sha256-{'a' * 64}.tar",
            "shard_sha256": "a" * 64,
            "shard_size": 1024,
            "metadata_member": "sample-token/metadata.json",
        }

        class Shard:
            def iter_batches(self, **kwargs):
                self.kwargs = kwargs
                yield {
                    field: np.asarray([ref[field]], dtype=object)
                    for field in WEB_DATASET_REF_COLUMNS
                }

        class Resolver:
            def __init__(self):
                self.active = False
                self.calls = []

            @contextlib.contextmanager
            def resolve_batch(self, refs):
                self.active = True
                self.calls.append(tuple(item["token"] for item in refs))
                try:
                    yield ({"token": "sample-token", "payload_paths": {}},)
                finally:
                    self.active = False

        shard = Shard()
        resolver = Resolver()
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train
        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            batches = worker_s1h_webdataset_batches(
                samples_per_gpu=1,
                prefetch_batches=2,
                pipeline=lambda sample: {**sample, "loaded": resolver.active},
                batch_resolver=resolver,
            )
            first = next(batches)
            with self.assertRaises(StopIteration):
                next(batches)

        self.assertEqual(resolver.calls, [("sample-token",)])
        self.assertFalse(resolver.active)
        self.assertTrue(first[0]["loaded"])
        self.assertEqual(
            shard.kwargs,
            {"batch_size": 1, "batch_format": "numpy", "prefetch_batches": 2},
        )

    def test_webdataset_worker_resamples_post_pipeline_empty_targets(self):
        def ref(token, ordinal):
            return {
                "ordinal": ordinal,
                "token": token,
                "scene": "scene-token",
                "class_ids": [0],
                "timestamp": 123 + ordinal,
                "source_digest": chr(ord("b") + ordinal) * 64,
                "split": "train",
                "shard_path": f"dataset-s1h/shards/sha256-{'a' * 64}.tar",
                "shard_sha256": "a" * 64,
                "shard_size": 1024,
                "metadata_member": f"{token}/metadata.json",
            }

        refs = (ref("empty-token", 0), ref("valid-token", 1))

        class Shard:
            def iter_batches(self, **kwargs):
                for item in refs:
                    yield {
                        field: np.asarray([item[field]], dtype=object)
                        for field in WEB_DATASET_REF_COLUMNS
                    }

        class Resolver:
            @contextlib.contextmanager
            def resolve_batch(self, batch_refs):
                yield tuple({"token": item["token"]} for item in batch_refs)

        class DataContainer:
            def __init__(self, values):
                self._data = np.asarray(values, dtype=np.int64)

        def pipeline(sample):
            labels = [] if sample["token"] == "empty-token" else [0]
            return {**sample, "gt_labels_3d": DataContainer(labels)}

        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=Shard())
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train
        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            batches = list(
                worker_s1h_webdataset_batches(
                    samples_per_gpu=1,
                    prefetch_batches=0,
                    pipeline=pipeline,
                    batch_resolver=Resolver(),
                    expected_sample_count=2,
                )
            )

        self.assertEqual(len(batches), 2)
        self.assertEqual([batch[0]["token"] for batch in batches], [
            "valid-token",
            "valid-token",
        ])
        self.assertIsNot(batches[0][0], batches[1][0])

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

    def test_build_dataset_defense_redacts_an_unsupported_format(self):
        private_format = "tos://access:secret@internal/format"
        fake_data = types.ModuleType("ray.data")
        fake_ray = types.ModuleType("ray")
        fake_ray.data = fake_data
        invalid_config = types.SimpleNamespace(
            format=private_format,
            uri="/mnt/data/input",
        )

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.data": fake_data}):
            with self.assertRaisesRegex(
                ValueError, "unsupported Ray Data format"
            ) as caught:
                build_dataset(invalid_config)

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)

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
        private_name = "tos://access:secret@internal/shard"
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=None)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train
        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            with self.assertRaisesRegex(RuntimeError, "shard.*unavailable") as caught:
                worker_iterator(private_name)

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)

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

    def test_binary_dataset_rejects_preoccupied_temporary_view_symlink(self):
        class Iterator:
            def iter_rows(self):
                return iter(({"path": "/selected/a.bin", "bytes": b"a"},))

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            cache = base / "cache"
            outside = base / "outside"
            cache.mkdir()
            outside.mkdir()
            sentinel = outside / "keep.txt"
            sentinel.write_text("keep", encoding="utf-8")
            temporary_view = cache / f".dataset-view.ray-data.{os.getpid()}.tmp"
            temporary_view.symlink_to(outside, target_is_directory=True)

            with self.assertRaisesRegex(ValueError, "unsafe"):
                stage_binary_dataset(
                    Iterator(),
                    source_root="/selected",
                    cache_paths=str(cache),
                    copy_workers=1,
                )

            self.assertEqual(sentinel.read_text(encoding="utf-8"), "keep")
            self.assertTrue(temporary_view.is_symlink())

    def test_binary_dataset_rejects_preoccupied_ready_view_symlink(self):
        class Iterator:
            def iter_rows(self):
                return iter(({"path": "/selected/a.bin", "bytes": b"a"},))

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            cache = base / "cache"
            outside = base / "outside"
            cache.mkdir()
            outside.mkdir()
            sentinel = outside / "keep.txt"
            sentinel.write_text("keep", encoding="utf-8")
            view = cache / "dataset-view"
            view.symlink_to(outside, target_is_directory=True)

            with self.assertRaisesRegex(ValueError, "unsafe"):
                stage_binary_dataset(
                    Iterator(),
                    source_root="/selected",
                    cache_paths=str(cache),
                    copy_workers=1,
                )

            self.assertEqual(sentinel.read_text(encoding="utf-8"), "keep")
            self.assertTrue(view.is_symlink())

    def test_binary_dataset_rejects_a_path_outside_selected_input(self):
        private_path = "/other/access-key-secret.bin"
        iterator = mock.Mock()
        iterator.iter_rows.return_value = iter((
            {"path": private_path, "bytes": b"secret"},
        ))
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(
                ValueError, "escaped selected input"
            ) as caught:
                stage_binary_dataset(
                    iterator,
                    source_root="/selected",
                    cache_paths=str(pathlib.Path(temporary) / "cache"),
                )

        self.assertNotIn(private_path, str(caught.exception))
        self.assertNotIn("access-key-secret", str(caught.exception))

    def test_binary_dataset_rejects_non_binary_payload_without_echoing_path(self):
        private_path = "/selected/internal/private-key.bin"
        iterator = mock.Mock()
        iterator.iter_rows.return_value = iter((
            {"path": private_path, "bytes": "not-binary"},
        ))
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "payload is not binary") as caught:
                stage_binary_dataset(
                    iterator,
                    source_root="/selected",
                    cache_paths=str(pathlib.Path(temporary) / "cache"),
                )

        self.assertNotIn(private_path, str(caught.exception))
        self.assertNotIn("private-key", str(caught.exception))


class _FakeRayDataset:
    def __init__(self, rows, calls=None):
        self.rows = tuple(dict(row) for row in rows)
        self.calls = [] if calls is None else calls

    def select_columns(self, columns):
        if not isinstance(columns, list):
            raise TypeError("Ray select_columns requires a list")
        names = tuple(columns)
        self.calls.append(("select_columns", names))
        return _FakeRayDataset(
            ({name: row[name] for name in names} for row in self.rows),
            self.calls,
        )

    def limit(self, limit):
        if isinstance(limit, bool) or not isinstance(limit, int) or limit < 0:
            raise TypeError("Ray limit requires a non-negative integer")
        self.calls.append(("limit", limit))
        return _FakeRayDataset(self.rows[:limit], self.calls)

    def union(self, other):
        if not isinstance(other, _FakeRayDataset):
            raise TypeError("Ray union requires another Dataset")
        self.calls.append(("union",))
        return _FakeRayDataset((*self.rows, *other.rows), self.calls)

    def count(self):
        raise AssertionError("training preparation must trust bounded provenance")

    def iter_rows(self):
        raise AssertionError("training preparation must not collect refs")

    def take_all(self):
        raise AssertionError("training preparation must never use take_all")

    def materialize(self):
        raise AssertionError("training preparation must remain lazy")


def _lightweight_refs(count):
    for index in range(count):
        row = _row(token=f"token-{index}", class_ids=(index % 3,))
        yield _sample_ref(row, index)


class S1HPlanningTest(unittest.TestCase):
    def test_prepare_pads_ten_refs_to_equal_four_worker_shards_lazily(self):
        dataset = _FakeRayDataset(_lightweight_refs(10))

        prepared, samples_per_worker, padding_count = (
            ray_data_module.prepare_s1h_training_dataset(
                dataset,
                sample_count=10,
                world_size=4,
            )
        )

        self.assertEqual(samples_per_worker, 3)
        self.assertEqual(padding_count, 2)
        self.assertEqual(
            [row["token"] for row in prepared.rows],
            [*(f"token-{index}" for index in range(10)), "token-0", "token-1"],
        )
        self.assertTrue(
            all(
                set(row) == set(REF_FIELDS)
                for row in prepared.rows
            ),
            "training must consume only the publisher's lightweight CBGS refs",
        )
        self.assertEqual(
            dataset.calls,
            [
                ("select_columns", REF_FIELDS),
                ("limit", 2),
                ("union",),
            ],
        )

    def test_prepare_keeps_an_already_divisible_plan_without_padding(self):
        dataset = _FakeRayDataset(_lightweight_refs(12))

        prepared, samples_per_worker, padding_count = (
            ray_data_module.prepare_s1h_training_dataset(
                dataset,
                sample_count=12,
                world_size=4,
            )
        )

        self.assertEqual([row["token"] for row in prepared.rows], [
            f"token-{index}" for index in range(12)
        ])
        self.assertEqual((samples_per_worker, padding_count), (3, 0))
        self.assertEqual(
            dataset.calls,
            [("select_columns", REF_FIELDS)],
        )

    def test_prepare_rejects_a_plan_smaller_than_the_worker_world(self):
        dataset = _FakeRayDataset(_lightweight_refs(3))

        with self.assertRaisesRegex(ValueError, "sample_count.*world_size"):
            ray_data_module.prepare_s1h_training_dataset(
                dataset,
                sample_count=3,
                world_size=4,
            )

        self.assertEqual(dataset.calls, [])

    def test_training_runtime_has_no_driver_side_cbgs_plan_transform(self):
        self.assertFalse(
            hasattr(ray_data_module, "apply_s1h_cbgs"),
            "CBGS refs must be ordered by the publisher, not collected by the driver",
        )

    def test_worker_batches_use_named_shard_and_bounded_numpy_prefetch(self):
        self.assertTrue(
            hasattr(ray_data_module, "worker_s1h_batches"),
            "S1H worker streaming is not implemented",
        )
        rows = (_row("token-a"), _row("token-b"), _row("token-c"))

        def numpy_batch(values):
            return {
                name: np.asarray([row[name] for row in values], dtype=object)
                for name in rows[0]
            }

        class Shard:
            def __init__(self):
                self.kwargs = None
                self.yielded = 0

            def iter_batches(self, **kwargs):
                self.kwargs = kwargs
                self.yielded += 1
                yield numpy_batch(rows[:2])
                self.yielded += 1
                yield numpy_batch(rows[2:])

            def iter_rows(self):
                raise AssertionError("worker rows must use bounded batch prefetch")

            def materialize(self):
                raise AssertionError("worker shard must remain streaming")

        shard = Shard()
        batch_resolver = mock.Mock(
            side_effect=AssertionError("full rows must decode without resolution")
        )
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            batches = ray_data_module.worker_s1h_batches(
                samples_per_gpu=2,
                prefetch_batches=3,
                pipeline=lambda sample: {**sample, "pipeline_ran": True},
                batch_resolver=batch_resolver,
            )
            first = next(batches)
            self.assertEqual(shard.yielded, 1)
            second = next(batches)
            with self.assertRaises(StopIteration):
                next(batches)

        fake_train.get_dataset_shard.assert_called_once_with("train")
        self.assertEqual(
            shard.kwargs,
            {
                "batch_size": 2,
                "batch_format": "numpy",
                "prefetch_batches": 3,
            },
        )
        self.assertEqual([sample["token"] for sample in first], ["token-a", "token-b"])
        self.assertEqual([sample["token"] for sample in second], ["token-c"])
        self.assertTrue(all(sample["pipeline_ran"] for sample in (*first, *second)))
        self.assertEqual(first[0]["points"].shape, (2, 5))
        batch_resolver.assert_not_called()

    def test_worker_ref_batches_resolve_once_for_each_current_batch(self):
        rows = (_row("token-a"), _row("token-b"), _row("token-c"))
        rows_by_token = {row["token"]: row for row in rows}

        def ref_batch(values):
            return {
                name: np.asarray(
                    [_sample_ref(row, index)[name] for index, row in enumerate(values)],
                    dtype=object,
                )
                for name in REF_FIELDS
            }

        class Shard:
            def __init__(self):
                self.yielded = 0

            def iter_batches(self, **_kwargs):
                self.yielded += 1
                yield ref_batch(rows[:2])
                self.yielded += 1
                yield ref_batch(rows[2:])

        shard = Shard()
        resolved_batches = []

        def batch_resolver(refs):
            tokens = tuple(ref["token"] for ref in refs)
            resolved_batches.append(tokens)
            return tuple(rows_by_token[token] for token in tokens)

        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            batches = ray_data_module.worker_s1h_batches(
                samples_per_gpu=2,
                prefetch_batches=1,
                batch_resolver=batch_resolver,
            )
            first = next(batches)
            self.assertEqual(shard.yielded, 1)
            self.assertEqual(resolved_batches, [("token-a", "token-b")])
            second = next(batches)
            with self.assertRaises(StopIteration):
                next(batches)

        self.assertEqual([sample["token"] for sample in first], ["token-a", "token-b"])
        self.assertEqual([sample["token"] for sample in second], ["token-c"])
        self.assertEqual(
            resolved_batches,
            [("token-a", "token-b"), ("token-c",)],
        )

    def test_worker_ref_batches_reuse_default_resolver_and_measure_prefetch_wait(self):
        from raytrain_runtime.data_metrics import (
            reset_data_metrics_for_tests,
            snapshot_data_metrics,
        )

        reset_data_metrics_for_tests()
        self.addCleanup(reset_data_metrics_for_tests)
        rows = (_row("token-a"), _row("token-b"), _row("token-c"))
        rows_by_token = {row["token"]: row for row in rows}

        def ref_batch(values):
            return {
                name: np.asarray(
                    [_sample_ref(row, index)[name] for index, row in enumerate(values)],
                    dtype=object,
                )
                for name in REF_FIELDS
            }

        shard = mock.Mock()
        shard.iter_batches.side_effect = lambda **_kwargs: iter(
            (ref_batch(rows[:2]), ref_batch(rows[2:]))
        )
        resolver = mock.Mock(
            side_effect=lambda refs: tuple(rows_by_token[ref["token"]] for ref in refs)
        )
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(
            sys.modules, {"ray": fake_ray, "ray.train": fake_train}
        ), mock.patch.object(
            s1h_parquet_module,
            "resolver_from_environment",
            return_value=resolver,
        ) as resolver_factory, mock.patch.object(
            ray_data_module.time,
            "perf_counter",
            side_effect=(1.0, 1.25, 2.0, 2.5, 3.0, 3.5),
        ):
            batches = list(
                ray_data_module.worker_s1h_batches(
                    samples_per_gpu=2,
                    prefetch_batches=2,
                )
            )

        self.assertEqual([len(batch) for batch in batches], [2, 1])
        resolver_factory.assert_called_once_with()
        self.assertEqual(resolver.call_count, 2)
        metrics = snapshot_data_metrics()
        self.assertEqual(metrics["dataset_batches_total"], 2.0)
        self.assertEqual(metrics["dataset_samples_total"], 3.0)
        self.assertEqual(metrics["dataset_prefetch_wait_seconds_total"], 0.75)

    def test_worker_ref_batch_requires_a_matching_platform_batch_resolver(self):
        rows = (_row("token-a"), _row("token-b"))
        ref_batch = {
            name: np.asarray(
                [_sample_ref(row, index)[name] for index, row in enumerate(rows)],
                dtype=object,
            )
            for name in REF_FIELDS
        }
        shard = mock.Mock()
        shard.iter_batches.side_effect = lambda **_kwargs: iter((ref_batch,))
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            with self.assertRaisesRegex(RuntimeError, "batch resolver"):
                next(ray_data_module.worker_s1h_batches(samples_per_gpu=2))

            with self.assertRaisesRegex(ValueError, "count"):
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=2,
                        batch_resolver=lambda _refs: rows[:1],
                    )
                )

            with self.assertRaisesRegex(ValueError, "does not match"):
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=2,
                        batch_resolver=lambda _refs: tuple(reversed(rows)),
                    )
                )

    def test_worker_ref_batch_checks_identity_and_redacts_resolver_failures(self):
        rows = (_row("token-a"), _row("token-b"))
        ref_batch = {
            name: np.asarray(
                [_sample_ref(row, index)[name] for index, row in enumerate(rows)],
                dtype=object,
            )
            for name in REF_FIELDS
        }
        shard = mock.Mock()
        shard.iter_batches.side_effect = lambda **_kwargs: iter((ref_batch,))
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        mismatched_rows = []
        wrong_token = dict(rows[0])
        wrong_token["token"] = "token-wrong"
        mismatched_rows.append(("token", (wrong_token, rows[1])))
        wrong_classes = dict(rows[0])
        wrong_classes["class_ids"] = [99]
        mismatched_rows.append(("class_ids", (wrong_classes, rows[1])))
        wrong_digest = dict(rows[0])
        wrong_digest["source_digest"] = "f" * 64
        mismatched_rows.append(("source_digest", (wrong_digest, rows[1])))

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            for field, resolved in mismatched_rows:
                with self.subTest(field=field):
                    with self.assertRaisesRegex(ValueError, "does not match"):
                        next(
                            ray_data_module.worker_s1h_batches(
                                samples_per_gpu=2,
                                batch_resolver=lambda _refs, rows=resolved: rows,
                            )
                        )

            private_value = "tos://access-key:secret@internal/private.parquet"

            def failing_resolver(_refs):
                raise RuntimeError(private_value)

            with self.assertRaisesRegex(RuntimeError, "resolver failed") as caught:
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=2,
                        batch_resolver=failing_resolver,
                    )
                )

            class BrokenRows(list):
                def __len__(self):
                    raise RuntimeError(private_value)

            with self.assertRaisesRegex(ValueError, "row sequence") as broken:
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=2,
                        batch_resolver=lambda _refs: BrokenRows(rows),
                    )
                )

        for error in (caught.exception, broken.exception):
            message = str(error)
            self.assertNotIn("tos://", message)
            self.assertNotIn("access-key", message)
            self.assertNotIn("secret", message)
            self.assertNotIn("internal", message)

    def test_worker_validates_the_whole_resolved_batch_before_pipeline(self):
        rows = (_row("token-a"), _row("token-b"))
        ref_batch = {
            name: np.asarray(
                [_sample_ref(row, index)[name] for index, row in enumerate(rows)],
                dtype=object,
            )
            for name in REF_FIELDS
        }
        wrong_second = dict(rows[1])
        wrong_second["source_digest"] = "f" * 64
        shard = mock.Mock()
        shard.iter_batches.side_effect = lambda **_kwargs: iter((ref_batch,))
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=shard)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train
        pipeline = mock.Mock(side_effect=lambda sample: sample)

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            with self.assertRaisesRegex(ValueError, "does not match"):
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=2,
                        pipeline=pipeline,
                        batch_resolver=lambda _refs: (rows[0], wrong_second),
                    )
                )

        pipeline.assert_not_called()

    def test_worker_batches_reject_unbounded_prefetch_and_missing_shard(self):
        self.assertTrue(hasattr(ray_data_module, "worker_s1h_batches"))
        private_name = "tos://access:secret@internal/shard"
        fake_train = types.ModuleType("ray.train")
        fake_train.get_dataset_shard = mock.Mock(return_value=None)
        fake_ray = types.ModuleType("ray")
        fake_ray.train = fake_train

        with mock.patch.dict(sys.modules, {"ray": fake_ray, "ray.train": fake_train}):
            with self.assertRaisesRegex(ValueError, "prefetch.*0.*16"):
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=1,
                        prefetch_batches=17,
                    )
                )
            with self.assertRaisesRegex(RuntimeError, "shard.*unavailable") as caught:
                next(
                    ray_data_module.worker_s1h_batches(
                        samples_per_gpu=1,
                        name=private_name,
                    )
                )

        message = str(caught.exception)
        self.assertNotIn("tos://", message)
        self.assertNotIn("access", message)
        self.assertNotIn("secret", message)
        self.assertNotIn("internal", message)


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
