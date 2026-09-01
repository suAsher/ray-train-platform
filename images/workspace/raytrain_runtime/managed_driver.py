"""Build and run the platform-owned Ray Train ``TorchTrainer``."""

from __future__ import annotations

import argparse
import contextlib
import dataclasses
import importlib.util
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
_RAY_JOB_WORKING_DIR_URI = re.compile(r"^gcs://_ray_pkg_[0-9a-f]+(?:\.zip)?$")
_RAY_VERSION = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$")
_MANAGED_RUNTIME_ROOT = pathlib.Path(__file__).resolve().parent.parent
_PREPARE_HOOK_ENV = "RAYTRAIN_MANAGED_PREPARE_HOOK"


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
    data_mode: str = "mount"
    dataset: Any | None = None
    ray_version: str = ""


@dataclasses.dataclass(frozen=True)
class _RayComponents:
    TorchTrainer: Any
    ScalingConfig: Any
    RunConfig: Any
    FailureConfig: Any
    CheckpointConfig: Any
    DataConfig: Any


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
    parser.add_argument(
        "--data-mode",
        choices=("mount", "cache", "ray-data", "ray-data-stage", "streaming"),
        default="mount",
    )
    parser.add_argument("--dataset-format", default="")
    parser.add_argument("--dataset-uri", default="")
    parser.add_argument("--dataset-id", default="")
    parser.add_argument("--dataset-version-id", default="")
    parser.add_argument("--dataset-manifest-path", default="")
    parser.add_argument("--dataset-manifest-sha256", default="")
    parser.add_argument("--dataset-root", default="")
    parser.add_argument("--dataset-train-samples", type=int)
    parser.add_argument(
        "--dataset-cache-policy", choices=("off", "auto", "bounded"), default=""
    )
    parser.add_argument("--dataset-prefetch-batches", type=int)
    parser.add_argument("--dataset-shuffle-seed", type=int)
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
    ray_version = environment.get("PLATFORM_RAY_VERSION", "").strip()
    if ray_version and not _RAY_VERSION.fullmatch(ray_version):
        raise ValueError("Ray version provenance is invalid")

    dataset = None
    streaming_values = (
        options.dataset_id,
        options.dataset_version_id,
        options.dataset_manifest_path,
        options.dataset_manifest_sha256,
        options.dataset_root,
        options.dataset_train_samples,
        options.dataset_cache_policy,
        options.dataset_prefetch_batches,
        options.dataset_shuffle_seed,
    )
    if options.data_mode == "streaming":
        if options.dataset_format or options.dataset_uri:
            raise ValueError("dataset format and URI are not used by streaming mode")
        from .ray_data import StreamingDatasetConfig

        def selected(option: Any, environment_name: str, default: Any = "") -> Any:
            if option is not None and option != "":
                return option
            return environment.get(environment_name, default)

        try:
            train_samples = int(
                selected(
                    options.dataset_train_samples,
                    "PLATFORM_DATASET_TRAIN_SAMPLES",
                )
            )
            prefetch_batches = int(
                selected(
                    options.dataset_prefetch_batches,
                    "RAYTRAIN_DATASET_PREFETCH_BATCHES",
                    2,
                )
            )
            shuffle_seed = int(
                selected(
                    options.dataset_shuffle_seed,
                    "RAYTRAIN_DATASET_SHUFFLE_SEED",
                    0,
                )
            )
        except (TypeError, ValueError):
            raise ValueError("streaming dataset numeric provenance is invalid") from None
        dataset = StreamingDatasetConfig(
            dataset_id=str(
                selected(options.dataset_id, "PLATFORM_DATASET_ID")
            ),
            version_id=str(
                selected(
                    options.dataset_version_id,
                    "PLATFORM_DATASET_VERSION_ID",
                )
            ),
            manifest_path=str(
                selected(
                    options.dataset_manifest_path,
                    "PLATFORM_DATASET_MANIFEST_PATH",
                )
            ),
            manifest_sha256=str(
                selected(
                    options.dataset_manifest_sha256,
                    "PLATFORM_DATASET_MANIFEST_SHA256",
                )
            ),
            dataset_root=str(
                selected(options.dataset_root, "PLATFORM_DATASET_ROOT")
            ),
            train_samples=train_samples,
            cache_policy=str(
                selected(
                    options.dataset_cache_policy,
                    "PLATFORM_DATASET_CACHE_POLICY",
                )
            ),
            schema_version=str(
                environment.get(
                    "PLATFORM_DATASET_SCHEMA_VERSION",
                    "s1h-lidar-parquet-v1",
                )
            ),
            prefetch_batches=prefetch_batches,
            shuffle_seed=shuffle_seed,
        )
    elif options.data_mode in ("ray-data", "ray-data-stage"):
        from .ray_data import DatasetConfig

        dataset = DatasetConfig(format=options.dataset_format, uri=options.dataset_uri)
        if any(value not in (None, "") for value in streaming_values):
            raise ValueError("streaming dataset provenance requires streaming mode")
    elif options.dataset_format or options.dataset_uri:
        raise ValueError("dataset format and URI require ray-data mode")
    elif any(value not in (None, "") for value in streaming_values):
        raise ValueError("streaming dataset provenance requires streaming mode")

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
        data_mode=options.data_mode,
        dataset=dataset,
        ray_version=ray_version,
    )


def _load_ray_components() -> _RayComponents:
    from ray.train import CheckpointConfig, DataConfig, FailureConfig, RunConfig, ScalingConfig
    from ray.train.torch import TorchTrainer

    return _RayComponents(
        TorchTrainer=TorchTrainer,
        ScalingConfig=ScalingConfig,
        RunConfig=RunConfig,
        FailureConfig=FailureConfig,
        CheckpointConfig=CheckpointConfig,
        DataConfig=DataConfig,
    )


def _validated_worker_runtime_env(runtime_env: Mapping[str, Any]) -> dict[str, str]:
    """Reuse only the immutable code package created by the enclosing Ray Job."""

    working_dir = str(runtime_env.get("working_dir", "")).strip()
    if not _RAY_JOB_WORKING_DIR_URI.fullmatch(working_dir):
        raise ValueError("managed Ray Train requires a Ray Job gcs:// working directory URI")
    return {"working_dir": working_dir}


def _current_job_worker_runtime_env() -> dict[str, str]:
    import ray

    if not ray.is_initialized():
        ray.init(address="auto")
    return _validated_worker_runtime_env(ray.get_runtime_context().runtime_env)


def _load_ray_data_dataset(config: Any) -> Any:
    from .ray_data import build_dataset

    return build_dataset(config)


def _load_s1h_streaming_dataset(
    config: Any,
    *,
    world_size: int,
) -> tuple[Any, int, int]:
    from .ray_data import build_s1h_streaming_dataset

    return build_s1h_streaming_dataset(config, world_size=world_size)


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
    environment = {
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
    ray_version = str(loop_config.get("ray_version", "")).strip()
    if ray_version:
        if not _RAY_VERSION.fullmatch(ray_version):
            raise ValueError("Ray version provenance is invalid")
        environment = {**environment, "PLATFORM_RAY_VERSION": ray_version}
    if str(loop_config.get("data_mode", "")) == "streaming":
        environment = {
            **environment,
            "PLATFORM_DATA_MODE": "streaming",
            "PLATFORM_DATASET_ID": str(loop_config["dataset_id"]),
            "PLATFORM_DATASET_VERSION_ID": str(
                loop_config["dataset_version_id"]
            ),
            "PLATFORM_DATASET_MANIFEST_SHA256": str(
                loop_config["dataset_manifest_sha256"]
            ),
            "PLATFORM_DATASET_ROOT": str(loop_config["dataset_root"]),
            "PLATFORM_DATASET_CACHE_POLICY": str(
                loop_config["dataset_cache_policy"]
            ),
            "PLATFORM_DATASET_SCHEMA_VERSION": str(
                loop_config.get(
                    "dataset_schema_version", "s1h-lidar-parquet-v1"
                )
            ),
            "RAYTRAIN_DATASET_WORKER_SAMPLES": str(
                int(loop_config["worker_sample_count"])
            ),
            "RAYTRAIN_DATASET_PADDING_COUNT": str(
                int(loop_config["dataset_padding_count"])
            ),
            "RAYTRAIN_DATASET_PREFETCH_BATCHES": str(
                int(loop_config["dataset_prefetch_batches"])
            ),
            "RAYTRAIN_DATASET_SHUFFLE_SEED": str(
                int(loop_config["dataset_shuffle_seed"])
            ),
        }
    return environment


def _stage_ray_data_for_worker(loop_config: Mapping[str, Any]) -> None:
    if str(loop_config.get("data_mode", "")) != "ray-data-stage":
        return
    from ray import train
    from ray.train import collective
    from .ray_data import stage_binary_dataset

    context = train.get_context()
    if context.get_local_rank() == 0:
        iterator = train.get_dataset_shard("train")
        if iterator is None:
            raise RuntimeError("Ray Data staging dataset is unavailable")
        stage_binary_dataset(
            iterator,
            source_root=os.environ.get("PLATFORM_DATASET_SOURCE_PATH", "/mnt/data/input"),
            cache_paths=os.environ.get("PLATFORM_CACHE_PATHS", ""),
            copy_workers=int(os.environ.get("RAYTRAIN_RAY_DATA_STAGE_WORKERS", "64")),
        )
    collective.barrier()
    dataset_path = pathlib.Path(os.environ.get("PLATFORM_DATASET_PATH", ""))
    if not dataset_path.is_dir():
        raise RuntimeError(f"Ray Data staged view is unavailable: {dataset_path}")


def _load_resume_checkpoint() -> Any | None:
    """Load Ray's worker checkpoint lazily so this module remains importable alone."""

    try:
        from ray import train
    except ImportError:
        return None
    try:
        return train.get_checkpoint()
    except RuntimeError as exc:
        # Ray 2.56 raises when this helper is exercised outside a Trainer worker
        # (for example by image smoke tests).  Treat only that documented
        # context error as "no resume checkpoint"; all real checkpoint errors
        # must still fail the run.
        if "cannot be used outside of a Ray Train training function" not in str(exc):
            raise
        return None


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
    _stage_ray_data_for_worker(loop_config)
    with _resume_checkpoint_environment() as resume_environment:
        environment = {**_train_loop_environment(loop_config), **resume_environment}
        with _temporary_environment(environment):
            _run_worker_prepare_hook()
            execute(entrypoint)


def _run_worker_prepare_hook(
    *, runtime_root: pathlib.Path = _MANAGED_RUNTIME_ROOT
) -> None:
    """Run an optional image-owned hook before importing user training code."""

    raw_hook = os.environ.get(_PREPARE_HOOK_ENV, "").strip()
    if not raw_hook:
        return
    relative = pathlib.PurePosixPath(raw_hook)
    if (
        relative.is_absolute()
        or not relative.parts
        or any(part in ("", ".", "..") for part in relative.parts)
        or relative.suffix != ".py"
        or "\\" in raw_hook
        or len(raw_hook.encode("utf-8")) > 512
    ):
        raise RuntimeError("managed worker prepare hook is invalid")
    trusted_root = pathlib.Path(runtime_root).resolve(strict=True)
    hook_path = trusted_root.joinpath(*relative.parts).resolve(strict=True)
    try:
        hook_path.relative_to(trusted_root)
    except ValueError:
        raise RuntimeError("managed worker prepare hook is invalid") from None
    if not hook_path.is_file():
        raise RuntimeError("managed worker prepare hook is invalid")
    try:
        spec = importlib.util.spec_from_file_location(
            "_raytrain_managed_prepare_hook", hook_path
        )
        if spec is None or spec.loader is None:
            raise RuntimeError
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        prepare = getattr(module, "prepare_current_worker", None)
        if not callable(prepare):
            raise RuntimeError
        prepare()
    except Exception:
        raise RuntimeError("managed worker prepare hook failed") from None


def _cpus_per_train_worker(config: DriverConfig) -> int:
    """Reserve node CPU for Ray Data operators when datasets are managed by Ray."""

    cpus_per_worker = max(1, config.cpus_per_node // config.gpus_per_node)
    if config.data_mode not in ("ray-data", "ray-data-stage", "streaming"):
        return cpus_per_worker

    maximum_headroom = config.cpus_per_node - config.gpus_per_node
    if maximum_headroom < 1:
        raise ValueError(
            "Ray Data requires CPU headroom beyond one CPU per training worker"
        )

    desired_headroom = min(8, max(4, config.cpus_per_node // 8))
    reserved_headroom = min(desired_headroom, maximum_headroom)
    return max(
        1,
        (config.cpus_per_node - reserved_headroom) // config.gpus_per_node,
    )


def build_trainer(
    config: DriverConfig,
    *,
    ray_components: Any | None = None,
    worker_runtime_env: Mapping[str, Any] | None = None,
) -> Any:
    """Construct a deterministic ``TorchTrainer`` without starting it."""

    storage_path = _validated_managed_storage(
        pathlib.Path(config.storage_path),
        _resolved(_STABLE_OUTPUT_ROOT),
    )
    ray_api = ray_components if ray_components is not None else _load_ray_components()
    if worker_runtime_env is None:
        worker_runtime_env = (
            {} if ray_components is not None else _current_job_worker_runtime_env()
        )
    workers = config.nodes * config.gpus_per_node
    cpus_per_worker = _cpus_per_train_worker(config)
    ray_copy_limit = config.keep_latest + config.keep_best
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
        "data_mode": config.data_mode,
        "ray_version": config.ray_version,
    }
    trainer_options = {
        "train_loop_per_worker": _train_loop,
        "train_loop_config": loop_config,
        "scaling_config": ray_api.ScalingConfig(
            num_workers=workers,
            use_gpu=True,
            resources_per_worker={"CPU": cpus_per_worker},
            placement_strategy="PACK",
        ),
        "run_config": ray_api.RunConfig(
            name=config.job_id,
            storage_path=storage_path,
            # Ray Train workers do not automatically inherit the enclosing Ray
            # Job's code. Reuse its immutable GCS package URI; passing the local
            # extracted path is rejected by Ray and is not portable to workers.
            worker_runtime_env=dict(worker_runtime_env),
            failure_config=ray_api.FailureConfig(max_failures=config.max_failures),
            checkpoint_config=ray_api.CheckpointConfig(
                num_to_keep=ray_copy_limit or None,
            ),
        ),
    }
    if config.data_mode in ("ray-data", "ray-data-stage"):
        if config.dataset is None:
            raise ValueError("ray-data mode requires a dataset config")
        trainer_options["datasets"] = {
            "train": _load_ray_data_dataset(config.dataset)
        }
        if config.data_mode == "ray-data-stage":
            trainer_options["dataset_config"] = ray_api.DataConfig(datasets_to_split=[])
    elif config.data_mode == "streaming":
        if config.dataset is None:
            raise ValueError("streaming mode requires pinned dataset provenance")
        streaming, samples_per_worker, padding_count = (
            _load_s1h_streaming_dataset(config.dataset, world_size=workers)
        )
        trainer_options["datasets"] = {"train": streaming}
        loop_config = {
            **loop_config,
            "worker_sample_count": samples_per_worker,
            "dataset_padding_count": padding_count,
            "dataset_id": config.dataset.dataset_id,
            "dataset_version_id": config.dataset.version_id,
            "dataset_manifest_sha256": config.dataset.manifest_sha256,
            "dataset_root": config.dataset.dataset_root,
            "dataset_cache_policy": config.dataset.cache_policy,
            "dataset_schema_version": config.dataset.schema_version,
            "dataset_prefetch_batches": config.dataset.prefetch_batches,
            "dataset_shuffle_seed": config.dataset.shuffle_seed,
        }
        trainer_options["train_loop_config"] = loop_config
    return ray_api.TorchTrainer(**trainer_options)


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
