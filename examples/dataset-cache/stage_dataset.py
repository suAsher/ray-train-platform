#!/usr/bin/env python3
"""Atomically stage the task-selected read-only dataset into node-local cache."""

from __future__ import annotations

import json
import os
import shutil
import sys
import time
from dataclasses import asdict, dataclass
from pathlib import Path


@dataclass(frozen=True)
class StageResult:
    path: Path
    copied: bool
    files: int
    bytes: int
    seconds: float


def _is_within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def _copy_dataset(source: Path, destination: Path) -> tuple[int, int]:
    shutil.copytree(source, destination, copy_function=shutil.copyfile)
    files = 0
    total_bytes = 0
    for current, _, names in os.walk(destination, followlinks=False):
        for name in names:
            path = Path(current) / name
            if path.is_file() and not path.is_symlink():
                files += 1
                total_bytes += path.stat().st_size
    return files, total_bytes


def stage_selected_dataset(
    source_root: Path,
    cache_root: Path,
    local_rank: int,
    timeout_seconds: int,
) -> StageResult:
    source = source_root.resolve()
    cache = cache_root.resolve()
    if not source.is_dir():
        raise FileNotFoundError(f"selected dataset does not exist: {source}")
    if _is_within(cache, source):
        raise ValueError("cache root must not be inside the source dataset")
    if timeout_seconds <= 0:
        raise ValueError("timeout_seconds must be positive")

    cache.mkdir(parents=True, exist_ok=True)
    destination = cache / "dataset"
    ready = cache / ".dataset.ready"

    if local_rank == 0 and not ready.exists():
        temporary = cache / f".dataset.tmp-{os.getpid()}"
        if temporary.exists():
            shutil.rmtree(temporary)
        if destination.exists():
            shutil.rmtree(destination)
        started = time.perf_counter()
        files, total_bytes = _copy_dataset(source, temporary)
        temporary.replace(destination)
        elapsed = max(time.perf_counter() - started, 1e-9)
        result = StageResult(
            path=destination,
            copied=True,
            files=files,
            bytes=total_bytes,
            seconds=round(elapsed, 6),
        )
        ready.write_text(json.dumps({**asdict(result), "path": str(result.path)}) + "\n", encoding="utf-8")
        return result

    deadline = time.monotonic() + timeout_seconds
    while not ready.is_file():
        if time.monotonic() >= deadline:
            raise TimeoutError(f"dataset cache warmup timed out: {destination}")
        time.sleep(1)
    if not destination.is_dir():
        raise RuntimeError(f"dataset cache ready marker exists without dataset: {destination}")
    payload = json.loads(ready.read_text(encoding="utf-8"))
    return StageResult(
        path=destination,
        copied=False,
        files=int(payload.get("files", 0)),
        bytes=int(payload.get("bytes", 0)),
        seconds=float(payload.get("seconds", 0.0)),
    )


def main() -> int:
    source_value = os.environ.get("PLATFORM_DATASET_PATH", "").strip()
    if not source_value:
        raise RuntimeError("PLATFORM_DATASET_PATH is required")
    cache_value = os.environ.get("PLATFORM_CACHE_PATH", "").strip()
    if not cache_value:
        print("RAYTRAIN_DATASET_CACHE=disabled", file=sys.stderr, flush=True)
        print(Path(source_value).resolve())
        return 0

    result = stage_selected_dataset(
        source_root=Path(source_value),
        cache_root=Path(cache_value),
        local_rank=int(os.environ.get("LOCAL_RANK", "0")),
        timeout_seconds=int(os.environ.get("PLATFORM_CACHE_STAGE_TIMEOUT", "7200")),
    )
    print(
        "RAYTRAIN_DATASET_CACHE="
        + json.dumps({**asdict(result), "path": str(result.path)}, ensure_ascii=False),
        file=sys.stderr,
        flush=True,
    )
    print(result.path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
