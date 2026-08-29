"""Validated, opt-in Ray Data adapter for managed Ray Train jobs."""

from __future__ import annotations

import dataclasses
import concurrent.futures
import hashlib
import os
import pathlib
import posixpath
import shutil
import sys
import time
import unicodedata
import urllib.parse
from typing import Any


_STABLE_INPUT_ROOT = "/mnt/data/input"
_SUPPORTED_FORMATS = frozenset(("parquet", "images", "files"))


@dataclasses.dataclass(frozen=True)
class DatasetConfig:
    """Immutable registered-dataset reference below the governed input mount."""

    format: str
    uri: str

    def __post_init__(self) -> None:
        if self.format not in _SUPPORTED_FORMATS:
            raise ValueError(f"unsupported Ray Data format: {self.format}")
        if not self.uri or any(
            unicodedata.category(character) == "Cc" for character in self.uri
        ):
            raise ValueError(
                "Ray Data URI must not be empty or contain control characters"
            )
        if "\\" in self.uri:
            raise ValueError("Ray Data URI must use POSIX separators")
        parsed = urllib.parse.urlsplit(self.uri)
        if parsed.scheme or parsed.netloc or parsed.username or parsed.password:
            raise ValueError("Ray Data URI must not contain a scheme or credentials")
        if parsed.query or parsed.fragment:
            raise ValueError("Ray Data URI must not contain a query or fragment")
        prefix = _STABLE_INPUT_ROOT + "/"
        if self.uri != _STABLE_INPUT_ROOT and not self.uri.startswith(prefix):
            raise ValueError(f"Ray Data URI must stay below {_STABLE_INPUT_ROOT}")
        relative = "." if self.uri == _STABLE_INPUT_ROOT else self.uri[len(prefix) :]
        if any(segment == ".." for segment in relative.split("/")) or (
            relative != "." and any(segment == "." for segment in relative.split("/"))
        ):
            raise ValueError("Ray Data URI must not contain traversal segments")
        if (relative == "." and self.format != "files") or posixpath.normpath(self.uri) != self.uri:
            raise ValueError("Ray Data URI must be a canonical mounted path")


def build_dataset(config: DatasetConfig) -> Any:
    """Build the registered Ray Dataset without loading Ray Data at import time."""

    from ray import data

    if config.format == "parquet":
        return data.read_parquet(config.uri)
    if config.format == "images":
        return data.read_images(config.uri, include_paths=True)
    if config.format == "files":
        return data.read_binary_files(config.uri, include_paths=True)
    raise ValueError(f"unsupported Ray Data format: {config.format}")


def worker_iterator(name: str = "train") -> Any:
    """Return prefetched Torch batches from a named Ray Train dataset shard."""

    from ray import train

    shard = train.get_dataset_shard(name)
    if shard is None:
        raise RuntimeError(f"Ray Data shard {name!r} is unavailable")
    return shard.iter_torch_batches(prefetch_batches=2)


@dataclasses.dataclass(frozen=True)
class StageSummary:
    path: pathlib.Path
    files: int
    bytes: int
    seconds: float
    reused: bool


def _relative_file(path_value: str, source_root: pathlib.Path) -> pathlib.Path:
    candidate = pathlib.Path(path_value).resolve(strict=False)
    try:
        relative = candidate.relative_to(source_root)
    except ValueError as exc:
        raise ValueError(f"Ray Data file escaped selected input: {path_value}") from exc
    if not relative.parts or relative.is_absolute() or ".." in relative.parts:
        raise ValueError(f"Ray Data file path is unsafe: {path_value}")
    return relative


def _cache_index(relative: pathlib.Path, root_count: int) -> int:
    digest = hashlib.sha256(relative.as_posix().encode("utf-8")).digest()
    return int.from_bytes(digest[:8], "big") % root_count


def _write_staged_file(
    relative: pathlib.Path,
    payload: bytes,
    cache_roots: tuple[pathlib.Path, ...],
    temporary_view: pathlib.Path,
) -> int:
    root = cache_roots[_cache_index(relative, len(cache_roots))]
    destination = root / "data" / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.{os.getpid()}.{time.time_ns()}.tmp")
    try:
        with temporary.open("xb") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
    finally:
        temporary.unlink(missing_ok=True)
    link = temporary_view / relative
    link.parent.mkdir(parents=True, exist_ok=True)
    link.symlink_to(destination)
    return len(payload)


def stage_binary_dataset(
    iterator: Any,
    *,
    source_root: str,
    cache_paths: str,
    copy_workers: int = 64,
) -> StageSummary:
    """Stream one full Ray Dataset into a node's two ephemeral NVMe volumes."""

    source = pathlib.Path(source_root).resolve(strict=False)
    roots = tuple(
        pathlib.Path(value).resolve(strict=False)
        for value in cache_paths.split(":")
        if value.strip()
    )
    if not roots or len(set(roots)) != len(roots):
        raise ValueError("Ray Data staging requires distinct cache paths")
    if copy_workers < 1 or copy_workers > 128:
        raise ValueError("Ray Data staging workers must be between 1 and 128")
    for root in roots:
        root.mkdir(parents=True, exist_ok=True)
        (root / "data").mkdir(parents=True, exist_ok=True)

    view = roots[0] / "dataset-view"
    marker = roots[0] / ".ray-data-stage.ready"
    if marker.is_file() and view.is_dir():
        values = marker.read_text(encoding="utf-8").strip().split("\t")
        if len(values) == 2:
            return StageSummary(view, int(values[0]), int(values[1]), 0.0, True)

    temporary_view = roots[0] / f".dataset-view.ray-data.{os.getpid()}.tmp"
    if temporary_view.exists():
        shutil.rmtree(temporary_view)
    temporary_view.mkdir(parents=True)
    started = time.perf_counter()
    files = 0
    byte_count = 0
    pending: set[concurrent.futures.Future[int]] = set()

    def collect(completed: set[concurrent.futures.Future[int]]) -> None:
        nonlocal files, byte_count
        for future in completed:
            byte_count += future.result()
            files += 1
            if files % 5000 == 0:
                print(
                    f"RAYTRAIN_RAY_DATA_STAGE_PROGRESS files={files} bytes={byte_count}",
                    file=sys.stderr,
                    flush=True,
                )

    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=copy_workers) as executor:
            for row in iterator.iter_rows():
                path_value = str(row["path"])
                payload = row["bytes"]
                if not isinstance(payload, (bytes, bytearray, memoryview)):
                    raise ValueError(f"Ray Data file payload is not binary: {path_value}")
                relative = _relative_file(path_value, source)
                pending.add(
                    executor.submit(
                        _write_staged_file,
                        relative,
                        bytes(payload),
                        roots,
                        temporary_view,
                    )
                )
                if len(pending) >= copy_workers * 4:
                    completed, pending = concurrent.futures.wait(
                        pending, return_when=concurrent.futures.FIRST_COMPLETED
                    )
                    collect(completed)
            if pending:
                completed, _ = concurrent.futures.wait(pending)
                collect(completed)
        if files == 0:
            raise ValueError("selected Ray Data staging dataset is empty")
        if view.exists():
            shutil.rmtree(view)
        os.replace(temporary_view, view)
        marker.write_text(f"{files}\t{byte_count}\n", encoding="utf-8")
    finally:
        if temporary_view.exists():
            shutil.rmtree(temporary_view)
    seconds = max(time.perf_counter() - started, 1e-9)
    print(
        f"RAYTRAIN_RAY_DATA_STAGE_COMPLETE files={files} bytes={byte_count} seconds={seconds:.6f}",
        file=sys.stderr,
        flush=True,
    )
    return StageSummary(view, files, byte_count, seconds, False)
