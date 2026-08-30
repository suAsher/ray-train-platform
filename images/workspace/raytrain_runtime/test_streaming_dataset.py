from __future__ import annotations

import dataclasses
import hashlib
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))


class _Dataset:
    def __init__(self, rows, calls=None):
        self.rows = tuple(dict(row) for row in rows)
        self.calls = [] if calls is None else calls

    def filter(self, predicate):
        self.calls.append(("filter",))
        return _Dataset((row for row in self.rows if predicate(row)), self.calls)

    def sort(self, key):
        self.calls.append(("sort", key))
        return _Dataset(sorted(self.rows, key=lambda row: row[key]), self.calls)

    def select_columns(self, columns):
        self.calls.append(("select_columns", tuple(columns)))
        return _Dataset(
            ({name: row[name] for name in columns} for row in self.rows),
            self.calls,
        )

    def limit(self, count):
        self.calls.append(("limit", count))
        return _Dataset(self.rows[:count], self.calls)

    def union(self, other):
        self.calls.append(("union",))
        return _Dataset((*self.rows, *other.rows), self.calls)

    def take_all(self):
        raise AssertionError("streaming dataset must not be collected on the driver")

    def materialize(self):
        raise AssertionError("streaming dataset must remain lazy")


def _manifest_row(ordinal: int, *, split: str = "train") -> dict:
    return {
        "ordinal": ordinal,
        "token": f"token-{ordinal}",
        "class_ids": [ordinal % 3],
        "source_digest": hashlib.sha256(str(ordinal).encode()).hexdigest(),
        "split": split,
        "shard_path": f"dataset-s1h/shards/sha256-{'a' * 64}.parquet",
        "row_index": ordinal,
    }


class StreamingDatasetConfigTest(unittest.TestCase):
    def test_config_is_frozen_and_confines_exact_manifest(self):
        from raytrain_runtime.ray_data import StreamingDatasetConfig

        config = StreamingDatasetConfig(
            dataset_id="dataset-s1h",
            version_id="version-1",
            manifest_path="/mnt/data/.platform/datasets/dataset-s1h/manifests/version-1.parquet",
            manifest_sha256="a" * 64,
            dataset_root="/mnt/data/.platform/datasets",
            train_samples=16,
            cache_policy="bounded",
            prefetch_batches=4,
            shuffle_seed=17,
        )

        with self.assertRaises(dataclasses.FrozenInstanceError):
            config.train_samples = 99  # type: ignore[misc]

    def test_config_rejects_unsafe_or_mismatched_manifest_and_provenance(self):
        from raytrain_runtime.ray_data import StreamingDatasetConfig

        base = {
            "dataset_id": "dataset-s1h",
            "version_id": "version-1",
            "manifest_path": "/mnt/data/.platform/datasets/dataset-s1h/manifests/version-1.parquet",
            "manifest_sha256": "a" * 64,
            "dataset_root": "/mnt/data/.platform/datasets",
            "train_samples": 16,
            "cache_policy": "auto",
            "prefetch_batches": 2,
            "shuffle_seed": 0,
        }
        invalid = (
            {"manifest_path": "/mnt/data/.platform/datasets/other/manifests/version-1.parquet"},
            {"manifest_path": "/mnt/data/.platform/datasets/dataset-s1h/../private.parquet"},
            {"manifest_sha256": "not-a-digest"},
            {"cache_policy": "unbounded"},
            {"train_samples": 0},
            {"prefetch_batches": 17},
        )
        for override in invalid:
            with self.subTest(override=override):
                with self.assertRaises(ValueError):
                    StreamingDatasetConfig(**{**base, **override})


class StreamingDatasetBuildTest(unittest.TestCase):
    def test_reads_verified_manifest_filters_train_and_pads_equal_worker_shards(self):
        from raytrain_runtime.ray_data import (
            StreamingDatasetConfig,
            build_s1h_streaming_dataset,
        )

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            manifest = root / "dataset-s1h" / "manifests" / "version-1.parquet"
            manifest.parent.mkdir(parents=True)
            manifest.write_bytes(b"trusted-reference-manifest")
            rows = (
                _manifest_row(2),
                _manifest_row(99, split="val"),
                _manifest_row(0),
                _manifest_row(1),
            )
            dataset = _Dataset(rows)
            data = types.SimpleNamespace(read_parquet=mock.Mock(return_value=dataset))
            config = StreamingDatasetConfig(
                dataset_id="dataset-s1h",
                version_id="version-1",
                manifest_path=str(manifest),
                manifest_sha256=hashlib.sha256(manifest.read_bytes()).hexdigest(),
                dataset_root=str(root),
                train_samples=3,
                cache_policy="off",
                prefetch_batches=2,
                shuffle_seed=7,
            )

            prepared, per_worker, padding = build_s1h_streaming_dataset(
                config,
                world_size=2,
                ray_data_module=data,
            )

        data.read_parquet.assert_called_once_with(str(manifest))
        self.assertEqual((per_worker, padding), (2, 1))
        self.assertEqual(
            [row["token"] for row in prepared.rows],
            ["token-0", "token-1", "token-2", "token-0"],
        )
        self.assertTrue(
            all(
                set(row)
                == {
                    "ordinal",
                    "token",
                    "class_ids",
                    "source_digest",
                    "split",
                    "shard_path",
                    "row_index",
                }
                for row in prepared.rows
            )
        )
        self.assertEqual(dataset.calls[:2], [("filter",), ("sort", "ordinal")])

    def test_digest_mismatch_fails_before_ray_reads_the_manifest(self):
        from raytrain_runtime.ray_data import (
            StreamingDatasetConfig,
            build_s1h_streaming_dataset,
        )

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            manifest = root / "dataset-s1h" / "manifests" / "version-1.parquet"
            manifest.parent.mkdir(parents=True)
            manifest.write_bytes(b"corrupt")
            config = StreamingDatasetConfig(
                dataset_id="dataset-s1h",
                version_id="version-1",
                manifest_path=str(manifest),
                manifest_sha256="a" * 64,
                dataset_root=str(root),
                train_samples=3,
                cache_policy="off",
            )
            data = types.SimpleNamespace(read_parquet=mock.Mock())

            with self.assertRaisesRegex(ValueError, "manifest integrity"):
                build_s1h_streaming_dataset(
                    config,
                    world_size=2,
                    ray_data_module=data,
                )

        data.read_parquet.assert_not_called()


if __name__ == "__main__":
    unittest.main(verbosity=2)
