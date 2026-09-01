from __future__ import annotations

import fcntl
import hashlib
import multiprocessing
import os
import pathlib
import sys
import tempfile
import time
import types
import unittest
from unittest import mock


RUNTIME_PARENT = pathlib.Path(__file__).resolve().parent.parent
if str(RUNTIME_PARENT) not in sys.path:
    sys.path.insert(0, str(RUNTIME_PARENT))


def _write_source(directory: pathlib.Path, payload: bytes) -> tuple[pathlib.Path, str]:
    digest = hashlib.sha256(payload).hexdigest()
    source = directory / f"sha256-{digest}.parquet"
    source.parent.mkdir(parents=True, exist_ok=True)
    source.write_bytes(payload)
    return source, digest


def _payload_for_root(index: int, root_index: int, size: int = 30) -> bytes:
    while True:
        prefix = f"payload-{index}-".encode()
        payload = (prefix + bytes([index % 251]) * size)[:size]
        digest = hashlib.sha256(payload).hexdigest()
        if int(digest[:16], 16) % 2 == root_index:
            return payload
        index += 1


def _resolve_in_process(cache, source: str, digest: str, output) -> None:
    try:
        output.put(("ok", str(cache.resolve(source, digest))))
    except Exception as error:  # pragma: no cover - reported to the parent
        output.put(("error", type(error).__name__, str(error)))


def _hold_lock(path: str, ready, release) -> None:
    descriptor = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        ready.set()
        release.wait(10)
    finally:
        fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)


class _GuardedReader:
    def __init__(self, stream) -> None:
        self._stream = stream
        self.maximum_read = 0

    def __enter__(self):
        self._stream.__enter__()
        return self

    def __exit__(self, *args):
        return self._stream.__exit__(*args)

    def read(self, size: int = -1) -> bytes:
        if size < 0:
            raise AssertionError("cache copied the whole shard in one read")
        self.maximum_read = max(self.maximum_read, size)
        return self._stream.read(size)

    def fileno(self) -> int:
        return self._stream.fileno()


class ShardCacheTest(unittest.TestCase):
    def test_off_returns_source_without_creating_cache_roots(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            source, digest = _write_source(base / "source", b"source payload")
            roots = (base / "missing-data1", base / "missing-data2")
            cache = ShardCache(roots=roots, policy="off")

            resolved = cache.resolve(source, digest)

            self.assertEqual(resolved, source)
            self.assertFalse(roots[0].exists())
            self.assertFalse(roots[1].exists())
            self.assertEqual(
                cache.metrics_snapshot(),
                {
                    "hit": 0,
                    "miss": 0,
                    "download": 0,
                    "fallback": 0,
                    "checksum_failure": 0,
                    "eviction": 0,
                    "stale_temp_reclaimed": 0,
                    "bytes": 0,
                },
            )

    def test_enabled_policies_require_exactly_two_distinct_existing_roots(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            other = root / "other"
            other.mkdir()
            for policy in ("auto", "bounded"):
                with self.subTest(policy=policy, problem="one root"):
                    with self.assertRaisesRegex(ValueError, "two distinct cache roots"):
                        ShardCache(roots=(root,), policy=policy)
                with self.subTest(policy=policy, problem="duplicate root"):
                    with self.assertRaisesRegex(ValueError, "two distinct cache roots"):
                        ShardCache(roots=(root, root), policy=policy)
                with self.subTest(policy=policy, problem="missing root"):
                    with self.assertRaisesRegex(ValueError, "cache root is unavailable"):
                        ShardCache(roots=(root, root / "missing"), policy=policy)
                nested = root / "nested"
                nested.mkdir(exist_ok=True)
                with self.subTest(policy=policy, problem="nested roots"):
                    with self.assertRaisesRegex(ValueError, "two distinct cache roots"):
                        ShardCache(roots=(root, nested), policy=policy)

            with self.assertRaisesRegex(ValueError, "cache policy"):
                ShardCache(roots=(root, other), policy="unlimited")

    def test_digest_stably_selects_one_of_two_roots_and_second_resolve_hits(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", b"stable allocation")
            cache = ShardCache(roots=roots, policy="auto", chunk_bytes=4)

            first = cache.resolve(source, digest)
            second = cache.resolve(source, digest)

            expected_root = roots[int(digest[:16], 16) % 2].resolve()
            self.assertEqual(first, second)
            self.assertEqual(first.read_bytes(), b"stable allocation")
            self.assertTrue(first.is_relative_to(expected_root))
            self.assertFalse(first.is_relative_to(roots[1 - (int(digest[:16], 16) % 2)]))
            self.assertEqual(cache.metrics_snapshot()["miss"], 1)
            self.assertEqual(cache.metrics_snapshot()["download"], 1)
            self.assertEqual(cache.metrics_snapshot()["hit"], 1)
            self.assertEqual(cache.metrics_snapshot()["bytes"], len(b"stable allocation"))

    def test_content_addressed_tar_uses_the_same_bounded_cache(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            payload = b"webdataset-tar-payload"
            digest = hashlib.sha256(payload).hexdigest()
            source = base / "source" / f"sha256-{digest}.tar"
            source.parent.mkdir()
            source.write_bytes(payload)
            cache = ShardCache(roots=roots, policy="bounded", suffix=".tar")

            first = cache.resolve(source, digest)
            second = cache.resolve(source, digest)

            self.assertEqual(first, second)
            self.assertEqual(first.suffix, ".tar")
            self.assertEqual(first.read_bytes(), payload)
            self.assertEqual(cache.metrics_snapshot()["download"], 1)
            self.assertEqual(cache.metrics_snapshot()["hit"], 1)

    def test_copy_is_streamed_fsynced_and_atomically_renamed(self):
        from raytrain_runtime import shard_cache

        payload = b"0123456789" * 4096
        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", payload)
            cache = shard_cache.ShardCache(
                roots=roots,
                policy="bounded",
                max_bytes=10 * len(payload),
                chunk_bytes=1024,
            )
            real_open = pathlib.Path.open
            guarded = _GuardedReader(real_open(source, "rb"))

            def open_path(path, *args, **kwargs):
                if pathlib.Path(path) == source and args and args[0] == "rb":
                    return guarded
                return real_open(path, *args, **kwargs)

            with mock.patch.object(
                pathlib.Path,
                "open",
                autospec=True,
                side_effect=open_path,
            ), mock.patch.object(
                shard_cache.os,
                "fsync",
                wraps=os.fsync,
            ) as fsync, mock.patch.object(
                shard_cache.os,
                "replace",
                wraps=os.replace,
            ) as replace:
                resolved = cache.resolve(source, digest)

            self.assertEqual(resolved.read_bytes(), payload)
            self.assertLessEqual(guarded.maximum_read, 1024)
            self.assertGreaterEqual(fsync.call_count, 2)
            replace.assert_called_once()
            self.assertEqual(list(resolved.parent.glob("*.tmp")), [])

    def test_short_destination_writes_are_retried_until_the_chunk_is_complete(self):
        from raytrain_runtime.shard_cache import ShardCache

        class ShortWriter:
            def __init__(self):
                self.payload = bytearray()

            def write(self, value):
                accepted = bytes(value[:3])
                self.payload.extend(accepted)
                return len(accepted)

        destination = ShortWriter()
        ShardCache._write_all(destination, b"0123456789")

        self.assertEqual(destination.payload, b"0123456789")

    def test_corrupt_cached_shard_is_removed_and_rebuilt_from_source(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            payload = b"trusted parquet payload"
            source, digest = _write_source(base / "source", payload)
            cache = ShardCache(roots=roots, policy="auto", chunk_bytes=3)
            cached = cache.resolve(source, digest)
            cached.chmod(0o600)
            cached.write_bytes(b"corrupt")

            rebuilt = cache.resolve(source, digest)

            self.assertEqual(rebuilt, cached)
            self.assertEqual(rebuilt.read_bytes(), payload)
            metrics = cache.metrics_snapshot()
            self.assertEqual(metrics["checksum_failure"], 1)
            self.assertEqual(metrics["miss"], 2)
            self.assertEqual(metrics["download"], 2)
            self.assertEqual(metrics["hit"], 0)

    def test_cache_hit_refreshes_lru_time_without_rehashing_the_shard(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", b"hot shard")
            cache = ShardCache(roots=roots, policy="auto")
            cached = cache.resolve(source, digest)
            past = time.time() - 600
            os.utime(cached, (past, past))
            cache.resolve(source, digest)
            before = cached.stat()
            time.sleep(0.01)

            with mock.patch.object(
                cache,
                "_stream_digest",
                side_effect=AssertionError("verified hit must not be rehashed"),
            ):
                cache.resolve(source, digest)

            after = cached.stat()
            self.assertGreater(
                max(after.st_atime_ns, after.st_mtime_ns),
                max(before.st_atime_ns, before.st_mtime_ns),
            )

    def test_source_digest_mismatch_fails_closed_without_path_leak(self):
        from raytrain_runtime.shard_cache import ShardCache, ShardIntegrityError

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            expected = hashlib.sha256(b"expected").hexdigest()
            secret = base / "AK=secret" / f"sha256-{expected}.parquet"
            secret.parent.mkdir()
            secret.write_bytes(b"unexpected")
            cache = ShardCache(roots=roots, policy="auto")

            with self.assertRaisesRegex(ShardIntegrityError, "checksum") as caught:
                cache.resolve(secret, expected)

            message = str(caught.exception)
            self.assertNotIn("AK=secret", message)
            self.assertNotIn(str(secret), message)
            self.assertEqual(cache.metrics_snapshot()["checksum_failure"], 1)
            self.assertFalse(
                any(candidate for root in roots for candidate in root.rglob("*.parquet"))
            )

    def test_invalid_digest_and_filename_are_rejected_without_path_leak(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source = base / "private-AK=secret.parquet"
            source.write_bytes(b"payload")
            cache = ShardCache(roots=roots, policy="auto")

            for digest in ("A" * 64, "abc", "../" + "a" * 64):
                with self.subTest(digest=digest):
                    with self.assertRaises(ValueError) as caught:
                        cache.resolve(source, digest)
                    self.assertNotIn("AK=secret", str(caught.exception))
                    self.assertNotIn(str(source), str(caught.exception))

            digest = hashlib.sha256(b"payload").hexdigest()
            with self.assertRaisesRegex(ValueError, "filename") as caught:
                cache.resolve(source, digest)
            self.assertNotIn("AK=secret", str(caught.exception))

    def test_copy_failure_and_insufficient_budget_fall_back_to_source(self):
        from raytrain_runtime import shard_cache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", b"copy failure")
            cache = shard_cache.ShardCache(roots=roots, policy="auto")
            with mock.patch.object(
                cache,
                "_copy_verified",
                side_effect=OSError(f"cannot copy {source}"),
            ):
                resolved = cache.resolve(source, digest)

            self.assertEqual(resolved, source)
            self.assertEqual(cache.metrics_snapshot()["fallback"], 1)

            large, large_digest = _write_source(base / "large", b"x" * 86)
            tiny_vfs = types.SimpleNamespace(
                f_frsize=1,
                f_bsize=1,
                f_blocks=100,
                f_bavail=100,
            )
            constrained = shard_cache.ShardCache(roots=roots, policy="bounded")
            with mock.patch.object(shard_cache.os, "statvfs", return_value=tiny_vfs):
                resolved = constrained.resolve(large, large_digest)

            self.assertEqual(resolved, large)
            self.assertEqual(constrained.metrics_snapshot()["fallback"], 1)
            self.assertEqual(constrained.metrics_snapshot()["download"], 0)

    def test_optional_max_bytes_is_combined_with_current_statvfs_budget(self):
        from raytrain_runtime import shard_cache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", b"x" * 60)
            large_vfs = types.SimpleNamespace(
                f_frsize=1,
                f_bsize=1,
                f_blocks=10_000,
                f_bavail=10_000,
            )
            cache = shard_cache.ShardCache(
                roots=roots,
                policy="bounded",
                max_bytes=70,
            )

            with mock.patch.object(shard_cache.os, "statvfs", return_value=large_vfs):
                resolved = cache.resolve(source, digest)

            self.assertEqual(resolved, source)
            self.assertEqual(cache.metrics_snapshot()["fallback"], 1)

    def test_stale_crash_temporary_is_reclaimed_before_capacity_check(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", b"current shard")
            cache = ShardCache(roots=roots, policy="bounded")
            target = cache._target_path(digest)
            target.parent.mkdir(parents=True)
            stale = target.parent / f".{digest}.crashed.tmp"
            stale.write_bytes(b"x" * 128)
            old = time.time() - 7200
            os.utime(stale, (old, old))

            resolved = cache.resolve(source, digest)

            self.assertNotEqual(resolved, source)
            self.assertFalse(stale.exists())
            self.assertEqual(cache.metrics_snapshot()["stale_temp_reclaimed"], 1)

    @unittest.skipUnless("fork" in multiprocessing.get_all_start_methods(), "requires fork")
    def test_fcntl_lock_allows_only_one_copy_across_processes(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source, digest = _write_source(base / "source", b"z" * (2 * 1024 * 1024))
            cache = ShardCache(roots=roots, policy="auto", chunk_bytes=4096)
            counter = base / "copy-count"
            original = ShardCache._copy_verified

            def counted(instance, *args, **kwargs):
                descriptor = os.open(counter, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
                try:
                    os.write(descriptor, b"copy\n")
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
                time.sleep(0.15)
                return original(instance, *args, **kwargs)

            context = multiprocessing.get_context("fork")
            output = context.Queue()
            with mock.patch.object(ShardCache, "_copy_verified", new=counted):
                workers = [
                    context.Process(
                        target=_resolve_in_process,
                        args=(cache, str(source), digest, output),
                    )
                    for _ in range(4)
                ]
                for worker in workers:
                    worker.start()
                for worker in workers:
                    worker.join(10)

            results = [output.get(timeout=2) for _ in workers]
            self.assertTrue(all(worker.exitcode == 0 for worker in workers))
            self.assertTrue(all(result[0] == "ok" for result in results), results)
            self.assertEqual(len({result[1] for result in results}), 1)
            self.assertEqual(counter.read_text().splitlines(), ["copy"])

    @unittest.skipUnless("fork" in multiprocessing.get_all_start_methods(), "requires fork")
    def test_concurrent_distinct_downloads_keep_each_root_within_high_watermark(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            payloads = (
                _payload_for_root(1000, 0, size=60),
                _payload_for_root(2000, 0, size=60),
            )
            sources = tuple(_write_source(base / "sources", value) for value in payloads)
            cache = ShardCache(roots=roots, policy="bounded", max_bytes=100)
            original = ShardCache._copy_verified
            context = multiprocessing.get_context("fork")
            entered = context.Value("i", 0)
            first_started = context.Event()
            second_started = context.Event()
            release = context.Event()

            def delayed(instance, *args, **kwargs):
                with entered.get_lock():
                    entered.value += 1
                    position = entered.value
                if position == 1:
                    first_started.set()
                    release.wait(5)
                elif position == 2:
                    second_started.set()
                return original(instance, *args, **kwargs)

            output = context.Queue()
            with mock.patch.object(ShardCache, "_copy_verified", new=delayed):
                first = context.Process(
                    target=_resolve_in_process,
                    args=(cache, str(sources[0][0]), sources[0][1], output),
                )
                second = context.Process(
                    target=_resolve_in_process,
                    args=(cache, str(sources[1][0]), sources[1][1], output),
                )
                first.start()
                self.assertTrue(first_started.wait(5))
                second.start()
                second_started.wait(0.5)
                release.set()
                first.join(10)
                second.join(10)

            results = [output.get(timeout=2), output.get(timeout=2)]
            self.assertTrue(all(result[0] == "ok" for result in results), results)
            cached = tuple(
                candidate
                for candidate in roots[0].rglob("sha256-*.parquet")
                if candidate.is_file()
            )
            self.assertLessEqual(sum(candidate.stat().st_size for candidate in cached), 85)

    @unittest.skipUnless("fork" in multiprocessing.get_all_start_methods(), "requires fork")
    def test_lru_evicts_to_low_watermark_but_skips_locked_and_external_files(self):
        from raytrain_runtime.shard_cache import ShardCache

        with tempfile.TemporaryDirectory() as temporary:
            base = pathlib.Path(temporary)
            roots = (base / "data1", base / "data2")
            for root in roots:
                root.mkdir()
            source_dir = base / "sources"
            payloads = tuple(_payload_for_root(index * 100, 0) for index in range(3))
            sources = tuple(_write_source(source_dir, payload) for payload in payloads)
            cache = ShardCache(roots=roots, policy="bounded", max_bytes=100)
            first = cache.resolve(*sources[0])
            second = cache.resolve(*sources[1])
            old = time.time() - 300
            newer = time.time() - 200
            os.utime(first, (old, old))
            os.utime(second, (newer, newer))
            outside = roots[0] / "must-survive.parquet"
            outside.write_bytes(b"outside namespace")
            unknown = first.parents[2] / "unknown.parquet"
            unknown.write_bytes(b"inside namespace but not a cache object")

            lock_path = cache._lock_path(sources[0][1])
            lock_path.parent.mkdir(parents=True, exist_ok=True)
            context = multiprocessing.get_context("fork")
            ready = context.Event()
            release = context.Event()
            holder = context.Process(
                target=_hold_lock,
                args=(str(lock_path), ready, release),
            )
            holder.start()
            self.assertTrue(ready.wait(5))
            try:
                third = cache.resolve(*sources[2])
            finally:
                release.set()
                holder.join(5)

            self.assertTrue(first.exists(), "locked oldest shard must survive")
            self.assertFalse(second.exists(), "next LRU shard should be evicted")
            self.assertTrue(third.exists())
            self.assertTrue(outside.exists())
            self.assertTrue(unknown.exists())
            self.assertEqual(cache.metrics_snapshot()["eviction"], 1)

    def test_metrics_snapshot_is_an_independent_integer_only_value(self):
        from raytrain_runtime.shard_cache import ShardCache

        cache = ShardCache(roots=(), policy="off")
        first = cache.metrics_snapshot()
        first["hit"] = 99
        second = cache.metrics_snapshot()

        self.assertEqual(second["hit"], 0)
        self.assertTrue(all(isinstance(value, int) for value in second.values()))


if __name__ == "__main__":
    unittest.main(verbosity=2)
