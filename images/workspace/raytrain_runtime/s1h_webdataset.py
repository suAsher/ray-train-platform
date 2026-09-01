"""Batch-scoped S1H WebDataset materialization over the bounded NVMe cache."""

from __future__ import annotations

import contextlib
import json
import os
import pathlib
import posixpath
import re
import shutil
import tarfile
import tempfile
import time
from collections.abc import Iterator, Mapping, Sequence
from typing import Any

from .shard_cache import ShardCache


WEB_DATASET_REF_COLUMNS = (
    "ordinal",
    "token",
    "scene",
    "class_ids",
    "timestamp",
    "source_digest",
    "split",
    "shard_path",
    "shard_sha256",
    "shard_size",
    "metadata_member",
)
_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$")
_MAX_METADATA_BYTES = 4 * 1024 * 1024
_COPY_CHUNK_BYTES = 8 * 1024 * 1024


class WebDatasetBatchResolver:
    """Resolve immutable TAR refs and expose files for one pipeline batch only."""

    def __init__(
        self,
        *,
        dataset_root: str | os.PathLike[str],
        dataset_id: str,
        cache_policy: str,
        cache_roots: Sequence[str | os.PathLike[str]],
        staging_root: str | os.PathLike[str],
        cache_max_bytes: int | None = None,
    ) -> None:
        if not isinstance(dataset_id, str) or not _IDENTIFIER.fullmatch(dataset_id):
            raise ValueError("WebDataset dataset ID is invalid")
        self._dataset_root = _existing_directory(
            dataset_root, field_name="WebDataset root"
        )
        self._staging_root = _existing_directory(
            staging_root, field_name="WebDataset staging root"
        )
        self._dataset_id = dataset_id
        self._cache = ShardCache(
            roots=cache_roots,
            policy=cache_policy,
            max_bytes=cache_max_bytes,
            suffix=".tar",
        )

    @contextlib.contextmanager
    def resolve_batch(
        self, refs: Sequence[Mapping[str, Any]]
    ) -> Iterator[tuple[dict[str, Any], ...]]:
        normalized = tuple(self._validate_ref(ref) for ref in refs)
        if not normalized:
            raise ValueError("WebDataset batch must not be empty")
        with tempfile.TemporaryDirectory(
            prefix="raytrain-s1h-batch-", dir=self._staging_root
        ) as temporary:
            root = pathlib.Path(temporary)
            samples: dict[str, dict[str, Any]] = {}
            grouped: dict[tuple[str, str, int], list[dict[str, Any]]] = {}
            for ref in normalized:
                grouped.setdefault(
                    (ref["shard_path"], ref["shard_sha256"], ref["shard_size"]),
                    [],
                ).append(ref)
            for (shard_path, digest, expected_size), shard_refs in grouped.items():
                source = self._source_path(shard_path, digest)
                try:
                    if source.stat().st_size != expected_size:
                        raise ValueError("WebDataset shard size does not match manifest")
                except OSError:
                    raise ValueError("WebDataset shard is unavailable") from None
                before = _cache_metrics(self._cache)
                resolve_started = time.perf_counter()
                try:
                    readable = self._cache.resolve(source, digest)
                finally:
                    after = _cache_metrics(self._cache)
                    _observe_cache_delta(before, after)
                resolve_seconds = max(0.0, time.perf_counter() - resolve_started)
                downloaded = after.get("download", 0) > before.get("download", 0)
                if downloaded:
                    _observe_metric("dataset_source_reads_total", 1)
                    _observe_metric("dataset_source_bytes_total", expected_size)
                    _observe_metric(
                        "dataset_source_read_seconds_total", resolve_seconds
                    )
                read_started = time.perf_counter()
                self._extract_refs(
                    readable, shard_refs, output_root=root, samples=samples
                )
                read_seconds = max(0.0, time.perf_counter() - read_started)
                _observe_metric("dataset_shard_reads_total", 1)
                if readable == source:
                    _observe_metric("dataset_source_reads_total", 1)
                    _observe_metric("dataset_source_bytes_total", expected_size)
                    _observe_metric(
                        "dataset_source_read_seconds_total", read_seconds
                    )
                else:
                    _observe_metric("dataset_cache_reads_total", 1)
                    _observe_metric("dataset_cache_bytes_read_total", expected_size)
                    _observe_metric(
                        "dataset_cache_read_seconds_total", read_seconds
                    )
            if set(samples) != {ref["token"] for ref in normalized}:
                raise ValueError("WebDataset batch materialization is incomplete")
            yield tuple(samples[ref["token"]] for ref in normalized)

    def cache_metrics(self) -> dict[str, int]:
        return self._cache.metrics_snapshot()

    def _validate_ref(self, value: Mapping[str, Any]) -> dict[str, Any]:
        if not isinstance(value, Mapping) or set(value) != set(WEB_DATASET_REF_COLUMNS):
            raise ValueError("WebDataset reference fields are invalid")
        ref = dict(value)
        for field in ("token", "scene"):
            if not isinstance(ref[field], str) or not _IDENTIFIER.fullmatch(ref[field]):
                raise ValueError("WebDataset reference identity is invalid")
        if ref["split"] not in {"train", "val", "test"}:
            raise ValueError("WebDataset reference split is invalid")
        if not isinstance(ref["source_digest"], str) or not _DIGEST.fullmatch(
            ref["source_digest"]
        ):
            raise ValueError("WebDataset source digest is invalid")
        digest = ref["shard_sha256"]
        if not isinstance(digest, str) or not _DIGEST.fullmatch(digest):
            raise ValueError("WebDataset shard digest is invalid")
        if (
            isinstance(ref["shard_size"], bool)
            or not isinstance(ref["shard_size"], int)
            or ref["shard_size"] <= 0
            or isinstance(ref["ordinal"], bool)
            or not isinstance(ref["ordinal"], int)
            or ref["ordinal"] < 0
            or isinstance(ref["timestamp"], bool)
            or not isinstance(ref["timestamp"], int)
        ):
            raise ValueError("WebDataset reference numeric fields are invalid")
        classes = ref["class_ids"]
        if not isinstance(classes, Sequence) or isinstance(classes, (str, bytes)) or any(
            isinstance(item, bool) or not isinstance(item, int) or item < 0
            for item in classes
        ):
            raise ValueError("WebDataset reference class IDs are invalid")
        expected_path = f"{self._dataset_id}/shards/sha256-{digest}.tar"
        if ref["shard_path"] != expected_path:
            raise ValueError("WebDataset shard path is invalid")
        expected_metadata = f"{ref['token']}/metadata.json"
        if ref["metadata_member"] != expected_metadata:
            raise ValueError("WebDataset metadata member is invalid")
        ref["class_ids"] = tuple(classes)
        return ref

    def _source_path(self, relative: str, digest: str) -> pathlib.Path:
        del digest
        if (
            posixpath.normpath(relative) != relative
            or relative.startswith("/")
            or any(part in {"", ".", ".."} for part in relative.split("/"))
        ):
            raise ValueError("WebDataset shard path is invalid")
        candidate = self._dataset_root.joinpath(*relative.split("/")).resolve(
            strict=False
        )
        try:
            candidate.relative_to(self._dataset_root)
        except ValueError:
            raise ValueError("WebDataset shard path escapes its root") from None
        return candidate

    def _extract_refs(
        self,
        shard: pathlib.Path,
        refs: list[dict[str, Any]],
        *,
        output_root: pathlib.Path,
        samples: dict[str, dict[str, Any]],
    ) -> None:
        try:
            with tarfile.open(shard, mode="r:") as archive:
                members = {member.name: member for member in archive.getmembers()}
                if len(members) != len(archive.getmembers()):
                    raise ValueError("WebDataset TAR contains duplicate members")
                for ref in refs:
                    metadata = self._read_metadata(
                        archive, members, ref["metadata_member"]
                    )
                    self._validate_metadata(metadata, ref)
                    payloads = metadata["payload_members"]
                    selected = (
                        payloads["lidar"],
                        *payloads["sweeps"],
                        *(payloads["cameras"][name] for name in sorted(payloads["cameras"])),
                    )
                    for name in selected:
                        self._extract_member(
                            archive,
                            members,
                            name,
                            token=ref["token"],
                            output_root=output_root,
                        )
                    samples[ref["token"]] = {
                        "token": ref["token"],
                        "scene": ref["scene"],
                        "split": ref["split"],
                        "class_ids": list(ref["class_ids"]),
                        "timestamp": ref["timestamp"],
                        "source_digest": ref["source_digest"],
                        "info": metadata["info"],
                        "payload_paths": {
                            "lidar": str(output_root / payloads["lidar"]),
                            "sweeps": [str(output_root / name) for name in payloads["sweeps"]],
                            "cameras": {
                                camera: str(output_root / name)
                                for camera, name in payloads["cameras"].items()
                            },
                        },
                    }
        except (OSError, tarfile.TarError):
            raise ValueError("WebDataset TAR is invalid") from None

    def _read_metadata(
        self,
        archive: tarfile.TarFile,
        members: Mapping[str, tarfile.TarInfo],
        name: str,
    ) -> dict[str, Any]:
        member = members.get(name)
        if member is None or not member.isfile() or not 0 < member.size <= _MAX_METADATA_BYTES:
            raise ValueError("WebDataset metadata member is invalid")
        stream = archive.extractfile(member)
        if stream is None:
            raise ValueError("WebDataset metadata member is invalid")
        try:
            value = json.loads(stream.read(_MAX_METADATA_BYTES + 1))
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise ValueError("WebDataset metadata member is invalid") from None
        if type(value) is not dict:
            raise ValueError("WebDataset metadata member is invalid")
        return value

    def _validate_metadata(
        self, metadata: Mapping[str, Any], ref: Mapping[str, Any]
    ) -> None:
        required = {
            "token",
            "scene",
            "split",
            "class_ids",
            "timestamp",
            "source_digest",
            "payload_members",
            "info",
        }
        if set(metadata) != required or any(
            metadata[field] != ref[field]
            for field in ("token", "scene", "split", "timestamp", "source_digest")
        ) or tuple(metadata["class_ids"]) != tuple(ref["class_ids"]):
            raise ValueError("WebDataset metadata does not match manifest")
        payloads = metadata["payload_members"]
        if (
            type(payloads) is not dict
            or set(payloads) != {"lidar", "sweeps", "cameras"}
            or not isinstance(payloads["lidar"], str)
            or type(payloads["sweeps"]) is not list
            or type(payloads["cameras"]) is not dict
            or not payloads["cameras"]
        ):
            raise ValueError("WebDataset payload member map is invalid")
        for name in (
            payloads["lidar"],
            *payloads["sweeps"],
            *payloads["cameras"].values(),
        ):
            _validate_member_name(name, token=ref["token"])
        if type(metadata["info"]) is not dict:
            raise ValueError("WebDataset sample info is invalid")

    def _extract_member(
        self,
        archive: tarfile.TarFile,
        members: Mapping[str, tarfile.TarInfo],
        name: str,
        *,
        token: str,
        output_root: pathlib.Path,
    ) -> None:
        _validate_member_name(name, token=token)
        member = members.get(name)
        if member is None or not member.isfile() or member.size <= 0:
            raise ValueError("WebDataset payload member is invalid")
        destination = output_root.joinpath(*name.split("/"))
        destination.parent.mkdir(parents=True, exist_ok=True)
        source = archive.extractfile(member)
        if source is None:
            raise ValueError("WebDataset payload member is invalid")
        with destination.open("xb") as output:
            copied = shutil.copyfileobj(source, output, length=_COPY_CHUNK_BYTES)
            del copied
        if destination.stat().st_size != member.size:
            raise ValueError("WebDataset payload extraction is incomplete")


def _existing_directory(
    value: str | os.PathLike[str], *, field_name: str
) -> pathlib.Path:
    try:
        path = pathlib.Path(value)
        if not path.is_absolute() or path.is_symlink():
            raise ValueError
        resolved = path.resolve(strict=True)
        if not resolved.is_dir():
            raise ValueError
        return resolved
    except (OSError, TypeError, ValueError):
        raise ValueError(f"{field_name} is unavailable") from None


def _validate_member_name(value: object, *, token: str) -> str:
    if (
        not isinstance(value, str)
        or not value.startswith(token + "/")
        or value.startswith("/")
        or "\\" in value
        or posixpath.normpath(value) != value
        or any(part in {"", ".", ".."} for part in value.split("/"))
    ):
        raise ValueError("WebDataset member path is invalid")
    return value


def resolver_from_environment(
    environ: Mapping[str, str] | None = None,
) -> WebDatasetBatchResolver:
    values = dict(os.environ if environ is None else environ)
    root = values.get("PLATFORM_DATASET_ROOT", "").strip()
    dataset_id = values.get("PLATFORM_DATASET_ID", "").strip()
    policy = values.get("PLATFORM_DATASET_CACHE_POLICY", "").strip()
    staging = values.get("RAYTRAIN_DATASET_STAGING_ROOT", "/tmp").strip()
    cache_roots = tuple(
        item for item in values.get("PLATFORM_CACHE_PATHS", "").split(":") if item
    )
    if not root or not dataset_id or not policy or not staging:
        raise RuntimeError("platform WebDataset provenance is unavailable")
    try:
        return WebDatasetBatchResolver(
            dataset_root=root,
            dataset_id=dataset_id,
            cache_policy=policy,
            cache_roots=cache_roots,
            staging_root=staging,
        )
    except ValueError:
        raise RuntimeError("platform WebDataset provenance is invalid") from None


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


def _cache_metrics(cache: Any) -> dict[str, float]:
    try:
        values = cache.metrics_snapshot()
    except Exception:
        return {}
    if not isinstance(values, Mapping):
        return {}
    return {
        key: float(value)
        for key in _CACHE_METRIC_NAMES
        if not isinstance((value := values.get(key)), bool)
        and isinstance(value, (int, float))
    }


def _observe_cache_delta(
    before: Mapping[str, float], after: Mapping[str, float]
) -> None:
    for source_name, metric_name in _CACHE_METRIC_NAMES.items():
        delta = float(after.get(source_name, 0)) - float(before.get(source_name, 0))
        if delta > 0:
            _observe_metric(metric_name, delta)


def _observe_metric(name: str, value: int | float) -> None:
    try:
        from .data_metrics import observe_data_metric

        observe_data_metric(name, value)
    except Exception:
        return
