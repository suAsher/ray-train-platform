"""Optional MMCV hook for managed Ray Train metrics and checkpoints."""

from __future__ import annotations

import os
from pathlib import Path
from collections.abc import Mapping
from typing import Any, Callable

from .reporting import finalize_checkpoint, report_metrics, sanitize_metrics, world_rank, world_size


try:
    from mmcv.runner import HOOKS as _MMCV_HOOKS
    from mmcv.runner import Hook as _MMCVHook

    MMCV_AVAILABLE = True
except ImportError:
    _MMCV_HOOKS = None

    class _MMCVHook:  # type: ignore[no-redef]
        """Small fallback base so this module remains importable without MMCV."""

    MMCV_AVAILABLE = False


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


def write_runner_checkpoint(
    runner: Any, checkpoint_dir: Path, metadata: Mapping[str, Any]
) -> Path:
    """Serialize model and resumable optimizer state without importing torch eagerly."""

    import torch

    checkpoint_dir.mkdir(parents=True, exist_ok=True)
    model = _runner_value(runner, "model")
    model = getattr(model, "module", model)
    payload = {
        **dict(metadata),
        "model": _state_dict(model),
        "optimizer": _state_dict(_runner_value(runner, "optimizer")),
        "scheduler": _state_dict(
            _runner_value(runner, "lr_scheduler", "scheduler", "lr_schedulers")
        ),
        "amp": _state_dict(_runner_value(runner, "amp_scaler", "scaler")),
    }
    destination = checkpoint_dir / "training_state.pth"
    torch.save(payload, destination)
    return destination


def _default_checkpoint_root() -> Path:
    configured = os.environ.get("PLATFORM_CHECKPOINT_PATH", "").strip()
    if configured:
        return Path(configured)
    output = os.environ.get("PLATFORM_OUTPUT_PATH", "/mnt/data/output").strip()
    return Path(output) / ".platform" / "ray-train" / "checkpoints"


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

    def _checkpoint_metadata(self, runner: Any) -> dict[str, Any]:
        return {
            "epoch": self._epoch(runner),
            "step": self._step(runner),
            "data_version": self.data_version,
            "code_sha": self.code_sha,
            "image_digest": self.image_digest,
            "world_size": int(self._world_size()),
            "checkpoint_policy": {
                "every_epochs": self.checkpoint_every_epochs,
                "keep_latest": self.keep_latest,
                "keep_best": self.keep_best,
                "best_metric": self.best_metric,
                "best_mode": self.best_mode,
            },
        }

    def after_train_epoch(self, runner: Any) -> None:
        if not self._checkpoint_due(runner):
            return
        metrics = self._metrics(runner)
        if self.keep_best and self.best_metric and self.best_metric not in metrics:
            raise ValueError(
                f"best metric {self.best_metric!r} is required on checkpoint epochs"
            )

        rank = int(self._rank())
        checkpoint_dir = None
        if rank == 0 and (self.keep_latest or self.keep_best):
            metadata = self._checkpoint_metadata(runner)
            checkpoint_dir = self.checkpoint_root / (
                f"checkpoint-epoch-{metadata['epoch']:06d}-step-{metadata['step']:012d}"
            )
            checkpoint_dir.mkdir(parents=True, exist_ok=False)
            self._write_state(runner, checkpoint_dir, metadata)
            finalize_checkpoint(checkpoint_dir, metadata)
        self._report(metrics, checkpoint_dir=checkpoint_dir, world_rank=rank)


if _MMCV_HOOKS is not None:
    RayTrainManagedHook = _MMCV_HOOKS.register_module()(RayTrainManagedHook)
