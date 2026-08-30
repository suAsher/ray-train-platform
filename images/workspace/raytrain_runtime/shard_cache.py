"""Bounded, content-addressed Parquet shard cache for two node-local NVMe roots."""

from __future__ import annotations

import contextlib
import fcntl
import hashlib
import os
import pathlib
import re
import stat
import tempfile
import threading
import time
from collections.abc import Iterator, Sequence
from typing import BinaryIO


_POLICIES = frozenset(("off", "auto", "bounded"))
_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_OBJECT_NAME = re.compile(r"^sha256-([0-9a-f]{64})\.parquet$")
_TEMPORARY_NAME = re.compile(r"^\.([0-9a-f]{64})\.[A-Za-z0-9_-]+\.tmp$")
_NAMESPACE = ".raytrain-parquet-shards-v1"
_HIGH_WATERMARK = 0.85
_LOW_WATERMARK = 0.70
_DEFAULT_CHUNK_BYTES = 8 * 1024 * 1024
_MAX_CHUNK_BYTES = 64 * 1024 * 1024
_STALE_TEMPORARY_SECONDS = 60 * 60
_METRIC_NAMES = (
    "hit",
    "miss",
    "download",
    "fallback",
    "checksum_failure",
    "eviction",
    "stale_temp_reclaimed",
    "bytes",
)


class ShardIntegrityError(RuntimeError):
    """The immutable source shard does not match its publication digest."""


class ShardCache:
    """Resolve immutable Parquet shards through a bounded dual-root cache.

    ``off`` performs no cache filesystem operations. Both enabled policies use
    live ``statvfs`` capacity; ``max_bytes``, when provided, is an additional
    per-root ceiling rather than a fixed platform default.

    ``bytes`` in :meth:`metrics_snapshot` is the cumulative number of bytes
    copied successfully into this process's cache. Metrics intentionally carry
    no source or cache paths.
    """

    def __init__(
        self,
        *,
        roots: Sequence[str | os.PathLike[str]],
        policy: str,
        max_bytes: int | None = None,
        chunk_bytes: int = _DEFAULT_CHUNK_BYTES,
    ) -> None:
        if policy not in _POLICIES:
            raise ValueError("dataset cache policy is invalid")
        if isinstance(max_bytes, bool) or (
            max_bytes is not None
            and (not isinstance(max_bytes, int) or max_bytes < 1)
        ):
            raise ValueError("cache max bytes must be a positive integer")
        if (
            isinstance(chunk_bytes, bool)
            or not isinstance(chunk_bytes, int)
            or not 1 <= chunk_bytes <= _MAX_CHUNK_BYTES
        ):
            raise ValueError("cache chunk bytes is invalid")

        self._policy = policy
        self._max_bytes = max_bytes
        self._chunk_bytes = chunk_bytes
        self._metrics = {name: 0 for name in _METRIC_NAMES}
        self._metrics_lock = threading.Lock()
        self._verified: dict[pathlib.Path, tuple[int, int, int, int, int]] = {}
        self._verified_lock = threading.Lock()

        supplied_roots = tuple(pathlib.Path(value) for value in roots)
        if policy == "off":
            self._roots = supplied_roots
            return
        if len(supplied_roots) != 2:
            raise ValueError("enabled cache requires two distinct cache roots")

        normalized = tuple(self._validate_root(root) for root in supplied_roots)
        if (
            normalized[0] == normalized[1]
            or normalized[0].is_relative_to(normalized[1])
            or normalized[1].is_relative_to(normalized[0])
        ):
            raise ValueError("enabled cache requires two distinct cache roots")
        self._roots = normalized

    def resolve(
        self,
        source_path: str | os.PathLike[str],
        digest: str,
    ) -> pathlib.Path:
        """Return a verified cache path, or the source for operational fallback."""

        source = pathlib.Path(source_path)
        self._validate_digest_and_name(source, digest)
        if self._policy == "off":
            return source
        try:
            source_metadata = source.stat()
        except OSError:
            raise RuntimeError("source shard is unavailable") from None
        if not stat.S_ISREG(source_metadata.st_mode):
            raise RuntimeError("source shard is unavailable")

        target = self._target_path(digest)
        lock_path = self._lock_path(digest)
        try:
            self._prepare_directory(target.parent)
            self._prepare_directory(lock_path.parent)
            with self._exclusive_lock(lock_path):
                if self._cached_file_is_valid(target, digest):
                    self._refresh_recency(target)
                    self._increment("hit")
                    return target
                if self._path_exists(target):
                    self._discard_invalid_target(target)
                self._increment("miss")
                root = self._root_for_digest(digest)
                with self._exclusive_lock(self._capacity_lock_path(root)):
                    if not self._ensure_capacity(
                        root=root,
                        incoming_bytes=source_metadata.st_size,
                        protected_digest=digest,
                    ):
                        self._increment("fallback")
                        return source
                    try:
                        copied_bytes = self._copy_verified(source, target, digest)
                    except ShardIntegrityError:
                        self._increment("checksum_failure")
                        raise
                    except (OSError, RuntimeError):
                        self._increment("fallback")
                        return source
                self._remember_verified(target)
                self._increment("download")
                self._increment("bytes", copied_bytes)
                return target
        except ShardIntegrityError:
            raise
        except (OSError, RuntimeError):
            self._increment("fallback")
            return source

    def metrics_snapshot(self) -> dict[str, int]:
        """Return an independent, path-free process-local metrics snapshot."""

        with self._metrics_lock:
            return dict(self._metrics)

    def _validate_root(self, root: pathlib.Path) -> pathlib.Path:
        if not root.is_absolute():
            raise ValueError("cache root is unavailable")
        try:
            if root.is_symlink():
                raise ValueError("cache root is unavailable")
            resolved = root.resolve(strict=True)
            if not resolved.is_dir():
                raise ValueError("cache root is unavailable")
            return resolved
        except OSError:
            raise ValueError("cache root is unavailable") from None

    def _validate_digest_and_name(self, source: pathlib.Path, digest: str) -> None:
        if not isinstance(digest, str) or _DIGEST.fullmatch(digest) is None:
            raise ValueError("shard digest is invalid")
        if source.name != f"sha256-{digest}.parquet":
            raise ValueError("source filename does not contain its shard digest")

    def _root_for_digest(self, digest: str) -> pathlib.Path:
        return self._roots[int(digest[:16], 16) % 2]

    def _namespace(self, root: pathlib.Path) -> pathlib.Path:
        return root / _NAMESPACE

    def _target_path(self, digest: str) -> pathlib.Path:
        root = self._root_for_digest(digest)
        return (
            self._namespace(root)
            / "objects"
            / digest[:2]
            / f"sha256-{digest}.parquet"
        )

    def _lock_path(self, digest: str) -> pathlib.Path:
        root = self._root_for_digest(digest)
        return self._namespace(root) / "locks" / digest[:2] / f"{digest}.lock"

    def _capacity_lock_path(self, root: pathlib.Path) -> pathlib.Path:
        return self._namespace(root) / "locks" / "capacity.lock"

    def _prepare_directory(self, directory: pathlib.Path) -> None:
        root = next(
            (candidate for candidate in self._roots if directory.is_relative_to(candidate)),
            None,
        )
        if root is None:
            raise RuntimeError("cache layout is invalid")
        relative = directory.relative_to(root)
        current = root
        for part in relative.parts:
            current = current / part
            try:
                current.mkdir(mode=0o700)
            except FileExistsError:
                pass
            metadata = current.lstat()
            if not stat.S_ISDIR(metadata.st_mode) or stat.S_ISLNK(metadata.st_mode):
                raise RuntimeError("cache layout is invalid")

    @contextlib.contextmanager
    def _exclusive_lock(self, path: pathlib.Path) -> Iterator[None]:
        descriptor = self._open_lock(path)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            yield
        finally:
            with contextlib.suppress(OSError):
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)

    def _open_lock(self, path: pathlib.Path) -> int:
        flags = os.O_RDWR | os.O_CREAT
        flags |= getattr(os, "O_CLOEXEC", 0)
        flags |= getattr(os, "O_NOFOLLOW", 0)
        return os.open(path, flags, 0o600)

    def _try_lock(self, path: pathlib.Path) -> int | None:
        try:
            descriptor = self._open_lock(path)
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return descriptor
        except (BlockingIOError, OSError):
            with contextlib.suppress(UnboundLocalError, OSError):
                os.close(descriptor)
            return None

    def _unlock(self, descriptor: int) -> None:
        with contextlib.suppress(OSError):
            fcntl.flock(descriptor, fcntl.LOCK_UN)
        os.close(descriptor)

    def _cached_file_is_valid(self, target: pathlib.Path, digest: str) -> bool:
        try:
            metadata = target.lstat()
        except FileNotFoundError:
            return False
        except OSError:
            return False
        if not stat.S_ISREG(metadata.st_mode) or stat.S_ISLNK(metadata.st_mode):
            self._increment("checksum_failure")
            return False
        signature = self._signature(metadata)
        with self._verified_lock:
            if self._verified.get(target) == signature:
                return True
        try:
            actual = self._stream_digest(target)
        except OSError:
            return False
        if actual != digest:
            self._increment("checksum_failure")
            return False
        with self._verified_lock:
            self._verified[target] = signature
        return True

    def _discard_invalid_target(self, target: pathlib.Path) -> None:
        try:
            target.unlink()
        except FileNotFoundError:
            return
        except OSError:
            raise RuntimeError("invalid cached shard cannot be removed") from None
        with self._verified_lock:
            self._verified.pop(target, None)

    def _copy_verified(
        self,
        source: pathlib.Path,
        target: pathlib.Path,
        digest: str,
    ) -> int:
        descriptor, temporary_name = tempfile.mkstemp(
            dir=target.parent,
            prefix=f".{digest}.",
            suffix=".tmp",
        )
        temporary = pathlib.Path(temporary_name)
        copied = 0
        hasher = hashlib.sha256()
        try:
            with source.open("rb") as source_stream, os.fdopen(
                descriptor,
                "wb",
                buffering=0,
            ) as destination:
                descriptor = -1
                while True:
                    block = source_stream.read(self._chunk_bytes)
                    if not block:
                        break
                    hasher.update(block)
                    self._write_all(destination, block)
                    copied += len(block)
                destination.flush()
                os.fsync(destination.fileno())
            if hasher.hexdigest() != digest:
                raise ShardIntegrityError("shard checksum verification failed")
            os.chmod(temporary, 0o440)
            os.replace(temporary, target)
            self._fsync_directory(target.parent)
            return copied
        finally:
            if descriptor >= 0:
                with contextlib.suppress(OSError):
                    os.close(descriptor)
            with contextlib.suppress(FileNotFoundError):
                temporary.unlink()

    @staticmethod
    def _write_all(destination: BinaryIO, block: bytes) -> None:
        remaining = memoryview(block)
        while remaining:
            written = destination.write(remaining)
            if not isinstance(written, int) or written <= 0:
                raise OSError("cache destination write failed")
            remaining = remaining[written:]

    def _stream_digest(self, path: pathlib.Path) -> str:
        hasher = hashlib.sha256()
        with path.open("rb") as stream:
            while True:
                block = stream.read(self._chunk_bytes)
                if not block:
                    break
                hasher.update(block)
        return hasher.hexdigest()

    def _remember_verified(self, target: pathlib.Path) -> None:
        metadata = target.lstat()
        with self._verified_lock:
            self._verified[target] = self._signature(metadata)

    def _refresh_recency(self, target: pathlib.Path) -> None:
        try:
            metadata = target.lstat()
            os.utime(
                target,
                ns=(time.time_ns(), metadata.st_mtime_ns),
                follow_symlinks=False,
            )
            refreshed = target.lstat()
        except OSError:
            return
        with self._verified_lock:
            self._verified[target] = self._signature(refreshed)

    @staticmethod
    def _signature(metadata: os.stat_result) -> tuple[int, int, int, int, int]:
        return (
            metadata.st_dev,
            metadata.st_ino,
            metadata.st_size,
            metadata.st_mtime_ns,
            metadata.st_ctime_ns,
        )

    def _ensure_capacity(
        self,
        *,
        root: pathlib.Path,
        incoming_bytes: int,
        protected_digest: str,
    ) -> bool:
        self._reclaim_stale_temporaries(root)
        candidates = self._cache_candidates(root)
        usage = sum(item[1].st_size for item in candidates)
        budget, available = self._budget(root, usage)
        high = int(budget * _HIGH_WATERMARK)
        low = int(budget * _LOW_WATERMARK)
        if incoming_bytes > high or incoming_bytes > available:
            return False
        if usage + incoming_bytes <= high:
            return True

        desired_usage = max(0, low - incoming_bytes)
        ordered = sorted(
            candidates,
            key=lambda item: (
                max(item[1].st_atime_ns, item[1].st_mtime_ns),
                item[0].name,
            ),
        )
        evicted_parent_directories: set[pathlib.Path] = set()
        for candidate, metadata, candidate_digest in ordered:
            if usage <= desired_usage:
                break
            if candidate_digest == protected_digest:
                continue
            lock_descriptor = self._try_lock(self._lock_path(candidate_digest))
            if lock_descriptor is None:
                continue
            try:
                try:
                    current = candidate.lstat()
                except FileNotFoundError:
                    continue
                if not stat.S_ISREG(current.st_mode) or stat.S_ISLNK(current.st_mode):
                    continue
                candidate.unlink()
                usage -= current.st_size
                evicted_parent_directories.add(candidate.parent)
                with self._verified_lock:
                    self._verified.pop(candidate, None)
                self._increment("eviction")
            except OSError:
                continue
            finally:
                self._unlock(lock_descriptor)
        for directory in evicted_parent_directories:
            with contextlib.suppress(OSError):
                self._fsync_directory(directory)

        _budget, available = self._budget(root, usage)
        return usage + incoming_bytes <= high and incoming_bytes <= available

    def _reclaim_stale_temporaries(self, root: pathlib.Path) -> None:
        objects = self._namespace(root) / "objects"
        cutoff_ns = time.time_ns() - _STALE_TEMPORARY_SECONDS * 1_000_000_000
        try:
            prefixes = tuple(objects.iterdir())
        except FileNotFoundError:
            return
        except OSError:
            raise RuntimeError("cache usage is unavailable") from None
        changed: set[pathlib.Path] = set()
        for prefix in prefixes:
            try:
                metadata = prefix.lstat()
            except OSError:
                continue
            if (
                not stat.S_ISDIR(metadata.st_mode)
                or stat.S_ISLNK(metadata.st_mode)
                or re.fullmatch(r"[0-9a-f]{2}", prefix.name) is None
            ):
                continue
            try:
                children = tuple(prefix.iterdir())
            except OSError:
                continue
            for child in children:
                matched = _TEMPORARY_NAME.fullmatch(child.name)
                if matched is None or matched.group(1)[:2] != prefix.name:
                    continue
                try:
                    child_metadata = child.lstat()
                    if (
                        not stat.S_ISREG(child_metadata.st_mode)
                        or stat.S_ISLNK(child_metadata.st_mode)
                        or child_metadata.st_mtime_ns > cutoff_ns
                    ):
                        continue
                    child.unlink()
                    changed.add(prefix)
                    self._increment("stale_temp_reclaimed")
                except OSError:
                    continue
        for directory in changed:
            with contextlib.suppress(OSError):
                self._fsync_directory(directory)

    def _cache_candidates(
        self,
        root: pathlib.Path,
    ) -> list[tuple[pathlib.Path, os.stat_result, str]]:
        objects = self._namespace(root) / "objects"
        try:
            prefixes = tuple(objects.iterdir())
        except FileNotFoundError:
            return []
        except OSError:
            raise RuntimeError("cache usage is unavailable") from None
        candidates = []
        for prefix in prefixes:
            try:
                prefix_metadata = prefix.lstat()
            except OSError:
                continue
            if (
                not stat.S_ISDIR(prefix_metadata.st_mode)
                or stat.S_ISLNK(prefix_metadata.st_mode)
                or re.fullmatch(r"[0-9a-f]{2}", prefix.name) is None
            ):
                continue
            try:
                children = tuple(prefix.iterdir())
            except OSError:
                continue
            for child in children:
                matched = _OBJECT_NAME.fullmatch(child.name)
                if matched is None or matched.group(1)[:2] != prefix.name:
                    continue
                try:
                    metadata = child.lstat()
                except OSError:
                    continue
                if stat.S_ISREG(metadata.st_mode) and not stat.S_ISLNK(metadata.st_mode):
                    candidates.append((child, metadata, matched.group(1)))
        return candidates

    def _budget(self, root: pathlib.Path, usage: int) -> tuple[int, int]:
        try:
            filesystem = os.statvfs(root)
            fragment = filesystem.f_frsize or filesystem.f_bsize
            available = max(0, int(fragment) * int(filesystem.f_bavail))
        except (AttributeError, OSError, TypeError, ValueError, OverflowError):
            raise RuntimeError("cache capacity is unavailable") from None
        budget = usage + available
        if self._max_bytes is not None:
            budget = min(budget, self._max_bytes)
        return max(0, budget), available

    @staticmethod
    def _path_exists(path: pathlib.Path) -> bool:
        try:
            path.lstat()
            return True
        except FileNotFoundError:
            return False

    @staticmethod
    def _fsync_directory(directory: pathlib.Path) -> None:
        flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0)
        descriptor = os.open(directory, flags)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def _increment(self, name: str, amount: int = 1) -> None:
        with self._metrics_lock:
            self._metrics[name] += amount
