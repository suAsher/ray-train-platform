"""Resolve lightweight S1H manifest refs to bounded Parquet row groups."""

from __future__ import annotations

import os
import contextlib
import pathlib
import re
import time
from collections import OrderedDict
from collections.abc import Mapping, Sequence
from typing import Any

from .s1h_dataset import ROW_FIELD_NAMES


REF_FIELD_NAMES = (
    "ordinal",
    "token",
    "class_ids",
    "source_digest",
    "split",
    "shard_path",
    "row_index",
)
_SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_CACHE_POLICIES = frozenset(("off", "auto", "bounded"))


class S1HParquetBatchResolver:
    """Read referenced row groups and retain bounded immutable Arrow buffers.

    Cache keys include the published content-addressed shard path. Publishers
    must never replace content under an existing digest. The budget bounds
    retained groups, not the transient memory needed to decode one group or
    materialize the requested training batch.
    """

    def __init__(
        self,
        *,
        dataset_root: str | pathlib.Path,
        dataset_id: str,
        cache_policy: str,
        cache_paths: str = "",
        cache: Any | None = None,
        parquet_module: Any | None = None,
        row_group_cache_bytes: int = 128 * 1024 * 1024,
    ) -> None:
        self._root = _validated_root(dataset_root)
        self._dataset_id = _validated_identifier("dataset ID", dataset_id)
        if cache_policy not in _CACHE_POLICIES:
            raise ValueError("dataset cache policy is invalid")
        self._cache_policy = cache_policy
        self._parquet = parquet_module
        self._cache = cache
        if isinstance(row_group_cache_bytes, bool) or not isinstance(row_group_cache_bytes, int) or row_group_cache_bytes < 0:
            raise ValueError("row group cache bytes must be a nonnegative integer")
        self._row_group_budget = row_group_cache_bytes
        self._row_groups: OrderedDict[tuple[str, int], tuple[Any, int]] = OrderedDict()
        self._row_group_bytes = 0
        if cache_policy != "off" and cache is None:
            roots = tuple(
                pathlib.Path(value).resolve(strict=False)
                for value in cache_paths.split(":")
                if value.strip()
            )
            if not roots:
                if cache_policy == "bounded":
                    raise ValueError("bounded dataset cache requires cache paths")
            else:
                from .shard_cache import ShardCache

                self._cache = ShardCache(roots=roots, policy=cache_policy)

    def __call__(
        self,
        refs: Sequence[Mapping[str, Any]],
    ) -> tuple[dict[str, Any], ...]:
        if isinstance(refs, (str, bytes, bytearray)) or not isinstance(refs, Sequence):
            raise ValueError("S1H refs must be a sequence")
        validated = tuple(_validated_ref(ref) for ref in refs)
        if not validated:
            return ()

        sources = tuple(dict.fromkeys(ref["shard_path"] for ref in validated))
        rows_by_locator: dict[tuple[str, int], dict[str, Any]] = {}
        try:
            for relative in sources:
                source, digest = self._source_path(relative)
                indexes = tuple(
                    ref["row_index"]
                    for ref in validated
                    if ref["shard_path"] == relative
                )
                read_started = time.perf_counter()
                before = _cache_metrics(self._cache)
                try:
                    with self._readable(source, digest) as readable:
                        resolved, read_bytes = self._read_rows(readable, indexes)
                finally:
                    _observe_cache_delta(before, _cache_metrics(self._cache))
                read_seconds = max(time.perf_counter() - read_started, 1e-9)
                _observe_metric("dataset_shard_reads_total", 1)
                _observe_metric(
                    "dataset_cache_reads_total"
                    if readable != source
                    else "dataset_source_reads_total",
                    1,
                )
                _observe_metric(
                    "dataset_cache_read_seconds_total"
                    if readable != source
                    else "dataset_source_read_seconds_total",
                    read_seconds,
                )
                if read_bytes:
                    _observe_metric(
                        "dataset_cache_bytes_read_total"
                        if readable != source
                        else "dataset_source_bytes_total",
                        read_bytes,
                    )
                rows_by_locator = {
                    **rows_by_locator,
                    **{
                        (relative, index): row
                        for index, row in zip(indexes, resolved)
                    },
                }
        except (ValueError, RuntimeError):
            raise
        except Exception:
            raise RuntimeError("platform S1H shard read failed") from None

        try:
            return tuple(
                dict(rows_by_locator[(ref["shard_path"], ref["row_index"])])
                for ref in validated
            )
        except Exception:
            raise RuntimeError("platform S1H shard read failed") from None

    def _source_path(self, value: str) -> tuple[pathlib.Path, str]:
        pattern = re.compile(
            rf"^{re.escape(self._dataset_id)}/shards/sha256-([0-9a-f]{{64}})\.parquet$"
        )
        matched = pattern.fullmatch(value) if isinstance(value, str) else None
        if matched is None:
            raise ValueError("invalid shard reference")
        candidate = self._root.joinpath(*value.split("/")).resolve(strict=False)
        try:
            candidate.relative_to(self._root)
        except ValueError:
            raise ValueError("invalid shard reference") from None
        return candidate, matched.group(1)

    def _readable(self, source: pathlib.Path, digest: str):
        if self._cache is None:
            return contextlib.nullcontext(source)
        # Class lookup supports simple legacy cache adapters without treating
        # dynamically-created mock attributes as a lease implementation.
        lease = getattr(type(self._cache), "readable", None)
        if callable(lease):
            return lease(self._cache, source, digest)
        return contextlib.nullcontext(pathlib.Path(self._cache.resolve(source, digest)))

    def _read_rows(
        self,
        path: pathlib.Path,
        indexes: tuple[int, ...],
    ) -> tuple[tuple[dict[str, Any], ...], int]:
        parquet = self._parquet
        if parquet is None:
            try:
                import pyarrow.parquet as parquet
            except ModuleNotFoundError:
                raise RuntimeError("PyArrow is required for S1H streaming") from None
        try:
            with contextlib.ExitStack() as stack:
                parquet_file = parquet.ParquetFile(path)
                close = getattr(parquet_file, "close", None)
                if callable(close):
                    stack.callback(close)
                return self._read_open_rows(parquet_file, path, indexes)
        except (IndexError, KeyError, TypeError, ValueError):
            raise ValueError("invalid S1H shard row reference") from None
        except RuntimeError:
            raise
        except Exception:
            raise RuntimeError("platform S1H shard read failed") from None

    def _read_open_rows(self, parquet_file: Any, path: pathlib.Path, indexes: tuple[int, ...]):
        locations = _row_group_locations(parquet_file.metadata, indexes)
        group_ids = tuple(dict.fromkeys(group for group, _local in locations))
        read_bytes = 0
        rows = {}
        for group in group_ids:
            key = (str(path), group)
            cached = self._row_groups.get(key)
            if cached is not None:
                table = cached[0]
                self._row_groups.move_to_end(key)
            else:
                table = parquet_file.read_row_group(group, columns=list(ROW_FIELD_NAMES))
                self._remember_row_group(key, table)
                read_bytes += _selected_row_group_compressed_bytes(parquet_file.metadata, (group,))
            # Convert selected rows only; never expand an entire point cloud
            # row group into Python objects for a small batch.
            for local in dict.fromkeys(local for selected, local in locations if selected == group):
                rows[(group, local)] = table.slice(local, 1).to_pylist()[0]
        return tuple(dict(rows[location]) for location in locations), read_bytes

    def _remember_row_group(self, key: tuple[str, int], table: Any) -> None:
        # Arrow buffers avoid Python object expansion and expose their byte
        # footprint. Unknown/oversized tables are consumed but never retained.
        size = getattr(table, "nbytes", None)
        if isinstance(size, bool) or not isinstance(size, int) or not 0 < size <= self._row_group_budget:
            return
        while self._row_groups and (self._row_group_bytes + size > self._row_group_budget or len(self._row_groups) >= 8):
            _old_key, (_old_table, old_size) = self._row_groups.popitem(last=False)
            self._row_group_bytes -= old_size
        self._row_groups[key] = (table, size)
        self._row_group_bytes += size


def resolver_from_environment(
    environ: Mapping[str, str] | None = None,
) -> S1HParquetBatchResolver:
    values = dict(os.environ if environ is None else environ)
    root = values.get("PLATFORM_DATASET_ROOT", "").strip()
    dataset_id = values.get("PLATFORM_DATASET_ID", "").strip()
    cache_policy = values.get("PLATFORM_DATASET_CACHE_POLICY", "").strip()
    if not root or not dataset_id or not cache_policy:
        raise RuntimeError("platform streaming dataset provenance is unavailable")
    try:
        return S1HParquetBatchResolver(
            dataset_root=root,
            dataset_id=dataset_id,
            cache_policy=cache_policy,
            cache_paths=values.get("PLATFORM_CACHE_PATHS", ""),
        )
    except ValueError:
        raise RuntimeError("platform streaming dataset provenance is invalid") from None


_CACHE_METRIC_NAMES = {
    "hit": "dataset_cache_hits_total",
    "miss": "dataset_cache_misses_total",
    "download": "dataset_cache_downloads_total",
    "fallback": "dataset_cache_fallbacks_total",
    "checksum_failure": "dataset_cache_checksum_failures_total",
    "eviction": "dataset_cache_evictions_total",
    "stale_temp_reclaimed": "dataset_cache_stale_temp_reclaimed_total",
    "bytes": "dataset_cache_bytes_total",
}


def _observe_metric(name: str, value: int | float) -> None:
    try:
        from .data_metrics import observe_data_metric

        observe_data_metric(name, value)
    except Exception:
        # Metrics are supplementary and must never interrupt shard reads.
        return


def _selected_row_group_compressed_bytes(metadata: Any, group_ids: Sequence[int]) -> int:
    """Best-effort compressed bytes for only the selected Parquet row groups."""

    try:
        total = 0
        for group_id in group_ids:
            group = metadata.row_group(group_id)
            column_count = group.num_columns
            if (
                isinstance(column_count, bool)
                or not isinstance(column_count, int)
                or column_count < 0
            ):
                return 0
            for column_index in range(column_count):
                size = group.column(column_index).total_compressed_size
                if isinstance(size, bool) or not isinstance(size, int) or size < 0:
                    return 0
                total += size
        return total
    except Exception:
        return 0


def _cache_metrics(cache: Any) -> dict[str, float]:
    snapshot = getattr(cache, "metrics_snapshot", None)
    if not callable(snapshot):
        return {}
    try:
        values = snapshot()
    except Exception:
        return {}
    if not isinstance(values, Mapping):
        return {}
    result = {}
    for key in _CACHE_METRIC_NAMES:
        value = values.get(key)
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            continue
        result[key] = float(value)
    return result


def _observe_cache_delta(before: Mapping[str, float], after: Mapping[str, float]) -> None:
    for source_name, metric_name in _CACHE_METRIC_NAMES.items():
        delta = float(after.get(source_name, 0)) - float(before.get(source_name, 0))
        if delta > 0:
            _observe_metric(metric_name, delta)


def _validated_root(value: str | pathlib.Path) -> pathlib.Path:
    path = pathlib.Path(value)
    if not path.is_absolute():
        raise ValueError("dataset root must be an absolute path")
    resolved = path.resolve(strict=False)
    if resolved == pathlib.Path(resolved.anchor):
        raise ValueError("dataset root must not be the filesystem root")
    return resolved


def _validated_identifier(name: str, value: object) -> str:
    if not isinstance(value, str) or not _SAFE_ID.fullmatch(value) or value in {".", ".."}:
        raise ValueError(f"{name} is invalid")
    return value


def _validated_ref(value: object) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != set(REF_FIELD_NAMES):
        raise ValueError("invalid shard reference")
    try:
        ordinal = value["ordinal"]
        row_index = value["row_index"]
        token = _validated_identifier("token", value["token"])
        digest = value["source_digest"]
        split = value["split"]
        class_ids = tuple(value["class_ids"])
        shard_path = value["shard_path"]
    except Exception:
        raise ValueError("invalid shard reference") from None
    if (
        isinstance(ordinal, bool)
        or not isinstance(ordinal, int)
        or ordinal < 0
        or isinstance(row_index, bool)
        or not isinstance(row_index, int)
        or row_index < 0
        or not isinstance(digest, str)
        or not _SHA256.fullmatch(digest)
        or split != "train"
        or not class_ids
        or any(isinstance(item, bool) or not isinstance(item, int) or item < 0 for item in class_ids)
        or not isinstance(shard_path, str)
    ):
        raise ValueError("invalid shard reference")
    return {
        "ordinal": ordinal,
        "token": token,
        "class_ids": class_ids,
        "source_digest": digest,
        "split": split,
        "shard_path": shard_path,
        "row_index": row_index,
    }


def _row_group_locations(metadata: Any, indexes: tuple[int, ...]) -> tuple[tuple[int, int], ...]:
    if not indexes:
        return ()
    boundaries = []
    offset = 0
    try:
        for group in range(metadata.num_row_groups):
            count = metadata.row_group(group).num_rows
            if isinstance(count, bool) or not isinstance(count, int) or count <= 0:
                raise ValueError
            boundaries.append((group, offset, offset + count))
            offset += count
    except Exception:
        raise ValueError("invalid S1H Parquet metadata") from None
    locations = []
    for index in indexes:
        location = next(
            ((group, index - start) for group, start, end in boundaries if start <= index < end),
            None,
        )
        if location is None:
            raise ValueError("invalid S1H shard row reference")
        locations.append(location)
    return tuple(locations)
