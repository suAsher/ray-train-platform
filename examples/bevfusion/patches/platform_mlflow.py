"""Bridge MMCV scalar logging to the RayTrain-owned MLflow run.

Copy this module to ``mmdet3d/utils/platform_mlflow.py`` in a BEVFusion
checkout. Only global rank zero creates a run; checkpoints and other files
continue to use ``PLATFORM_OUTPUT_PATH``.
"""

from __future__ import annotations

import os
import re
from typing import Any, Dict, Optional


_SAFE_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_RAY_VERSION = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$")


def _required_environment(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError("platform MLflow contract is missing {}".format(name))
    return value


def _mapping_value(value: Any, name: str, default: Any = None) -> Any:
    if value is None:
        return default
    getter = getattr(value, "get", None)
    if callable(getter):
        return getter(name, default)
    return default


def _optional_environment(name: str, pattern: Any = _SAFE_IDENTIFIER) -> str:
    value = os.environ.get(name, "").strip()
    if value and pattern.fullmatch(value) is None:
        raise RuntimeError("platform MLflow optional provenance is invalid")
    return value


def _streaming_provenance() -> Dict[str, str]:
    data_mode = os.environ.get("PLATFORM_DATA_MODE", "").strip()
    if data_mode not in ("", "mount", "cache", "ray-data", "ray-data-stage", "streaming"):
        raise RuntimeError("platform MLflow optional provenance is invalid")
    cache_policy = os.environ.get("PLATFORM_DATASET_CACHE_POLICY", "").strip()
    if cache_policy not in ("", "off", "auto", "bounded"):
        raise RuntimeError("platform MLflow optional provenance is invalid")
    values = {
        "data_mode": data_mode,
        "dataset_id": _optional_environment("PLATFORM_DATASET_ID"),
        "dataset_version_id": _optional_environment("PLATFORM_DATASET_VERSION_ID"),
        "dataset_cache_policy": cache_policy,
        "ray_version": _optional_environment("PLATFORM_RAY_VERSION", _RAY_VERSION),
    }
    if data_mode == "streaming" and not all(
        values[name]
        for name in (
            "dataset_id",
            "dataset_version_id",
            "dataset_cache_policy",
            "ray_version",
        )
    ):
        raise RuntimeError("platform MLflow streaming provenance is incomplete")
    return {name: value for name, value in values.items() if value}


def _platform_tags() -> Dict[str, str]:
    base = {
        "platform.job_id": _required_environment("RAYTRAIN_JOB_ID"),
        "platform.tenant_id": _required_environment("RAYTRAIN_TENANT_ID"),
        "platform.submitter_user_id": _required_environment(
            "RAYTRAIN_SUBMITTER_USER_ID"
        ),
        "platform.provenance": _required_environment("RAYTRAIN_MLFLOW_PROVENANCE"),
        "platform.cluster_attempt": _required_environment(
            "RAYTRAIN_CLUSTER_ATTEMPT"
        ),
    }
    provenance = _streaming_provenance()
    return {
        **base,
        **{"platform." + name: value for name, value in provenance.items()},
    }


def start_platform_mlflow(cfg: Any, rank: int, world_size: int) -> Optional[Any]:
    """Start the governed MLflow run and install MMCV's scalar logger."""
    tracking_uri = os.environ.get("MLFLOW_TRACKING_URI", "").strip()
    if not tracking_uri or rank != 0:
        return None

    import mlflow

    mlflow.set_tracking_uri(tracking_uri)
    mlflow.set_experiment(_required_environment("MLFLOW_EXPERIMENT_NAME"))
    mlflow.start_run(
        run_name=_required_environment("MLFLOW_RUN_NAME"),
        tags=_platform_tags(),
    )

    optimizer = _mapping_value(cfg, "optimizer", {})
    runner = _mapping_value(cfg, "runner", {})
    filename = getattr(cfg, "filename", "") or ""
    mlflow.log_params(
        {
            "config_file": os.path.basename(filename),
            "learning_rate": _mapping_value(optimizer, "lr"),
            "max_epochs": _mapping_value(runner, "max_epochs"),
            "optimizer": _mapping_value(optimizer, "type"),
            "seed": _mapping_value(cfg, "seed"),
            "world_size": world_size,
            **_streaming_provenance(),
        }
    )

    interval = int(cfg.log_config.get("interval", 10))
    cfg.log_config.hooks.append(
        {
            "type": "MlflowLoggerHook",
            "log_model": False,
            "interval": interval,
            "ignore_last": False,
            "reset_flag": False,
            "by_epoch": True,
        }
    )
    return mlflow


def finish_platform_mlflow(client: Optional[Any], status: str) -> None:
    """Finish a rank-zero run without changing the training exception path."""
    if client is not None and client.active_run() is not None:
        client.end_run(status=status)
