from __future__ import annotations

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


class _RowGroup:
    def __init__(self, rows):
        self.rows = tuple(dict(row) for row in rows)

    def slice(self, offset, length):
        return _RowGroup(self.rows[offset : offset + length])

    def to_pylist(self):
        return [dict(row) for row in self.rows]


class _Metadata:
    def __init__(self, groups):
        self.groups = tuple(tuple(group) for group in groups)
        self.num_row_groups = len(self.groups)

    def row_group(self, index):
        return types.SimpleNamespace(num_rows=len(self.groups[index]))


class _ParquetFile:
    opened = {}

    def __init__(self, path):
        self.path = pathlib.Path(path)
        self.groups = self.opened[self.path]
        self.metadata = _Metadata(self.groups)
        self.reads = []

    def read_row_group(self, index, *, columns):
        self.reads.append((index, tuple(columns)))
        return _RowGroup(self.groups[index])


def _payload_row(token: str, source_digest: str) -> dict:
    return {
        "token": token,
        "scene": "scene-a",
        "split": "train",
        "class_ids": [1, 2],
        "timestamp": 123,
        "points": b"\x00" * 20,
        "info": b"trusted",
        "source_digest": source_digest,
    }


def _ref(token: str, source_digest: str, shard_path: str, row_index: int) -> dict:
    return {
        "ordinal": row_index,
        "token": token,
        "class_ids": [1, 2],
        "source_digest": source_digest,
        "split": "train",
        "shard_path": shard_path,
        "row_index": row_index,
    }


class S1HParquetResolverTest(unittest.TestCase):
    def setUp(self):
        from raytrain_runtime.data_metrics import reset_data_metrics_for_tests

        reset_data_metrics_for_tests()

    def tearDown(self):
        from raytrain_runtime.data_metrics import reset_data_metrics_for_tests

        reset_data_metrics_for_tests()

    def test_reads_only_required_row_groups_and_preserves_ref_order(self):
        from raytrain_runtime.s1h_parquet import S1HParquetBatchResolver

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            digest = hashlib.sha256(b"shard").hexdigest()
            relative = f"dataset-s1h/shards/sha256-{digest}.parquet"
            source = root / relative
            source.parent.mkdir(parents=True)
            source.write_bytes(b"parquet-placeholder")
            rows = tuple(
                _payload_row(f"token-{index}", hashlib.sha256(str(index).encode()).hexdigest())
                for index in range(5)
            )
            _ParquetFile.opened = {source: (rows[:2], rows[2:])}
            opened = []

            def parquet_file(path):
                value = _ParquetFile(path)
                opened.append(value)
                return value

            resolver = S1HParquetBatchResolver(
                dataset_root=root,
                dataset_id="dataset-s1h",
                cache_policy="off",
                parquet_module=types.SimpleNamespace(ParquetFile=parquet_file),
            )
            resolved = resolver(
                (
                    _ref("token-4", rows[4]["source_digest"], relative, 4),
                    _ref("token-2", rows[2]["source_digest"], relative, 2),
                )
            )

        self.assertEqual([row["token"] for row in resolved], ["token-4", "token-2"])
        self.assertEqual(
            opened[0].reads,
            [(1, ("token", "scene", "split", "class_ids", "timestamp", "points", "info", "source_digest"))],
        )

    def test_cache_is_given_the_content_digest_without_loading_the_file(self):
        from raytrain_runtime.s1h_parquet import S1HParquetBatchResolver

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            digest = hashlib.sha256(b"shard").hexdigest()
            relative = f"dataset-s1h/shards/sha256-{digest}.parquet"
            source = root / relative
            source.parent.mkdir(parents=True)
            source.write_bytes(b"source")
            cached = root / "cached.parquet"
            cached.write_bytes(b"cached")
            row = _payload_row("token-a", hashlib.sha256(b"row").hexdigest())
            _ParquetFile.opened = {cached: ((row,),)}
            cache = mock.Mock()
            cache.resolve.return_value = cached

            resolver = S1HParquetBatchResolver(
                dataset_root=root,
                dataset_id="dataset-s1h",
                cache_policy="bounded",
                cache=cache,
                parquet_module=types.SimpleNamespace(ParquetFile=_ParquetFile),
            )
            result = resolver((_ref("token-a", row["source_digest"], relative, 0),))

        self.assertEqual(result[0]["token"], "token-a")
        cache.resolve.assert_called_once_with(source, digest)

    def test_records_source_and_cache_reads_without_exposing_storage_paths(self):
        from raytrain_runtime.data_metrics import snapshot_data_metrics
        from raytrain_runtime.s1h_parquet import S1HParquetBatchResolver

        class Cache:
            def __init__(self, cached):
                self.cached = cached
                self.values = {
                    "hit": 0,
                    "miss": 0,
                    "download": 0,
                    "fallback": 0,
                    "checksum_failure": 0,
                    "eviction": 0,
                    "stale_temp_reclaimed": 0,
                    "bytes": 0,
                }

            def metrics_snapshot(self):
                return dict(self.values)

            def resolve(self, _source, _digest):
                self.values = {
                    **self.values,
                    "hit": self.values["hit"] + 1,
                    "stale_temp_reclaimed": self.values["stale_temp_reclaimed"] + 1,
                }
                return self.cached

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            digest = hashlib.sha256(b"shard").hexdigest()
            relative = f"dataset-s1h/shards/sha256-{digest}.parquet"
            source = root / relative
            source.parent.mkdir(parents=True)
            source.write_bytes(b"source")
            cached = root / "cached.parquet"
            cached.write_bytes(b"cached")
            row = _payload_row("token-a", hashlib.sha256(b"row").hexdigest())
            _ParquetFile.opened = {source: ((row,),), cached: ((row,),)}

            source_resolver = S1HParquetBatchResolver(
                dataset_root=root,
                dataset_id="dataset-s1h",
                cache_policy="off",
                parquet_module=types.SimpleNamespace(ParquetFile=_ParquetFile),
            )
            source_resolver((_ref("token-a", row["source_digest"], relative, 0),))
            cached_resolver = S1HParquetBatchResolver(
                dataset_root=root,
                dataset_id="dataset-s1h",
                cache_policy="bounded",
                cache=Cache(cached),
                parquet_module=types.SimpleNamespace(ParquetFile=_ParquetFile),
            )
            cached_resolver((_ref("token-a", row["source_digest"], relative, 0),))

        metrics = snapshot_data_metrics()
        self.assertEqual(metrics["dataset_shard_reads_total"], 2.0)
        self.assertEqual(metrics["dataset_source_reads_total"], 1.0)
        self.assertEqual(metrics["dataset_cache_reads_total"], 1.0)
        self.assertEqual(metrics["dataset_cache_hits_total"], 1.0)
        self.assertEqual(metrics["dataset_cache_stale_temp_reclaimed_total"], 1.0)
        self.assertGreater(metrics["dataset_source_read_seconds_total"], 0)
        self.assertGreater(metrics["dataset_cache_read_seconds_total"], 0)
        self.assertNotIn(str(root), str(metrics))
        self.assertNotIn("cached.parquet", str(metrics))

    def test_rejects_traversal_wrong_dataset_and_invalid_row_without_leaking_path(self):
        from raytrain_runtime.s1h_parquet import S1HParquetBatchResolver

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            resolver = S1HParquetBatchResolver(
                dataset_root=root,
                dataset_id="dataset-s1h",
                cache_policy="off",
                parquet_module=types.SimpleNamespace(ParquetFile=_ParquetFile),
            )
            private = "../../AK=secret/private.parquet"
            invalid = _ref("token-a", "a" * 64, private, 0)
            with self.assertRaisesRegex(ValueError, "invalid shard reference") as caught:
                resolver((invalid,))

            wrong = _ref(
                "token-a",
                "a" * 64,
                f"other/shards/sha256-{'b' * 64}.parquet",
                0,
            )
            with self.assertRaisesRegex(ValueError, "invalid shard reference"):
                resolver((wrong,))

        self.assertNotIn("AK=secret", str(caught.exception))
        self.assertNotIn("private.parquet", str(caught.exception))

    def test_environment_factory_requires_platform_provenance(self):
        from raytrain_runtime.s1h_parquet import resolver_from_environment

        with mock.patch.dict("os.environ", {}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "provenance"):
                resolver_from_environment()


if __name__ == "__main__":
    unittest.main(verbosity=2)
