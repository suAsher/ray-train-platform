"""Build and run the platform-owned Ray Train ``TorchTrainer``."""

from __future__ import annotations

import argparse
import contextlib
import dataclasses
import os
import pathlib
import re
import sys
from collections.abc import Mapping, Sequence
from typing import Any

from .entrypoint import PythonEntrypoint, execute, parse_python_entrypoint
from .reporting import validate_checkpoint


_STABLE_OUTPUT_ROOT = pathlib.Path("/mnt/data/output")
_MANAGED_STORAGE_ROOT = _STABLE_OUTPUT_ROOT / ".platform" / "ray-train"
_SAFE_JOB_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")
_PARENT_JOB_ID = re.compile(r"^job-[0-9a-f]{24}$")


@dataclasses.dataclass(frozen=True)
class DriverConfig:
    """Immutable snapshot used to construct one managed Train run."""

    entrypoint: PythonEntrypoint
    nodes: int
    gpus_per_node: int
    cpus_per_node: int
    max_failures: int
    checkpoint_every_epochs: int
    keep_latest: int
    keep_best: int
    best_metric: str
    best_mode: str
    job_id: str
    parent_job_id: str
    storage_path: str


@dataclasses.dataclass(frozen=True)
class _RayComponents:
    TorchTrainer: Any
    ScalingConfig: Any
    RunConfig: Any
    FailureConfig: Any
    CheckpointConfig: Any


class _ArgumentParser(argparse.ArgumentParser):
    def error(self, message: str) -> None:
        raise ValueError(f"invalid managed driver arguments: {message}")


def _parser() -> argparse.ArgumentParser:
    parser = _ArgumentParser(description="Platform managed Ray Train launcher")
    parser.add_argument("--nodes", type=int, required=True)
    parser.add_argument("--gpus-per-node", type=int, required=True)
    parser.add_argument("--cpus-per-node", type=int, required=True)
    parser.add_argument("--max-failures", type=int, default=2)
    parser.add_argument("--checkpoint-every-epochs", type=int, default=1)
    parser.add_argument("--checkpoint-keep-latest", type=int, default=3)
    parser.add_argument("--checkpoint-keep-best", type=int, default=1)
    parser.add_argument("--best-metric", default="")
    parser.add_argument("--best-mode", choices=("min", "max"), default="max")
    parser.add_argument("--job-id", default="")
    parser.add_argument("--parent-job-id", default="")
    parser.add_argument("--storage-path", default="")
    return parser


def _require_range(name: str, value: int, minimum: int, maximum: int | None = None) -> None:
    if value < minimum or (maximum is not None and value > maximum):
        suffix = f" and {maximum}" if maximum is not None else ""
        raise ValueError(f"{name} must be between {minimum}{suffix}")


def _resolved(path: pathlib.Path) -> pathlib.Path:
    return path.expanduser().resolve(strict=False)


def _relative_to(path: pathlib.Path, root: pathlib.Path) -> pathlib.Path | None:
    try:
        return path.relative_to(root)
    except ValueError:
        return None


def _validated_managed_storage(path: pathlib.Path, output_root: pathlib.Path) -> str:
    candidate = _resolved(path)
    managed_root = _resolved(_MANAGED_STORAGE_ROOT)
    relative = _relative_to(candidate, managed_root)
    if relative is None or not relative.parts:
        raise ValueError("storage path must remain below /mnt/data/output/.platform/ray-train")
    if _relative_to(candidate, output_root) is None:
        raise ValueError("storage path must remain inside PLATFORM_OUTPUT_PATH")
    return str(candidate)


def _storage_path(
    requested: str,
    job_id: str,
    environ: Mapping[str, str],
) -> str:
    output_value = environ.get("PLATFORM_OUTPUT_PATH", "").strip()
    if not output_value:
        raise ValueError("PLATFORM_OUTPUT_PATH is required for managed Ray Train")
    output_root = _resolved(pathlib.Path(output_value))
    stable_output = _resolved(_STABLE_OUTPUT_ROOT)
    if output_root != stable_output:
        raise ValueError("PLATFORM_OUTPUT_PATH must be the stable /mnt/data/output mount")

    candidate = pathlib.Path(requested) if requested else _MANAGED_STORAGE_ROOT / job_id
    return _validated_managed_storage(candidate, output_root)


def parse_driver_config(
    argv: Sequence[str] | None = None,
    *,
    environ: Mapping[str, str] | None = None,
) -> DriverConfig:
    """Validate the complete driver request before importing or scheduling Ray."""

    arguments = tuple(sys.argv[1:] if argv is None else argv)
    try:
        separator = arguments.index("--")
    except ValueError as exc:
        raise ValueError("managed driver requires a Python entrypoint after --") from exc
    if separator == len(arguments) - 1:
        raise ValueError("managed driver requires a Python entrypoint after --")

    options = _parser().parse_args(arguments[:separator])
    entrypoint = parse_python_entrypoint(arguments[separator + 1 :])
    environment = dict(os.environ if environ is None else environ)

    _require_range("nodes", options.nodes, 1)
    _require_range("gpus-per-node", options.gpus_per_node, 1)
    _require_range("cpus-per-node", options.cpus_per_node, 1)
    _require_range("max-failures", options.max_failures, 0, 10)
    _require_range("checkpoint-every-epochs", options.checkpoint_every_epochs, 0, 100000)
    _require_range("checkpoint-keep-latest", options.checkpoint_keep_latest, 0, 1000)
    _require_range("checkpoint-keep-best", options.checkpoint_keep_best, 0, 1000)

    job_id = (options.job_id or environment.get("PLATFORM_JOB_ID", "")).strip()
    if not job_id or not _SAFE_JOB_ID.fullmatch(job_id) or job_id in (".", ".."):
        raise ValueError("job ID must be a safe platform identifier")
    parent_job_id = (
        options.parent_job_id or environment.get("PLATFORM_PARENT_JOB_ID", "")
    ).strip()
    if parent_job_id and not _PARENT_JOB_ID.fullmatch(parent_job_id):
        raise ValueError("parent job ID must match job- followed by 24 lowercase hex characters")

    best_metric = (options.best_metric or environment.get("PLATFORM_BEST_METRIC", "")).strip()
    if any(character in best_metric for character in ("\n", "\r", "\x00")):
        raise ValueError("best metric must not contain control characters")

    return DriverConfig(
        entrypoint=entrypoint,
        nodes=options.nodes,
        gpus_per_node=options.gpus_per_node,
        cpus_per_node=options.cpus_per_node,
        max_failures=options.max_failures,
        checkpoint_every_epochs=options.checkpoint_every_epochs,
        keep_latest=options.checkpoint_keep_latest,
        keep_best=options.checkpoint_keep_best,
        best_metric=best_metric,
        best_mode=options.best_mode,
        job_id=job_id,
        parent_job_id=parent_job_id,
        storage_path=_storage_path(options.storage_path, job_id, environment),
    )


def _load_ray_components() -> _RayComponents:
    from ray.train import CheckpointConfig, FailureConfig, RunConfig, ScalingConfig
    from ray.train.torch import TorchTrainer

    return _RayComponents(
        TorchTrainer=TorchTrainer,
        ScalingConfig=ScalingConfig,
        RunConfig=RunConfig,
        FailureConfig=FailureConfig,
        CheckpointConfig=CheckpointConfig,
    )


@contextlib.contextmanager
def _temporary_environment(values: Mapping[str, str | None]):
    """Apply worker-scoped environment values and restore the exact prior state."""

    previous = {name: os.environ.get(name) for name in values}
    existed = {name for name in values if name in os.environ}
    try:
        for name, value in values.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value
        yield
    finally:
        for name, value in previous.items():
            if name in existed:
                os.environ[name] = value if value is not None else ""
            else:
                os.environ.pop(name, None)


def _train_loop_environment(loop_config: Mapping[str, Any]) -> dict[str, str | None]:
    storage_path = _validated_managed_storage(
        pathlib.Path(str(loop_config["storage_path"])),
        _resolved(_STABLE_OUTPUT_ROOT),
    )
    return {
        "RAYTRAIN_CHECKPOINT_EVERY_EPOCHS": str(
            int(loop_config["checkpoint_every_epochs"])
        ),
        "RAYTRAIN_CHECKPOINT_KEEP_LATEST": str(int(loop_config["keep_latest"])),
        "RAYTRAIN_CHECKPOINT_KEEP_BEST": str(int(loop_config["keep_best"])),
        "RAYTRAIN_CHECKPOINT_BEST_METRIC": str(loop_config["best_metric"]),
        "RAYTRAIN_CHECKPOINT_BEST_MODE": str(loop_config["best_mode"]),
        "RAYTRAIN_CHECKPOINT_OUTPUT_PATH": str(
            pathlib.Path(storage_path) / "checkpoints"
        ),
        "RAYTRAIN_RESUME_CHECKPOINT_PATH": None,
    }


def _load_resume_checkpoint() -> Any | None:
    """Load Ray's worker checkpoint lazily so this module remains importable alone."""

    try:
        from ray import train
    except ImportError:
        return None
    return train.get_checkpoint()


@contextlib.contextmanager
def _resume_checkpoint_environment():
    checkpoint = _load_resume_checkpoint()
    if checkpoint is None:
        yield {"RAYTRAIN_RESUME_CHECKPOINT_PATH": None}
        return
    as_directory = getattr(checkpoint, "as_directory", None)
    if not callable(as_directory):
        raise ValueError("Ray resume checkpoint cannot be opened as a directory")
    with as_directory() as checkpoint_directory:
        checkpoint_path = pathlib.Path(str(checkpoint_directory)).resolve(strict=True)
        validate_checkpoint(checkpoint_path)
        yield {"RAYTRAIN_RESUME_CHECKPOINT_PATH": str(checkpoint_path)}


def _train_loop(loop_config: Mapping[str, Any]) -> None:
    payload = loop_config["entrypoint"]
    entrypoint = PythonEntrypoint(
        kind=str(payload["kind"]),
        target=str(payload["target"]),
        argv=tuple(str(value) for value in payload["argv"]),
    )
    with _resume_checkpoint_environment() as resume_environment:
        environment = {**_train_loop_environment(loop_config), **resume_environment}
        with _temporary_environment(environment):
            execute(entrypoint)


def build_trainer(config: DriverConfig, *, ray_components: Any | None = None) -> Any:
    """Construct a deterministic ``TorchTrainer`` without starting it."""

    storage_path = _validated_managed_storage(
        pathlib.Path(config.storage_path),
        _resolved(_STABLE_OUTPUT_ROOT),
    )
    ray_api = ray_components if ray_components is not None else _load_ray_components()
    workers = config.nodes * config.gpus_per_node
    cpus_per_worker = max(1, config.cpus_per_node // config.gpus_per_node)
    loop_config = {
        "entrypoint": dataclasses.asdict(config.entrypoint),
        "checkpoint_every_epochs": config.checkpoint_every_epochs,
        "keep_latest": config.keep_latest,
        "keep_best": config.keep_best,
        "best_metric": config.best_metric,
        "best_mode": config.best_mode,
        "job_id": config.job_id,
        "parent_job_id": config.parent_job_id,
        "storage_path": storage_path,
    }
    return ray_api.TorchTrainer(
        train_loop_per_worker=_train_loop,
        train_loop_config=loop_config,
        scaling_config=ray_api.ScalingConfig(
            num_workers=workers,
            use_gpu=True,
            resources_per_worker={"CPU": cpus_per_worker},
            placement_strategy="PACK",
        ),
        run_config=ray_api.RunConfig(
            name=config.job_id,
            storage_path=storage_path,
            failure_config=ray_api.FailureConfig(max_failures=config.max_failures),
            checkpoint_config=ray_api.CheckpointConfig(
                num_to_keep=None,
                checkpoint_score_attribute=None,
                checkpoint_score_order=config.best_mode,
            ),
        ),
    )


def main(argv: Sequence[str] | None = None) -> int:
    arguments = tuple(sys.argv[1:] if argv is None else argv)
    try:
        separator = arguments.index("--")
    except ValueError:
        separator = len(arguments)
    driver_arguments = arguments[:separator]
    if "-h" in driver_arguments or "--help" in driver_arguments:
        _parser().print_help()
        print("entrypoint: -- python file.py ... | -- python -m module ...")
        return 0
    try:
        config = parse_driver_config(arguments)
        trainer = build_trainer(config)
        trainer.fit()
    except ValueError as exc:
        print(f"raytrain-managed: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
