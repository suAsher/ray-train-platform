"""Process-local, path-free metrics for managed streaming datasets."""

from __future__ import annotations

import math
import numbers
import threading
from collections import deque
from collections.abc import Mapping


DATA_METRIC_NAMES = frozenset(
    (
        "dataset_batches_total",
        "dataset_samples_total",
        "dataset_shard_reads_total",
        "dataset_source_reads_total",
        "dataset_cache_reads_total",
        "dataset_source_bytes_total",
        "dataset_cache_bytes_read_total",
        "dataset_source_read_seconds_total",
        "dataset_cache_read_seconds_total",
        "dataset_prefetch_wait_seconds_total",
        "dataset_cache_hits_total",
        "dataset_cache_misses_total",
        "dataset_cache_downloads_total",
        "dataset_cache_fallbacks_total",
        "dataset_cache_checksum_failures_total",
        "dataset_cache_evictions_total",
        "dataset_cache_stale_temp_reclaimed_total",
        "dataset_cache_bytes_total",
    )
)

_VALUES = {name: 0.0 for name in DATA_METRIC_NAMES}
_P95_METRICS = {
    "dataset_source_read_seconds_total": "dataset_source_read_p95_seconds",
    "dataset_cache_read_seconds_total": "dataset_cache_read_p95_seconds",
    "dataset_prefetch_wait_seconds_total": "dataset_prefetch_wait_p95_seconds",
}
_P95_WINDOW_SIZE = 1024
_OBSERVATIONS = {
    name: deque(maxlen=_P95_WINDOW_SIZE) for name in _P95_METRICS
}
_LOCK = threading.Lock()


def observe_data_metric(name: str, value: numbers.Real) -> None:
    """Add one non-negative finite observation without accepting arbitrary keys."""

    if name not in DATA_METRIC_NAMES:
        raise ValueError("runtime data metric is unsupported")
    if isinstance(value, bool) or not isinstance(value, numbers.Real):
        raise ValueError("runtime data metric value must be non-negative and finite")
    amount = float(value)
    if amount < 0 or not math.isfinite(amount):
        raise ValueError("runtime data metric value must be non-negative and finite")
    if amount == 0:
        return
    with _LOCK:
        _VALUES[name] += amount
        if name in _OBSERVATIONS:
            _OBSERVATIONS[name].append(amount)


def observe_data_metrics(values: Mapping[str, numbers.Real]) -> None:
    """Apply a copied metric mapping through the same strict allowlist."""

    for name, value in dict(values).items():
        observe_data_metric(name, value)


def snapshot_data_metrics() -> dict[str, float]:
    """Return an independent snapshot containing only observed values."""

    with _LOCK:
        counters = {name: value for name, value in _VALUES.items() if value > 0}
        percentiles = {
            _P95_METRICS[name]: _nearest_rank_p95(values)
            for name, values in _OBSERVATIONS.items()
            if values
        }
        return {**counters, **percentiles}


def reset_data_metrics_for_tests() -> None:
    """Reset process-local counters; production code never calls this helper."""

    with _LOCK:
        for name in _VALUES:
            _VALUES[name] = 0.0
        for values in _OBSERVATIONS.values():
            values.clear()


def _nearest_rank_p95(values: deque[float]) -> float:
    ordered = sorted(values)
    rank = max(1, math.ceil(0.95 * len(ordered)))
    return ordered[rank - 1]
