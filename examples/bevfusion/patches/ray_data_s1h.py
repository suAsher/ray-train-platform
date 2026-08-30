"""Minimal BEVFusion training-loader adapter for platform S1H Ray Data."""

from __future__ import annotations

import dataclasses
from collections.abc import Callable, Iterator, Mapping, Sequence
from typing import Any


_NATIVE_S1H_MODES = frozenset(("streaming",))
_LEGACY_DATA_MODES = frozenset(("mount", "cache", "ray-data", "ray-data-stage"))
_MAX_PREFETCH_BATCHES = 16


@dataclasses.dataclass(frozen=True)
class S1HWorkerDataLoader:
    """MMCV-compatible iterable over the current Ray Train worker shard.

    Ray Data already owns worker partitioning. This adapter therefore exposes
    no sampler and batches decoded rows with the config's per-GPU batch size.
    The platform supplies the worker sample count as provenance so an
    epoch-based runner can determine its number of iterations. Iteration also
    enforces that exact count before accepting each complete batch.
    """

    pipeline: Callable[[dict[str, Any]], Any]
    collate_fn: Callable[..., Any]
    samples_per_gpu: int
    worker_sample_count: int
    prefetch_batches: int = 2
    dataset_name: str = "train"
    batch_resolver: Callable[
        [Sequence[Mapping[str, Any]]], Sequence[Mapping[str, Any]]
    ] | None = None
    sampler: None = dataclasses.field(default=None, init=False)
    batch_sampler: None = dataclasses.field(default=None, init=False)

    def __post_init__(self) -> None:
        if not callable(self.pipeline):
            raise ValueError("pipeline must be callable")
        if not callable(self.collate_fn):
            raise ValueError("collate_fn must be callable")
        _positive_integer("samples_per_gpu", self.samples_per_gpu)
        _positive_integer("worker_sample_count", self.worker_sample_count)
        if (
            isinstance(self.prefetch_batches, bool)
            or not isinstance(self.prefetch_batches, int)
            or not 0 <= self.prefetch_batches <= _MAX_PREFETCH_BATCHES
        ):
            raise ValueError(
                f"prefetch_batches must be between 0 and {_MAX_PREFETCH_BATCHES}"
            )
        if self.dataset_name != "train":
            raise ValueError("dataset_name must be train for the S1H training loader")
        if self.batch_resolver is not None and not callable(self.batch_resolver):
            raise ValueError("batch_resolver must be callable")

    def __len__(self) -> int:
        return (
            self.worker_sample_count + self.samples_per_gpu - 1
        ) // self.samples_per_gpu

    def __iter__(self) -> Iterator[Any]:
        from raytrain_runtime.ray_data import worker_s1h_batches

        worker_arguments = {
            "name": self.dataset_name,
            "samples_per_gpu": self.samples_per_gpu,
            "prefetch_batches": self.prefetch_batches,
            "pipeline": self.pipeline,
        }
        if self.batch_resolver is not None:
            worker_arguments["batch_resolver"] = self.batch_resolver
        observed_sample_count = 0
        for samples in worker_s1h_batches(
            **worker_arguments,
        ):
            try:
                batch_sample_count = len(samples)
            except Exception:
                raise RuntimeError(
                    "S1H worker shard returned an invalid batch"
                ) from None
            next_sample_count = observed_sample_count + batch_sample_count
            if next_sample_count > self.worker_sample_count:
                raise RuntimeError(
                    "S1H worker shard exceeded declared sample count"
                )
            observed_sample_count = next_sample_count
            yield self.collate_fn(
                samples,
                samples_per_gpu=self.samples_per_gpu,
            )
        if observed_sample_count != self.worker_sample_count:
            raise RuntimeError(
                "S1H worker shard ended before declared sample count"
            )


def build_bevfusion_train_dataloader(
    *,
    data_mode: str,
    legacy_builder: Callable[[], Any],
    pipeline: Callable[[dict[str, Any]], Any],
    collate_fn: Callable[..., Any],
    samples_per_gpu: int,
    worker_sample_count: int,
    prefetch_batches: int = 2,
    batch_resolver: Callable[
        [Sequence[Mapping[str, Any]]], Sequence[Mapping[str, Any]]
    ] | None = None,
) -> Any:
    """Select the S1H shard loader only for an explicit native data mode.

    ``legacy_builder`` remains the sole path for mount, cache, existing
    ``ray-data``, and staging modes, preserving their existing behavior.
    """

    if not callable(legacy_builder):
        raise ValueError("legacy_builder must be callable")
    if data_mode in _LEGACY_DATA_MODES:
        return legacy_builder()
    if data_mode not in _NATIVE_S1H_MODES:
        raise ValueError("data_mode is not supported by the S1H adapter")
    return S1HWorkerDataLoader(
        pipeline=pipeline,
        collate_fn=collate_fn,
        samples_per_gpu=samples_per_gpu,
        worker_sample_count=worker_sample_count,
        prefetch_batches=prefetch_batches,
        batch_resolver=batch_resolver,
    )


def _positive_integer(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError(f"{name} must be a positive integer")
    return value
