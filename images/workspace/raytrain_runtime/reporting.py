"""Framework-neutral metrics and complete-checkpoint reporting for Ray Train."""

from __future__ import annotations

import copy
import hashlib
import json
import math
import numbers
import os
import re
import threading
from pathlib import Path, PurePosixPath
from collections.abc import Mapping
import shutil
from typing import Any
import uuid


MANIFEST_NAME = "manifest.json"
MANIFEST_VERSION = 1
RETENTION_INDEX_NAME = "retention-index.json"
RETENTION_INDEX_VERSION = 1

_GAUGE_TAG_KEYS = (
    "platform_job_id",
    "exported_namespace",
    "ray_io_cluster",
    "ray_io_node_type",
    "pod",
    "rank",
    "gpu",
    "dataset_id",
    "dataset_version_id",
    "ray_version",
    "data_mode",
    "cache_policy",
)
_CUSTOM_METRIC_NAMES = {
    "step": "platform_training_step",
    "time": "platform_training_step_time_seconds",
    "step_time": "platform_training_step_time_seconds",
    "data_time": "platform_training_data_time_seconds",
    "nccl_time": "platform_training_nccl_duration_seconds",
    "nccl_duration": "platform_training_nccl_duration_seconds",
    "dataset_batches_total": "platform_training_dataset_batches_total",
    "dataset_samples_total": "platform_training_dataset_samples_total",
    "dataset_shard_reads_total": "platform_training_dataset_shard_reads_total",
    "dataset_source_reads_total": "platform_training_dataset_source_reads_total",
    "dataset_cache_reads_total": "platform_training_dataset_cache_reads_total",
    "dataset_source_read_seconds_total": "platform_training_dataset_source_read_seconds_total",
    "dataset_cache_read_seconds_total": "platform_training_dataset_cache_read_seconds_total",
    "dataset_prefetch_wait_seconds_total": "platform_training_dataset_prefetch_wait_seconds_total",
    "dataset_cache_hits_total": "platform_training_dataset_cache_hits_total",
    "dataset_cache_misses_total": "platform_training_dataset_cache_misses_total",
    "dataset_cache_downloads_total": "platform_training_dataset_cache_downloads_total",
    "dataset_cache_fallbacks_total": "platform_training_dataset_cache_fallbacks_total",
    "dataset_cache_checksum_failures_total": "platform_training_dataset_cache_checksum_failures_total",
    "dataset_cache_evictions_total": "platform_training_dataset_cache_evictions_total",
    "dataset_cache_stale_temp_reclaimed_total": "platform_training_dataset_cache_stale_temp_reclaimed_total",
    "dataset_cache_bytes_total": "platform_training_dataset_cache_bytes_total",
}
_MLFLOW_DATA_METRICS = {
    name: "rank0_worker_" + name
    for name in _CUSTOM_METRIC_NAMES
    if name.startswith("dataset_")
}
_CUSTOM_GAUGES: dict[str, Any] = {}
_CUSTOM_GAUGES_LOCK = threading.Lock()
_SAFE_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_SAFE_DNS_VALUE = re.compile(r"^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$")
_SAFE_GPU_IDENTITY = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")


def _scalar(value: Any) -> float | None:
    """Return one finite numeric scalar without accepting bools or containers."""

    if isinstance(value, bool):
        return None
    candidate = value
    if not isinstance(candidate, numbers.Real):
        item = getattr(candidate, "item", None)
        if not callable(item):
            return None
        try:
            candidate = item()
        except (TypeError, ValueError):
            return None
        if isinstance(candidate, bool) or not isinstance(candidate, numbers.Real):
            return None
    converted = float(candidate)
    return converted if math.isfinite(converted) else None


def sanitize_metrics(
    metrics: Mapping[Any, Any], *, reject_invalid: bool = False
) -> dict[str, float]:
    """Copy finite scalar metrics into a plain mapping.

    Framework log buffers often contain tensors, strings, arrays, and NaN
    placeholders. Callers may omit those values or request strict rejection;
    the source mapping and its values are never modified.
    """

    clean: dict[str, float] = {}
    for key, value in dict(metrics).items():
        scalar = _scalar(value)
        if scalar is None:
            if reject_invalid:
                raise ValueError(f"metric {key!r} must be a finite scalar")
            continue
        clean[str(key)] = scalar
    return clean


def _sha256_stable_file(path: Path) -> tuple[int, str]:
    before = os.stat(path, follow_symlinks=False)
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    after = os.stat(path, follow_symlinks=False)
    stable_fields = ("st_dev", "st_ino", "st_size", "st_mtime_ns")
    if any(getattr(before, field) != getattr(after, field) for field in stable_fields):
        raise ValueError(f"checkpoint file changed while hashing: {path.name}")
    return after.st_size, digest.hexdigest()


def _checkpoint_files(root: Path) -> list[dict[str, Any]]:
    resolved_root = root.resolve(strict=True)
    entries: list[dict[str, Any]] = []
    for path in sorted(root.rglob("*"), key=lambda item: item.as_posix()):
        relative = path.relative_to(root)
        if relative.as_posix() == MANIFEST_NAME or (
            relative.parent == Path(".")
            and relative.name.startswith(".manifest.")
            and relative.name.endswith(".tmp")
        ):
            continue
        if path.is_symlink():
            raise ValueError(f"checkpoint must not contain symlink: {relative.as_posix()}")
        resolved = path.resolve(strict=True)
        try:
            resolved.relative_to(resolved_root)
        except ValueError as exc:
            raise ValueError(
                f"checkpoint path escapes root: {relative.as_posix()}"
            ) from exc
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError(f"checkpoint contains unsupported entry: {relative.as_posix()}")
        size, digest = _sha256_stable_file(path)
        entries.append(
            {"path": relative.as_posix(), "size": size, "sha256": digest}
        )
    if not entries:
        raise ValueError("checkpoint must contain at least one finalized file")
    return entries


def _atomic_json(path: Path, payload: Mapping[str, Any]) -> None:
    encoded = (
        json.dumps(
            payload,
            allow_nan=False,
            sort_keys=True,
            separators=(",", ":"),
        )
        + "\n"
    ).encode("utf-8")
    temporary_prefix = ".manifest" if path.name == MANIFEST_NAME else f".{path.name}"
    temporary = path.with_name(f"{temporary_prefix}.{uuid.uuid4().hex}.tmp")
    try:
        with temporary.open("xb") as stream:
            stream.write(encoded)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def finalize_checkpoint(
    checkpoint_dir: str | os.PathLike[str], metadata: Mapping[str, Any]
) -> dict[str, Any]:
    """Write the completion manifest only after every checkpoint file is stable."""

    root = Path(checkpoint_dir)
    if not root.is_dir() or root.is_symlink():
        raise ValueError("checkpoint directory must be a real directory")
    manifest = {
        "version": MANIFEST_VERSION,
        "complete": True,
        "metadata": copy.deepcopy(dict(metadata)),
        "files": _checkpoint_files(root),
    }
    _atomic_json(root / MANIFEST_NAME, manifest)
    return copy.deepcopy(manifest)


def _manifest_relative_path(value: Any) -> PurePosixPath:
    if not isinstance(value, str):
        raise ValueError("checkpoint manifest file path must be a string")
    relative = PurePosixPath(value)
    if relative.is_absolute() or not relative.parts or ".." in relative.parts:
        raise ValueError("checkpoint manifest contains an unsafe relative path")
    return relative


def validate_checkpoint(checkpoint_dir: str | os.PathLike[str]) -> dict[str, Any]:
    """Verify completion, containment, size, and digest before reporting."""

    root = Path(checkpoint_dir)
    manifest_path = root / MANIFEST_NAME
    if root.is_symlink() or manifest_path.is_symlink() or not manifest_path.is_file():
        raise ValueError("checkpoint does not have a complete manifest")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError("checkpoint manifest is not readable") from exc
    if manifest.get("complete") is not True:
        raise ValueError("checkpoint manifest is not complete")
    if manifest.get("version") != MANIFEST_VERSION or not isinstance(
        manifest.get("files"), list
    ):
        raise ValueError("checkpoint manifest has an unsupported format")

    resolved_root = root.resolve(strict=True)
    expected_paths: set[str] = set()
    for item in manifest["files"]:
        if not isinstance(item, dict):
            raise ValueError("checkpoint manifest file entry is invalid")
        relative = _manifest_relative_path(item.get("path"))
        relative_text = relative.as_posix()
        if relative_text in expected_paths:
            raise ValueError("checkpoint manifest contains duplicate files")
        expected_paths.add(relative_text)
        path = root.joinpath(*relative.parts)
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"checkpoint integrity failure: {relative_text}")
        try:
            path.resolve(strict=True).relative_to(resolved_root)
        except ValueError as exc:
            raise ValueError(f"checkpoint path escapes root: {relative_text}") from exc
        size, digest = _sha256_stable_file(path)
        if item.get("size") != size or item.get("sha256") != digest:
            raise ValueError(f"checkpoint integrity failure: {relative_text}")

    actual_paths = {
        item["path"] for item in _checkpoint_files(root)
    }
    if actual_paths != expected_paths:
        raise ValueError("checkpoint integrity failure: manifest file set differs")
    return copy.deepcopy(manifest)


def _retention_record(path: Path, manifest: Mapping[str, Any]) -> dict[str, Any]:
    metadata = manifest.get("metadata")
    if not isinstance(metadata, Mapping):
        raise ValueError("checkpoint manifest metadata is invalid")
    epoch = metadata.get("epoch")
    step = metadata.get("step")
    if isinstance(epoch, bool) or not isinstance(epoch, numbers.Integral):
        raise ValueError("checkpoint epoch must be an integer")
    if isinstance(step, bool) or not isinstance(step, numbers.Integral):
        raise ValueError("checkpoint step must be an integer")
    score = None
    if metadata.get("score") is not None:
        score = _scalar(metadata.get("score"))
        if score is None:
            raise ValueError("checkpoint score must be a finite scalar")
    return {
        "path": path.name,
        "epoch": int(epoch),
        "step": int(step),
        "score": score,
        "score_metric": str(metadata.get("score_metric", "")),
    }


def _complete_checkpoint_records(root: Path) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for candidate in sorted(root.iterdir(), key=lambda item: item.name):
        if candidate.name.startswith(".") or candidate.is_symlink() or not candidate.is_dir():
            continue
        try:
            manifest = validate_checkpoint(candidate)
        except ValueError:
            # Incomplete or corrupt directories are never retention deletion targets.
            continue
        records.append(_retention_record(candidate, manifest))
    return records


def retain_checkpoints(
    checkpoint_root: str | os.PathLike[str],
    current_checkpoint: str | os.PathLike[str] | None,
    *,
    keep_latest: int,
    keep_best: int,
    best_mode: str,
) -> dict[str, Any]:
    """Atomically publish and enforce the union of latest-N and best-M.

    This function must be called only after Ray has accepted ``current_checkpoint``
    (when one is present), or after a metrics-only report has succeeded.
    Only complete, integrity-checked direct children are eligible for deletion;
    incomplete or corrupt directories are left untouched for diagnosis.
    """

    latest_count = int(keep_latest)
    best_count = int(keep_best)
    if latest_count < 0 or best_count < 0:
        raise ValueError("checkpoint retention must be non-negative")
    if best_mode not in ("min", "max"):
        raise ValueError("best_mode must be min or max")

    root = Path(checkpoint_root)
    if root.is_symlink() or not root.is_dir():
        raise ValueError("checkpoint root must be a real directory")
    resolved_root = root.resolve(strict=True)
    if current_checkpoint is not None:
        current = Path(current_checkpoint)
        if current.is_symlink() or not current.is_dir():
            raise ValueError("current checkpoint must be a real directory")
        try:
            resolved_current = current.resolve(strict=True)
            resolved_current.relative_to(resolved_root)
        except ValueError as exc:
            raise ValueError("current checkpoint must be below checkpoint root") from exc
        if resolved_current.parent != resolved_root:
            raise ValueError("current checkpoint must be a direct child of checkpoint root")
        validate_checkpoint(current)

    records = _complete_checkpoint_records(root)
    newest_first = sorted(
        records,
        key=lambda item: (item["epoch"], item["step"], item["path"]),
        reverse=True,
    )
    selected_paths = {
        item["path"] for item in newest_first[:latest_count]
    }
    scored = [item for item in newest_first if item["score"] is not None]
    scored.sort(
        key=lambda item: (
            item["score"] if best_mode == "min" else -item["score"],
            -item["epoch"],
            -item["step"],
            item["path"],
        )
    )
    selected_paths.update(item["path"] for item in scored[:best_count])
    retained = sorted(
        (item for item in records if item["path"] in selected_paths),
        key=lambda item: (item["epoch"], item["step"], item["path"]),
    )
    index = {
        "version": RETENTION_INDEX_VERSION,
        "complete": True,
        "policy": {
            "keep_latest": latest_count,
            "keep_best": best_count,
            "best_mode": best_mode,
        },
        "checkpoints": retained,
    }

    # Publish the new source of truth first. A failed atomic replace cannot prune.
    _atomic_json(root / RETENTION_INDEX_NAME, index)
    for record in records:
        if record["path"] in selected_paths:
            continue
        candidate = root / record["path"]
        if candidate.resolve(strict=True).parent != resolved_root:
            raise ValueError("checkpoint retention target escaped checkpoint root")
        validate_checkpoint(candidate)
        shutil.rmtree(candidate)
    return copy.deepcopy(index)


def _load_train_api() -> Any:
    from ray import train

    return train


def _load_gauge_class() -> Any:
    from ray.util.metrics import Gauge

    return Gauge


def _managed_metric_tags(rank: int) -> dict[str, str] | None:
    assigned_gpu = os.environ.get("CUDA_VISIBLE_DEVICES", "").split(",", 1)[0].strip()
    if not _SAFE_GPU_IDENTITY.fullmatch(assigned_gpu):
        assigned_gpu = ""
    tags = {
        "platform_job_id": os.environ.get("PLATFORM_JOB_ID", "").strip(),
        "exported_namespace": os.environ.get("PLATFORM_POD_NAMESPACE", "").strip(),
        "ray_io_cluster": os.environ.get("PLATFORM_RAY_CLUSTER", "").strip(),
        "ray_io_node_type": os.environ.get("PLATFORM_RAY_NODE_TYPE", "").strip(),
        "pod": os.environ.get("PLATFORM_POD_NAME", "").strip(),
        "rank": str(rank),
        "gpu": assigned_gpu,
        "dataset_id": os.environ.get("PLATFORM_DATASET_ID", "").strip(),
        "dataset_version_id": os.environ.get(
            "PLATFORM_DATASET_VERSION_ID", ""
        ).strip(),
        "ray_version": os.environ.get("PLATFORM_RAY_VERSION", "").strip(),
        "data_mode": os.environ.get("PLATFORM_DATA_MODE", "").strip(),
        "cache_policy": os.environ.get(
            "PLATFORM_DATASET_CACHE_POLICY", ""
        ).strip(),
    }
    if not _SAFE_IDENTIFIER.fullmatch(tags["platform_job_id"]):
        return None
    for key in ("exported_namespace", "ray_io_cluster", "pod"):
        if len(tags[key]) > 253 or not _SAFE_DNS_VALUE.fullmatch(tags[key]):
            return None
    if tags["ray_io_node_type"] != "worker" or rank < 0 or rank > 1_000_000:
        return None
    for key in ("dataset_id", "dataset_version_id", "ray_version"):
        if tags[key] and not _SAFE_IDENTIFIER.fullmatch(tags[key]):
            return None
    if tags["data_mode"] not in ("", "mount", "cache", "ray-data", "ray-data-stage", "streaming"):
        return None
    if tags["cache_policy"] not in ("", "off", "auto", "bounded"):
        return None
    return tags


def _export_managed_metrics(metrics: Mapping[str, float], rank: int) -> None:
    tags = _managed_metric_tags(rank)
    if tags is None:
        return
    for key, value in metrics.items():
        metric_name = _CUSTOM_METRIC_NAMES.get(key.strip().lower())
        if metric_name is None:
            continue
        try:
            with _CUSTOM_GAUGES_LOCK:
                gauge = _CUSTOM_GAUGES.get(metric_name)
                if gauge is None:
                    gauge = _load_gauge_class()(
                        metric_name,
                        description="Managed Ray Train worker performance metric.",
                        tag_keys=_GAUGE_TAG_KEYS,
                    )
                    _CUSTOM_GAUGES[metric_name] = gauge
            gauge.set(value, tags=tags)
        except Exception:
            # Observability must never interrupt training or checkpoint parity.
            continue


def _export_mlflow_data_metrics(metrics: Mapping[str, float], rank: int) -> None:
    """Attach honest rank-zero-local counters to the governed MLflow run.

    Process-local counters cannot be described as job totals. Prometheus keeps
    the authoritative per-rank series and platform-wide aggregate; MLflow uses
    an explicit ``rank0_worker_`` prefix so experiment users cannot mistake the
    representative worker values for a distributed sum.
    """

    if rank != 0 or not os.environ.get("MLFLOW_TRACKING_URI", "").strip():
        return
    selected = {
        _MLFLOW_DATA_METRICS[key]: value
        for key, value in metrics.items()
        if key in _MLFLOW_DATA_METRICS
    }
    if not selected:
        return
    step_value = metrics.get("step", 0.0)
    step = int(step_value) if 0 <= step_value <= 9_223_372_036_854_775_807 else 0
    try:
        import mlflow

        if mlflow.active_run() is None:
            return
        mlflow.log_metrics(selected, step=step)
    except Exception:
        # MLflow is supplementary; a telemetry outage must not stop training.
        return


def world_rank(*, train_api: Any | None = None) -> int:
    api = train_api if train_api is not None else _load_train_api()
    return int(api.get_context().get_world_rank())


def world_size(*, train_api: Any | None = None) -> int:
    api = train_api if train_api is not None else _load_train_api()
    return int(api.get_context().get_world_size())


def report_metrics(
    metrics: Mapping[Any, Any],
    checkpoint_dir: str | os.PathLike[str] | None = None,
    *,
    world_rank: int | None = None,
    train_api: Any | None = None,
) -> None:
    """Report once on every rank, attaching a verified checkpoint on rank zero."""

    clean = sanitize_metrics(metrics, reject_invalid=True)
    api = train_api if train_api is not None else _load_train_api()
    rank = int(api.get_context().get_world_rank()) if world_rank is None else int(world_rank)
    _export_managed_metrics(clean, rank)
    _export_mlflow_data_metrics(clean, rank)
    checkpoint = None
    checkpoint_error = None
    if checkpoint_dir is not None and rank == 0:
        try:
            validate_checkpoint(checkpoint_dir)
            checkpoint = api.Checkpoint.from_directory(str(checkpoint_dir))
        except Exception as exc:
            checkpoint_error = exc
    api.report(clean, checkpoint=checkpoint)
    if checkpoint_error is not None:
        raise checkpoint_error
