#!/usr/bin/env python3
"""Atomically shard a selected read-only dataset across node-local caches."""

from __future__ import annotations

import concurrent.futures
import hashlib
import json
import os
import re
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


@dataclass(frozen=True)
class MetricIdentity:
    job_id: str
    namespace: str
    ray_cluster: str
    node_type: str
    pod: str


_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_DNS_VALUE = re.compile(r"^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$")
_METRIC_KEYS = (
    "version", "platform_job_id", "exported_namespace", "ray_io_cluster",
    "ray_io_node_type", "pod", "bytes", "files", "seconds", "copied", "hits", "misses",
)


def _validate_metric_identity(identity: MetricIdentity) -> None:
    if not _IDENTIFIER.fullmatch(identity.job_id):
        raise ValueError("job metric label is invalid")
    for value in (identity.namespace, identity.ray_cluster, identity.pod):
        if len(value) > 253 or not _DNS_VALUE.fullmatch(value):
            raise ValueError("Kubernetes metric label is invalid")
    if identity.node_type != "worker":
        raise ValueError("worker metric label is invalid")


def _read_metric_counts(path: Path, identity: MetricIdentity) -> tuple[int, int]:
    if not path.exists():
        return 0, 0
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 4096:
        return 0, 0
    values: dict[str, str] = {}
    try:
        for line in path.read_text(encoding="utf-8").splitlines():
            key, separator, value = line.partition("=")
            if not separator or key in values or key not in _METRIC_KEYS:
                return 0, 0
            values[key] = value
    except OSError:
        return 0, 0
    expected = {
        "platform_job_id": identity.job_id,
        "exported_namespace": identity.namespace,
        "ray_io_cluster": identity.ray_cluster,
        "ray_io_node_type": identity.node_type,
        "pod": identity.pod,
    }
    if set(values) != set(_METRIC_KEYS) or values.get("version") != "1" or any(values.get(key) != value for key, value in expected.items()):
        return 0, 0
    try:
        hits, misses = int(values["hits"]), int(values["misses"])
    except ValueError:
        return 0, 0
    return (hits, misses) if 0 <= hits <= 1_000_000_000 and 0 <= misses <= 1_000_000_000 else (0, 0)


def write_preload_metrics(cache_roots: Sequence[Path], result: StageResult, identity: MetricIdentity) -> None:
    _validate_metric_identity(identity)
    by_root = {str(Path(item.path).resolve()): item for item in result.roots}
    for raw_root in cache_roots:
        root = Path(raw_root).resolve()
        metric_dir = root / ".ray-cache-metrics"
        if metric_dir.is_symlink():
            raise ValueError("cache metric directory must not be a symbolic link")
        metric_dir.mkdir(mode=0o755, exist_ok=True)
        if metric_dir.is_symlink() or not metric_dir.is_dir():
            raise ValueError("cache metric directory must be a real directory")
        metric_path = metric_dir / "preload.metrics"
        hits, misses = _read_metric_counts(metric_path, identity)
        hits += 0 if result.copied else 1
        misses += 1 if result.copied else 0
        root_result = by_root.get(str(root), RootStageResult(path=str(root), files=0, bytes=0))
        values = {
            "version": "1", "platform_job_id": identity.job_id,
            "exported_namespace": identity.namespace, "ray_io_cluster": identity.ray_cluster,
            "ray_io_node_type": identity.node_type, "pod": identity.pod,
            "bytes": str(max(root_result.bytes, 0)), "files": str(max(root_result.files, 0)),
            "seconds": format(max(result.seconds, 0.0), ".6f"),
            "copied": "1" if result.copied else "0",
            "hits": str(hits), "misses": str(misses),
        }
        encoded = "".join(f"{key}={values[key]}\n" for key in _METRIC_KEYS)
        temporary = metric_path.with_name(f".preload.{os.getpid()}.{time.time_ns()}.tmp")
        try:
            with temporary.open("x", encoding="utf-8") as stream:
                stream.write(encoded)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, metric_path)
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass


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
    copy_workers: int = 32,
    max_bytes_per_root: int = 0,
) -> StageResult:
    source = source_root.resolve()
    if not source.is_dir():
        raise FileNotFoundError(f"selected dataset does not exist: {source}")
    roots = _validated_roots(source, cache_roots)
    if timeout_seconds <= 0:
        raise ValueError("timeout_seconds must be positive")
    if copy_workers <= 0 or copy_workers > 128:
        raise ValueError("copy_workers must be between 1 and 128")
    if max_bytes_per_root < 0:
        raise ValueError("max_bytes_per_root must not be negative")

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
        planned_bytes = [0 for _ in roots]
        planned_files = 0
        for source_file, relative in _source_files(source):
            index = cache_root_index(relative, len(roots))
            planned_bytes[index] += source_file.stat().st_size
            planned_files += 1
        if planned_files == 0:
            raise ValueError("selected dataset is empty")
        for index, planned_size in enumerate(planned_bytes):
            if max_bytes_per_root and planned_size > max_bytes_per_root:
                raise ValueError(
                    f"selected dataset exceeds cache capacity on {roots[index]}: "
                    f"planned={planned_size} limit={max_bytes_per_root}"
                )
            available = shutil.disk_usage(roots[index]).free
            if planned_size > available:
                raise ValueError(
                    f"selected dataset exceeds cache capacity on {roots[index]}: "
                    f"planned={planned_size} available={available}"
                )

        def copy_planned(item: tuple[Path, Path]) -> tuple[int, int]:
            source_file, relative = item
            index = cache_root_index(relative, len(roots))
            destination = roots[index] / "data" / relative
            copied_bytes = _copy_file(source_file, destination)
            link = temporary_view / relative
            link.parent.mkdir(parents=True, exist_ok=True)
            link.symlink_to(destination)
            return index, copied_bytes

        with concurrent.futures.ThreadPoolExecutor(max_workers=copy_workers) as executor:
            items = iter(_source_files(source))
            pending: set[concurrent.futures.Future[tuple[int, int]]] = set()
            for _ in range(copy_workers * 4):
                item = next(items, None)
                if item is None:
                    break
                pending.add(executor.submit(copy_planned, item))

            completed = 0
            while pending:
                finished, pending = concurrent.futures.wait(
                    pending, return_when=concurrent.futures.FIRST_COMPLETED
                )
                for future in finished:
                    index, copied_bytes = future.result()
                    root_files[index] += 1
                    root_bytes[index] += copied_bytes
                    completed += 1
                    if completed % 5000 == 0 or completed == planned_files:
                        print(
                            f"RAYTRAIN_DATASET_CACHE_PROGRESS files={completed}/{planned_files} "
                            f"bytes={sum(root_bytes)}",
                            file=sys.stderr,
                            flush=True,
                        )
                    item = next(items, None)
                    if item is not None:
                        pending.add(executor.submit(copy_planned, item))
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

    reuse_started = time.perf_counter()
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
        seconds=round(max(time.perf_counter() - reuse_started, 1e-9), 6),
        roots=tuple(RootStageResult(**entry) for entry in payload.get("roots", [])),
    )


def main() -> int:
    source_value = os.environ.get("PLATFORM_DATASET_SOURCE_PATH", "").strip()
    if not source_value:
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
        copy_workers=int(os.environ.get("PLATFORM_CACHE_COPY_WORKERS", "32")),
        max_bytes_per_root=int(os.environ.get("PLATFORM_CACHE_LIMIT_BYTES_PER_DISK", "0")),
    )
    identity_values = {
        "job_id": os.environ.get("PLATFORM_JOB_ID", "").strip(),
        "namespace": os.environ.get("PLATFORM_POD_NAMESPACE", "").strip(),
        "ray_cluster": os.environ.get("PLATFORM_RAY_CLUSTER", "").strip(),
        "node_type": os.environ.get("PLATFORM_RAY_NODE_TYPE", "").strip(),
        "pod": os.environ.get("PLATFORM_POD_NAME", "").strip(),
    }
    if any(identity_values.values()):
        if not all(identity_values.values()):
            raise ValueError("cache metric identity is incomplete")
        write_preload_metrics(
            [Path(item) for item in paths_value.split(":") if item],
            result,
            MetricIdentity(**identity_values),
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
