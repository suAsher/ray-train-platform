from __future__ import annotations

import hashlib
import copy
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
        self.nbytes = sum(len(row["points"]) + len(row["info"]) + 256 for row in rows)

    def slice(self, offset, length):
        return _RowGroup(self.rows[offset : offset + length])

    def to_pylist(self):
        if len(self.rows) > 1:
            raise AssertionError("resolver expanded unselected rows into Python objects")
        return copy.deepcopy(list(self.rows))


class _Metadata:
    def __init__(self, groups):
        self.groups = tuple(tuple(group) for group in groups)
        self.num_row_groups = len(self.groups)

    def row_group(self, index):
        row_count = len(self.groups[index])
        return types.SimpleNamespace(
            num_rows=row_count,
            num_columns=2,
            column=lambda column_index: types.SimpleNamespace(
                total_compressed_size=(column_index + 1) * row_count,
            ),
        )


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
    def test_real_parquet_reuses_arrow_buffers_and_reads_nvme_when_source_is_gone(self):
        try:
            import pyarrow as pa
            import pyarrow.parquet as pq
        except ImportError:
            self.skipTest("PyArrow is required for the real Parquet regression")
        from raytrain_runtime.s1h_parquet import S1HParquetBatchResolver
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            roots = (root / "nvme1", root / "nvme2")
            for cache_root in roots:
                cache_root.mkdir()
            rows = [_payload_row(f"token-{i}", "b" * 64) for i in range(4)]
            buffer = pa.BufferOutputStream()
            pq.write_table(pa.Table.from_pylist(rows), buffer, row_group_size=2)
            payload = buffer.getvalue().to_pybytes()
            digest = hashlib.sha256(payload).hexdigest()
            relative = f"dataset-s1h/shards/sha256-{digest}.parquet"
            source = root / relative
            source.parent.mkdir(parents=True)
            source.write_bytes(payload)
            cache = ShardCache(roots=roots, policy="bounded")
            read_groups = []
            closed = []

            class Reader:
                def __init__(self, path):
                    self.reader = pq.ParquetFile(path)
                    self.metadata = self.reader.metadata

                def read_row_group(self, index, **kwargs):
                    read_groups.append(index)
                    return self.reader.read_row_group(index, **kwargs)

                def close(self):
                    self.reader.close()
                    closed.append(True)

            resolver = S1HParquetBatchResolver(
                dataset_root=root, dataset_id="dataset-s1h", cache_policy="bounded",
                cache=cache, parquet_module=types.SimpleNamespace(ParquetFile=Reader),
            )
            first = resolver((_ref("token-0", "b" * 64, relative, 0),))[0]
            first["class_ids"].append(99)
            source.unlink()
            actual = resolver(tuple(_ref(f"token-{i}", "b" * 64, relative, i) for i in (1, 0)))
            self.assertEqual(actual, (rows[1], rows[0]))
            self.assertEqual(read_groups, [0])
            self.assertEqual(len(closed), 2)

            # A new process-local resolver/cache also verifies and reads the
            # persisted shard while the original source is absent.
            cold_resolver = S1HParquetBatchResolver(
                dataset_root=root, dataset_id="dataset-s1h", cache_policy="bounded",
                cache=ShardCache(roots=roots, policy="bounded"),
                parquet_module=types.SimpleNamespace(ParquetFile=Reader),
            )
            actual = cold_resolver(tuple(_ref(f"token-{i}", "b" * 64, relative, i) for i in (3, 1)))
            self.assertEqual(actual, (rows[3], rows[1]))
            self.assertEqual(read_groups, [0, 1, 0])
            self.assertEqual(len(closed), 3)

    def test_adjacent_batches_reuse_bounded_row_group_buffers(self):
        from raytrain_runtime.s1h_parquet import S1HParquetBatchResolver

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary).resolve()
            relative = f"dataset-s1h/shards/sha256-{'a' * 64}.parquet"
            source = root / relative
            source.parent.mkdir(parents=True)
            source.touch()
            rows = [_payload_row(f"token-{i}", "b" * 64) for i in range(4)]
            _ParquetFile.opened = {source: (rows[:2], rows[2:])}
            opened = []

            def factory(path):
                value = _ParquetFile(path)
                opened.append(value)
                return value

            resolver = S1HParquetBatchResolver(
                dataset_root=root, dataset_id="dataset-s1h", cache_policy="off",
                parquet_module=types.SimpleNamespace(ParquetFile=factory),
                row_group_cache_bytes=600,
            )
            def read(index):
                return resolver((_ref(f"token-{index}", "b" * 64, relative, index),))[0]

            first = read(0)
            first["class_ids"].append(99)
            self.assertEqual(read(1)["token"], "token-1")
            self.assertEqual(read(0)["class_ids"], [1, 2])
            self.assertEqual(sum(len(value.reads) for value in opened), 1)
            read(2)  # The second group evicts the first under the byte budget.
            read(0)
            self.assertEqual(sum(len(value.reads) for value in opened), 3)
            resolver._row_group_budget = 1
            resolver._row_groups.clear()
            resolver._row_group_bytes = 0
            read(0)
            read(1)
            self.assertEqual(sum(len(value.reads) for value in opened), 5)
            self.assertEqual(resolver._row_group_bytes, 0)

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
            source.unlink()  # A cache hit must survive an unavailable source.
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
        self.assertEqual(metrics["dataset_source_bytes_total"], 3.0)
        self.assertEqual(metrics["dataset_cache_bytes_read_total"], 3.0)
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
