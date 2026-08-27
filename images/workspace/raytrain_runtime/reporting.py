"""Framework-neutral metrics and complete-checkpoint reporting for Ray Train."""

from __future__ import annotations

import copy
import hashlib
import json
import math
import numbers
import os
from pathlib import Path, PurePosixPath
from collections.abc import Mapping
import shutil
from typing import Any
import uuid


MANIFEST_NAME = "manifest.json"
MANIFEST_VERSION = 1
RETENTION_INDEX_NAME = "retention-index.json"
RETENTION_INDEX_VERSION = 1


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
        if candidate.is_symlink() or not candidate.is_dir():
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
    checkpoint = None
    if checkpoint_dir is not None and rank == 0:
        validate_checkpoint(checkpoint_dir)
        checkpoint = api.Checkpoint.from_directory(str(checkpoint_dir))
    api.report(clean, checkpoint=checkpoint)
