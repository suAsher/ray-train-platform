"""Optional MMCV hook for managed Ray Train metrics and checkpoints."""

from __future__ import annotations

import copy
import json
import math
import os
from pathlib import Path
import random
import shutil
from collections.abc import Mapping
from typing import Any, Callable
import uuid

from .reporting import (
    MANIFEST_NAME,
    RETENTION_INDEX_NAME,
    finalize_checkpoint,
    report_metrics,
    retain_checkpoints,
    sanitize_metrics,
    validate_checkpoint,
    world_rank,
    world_size,
)


try:
    from mmcv.runner import HOOKS as _MMCV_HOOKS
    from mmcv.runner import Hook as _MMCVHook

    MMCV_AVAILABLE = True
except ImportError:
    _MMCV_HOOKS = None

    class _MMCVHook:  # type: ignore[no-redef]
        """Small fallback base so this module remains importable without MMCV."""

    MMCV_AVAILABLE = False


CHECKPOINT_STATE_NAME = "training_state.pth"
CHECKPOINT_STATE_VERSION = 1
TRAINING_PARAMETER_LIMIT = 128
TRAINING_PARAMETER_DEPTH = 4
TRAINING_PARAMETER_STRING_LIMIT = 512
SAFE_RUNNER_META_FIELDS = (
    "hook_msgs",
    "seed",
    "experiment_name",
    "experiment",
    "exp_name",
)
EXTERNAL_CHECKPOINT_INDEX_MAX_BYTES = 1024 * 1024
EXTERNAL_CHECKPOINT_SCAN_LIMIT = 512
EXTERNAL_JOB_ROOT_SCAN_LIMIT = 128


def extract_scalar_metrics(metrics: Mapping[Any, Any]) -> dict[str, float]:
    """Copy finite numeric scalars out of a framework log buffer."""

    return sanitize_metrics(metrics, reject_invalid=False)


def build_hook_config(
    *,
    interval: int,
    checkpoint_every_epochs: int,
    keep_latest: int,
    keep_best: int,
    best_metric: str,
    best_mode: str,
) -> dict[str, Any]:
    """Build the explicit Task 9 policy handoff consumed by MMCV."""

    return {
        "type": "RayTrainManagedHook",
        "interval": int(interval),
        "checkpoint_every_epochs": int(checkpoint_every_epochs),
        "keep_latest": int(keep_latest),
        "keep_best": int(keep_best),
        "best_metric": str(best_metric),
        "best_mode": str(best_mode),
    }


def build_restore_hook_config() -> dict[str, str]:
    """Build the independent early-resume hook configuration."""

    return {"type": "RayTrainManagedRestoreHook"}


def _state_dict(value: Any) -> Any:
    if value is None:
        return None
    if isinstance(value, dict):
        return {str(key): _state_dict(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [_state_dict(item) for item in value]
    state_dict = getattr(value, "state_dict", None)
    return state_dict() if callable(state_dict) else value


def _runner_value(runner: Any, *names: str) -> Any:
    for name in names:
        value = getattr(runner, name, None)
        if value is not None:
            return value
    return None


def _parameter_source(runner: Any) -> Any:
    source = _runner_value(runner, "cfg", "config")
    if source is None and isinstance(getattr(runner, "meta", None), Mapping):
        source = runner.meta.get("training_parameters", runner.meta.get("config", {}))
    to_dict = getattr(source, "to_dict", None)
    return to_dict() if callable(to_dict) else source


def _bounded_copy(value: Any, budget: list[int], depth: int) -> Any:
    if depth > TRAINING_PARAMETER_DEPTH:
        return "<max-depth>"
    if isinstance(value, str):
        return value[:TRAINING_PARAMETER_STRING_LIMIT]
    if isinstance(value, float) and not math.isfinite(value):
        return "<non-finite>"
    if value is None or isinstance(value, (bool, int, float)):
        return value
    if isinstance(value, Mapping):
        copied: dict[str, Any] = {}
        for key, item in sorted(value.items(), key=lambda pair: str(pair[0])):
            if budget[0] <= 0:
                copied["__truncated__"] = True
                break
            budget[0] -= 1
            key_text = str(key)[:TRAINING_PARAMETER_STRING_LIMIT]
            normalized_key = key_text.lower().replace("-", "_")
            sensitive = normalized_key in {"ak", "sk"} or any(
                marker in normalized_key
                for marker in (
                    "password",
                    "passwd",
                    "secret",
                    "token",
                    "credential",
                    "api_key",
                    "access_key",
                    "private_key",
                )
            )
            copied[key_text] = (
                "<redacted>"
                if sensitive
                else _bounded_copy(item, budget, depth + 1)
            )
        return copied
    if isinstance(value, (list, tuple)):
        copied_list = []
        for item in value:
            if budget[0] <= 0:
                copied_list.append("<truncated>")
                break
            budget[0] -= 1
            copied_list.append(_bounded_copy(item, budget, depth + 1))
        return copied_list
    return repr(value)[:TRAINING_PARAMETER_STRING_LIMIT]


def bounded_training_parameters(runner: Any) -> Any:
    """Return a deterministic, immutable and bounded config summary."""

    return _bounded_copy(
        _parameter_source(runner), [TRAINING_PARAMETER_LIMIT], depth=0
    )


def _safe_runner_metadata(value: Any) -> dict[str, Any]:
    """Copy only bounded MMCV state that is useful during resume."""

    if not isinstance(value, Mapping):
        return {}
    budget = [TRAINING_PARAMETER_LIMIT]
    copied: dict[str, Any] = {}
    for name in SAFE_RUNNER_META_FIELDS:
        if name not in value or budget[0] <= 0:
            continue
        budget[0] -= 1
        copied[name] = _bounded_copy(value[name], budget, depth=1)
    return copied


def _load_numpy() -> Any | None:
    try:
        import numpy
    except ImportError:
        return None
    return numpy


def _capture_rng_state(torch_api: Any, numpy_api: Any | None) -> dict[str, Any]:
    cuda_api = getattr(torch_api, "cuda", None)
    cuda_available = bool(
        cuda_api is not None
        and callable(getattr(cuda_api, "is_available", None))
        and cuda_api.is_available()
    )
    return {
        "python": copy.deepcopy(random.getstate()),
        "numpy": (
            copy.deepcopy(numpy_api.random.get_state())
            if numpy_api is not None
            else None
        ),
        "torch_cpu": copy.deepcopy(torch_api.get_rng_state()),
        "torch_cuda": (
            copy.deepcopy(cuda_api.get_rng_state_all()) if cuda_available else None
        ),
    }


def write_runner_checkpoint(
    runner: Any, checkpoint_dir: Path, metadata: Mapping[str, Any]
) -> Path:
    """Serialize model and resumable optimizer state without importing torch eagerly."""

    import torch

    numpy = _load_numpy()
    checkpoint_dir.mkdir(parents=True, exist_ok=True)
    model = _runner_value(runner, "model")
    model = getattr(model, "module", model)
    payload = {
        "format_version": CHECKPOINT_STATE_VERSION,
        **dict(metadata),
        "model": _state_dict(model),
        "optimizer": _state_dict(_runner_value(runner, "optimizer")),
        "scheduler": _state_dict(
            _runner_value(runner, "lr_scheduler", "scheduler", "lr_schedulers")
        ),
        "amp": _state_dict(_runner_value(runner, "amp_scaler", "scaler")),
        "runner": {
            "epoch": int(getattr(runner, "epoch", 0)),
            "iter": int(getattr(runner, "iter", 0)),
            "safe_meta": _safe_runner_metadata(getattr(runner, "meta", {})),
        },
        "rng": _capture_rng_state(torch, numpy),
        "training_parameters": bounded_training_parameters(runner),
    }
    destination = checkpoint_dir / CHECKPOINT_STATE_NAME
    temporary = checkpoint_dir / f".training_state.{uuid.uuid4().hex}.tmp"
    try:
        torch.save(payload, temporary)
        with temporary.open("rb") as stream:
            os.fsync(stream.fileno())
        os.replace(temporary, destination)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass
    return destination


def _torch_load(torch_api: Any, state_path: Path) -> Any:
    try:
        return torch_api.load(state_path, map_location="cpu", weights_only=False)
    except TypeError:
        return torch_api.load(state_path, map_location="cpu")


def _loadable_target(runner: Any, state: Any, label: str, *names: str) -> Any:
    if state is None:
        return None
    target = _runner_value(runner, *names)
    if label == "model":
        target = getattr(target, "module", target)
    if target is None or not callable(getattr(target, "load_state_dict", None)):
        raise ValueError(f"checkpoint {label} state cannot be restored by this runner")
    return target


def _validate_resume_payload(
    runner: Any, payload: Any, torch_api: Any, numpy_api: Any | None
) -> dict[str, Any]:
    if (
        not isinstance(payload, Mapping)
        or payload.get("format_version") != CHECKPOINT_STATE_VERSION
    ):
        raise ValueError("checkpoint training state has an unsupported format")
    runner_state = payload.get("runner")
    if not isinstance(runner_state, Mapping):
        raise ValueError("checkpoint runner state is missing")
    if any(
        isinstance(runner_state.get(name), bool)
        or not isinstance(runner_state.get(name), int)
        for name in ("epoch", "iter")
    ) or not isinstance(runner_state.get("safe_meta"), Mapping):
        raise ValueError("checkpoint runner state is invalid")
    rng = payload.get("rng")
    if not isinstance(rng, Mapping) or "python" not in rng or "torch_cpu" not in rng:
        raise ValueError("checkpoint RNG state is missing")
    if rng.get("numpy") is not None and numpy_api is None:
        raise ValueError("checkpoint NumPy RNG state cannot be restored")
    cuda_state = rng.get("torch_cuda")
    cuda_api = getattr(torch_api, "cuda", None)
    if cuda_state is not None and not (
        cuda_api is not None
        and callable(getattr(cuda_api, "is_available", None))
        and cuda_api.is_available()
        and callable(getattr(cuda_api, "set_rng_state_all", None))
    ):
        raise ValueError("checkpoint CUDA RNG state cannot be restored")
    if payload.get("model") is None:
        raise ValueError("checkpoint model state is missing")

    targets = {
        "model": _loadable_target(runner, payload.get("model"), "model", "model"),
        "optimizer": _loadable_target(
            runner, payload.get("optimizer"), "optimizer", "optimizer"
        ),
        "scheduler": _loadable_target(
            runner,
            payload.get("scheduler"),
            "scheduler",
            "lr_scheduler",
            "scheduler",
            "lr_schedulers",
        ),
        "amp": _loadable_target(
            runner, payload.get("amp"), "AMP", "amp_scaler", "scaler"
        ),
    }
    return {
        "payload": payload,
        "runner": runner_state,
        "rng": rng,
        "targets": targets,
        "cuda": cuda_api,
    }


def _runner_progress_fields(runner: Any) -> tuple[str, str]:
    fields = (
        "_epoch" if hasattr(runner, "_epoch") else "epoch",
        "_iter" if hasattr(runner, "_iter") else "iter",
    )
    for field in fields:
        descriptor = getattr(type(runner), field, None)
        if isinstance(descriptor, property) and descriptor.fset is None:
            raise ValueError(f"runner progress field {field} is not restorable")
    return fields


def restore_runner_checkpoint(runner: Any, checkpoint_dir: Path) -> Path:
    """Validate and restore a complete managed checkpoint before training starts."""

    root = Path(checkpoint_dir)
    validate_checkpoint(root)
    state_path = root / CHECKPOINT_STATE_NAME
    import torch

    numpy = _load_numpy()
    restore = _validate_resume_payload(
        runner, _torch_load(torch, state_path), torch, numpy
    )
    epoch_field, iter_field = _runner_progress_fields(runner)
    payload = restore["payload"]
    for label, target in restore["targets"].items():
        if target is not None:
            target.load_state_dict(payload[label])

    runner_state = restore["runner"]
    setattr(runner, epoch_field, int(runner_state["epoch"]))
    setattr(runner, iter_field, int(runner_state["iter"]))
    runner.meta = copy.deepcopy(dict(runner_state["safe_meta"]))
    rng = restore["rng"]
    random.setstate(rng["python"])
    if rng.get("numpy") is not None:
        numpy.random.set_state(rng["numpy"])
    torch.set_rng_state(rng["torch_cpu"])
    if rng.get("torch_cuda") is not None:
        restore["cuda"].set_rng_state_all(rng["torch_cuda"])
    return state_path


def _checkpoint_order(checkpoint: Path) -> tuple[int, int, str] | None:
    try:
        manifest = validate_checkpoint(checkpoint)
    except ValueError:
        return None
    metadata = manifest.get("metadata")
    if not isinstance(metadata, Mapping):
        return None
    epoch = metadata.get("epoch")
    step = metadata.get("step")
    if (
        isinstance(epoch, bool)
        or not isinstance(epoch, int)
        or isinstance(step, bool)
        or not isinstance(step, int)
    ):
        return None
    return int(epoch), int(step), checkpoint.name


def _safe_collection(path: Path, boundary: Path) -> Path | None:
    if path.is_symlink() or not path.is_dir():
        return None
    try:
        resolved = path.resolve(strict=True)
        resolved.relative_to(boundary)
    except (OSError, ValueError):
        return None
    return resolved


def _indexed_checkpoint(
    collection: Path, *, max_checkpoints: int
) -> Path | None:
    index_path = collection / RETENTION_INDEX_NAME
    if index_path.is_symlink() or not index_path.is_file():
        return None
    try:
        if index_path.stat().st_size > EXTERNAL_CHECKPOINT_INDEX_MAX_BYTES:
            raise ValueError("checkpoint retention index exceeds bounded size")
        index = json.loads(index_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError("checkpoint retention index is not readable") from exc
    if not isinstance(index, Mapping):
        return None
    records = index.get("checkpoints")
    if index.get("complete") is not True or not isinstance(records, list):
        return None
    if len(records) > max_checkpoints:
        raise ValueError("checkpoint retention index exceeds bounded candidate count")

    candidates: list[tuple[tuple[int, int, str], Path]] = []
    for record in records:
        if not isinstance(record, Mapping):
            continue
        name = record.get("path")
        if (
            not isinstance(name, str)
            or not name
            or name.startswith(".")
            or Path(name).name != name
        ):
            continue
        candidate = collection / name
        if candidate.is_symlink() or not candidate.is_dir():
            continue
        order = _checkpoint_order(candidate)
        if order is not None:
            candidates.append((order, candidate))
    return max(candidates, default=(None, None), key=lambda item: item[0])[1]


def _scanned_checkpoint(
    collection: Path, *, max_checkpoints: int
) -> Path | None:
    names: list[str] = []
    with os.scandir(collection) as entries:
        for entry in entries:
            if (
                entry.name.startswith("checkpoint-")
                and not entry.is_symlink()
                and entry.is_dir(follow_symlinks=False)
            ):
                names.append(entry.name)
                if len(names) > max_checkpoints:
                    raise ValueError("checkpoint directory scan exceeds bounded candidate count")
    candidates: list[tuple[tuple[int, int, str], Path]] = []
    for name in sorted(names):
        candidate = collection / name
        order = _checkpoint_order(candidate)
        if order is not None:
            candidates.append((order, candidate))
    return max(candidates, default=(None, None), key=lambda item: item[0])[1]


def resolve_external_managed_checkpoint(
    external_path: str | os.PathLike[str],
    *,
    max_job_roots: int = EXTERNAL_JOB_ROOT_SCAN_LIMIT,
    max_checkpoints: int = EXTERNAL_CHECKPOINT_SCAN_LIMIT,
) -> Path | None:
    """Resolve a managed checkpoint without consuming a legacy resume path."""

    if max_job_roots < 1 or max_checkpoints < 1:
        raise ValueError("checkpoint resolver bounds must be positive")
    external = Path(external_path)
    if external.is_symlink() or not external.is_dir():
        return None
    boundary = external.resolve(strict=True)
    direct_manifest = external / MANIFEST_NAME
    if direct_manifest.exists() or direct_manifest.is_symlink():
        validate_checkpoint(external)
        return external

    collections: list[Path] = []
    for path in (external, external / "checkpoints"):
        safe = _safe_collection(path, boundary)
        if safe is not None and safe not in collections:
            collections.append(safe)

    managed_root = _safe_collection(external / ".platform" / "ray-train", boundary)
    if managed_root is not None:
        job_roots: list[str] = []
        with os.scandir(managed_root) as entries:
            for entry in entries:
                if (
                    entry.name.startswith("job-")
                    and not entry.is_symlink()
                    and entry.is_dir(follow_symlinks=False)
                ):
                    job_roots.append(entry.name)
                    if len(job_roots) > max_job_roots:
                        raise ValueError("managed job root scan exceeds bounded candidate count")
        for name in sorted(job_roots):
            safe = _safe_collection(managed_root / name / "checkpoints", boundary)
            if safe is not None and safe not in collections:
                collections.append(safe)

    indexed: list[tuple[tuple[int, int, str], Path]] = []
    for collection in collections:
        candidate = _indexed_checkpoint(
            collection, max_checkpoints=max_checkpoints
        )
        if candidate is not None:
            order = _checkpoint_order(candidate)
            if order is not None:
                indexed.append((order, candidate))
    if indexed:
        return max(indexed, key=lambda item: item[0])[1]

    scanned: list[tuple[tuple[int, int, str], Path]] = []
    for collection in collections:
        candidate = _scanned_checkpoint(
            collection, max_checkpoints=max_checkpoints
        )
        if candidate is not None:
            order = _checkpoint_order(candidate)
            if order is not None:
                scanned.append((order, candidate))
    return max(scanned, default=(None, None), key=lambda item: item[0])[1]


def _default_checkpoint_root() -> Path:
    configured = os.environ.get("RAYTRAIN_CHECKPOINT_OUTPUT_PATH", "").strip()
    if configured:
        return Path(configured)
    output = os.environ.get("PLATFORM_OUTPUT_PATH", "/mnt/data/output").strip()
    return Path(output) / ".platform" / "ray-train" / "checkpoints"


def _checkpoint_name(metadata: Mapping[str, Any]) -> str:
    return (
        f"checkpoint-epoch-{int(metadata['epoch']):06d}-"
        f"step-{int(metadata['step']):012d}"
    )


def _complete_checkpoint_matches(
    checkpoint: Path, metadata: Mapping[str, Any]
) -> bool:
    manifest = validate_checkpoint(checkpoint)
    existing = manifest.get("metadata", {})
    return (
        isinstance(existing, Mapping)
        and existing.get("epoch") == metadata.get("epoch")
        and existing.get("step") == metadata.get("step")
    )


def _remove_private_staging(path: Path) -> None:
    if path.is_symlink():
        path.unlink()
    elif path.exists():
        shutil.rmtree(path)


def _publish_runner_checkpoint(
    runner: Any,
    checkpoint_root: Path,
    metadata: Mapping[str, Any],
    state_writer: Callable[[Any, Path, Mapping[str, Any]], Any],
) -> Path:
    """Finalize in a unique hidden directory, then atomically publish it."""

    checkpoint_root.mkdir(parents=True, exist_ok=True)
    if checkpoint_root.is_symlink() or not checkpoint_root.is_dir():
        raise ValueError("checkpoint root must be a real directory")
    final = checkpoint_root / _checkpoint_name(metadata)
    if final.exists() or final.is_symlink():
        if final.is_symlink() or not final.is_dir():
            raise ValueError("checkpoint final path must be a real directory")
        try:
            matches = _complete_checkpoint_matches(final, metadata)
        except ValueError:
            matches = False
        if matches:
            return final
        quarantine = checkpoint_root / (
            f".checkpoint-incomplete-{final.name}-{uuid.uuid4().hex}"
        )
        os.replace(final, quarantine)

    staging = checkpoint_root / f".checkpoint-staging-{uuid.uuid4().hex}"
    staging.mkdir(exist_ok=False)
    try:
        state_writer(runner, staging, metadata)
        finalize_checkpoint(staging, metadata)
        os.replace(staging, final)
    except Exception:
        _remove_private_staging(staging)
        raise
    return final


class RayTrainManagedRestoreHook(_MMCVHook):
    """Restore managed state before optimizer, LR, and logger hooks run."""

    def __init__(
        self,
        *,
        state_loader: Callable[[Any, Path], Any] | None = None,
    ) -> None:
        self._load_state = (
            state_loader if state_loader is not None else restore_runner_checkpoint
        )

    def before_run(self, runner: Any) -> None:
        ray_resume = os.environ.get("RAYTRAIN_RESUME_CHECKPOINT_PATH", "").strip()
        if ray_resume:
            checkpoint = Path(ray_resume)
            validate_checkpoint(checkpoint)
            self._load_state(runner, checkpoint)
            return
        external_resume = os.environ.get("PLATFORM_CHECKPOINT_PATH", "").strip()
        if not external_resume:
            return
        checkpoint = resolve_external_managed_checkpoint(external_resume)
        if checkpoint is None:
            return
        self._load_state(runner, checkpoint)


class RayTrainManagedHook(_MMCVHook):
    """Report MMCV scalars and complete checkpoints from every Train worker."""

    def __init__(
        self,
        *,
        interval: int = 10,
        checkpoint_every_epochs: int = 1,
        keep_latest: int = 3,
        keep_best: int = 1,
        best_metric: str = "",
        best_mode: str = "max",
        checkpoint_root: str | os.PathLike[str] | None = None,
        data_version: str | None = None,
        code_sha: str | None = None,
        image_digest: str | None = None,
        rank_fn: Callable[[], int] | None = None,
        world_size_fn: Callable[[], int] | None = None,
        report_fn: Callable[..., None] | None = None,
        state_writer: Callable[[Any, Path, Mapping[str, Any]], Any] | None = None,
    ) -> None:
        if int(interval) < 1:
            raise ValueError("interval must be at least 1")
        if int(checkpoint_every_epochs) < 0:
            raise ValueError("checkpoint_every_epochs must be non-negative")
        if int(keep_latest) < 0 or int(keep_best) < 0:
            raise ValueError("checkpoint retention must be non-negative")
        if best_mode not in ("min", "max"):
            raise ValueError("best_mode must be min or max")

        self.interval = int(interval)
        self.checkpoint_every_epochs = int(checkpoint_every_epochs)
        self.keep_latest = int(keep_latest)
        self.keep_best = int(keep_best)
        self.best_metric = str(best_metric)
        self.best_mode = str(best_mode)
        self.checkpoint_root = (
            Path(checkpoint_root) if checkpoint_root is not None else _default_checkpoint_root()
        )
        self.data_version = (
            os.environ.get("PLATFORM_DATASET_VERSION_ID", "")
            if data_version is None
            else str(data_version)
        )
        self.code_sha = (
            os.environ.get("PLATFORM_CODE_SHA", "") if code_sha is None else str(code_sha)
        )
        self.image_digest = (
            os.environ.get("PLATFORM_RUNTIME_IMAGE_DIGEST", "")
            if image_digest is None
            else str(image_digest)
        )
        self._rank = rank_fn if rank_fn is not None else world_rank
        self._world_size = world_size_fn if world_size_fn is not None else world_size
        self._report = report_fn if report_fn is not None else report_metrics
        self._write_state = state_writer if state_writer is not None else write_runner_checkpoint

    @staticmethod
    def _epoch(runner: Any) -> int:
        return int(getattr(runner, "epoch", 0)) + 1

    @staticmethod
    def _step(runner: Any) -> int:
        return int(getattr(runner, "iter", 0)) + 1

    def _metrics(self, runner: Any) -> dict[str, float]:
        log_buffer = getattr(runner, "log_buffer", None)
        output = getattr(log_buffer, "output", {})
        metrics = extract_scalar_metrics(dict(output or {}))
        metrics["epoch"] = float(self._epoch(runner))
        metrics["step"] = float(self._step(runner))
        return metrics

    def after_train_iter(self, runner: Any) -> None:
        if self._step(runner) % self.interval:
            return
        self._report(self._metrics(runner), world_rank=int(self._rank()))

    def _checkpoint_due(self, runner: Any) -> bool:
        return bool(
            self.checkpoint_every_epochs
            and self._epoch(runner) % self.checkpoint_every_epochs == 0
        )

    def _checkpoint_metadata(
        self, runner: Any, metrics: Mapping[str, float]
    ) -> dict[str, Any]:
        metadata = {
            "epoch": self._epoch(runner),
            "step": self._step(runner),
            "data_version": self.data_version,
            "code_sha": self.code_sha,
            "image_digest": self.image_digest,
            "world_size": int(self._world_size()),
            "training_parameters": bounded_training_parameters(runner),
            "checkpoint_policy": {
                "every_epochs": self.checkpoint_every_epochs,
                "keep_latest": self.keep_latest,
                "keep_best": self.keep_best,
                "best_metric": self.best_metric,
                "best_mode": self.best_mode,
            },
        }
        if self.best_metric and self.best_metric in metrics:
            metadata.update(
                {"score": metrics[self.best_metric], "score_metric": self.best_metric}
            )
        return metadata

    def after_train_epoch(self, runner: Any) -> None:
        if not self._checkpoint_due(runner):
            return
        metrics = self._metrics(runner)
        rank = int(self._rank())
        checkpoint_dir = None
        checkpoint_error = None
        score_available = not self.best_metric or self.best_metric in metrics
        if (
            rank == 0
            and (
                self.keep_latest
                or (self.keep_best and score_available)
            )
        ):
            try:
                metadata = self._checkpoint_metadata(runner, metrics)
                checkpoint_dir = _publish_runner_checkpoint(
                    runner,
                    self.checkpoint_root,
                    metadata,
                    self._write_state,
                )
            except Exception as exc:
                checkpoint_dir = None
                checkpoint_error = exc
        self._report(metrics, checkpoint_dir=checkpoint_dir, world_rank=rank)
        if checkpoint_error is not None:
            raise checkpoint_error
        if rank == 0:
            self.checkpoint_root.mkdir(parents=True, exist_ok=True)
            retain_checkpoints(
                self.checkpoint_root,
                checkpoint_dir,
                keep_latest=self.keep_latest,
                keep_best=self.keep_best,
                best_mode=self.best_mode,
            )


if _MMCV_HOOKS is not None:
    RayTrainManagedRestoreHook = _MMCV_HOOKS.register_module()(
        RayTrainManagedRestoreHook
    )
    RayTrainManagedHook = _MMCV_HOOKS.register_module()(RayTrainManagedHook)
