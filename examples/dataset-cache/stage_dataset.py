#!/usr/bin/env python3
"""Atomically shard a selected read-only dataset across node-local caches."""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Iterable, Sequence


@dataclass(frozen=True)
class RootStageResult:
    path: str
    files: int
    bytes: int


@dataclass(frozen=True)
class StageResult:
    path: Path
    copied: bool
    files: int
    bytes: int
    seconds: float
    roots: tuple[RootStageResult, ...]


def _is_within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def cache_root_index(relative_path: Path, root_count: int) -> int:
    """Return a stable shard for a safe dataset-relative path."""
    if root_count <= 0:
        raise ValueError("root_count must be positive")
    if relative_path.is_absolute() or ".." in relative_path.parts:
        raise ValueError("dataset path must be safe and relative")
    digest = hashlib.sha256(relative_path.as_posix().encode("utf-8")).digest()
    return int.from_bytes(digest[:8], "big") % root_count


def _validated_roots(source: Path, cache_roots: Sequence[Path]) -> tuple[Path, ...]:
    roots = tuple(Path(root).resolve() for root in cache_roots)
    if not roots:
        raise ValueError("at least one cache root is required")
    if len(set(roots)) != len(roots):
        raise ValueError("cache roots must not overlap")
    for root in roots:
        if _is_within(root, source):
            raise ValueError("cache root must not be inside the source dataset")
    for index, left in enumerate(roots):
        for right in roots[index + 1 :]:
            if _is_within(left, right) or _is_within(right, left):
                raise ValueError("cache roots must not overlap")
    return roots


def _source_files(source: Path) -> Iterable[tuple[Path, Path]]:
    for path in sorted(source.rglob("*"), key=lambda item: item.as_posix()):
        if path.is_symlink():
            raise ValueError(f"source dataset contains a symbolic link: {path}")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError(f"source dataset contains an unsupported entry: {path}")
        relative = path.relative_to(source)
        if relative.is_absolute() or ".." in relative.parts:
            raise ValueError(f"source dataset contains an unsafe path: {path}")
        yield path, relative


def _copy_file(source: Path, destination: Path) -> int:
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.tmp-{os.getpid()}")
    shutil.copyfile(source, temporary)
    temporary.replace(destination)
    return destination.stat().st_size


def _result_payload(result: StageResult) -> dict:
    return {
        "path": str(result.path),
        "copied": result.copied,
        "files": result.files,
        "bytes": result.bytes,
        "seconds": result.seconds,
        "roots": [asdict(root) for root in result.roots],
    }


def stage_selected_dataset(
    source_root: Path,
    cache_roots: Sequence[Path],
    local_rank: int,
    timeout_seconds: int,
) -> StageResult:
    source = source_root.resolve()
    if not source.is_dir():
        raise FileNotFoundError(f"selected dataset does not exist: {source}")
    roots = _validated_roots(source, cache_roots)
    if timeout_seconds <= 0:
        raise ValueError("timeout_seconds must be positive")

    for root in roots:
        root.mkdir(parents=True, exist_ok=True)
        (root / "data").mkdir(parents=True, exist_ok=True)
    view = roots[0] / "dataset-view"
    ready = roots[0] / ".dataset.ready"

    if local_rank == 0 and not ready.exists():
        temporary_view = roots[0] / f".dataset-view.tmp-{os.getpid()}"
        if temporary_view.exists():
            shutil.rmtree(temporary_view)
        if view.exists():
            shutil.rmtree(view)
        temporary_view.mkdir(parents=True)
        started = time.perf_counter()
        root_files = [0 for _ in roots]
        root_bytes = [0 for _ in roots]
        for source_file, relative in _source_files(source):
            index = cache_root_index(relative, len(roots))
            destination = roots[index] / "data" / relative
            copied_bytes = _copy_file(source_file, destination)
            root_files[index] += 1
            root_bytes[index] += copied_bytes
            link = temporary_view / relative
            link.parent.mkdir(parents=True, exist_ok=True)
            link.symlink_to(destination)
        temporary_view.replace(view)
        elapsed = max(time.perf_counter() - started, 1e-9)
        result = StageResult(
            path=view,
            copied=True,
            files=sum(root_files),
            bytes=sum(root_bytes),
            seconds=round(elapsed, 6),
            roots=tuple(
                RootStageResult(path=str(root), files=root_files[index], bytes=root_bytes[index])
                for index, root in enumerate(roots)
            ),
        )
        temporary_ready = ready.with_name(f".{ready.name}.tmp-{os.getpid()}")
        temporary_ready.write_text(json.dumps(_result_payload(result)) + "\n", encoding="utf-8")
        temporary_ready.replace(ready)
        return result

    deadline = time.monotonic() + timeout_seconds
    while not ready.is_file():
        if time.monotonic() >= deadline:
            raise TimeoutError(f"dataset cache warmup timed out: {view}")
        time.sleep(1)
    if not view.is_dir():
        raise RuntimeError(f"dataset cache ready marker exists without dataset: {view}")
    payload = json.loads(ready.read_text(encoding="utf-8"))
    return StageResult(
        path=view,
        copied=False,
        files=int(payload.get("files", 0)),
        bytes=int(payload.get("bytes", 0)),
        seconds=float(payload.get("seconds", 0.0)),
        roots=tuple(RootStageResult(**entry) for entry in payload.get("roots", [])),
    )


def main() -> int:
    source_value = os.environ.get("PLATFORM_DATASET_PATH", "").strip()
    if not source_value:
        raise RuntimeError("PLATFORM_DATASET_PATH is required")
    paths_value = os.environ.get("PLATFORM_CACHE_PATHS", "").strip()
    if not paths_value:
        paths_value = os.environ.get("PLATFORM_CACHE_PATH", "").strip()
    if not paths_value:
        print("RAYTRAIN_DATASET_CACHE=disabled", file=sys.stderr, flush=True)
        print(Path(source_value).resolve())
        return 0

    result = stage_selected_dataset(
        source_root=Path(source_value),
        cache_roots=[Path(item) for item in paths_value.split(":") if item],
        local_rank=int(os.environ.get("LOCAL_RANK", "0")),
        timeout_seconds=int(os.environ.get("PLATFORM_CACHE_STAGE_TIMEOUT", "7200")),
    )
    print(
        "RAYTRAIN_DATASET_CACHE=" + json.dumps(_result_payload(result), ensure_ascii=False),
        file=sys.stderr,
        flush=True,
    )
    print(result.path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
