"""Cloud entrypoint for bounded immutable dataset publication."""

from __future__ import annotations

import argparse
import hashlib
import json
import posixpath
import re
import sys
import tempfile
from collections import Counter
from collections.abc import Mapping
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from dataclasses import dataclass
from itertools import groupby
from pathlib import Path
from threading import local
from typing import Any

from .irsa import VKEIRSAProvider
from .pack import (
    MAX_SHARD_BYTES,
    PackConfig,
    TrustedIndexDocument,
    _prepare_output_directory,
    _require_pyarrow,
    _sha256_file,
    _validated_sorted_index_samples,
    _validate_publication_id,
    _write_parquet_shard,
    build_cbgs_sample_plan,
    load_trusted_index_manifest,
    load_trusted_index_document,
    plan_shards,
    prepare_rows,
)
from .schema import dump_trusted_info
from .site_metadata import sample_site_id
from .tos_storage import (
    MAX_INDEX_BYTES,
    TOSStorage,
    TOSStorageError,
    _normalize_bucket,
    _normalize_endpoint,
    _normalize_prefix,
    _normalize_region,
    _normalize_relative_key,
)


DEFAULT_TERMINATION_LOG_PATH = Path("/dev/termination-log")
TERMINATION_LOG_PATH = DEFAULT_TERMINATION_LOG_PATH
TERMINATION_MESSAGE_MAX_BYTES = 4096
DEFAULT_PACK_CONFIG = PackConfig()

_SANITIZED_FAILURE = "dataset cloud publication failed"
_PARQUET_CONTENT_TYPE = "application/vnd.apache.parquet"
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
_MAX_RECEIPT_INTEGER = (1 << 63) - 1
DEFAULT_HEAD_WORKERS = 64
DEFAULT_HEAD_BATCH_SIZE = 4096
DEFAULT_DOWNLOAD_WORKERS = 64
DEFAULT_SHARD_WORKERS = 64
MAX_SHARD_WORKERS = 64
DEFAULT_SHARD_DOWNLOAD_WORKERS = 8


class CloudPublishError(RuntimeError):
    """A caller-safe failure that never includes source or credential context."""


@dataclass(frozen=True)
class CloudPublishRequest:
    run_id: str
    dataset_id: str
    dataset_version_id: str
    version: str
    schema_version: str
    source_bucket: str
    target_bucket: str
    tos_endpoint: str
    tos_region: str
    source_root: str
    source_index: str
    internal_prefix: str
    output_dir: Path


@dataclass(frozen=True)
class _SourceObject:
    size: int
    sha256: str | None


@dataclass(frozen=True)
class _RemoteSample:
    sample: dict[str, Any]
    source_key: str
    source: _SourceObject
    estimated_bytes: int


@dataclass(frozen=True)
class _RemoteShard:
    samples: tuple[_RemoteSample, ...]
    scenes: tuple[str, ...]
    estimated_bytes: int


@dataclass(frozen=True)
class _PublishedShard:
    manifest_rows: tuple[dict[str, Any], ...]
    size: int


class _SanitizedArgumentParser(argparse.ArgumentParser):
    def error(self, _message: str) -> None:
        raise CloudPublishError(_SANITIZED_FAILURE)


def build_argument_parser() -> argparse.ArgumentParser:
    """Build the exact CLI contract rendered by the Kubernetes adapter."""

    parser = _SanitizedArgumentParser(
        description="Publish a trusted dataset index through scoped TOS access."
    )
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--dataset-id", required=True)
    parser.add_argument("--dataset-version-id", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--schema-version", required=True)
    parser.add_argument("--source-bucket", required=True)
    parser.add_argument("--target-bucket", required=True)
    parser.add_argument("--tos-endpoint", required=True)
    parser.add_argument("--tos-region", required=True)
    parser.add_argument("--source-root", required=True)
    parser.add_argument("--source-index", required=True)
    parser.add_argument("--internal-prefix", required=True)
    parser.add_argument("--output-dir", required=True, type=Path)
    return parser


def publish_cloud_dataset(
    request: CloudPublishRequest,
    *,
    storage: Any,
    pack_config: PackConfig | None = None,
) -> dict[str, Any]:
    """Publish one trusted index while exposing only a sanitized failure."""

    try:
        return _publish_cloud_dataset(
            request,
            storage=storage,
            pack_config=pack_config or DEFAULT_PACK_CONFIG,
        )
    except Exception:
        raise CloudPublishError(_SANITIZED_FAILURE) from None


def _publish_cloud_dataset(
    request: CloudPublishRequest,
    *,
    storage: Any,
    pack_config: PackConfig,
) -> dict[str, Any]:
    validated = _validate_request(request)
    _validate_pack_config(pack_config)

    index_document = load_cloud_trusted_index(
        storage=storage,
        source_root=validated.source_root,
        source_index=validated.source_index,
    )
    if not index_document.samples:
        raise ValueError("trusted publisher index must contain at least one sample")
    _emit_progress("index-loaded", samples=len(index_document.samples))
    samples = _validated_remote_samples(
        list(index_document.samples),
        source_root=validated.source_root,
    )

    # Validate the entire untrusted path surface before making source-object calls.
    pa, parquet = _require_pyarrow()
    remote_samples, source_objects = _inspect_remote_samples(samples, storage=storage)
    _emit_progress("source-metadata-verified", objects=len(source_objects))
    remote_shards = _plan_remote_shards(
        remote_samples,
        target_shard_bytes=pack_config.target_shard_bytes,
    )
    _emit_progress("shards-planned", shards=len(remote_shards))
    output_dir = _prepare_output_directory(validated.output_dir)
    manifest_key = (
        f"{validated.dataset_id}/manifests/"
        f"{validated.dataset_version_id}.parquet"
    )

    logical_bytes = sum(source.size for source in source_objects.values())
    payload_row_count = 0
    packed_shard_bytes = 0
    payload_locators: list[dict[str, Any]] = []

    with tempfile.TemporaryDirectory(
        prefix=".raytrain-cloud-publication-",
        dir=output_dir,
    ) as publication_directory:
        publication_root = Path(publication_directory)
        manifest_path = publication_root / "reference-manifest.parquet"
        published_shards = _publish_remote_shards(
            remote_shards,
            request=validated,
            storage=storage,
            pack_config=pack_config,
            publication_root=publication_root,
            pa=pa,
            parquet=parquet,
        )
        for published in published_shards:
            payload_locators.extend(published.manifest_rows)
            payload_row_count += len(published.manifest_rows)
            packed_shard_bytes += published.size

        if payload_row_count != len(samples):
            raise ValueError("manifest row count does not match the trusted index")
        locator_by_token = {row["token"]: row for row in payload_locators}
        if len(locator_by_token) != len(payload_locators):
            raise ValueError("manifest contains duplicate payload tokens")
        manifest_rows = build_reference_manifest_rows(
            index_document.samples,
            locator_by_token,
            class_count=index_document.class_count,
            cbgs_seed=index_document.cbgs_seed,
        )
        split_counts = Counter(row["split"] for row in manifest_rows)
        manifest_schema = _manifest_schema(pa, validated.schema_version)
        table = pa.Table.from_pylist(list(manifest_rows), schema=manifest_schema)
        parquet.write_table(
            table,
            manifest_path,
            compression="zstd",
            use_dictionary=["token", "split", "shard_path"],
            version="2.6",
            write_statistics=True,
            row_group_size=min(len(manifest_rows), 65_536),
        )
        manifest_size = manifest_path.stat().st_size
        if manifest_size <= 0 or manifest_size > MAX_SHARD_BYTES:
            raise ValueError("reference manifest exceeds the immutable object bound")
        manifest_digest = _sha256_file(manifest_path)

        result = _build_result(
            request=validated,
            partition_count=len(remote_shards),
            source_object_count=len(source_objects),
            logical_bytes=logical_bytes,
            packed_bytes=packed_shard_bytes + manifest_size,
            split_counts=split_counts,
            manifest_digest=manifest_digest,
            manifest_key=manifest_key,
        )
        _encode_result(result)
        _put_verified_file(
            storage,
            key=manifest_key,
            path=manifest_path,
            sha256=manifest_digest,
            size=manifest_size,
        )
        _emit_progress(
            "publication-ready",
            partitions=len(remote_shards),
            samples=len(manifest_rows),
        )

    return result


def _emit_progress(stage: str, **counts: int) -> None:
    payload = {"component": "dataset-publisher", "stage": stage, **counts}
    print(json.dumps(payload, sort_keys=True, separators=(",", ":")), flush=True)


def load_cloud_trusted_index(
    *, storage: Any, source_root: str, source_index: str
) -> TrustedIndexDocument:
    """Load one v2 index or a verified set of bounded v2 index parts."""

    index_payload = storage.get_index(source_index, maximum_bytes=MAX_INDEX_BYTES)
    try:
        return load_trusted_index_document(index_payload)
    except ValueError:
        manifest = load_trusted_index_manifest(index_payload)

    manifest_directory = posixpath.dirname(source_index)
    index_stem = posixpath.splitext(posixpath.basename(source_index))[0]
    part_directory = f"{index_stem}.parts"
    samples: list[dict[str, Any]] = []
    seen_tokens: set[str] = set()
    for part in manifest.parts:
        expected_part_key = f"{part_directory}/sha256-{part.sha256}.pkl"
        if part.key != expected_part_key:
            raise ValueError("trusted index part key does not match its digest")
        full_key = posixpath.join(manifest_directory, part.key)
        if _normalize_relative_key(full_key) != full_key:
            raise ValueError("trusted index part key must be normalized")
        _validate_scoped_key(source_root, full_key)
        payload = storage.get_index(full_key, maximum_bytes=MAX_INDEX_BYTES)
        if hashlib.sha256(payload).hexdigest() != part.sha256:
            raise ValueError("trusted index part digest verification failed")
        document = load_trusted_index_document(payload)
        if (
            document.class_count != manifest.class_count
            or document.cbgs_seed != manifest.cbgs_seed
            or len(document.samples) != part.sample_count
        ):
            raise ValueError("trusted index part contract does not match manifest")
        for sample in document.samples:
            token = sample.get("token")
            if token in seen_tokens:
                raise ValueError("trusted index contains a duplicate token")
            seen_tokens.add(token)
            samples.append(dict(sample))
    if len(samples) != manifest.sample_count:
        raise ValueError("trusted index assembled sample count is invalid")
    return TrustedIndexDocument(
        samples=tuple(samples),
        class_count=manifest.class_count,
        cbgs_seed=manifest.cbgs_seed,
    )


def _validate_request(request: object) -> CloudPublishRequest:
    if not isinstance(request, CloudPublishRequest):
        raise ValueError("cloud publisher request is invalid")
    for field_name, value in (
        ("run_id", request.run_id),
        ("dataset_id", request.dataset_id),
        ("dataset_version_id", request.dataset_version_id),
        ("version", request.version),
        ("schema_version", request.schema_version),
    ):
        _validate_publication_id(field_name, value)

    _normalize_bucket(request.source_bucket)
    _normalize_bucket(request.target_bucket)
    region = _normalize_region(request.tos_region)
    _normalize_endpoint(request.tos_endpoint, region=region)
    if _normalize_prefix(request.source_root) != request.source_root:
        raise ValueError("source root must be normalized")
    if _normalize_prefix(request.internal_prefix) != request.internal_prefix:
        raise ValueError("internal prefix must be normalized")
    if _normalize_relative_key(request.source_index) != request.source_index:
        raise ValueError("source index must be normalized")
    _validate_scoped_key(request.source_root, request.source_index)
    generated_keys = (
        f"{request.dataset_id}/shards/sha256-{'0' * 64}.parquet",
        f"{request.dataset_id}/manifests/{request.dataset_version_id}.parquet",
    )
    for generated_key in generated_keys:
        if _normalize_relative_key(generated_key) != generated_key:
            raise ValueError("generated immutable object key is invalid")
        _validate_scoped_key(request.internal_prefix, generated_key)
    if not isinstance(request.output_dir, Path):
        raise ValueError("output directory is invalid")
    return request


def _validate_pack_config(config: object) -> PackConfig:
    if not isinstance(config, PackConfig):
        raise ValueError("pack configuration is invalid")
    if config.target_shard_bytes > MAX_SHARD_BYTES:
        raise ValueError("target shard size exceeds the immutable object bound")
    if config.target_row_group_bytes > MAX_SHARD_BYTES:
        raise ValueError("target row group size exceeds the immutable object bound")
    return config


def _validated_remote_samples(
    samples: list[dict[str, Any]],
    *,
    source_root: str,
) -> tuple[dict[str, Any], ...]:
    ordered = _validated_sorted_index_samples(samples)
    validated = []
    for sample in ordered:
        source_key = _normalize_relative_key(sample["lidar_path"])
        if source_key != sample["lidar_path"]:
            raise ValueError("lidar object key must be normalized")
        _validate_scoped_key(source_root, source_key)
        validated.append({**sample, "lidar_path": source_key})
    return tuple(validated)


def _validate_scoped_key(prefix: str, relative_key: str) -> None:
    full_key = f"{prefix}/{relative_key}"
    if _normalize_relative_key(full_key) != full_key:
        raise ValueError("scoped TOS object key is invalid")


def _inspect_remote_samples(
    samples: tuple[dict[str, Any], ...],
    *,
    storage: Any,
    max_workers: int = DEFAULT_HEAD_WORKERS,
    batch_size: int = DEFAULT_HEAD_BATCH_SIZE,
    prefer_listing: bool = True,
) -> tuple[tuple[_RemoteSample, ...], dict[str, _SourceObject]]:
    if max_workers < 1 or max_workers > 128 or batch_size < max_workers:
        raise ValueError("remote metadata inspection bounds are invalid")
    source_keys = tuple(dict.fromkeys(sample["lidar_path"] for sample in samples))
    if prefer_listing and callable(getattr(storage, "list_source", None)):
        source_objects = _list_remote_source_objects(source_keys, storage=storage)
    else:
        source_objects = _head_remote_source_objects(
            source_keys,
            storage=storage,
            max_workers=max_workers,
            batch_size=batch_size,
        )
    inspected = tuple(
        _RemoteSample(
            sample=sample,
            source_key=sample["lidar_path"],
            source=source_objects[sample["lidar_path"]],
            estimated_bytes=_estimate_remote_sample_bytes(
                sample,
                source_objects[sample["lidar_path"]].size,
            ),
        )
        for sample in samples
    )
    return inspected, source_objects


def _head_remote_source_objects(
    source_keys: tuple[str, ...],
    *,
    storage: Any,
    max_workers: int,
    batch_size: int,
) -> dict[str, _SourceObject]:
    inspected_sources: list[_SourceObject] = []
    with ThreadPoolExecutor(
        max_workers=min(max_workers, len(source_keys)),
        thread_name_prefix="tos-head",
    ) as executor:
        for offset in range(0, len(source_keys), batch_size):
            batch = source_keys[offset : offset + batch_size]
            inspected_sources.extend(
                _validated_source_object(info)
                for info in executor.map(storage.head_source, batch)
            )
    if len(inspected_sources) != len(source_keys):
        raise ValueError("remote metadata inspection count is invalid")
    return dict(zip(source_keys, inspected_sources))


def _list_remote_source_objects(
    source_keys: tuple[str, ...],
    *,
    storage: Any,
) -> dict[str, _SourceObject]:
    required = frozenset(source_keys)
    found: dict[str, _SourceObject] = {}
    marker = None
    seen_markers: set[str] = set()
    page_count = 0
    listed_count = 0
    while True:
        page = storage.list_source(marker=marker, max_keys=1000)
        page_count += 1
        objects = getattr(page, "objects", None)
        next_marker = getattr(page, "next_marker", None)
        if not isinstance(objects, tuple):
            raise ValueError("source listing returned invalid objects")
        listed_count += len(objects)
        for listed in objects:
            key = getattr(listed, "key", None)
            if key in required:
                found[key] = _validated_source_object(listed)
        if page_count % 100 == 0 or next_marker is None:
            _emit_progress(
                "source-list-progress",
                pages=page_count,
                listed=listed_count,
                matched=len(found),
            )
        if next_marker is None:
            break
        if not isinstance(next_marker, str) or not next_marker or next_marker in seen_markers:
            raise ValueError("source listing marker is invalid")
        seen_markers.add(next_marker)
        marker = next_marker
    if set(found) != set(required):
        raise ValueError("trusted index references missing source objects")
    return {key: found[key] for key in source_keys}


def _validated_source_object(info: object) -> _SourceObject:
    size = getattr(info, "size", None)
    digest = getattr(info, "sha256", None)
    if (
        isinstance(size, bool)
        or not isinstance(size, int)
        or size <= 0
        or size > MAX_SHARD_BYTES
    ):
        raise ValueError("source object size exceeds the immutable shard bound")
    if digest is not None and (
        not isinstance(digest, str) or not _SHA256_PATTERN.fullmatch(digest)
    ):
        raise ValueError("source object digest is invalid")
    return _SourceObject(size=size, sha256=digest)


def _estimate_remote_sample_bytes(sample: Mapping[str, Any], source_size: int) -> int:
    info_payload = dump_trusted_info(sample["info"])
    return (
        len(sample["token"].encode("utf-8"))
        + len(sample["scene"].encode("utf-8"))
        + len(sample["split"].encode("utf-8"))
        + len(sample["class_ids"]) * 2
        + 8
        + source_size
        + len(info_payload)
        + 64
        + 64
    )


def _plan_remote_shards(
    samples: tuple[_RemoteSample, ...],
    *,
    target_shard_bytes: int,
) -> tuple[_RemoteShard, ...]:
    planned = []
    current: tuple[_RemoteSample, ...] = ()
    current_size = 0
    ordered = sorted(samples, key=lambda value: (
        sample_site_id(value.sample), value.sample["scene"],
        value.sample["timestamp"], value.sample["token"],
    ))
    for (site, _scene), grouped in groupby(
        ordered, key=lambda value: (sample_site_id(value.sample), value.sample["scene"])
    ):
        if current and sample_site_id(current[0].sample) != site:
            planned.append(_remote_shard(current, current_size))
            current, current_size = (), 0
        scene_samples = tuple(grouped)
        scene_size = sum(sample.estimated_bytes for sample in scene_samples)
        if scene_size > MAX_SHARD_BYTES:
            if current:
                planned.append(_remote_shard(current, current_size))
                current = ()
                current_size = 0
            planned.extend(
                _remote_shard(chunk, sum(sample.estimated_bytes for sample in chunk))
                for chunk in _split_oversized_remote_scene(
                    scene_samples,
                    target_shard_bytes=target_shard_bytes,
                )
            )
            continue
        if current and current_size + scene_size > target_shard_bytes:
            planned.append(_remote_shard(current, current_size))
            current = ()
            current_size = 0
        current = current + scene_samples
        current_size += scene_size
    if current:
        planned.append(_remote_shard(current, current_size))
    if not planned:
        raise ValueError("trusted publisher index did not produce any shards")
    return tuple(planned)


def _split_oversized_remote_scene(
    samples: tuple[_RemoteSample, ...],
    *,
    target_shard_bytes: int,
) -> tuple[tuple[_RemoteSample, ...], ...]:
    chunks: tuple[tuple[_RemoteSample, ...], ...] = ()
    current: tuple[_RemoteSample, ...] = ()
    current_size = 0
    shard_limit = min(target_shard_bytes, MAX_SHARD_BYTES)
    for sample in samples:
        if sample.estimated_bytes <= 0 or sample.estimated_bytes > MAX_SHARD_BYTES:
            raise ValueError("one sample exceeds the maximum immutable shard size")
        if current and current_size + sample.estimated_bytes > shard_limit:
            chunks = chunks + (current,)
            current = ()
            current_size = 0
        current = current + (sample,)
        current_size += sample.estimated_bytes
    if current:
        chunks = chunks + (current,)
    if not chunks:
        raise ValueError("oversized scene did not produce any shards")
    return chunks


def _remote_shard(
    samples: tuple[_RemoteSample, ...],
    estimated_bytes: int,
) -> _RemoteShard:
    scenes = tuple(dict.fromkeys(sample.sample["scene"] for sample in samples))
    return _RemoteShard(
        samples=samples,
        scenes=scenes,
        estimated_bytes=estimated_bytes,
    )


def _publish_one_shard(
    remote_shard: _RemoteShard,
    *,
    request: CloudPublishRequest,
    storage: Any,
    pack_config: PackConfig,
    publication_root: Path,
    pa: Any,
    parquet: Any,
    first_ordinal: int,
    download_workers: int = DEFAULT_SHARD_DOWNLOAD_WORKERS,
) -> _PublishedShard:
    if (
        remote_shard.estimated_bytes <= 0
        or remote_shard.estimated_bytes > MAX_SHARD_BYTES
    ):
        raise ValueError("remote shard exceeds the immutable object bound")
    with tempfile.TemporaryDirectory(
        prefix="shard-",
        dir=publication_root,
    ) as shard_directory:
        shard_root = Path(shard_directory)
        source_root = shard_root / "source"
        packed_root = shard_root / "packed"
        source_root.mkdir(mode=0o750)
        packed_root.mkdir(mode=0o750)
        _stage_sources(
            remote_shard,
            storage=storage,
            source_root=source_root,
            max_workers=download_workers,
        )

        rows = prepare_rows(
            (sample.sample for sample in remote_shard.samples),
            input_root=source_root,
        )
        local_plans = plan_shards(
            rows,
            target_shard_bytes=pack_config.target_shard_bytes,
        )
        if len(local_plans) != 1 or local_plans[0].scenes != remote_shard.scenes:
            raise ValueError("remote and local immutable shard plans differ")
        local_plan = local_plans[0]
        record = _write_parquet_shard(
            packed_root,
            local_plan,
            config=pack_config,
            pa=pa,
            parquet=parquet,
        )
        shard_size = record["size_bytes"]
        if shard_size <= 0 or shard_size > MAX_SHARD_BYTES:
            raise ValueError("Parquet shard exceeds the immutable object bound")
        shard_path = f"{request.dataset_id}/shards/{record['path']}"
        shard_file = packed_root / record["path"]
        _put_verified_file(
            storage,
            key=shard_path,
            path=shard_file,
            sha256=record["sha256"],
            size=shard_size,
        )
        manifest_rows = tuple(
            {
                "ordinal": first_ordinal + row_index,
                "token": row["token"],
                "class_ids": list(row["class_ids"]),
                "source_digest": row["source_digest"],
                "split": row["split"],
                "shard_path": shard_path,
                "row_index": row_index,
            }
            for row_index, row in enumerate(local_plan.rows)
        )
        return _PublishedShard(manifest_rows=manifest_rows, size=shard_size)


def _publish_remote_shards(
    remote_shards: tuple[_RemoteShard, ...],
    *,
    request: CloudPublishRequest,
    storage: Any,
    pack_config: PackConfig,
    publication_root: Path,
    pa: Any,
    parquet: Any,
    max_workers: int = DEFAULT_SHARD_WORKERS,
) -> tuple[_PublishedShard, ...]:
    """Publish bounded shards concurrently while preserving manifest order."""

    if max_workers < 1 or max_workers > MAX_SHARD_WORKERS:
        raise ValueError("remote shard worker count is outside its allowed bound")
    first_ordinal = 0
    work = []
    for remote_shard in remote_shards:
        work.append((first_ordinal, remote_shard))
        first_ordinal += len(remote_shard.samples)
    if not work:
        return ()

    published: list[_PublishedShard | None] = [None] * len(work)
    completed_samples = 0
    worker_state = local()

    def publish(item: tuple[int, _RemoteShard]) -> _PublishedShard:
        ordinal, remote_shard = item
        worker_storage = getattr(worker_state, "storage", None)
        if worker_storage is None:
            fork_storage = getattr(storage, "fork", None)
            worker_storage = fork_storage() if callable(fork_storage) else storage
            worker_state.storage = worker_storage
        return _publish_one_shard(
            remote_shard,
            request=request,
            storage=worker_storage,
            pack_config=pack_config,
            publication_root=publication_root,
            pa=pa,
            parquet=parquet,
            first_ordinal=ordinal,
            download_workers=DEFAULT_SHARD_DOWNLOAD_WORKERS,
        )

    with ThreadPoolExecutor(
        max_workers=min(max_workers, len(work)),
        thread_name_prefix="parquet-shard",
    ) as executor:
        next_index = 0
        pending = {}
        for _ in range(min(max_workers, len(work))):
            future = executor.submit(publish, work[next_index])
            pending[future] = next_index
            next_index += 1
        completed = 0
        try:
            while pending:
                done, _ = wait(tuple(pending), return_when=FIRST_COMPLETED)
                failed = [future for future in done if future.exception() is not None]
                if failed:
                    failed[0].result()
                for future in done:
                    result_index = pending.pop(future)
                    result = future.result()
                    published[result_index] = result
                    completed += 1
                    completed_samples += len(result.manifest_rows)
                    _emit_progress(
                        "shard-published",
                        completed=completed,
                        total=len(work),
                        samples=completed_samples,
                    )
                    if next_index < len(work):
                        future = executor.submit(publish, work[next_index])
                        pending[future] = next_index
                        next_index += 1
        except Exception:
            for future in pending:
                future.cancel()
            raise
    if any(result is None for result in published):
        raise ValueError("remote shard publication is incomplete")
    return tuple(result for result in published if result is not None)


def _stage_sources(
    remote_shard: _RemoteShard,
    *,
    storage: Any,
    source_root: Path,
    max_workers: int = DEFAULT_DOWNLOAD_WORKERS,
) -> None:
    if max_workers <= 0:
        raise ValueError("source download worker count must be positive")
    samples_by_key = {
        sample.source_key: sample
        for sample in remote_shard.samples
    }
    unique_samples = tuple(samples_by_key.values())
    if not unique_samples:
        return
    with ThreadPoolExecutor(
        max_workers=min(max_workers, len(unique_samples)),
        thread_name_prefix="tos-download",
    ) as executor:
        tuple(
            executor.map(
                lambda sample: _stage_source(
                    sample,
                    storage=storage,
                    source_root=source_root,
                ),
                unique_samples,
            )
        )


def _stage_source(
    remote_sample: _RemoteSample,
    *,
    storage: Any,
    source_root: Path,
) -> None:
    destination = source_root.joinpath(*remote_sample.source_key.split("/"))
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    downloaded = storage.download_file(
        remote_sample.source_key,
        destination,
        maximum_bytes=remote_sample.source.size,
    )
    actual_size = destination.stat().st_size
    if actual_size != remote_sample.source.size or getattr(
        downloaded, "size", None
    ) != remote_sample.source.size:
        raise ValueError("staged source object size verification failed")
    expected_digest = remote_sample.source.sha256
    downloaded_digest = getattr(downloaded, "sha256", None)
    if expected_digest is not None and (
        downloaded_digest != expected_digest
        or _sha256_file(destination) != expected_digest
    ):
        raise ValueError("staged source object digest verification failed")


def build_reference_manifest_rows(
    samples: tuple[dict[str, Any], ...] | list[dict[str, Any]],
    locators: Mapping[str, Mapping[str, Any]],
    *,
    class_count: int,
    cbgs_seed: int,
) -> tuple[dict[str, Any], ...]:
    """Bind publisher-generated CBGS refs to immutable payload locators."""

    normalized = _validated_sorted_index_samples(samples)
    sample_by_token = {sample["token"]: sample for sample in normalized}
    if len(sample_by_token) != len(normalized):
        raise ValueError("trusted index contains duplicate tokens")
    if set(locators) != set(sample_by_token):
        raise ValueError("payload locator set does not match the trusted index")

    train_plan = build_cbgs_sample_plan(
        samples,
        class_count=class_count,
        seed=cbgs_seed,
    )
    evaluation = tuple(sample for sample in normalized if sample["split"] != "train")
    ordered = tuple(train_plan) + evaluation
    rows = []
    required_locator_fields = {
        "token",
        "class_ids",
        "source_digest",
        "split",
        "shard_path",
        "row_index",
    }
    for ordinal, sample in enumerate(ordered):
        token = sample["token"]
        locator = locators[token]
        if set(locator) not in (required_locator_fields, required_locator_fields | {"ordinal"}):
            raise ValueError("payload locator structure is invalid")
        canonical = sample_by_token[token]
        if (
            locator["token"] != token
            or tuple(locator["class_ids"]) != tuple(canonical["class_ids"])
            or locator["split"] != canonical["split"]
        ):
            raise ValueError("payload locator metadata does not match the trusted index")
        rows.append({**dict(locator), "ordinal": ordinal, "site_id": sample_site_id(canonical)})
    if not rows:
        raise ValueError("reference manifest must contain at least one row")
    return tuple(rows)


def _manifest_schema(pa: Any, schema_version: str) -> Any:
    return pa.schema(
        [
            pa.field("ordinal", pa.int64(), nullable=False),
            pa.field("token", pa.string(), nullable=False),
            pa.field("site_id", pa.string(), nullable=False),
            pa.field("class_ids", pa.list_(pa.int16()), nullable=False),
            pa.field("source_digest", pa.string(), nullable=False),
            pa.field("split", pa.string(), nullable=False),
            pa.field("shard_path", pa.string(), nullable=False),
            pa.field("row_index", pa.int64(), nullable=False),
        ],
        metadata={
            b"raytrain.manifest_format": b"reference-manifest-v1",
            b"raytrain.schema_version": schema_version.encode("utf-8"),
        },
    )


def _put_verified_file(
    storage: Any,
    *,
    key: str,
    path: Path,
    sha256: str,
    size: int,
) -> None:
    try:
        with path.open("rb") as content:
            storage.put_immutable(
                key,
                content,
                sha256=sha256,
                size=size,
                maximum_bytes=size,
                content_type=_PARQUET_CONTENT_TYPE,
            )
    except TOSStorageError:
        _verify_existing_immutable(
            storage,
            key=key,
            sha256=sha256,
            size=size,
        )
        return
    _verify_existing_immutable(
        storage,
        key=key,
        sha256=sha256,
        size=size,
    )


def _verify_existing_immutable(
    storage: Any,
    *,
    key: str,
    sha256: str,
    size: int,
) -> None:
    try:
        storage.verify_immutable(
            key,
            expected_size=size,
            expected_sha256=sha256,
        )
    except (TOSStorageError, ValueError):
        raise CloudPublishError(_SANITIZED_FAILURE) from None


def _build_result(
    *,
    request: CloudPublishRequest,
    partition_count: int,
    source_object_count: int,
    logical_bytes: int,
    packed_bytes: int,
    split_counts: Mapping[str, int],
    manifest_digest: str,
    manifest_key: str,
) -> dict[str, Any]:
    numeric_values = (
        partition_count,
        source_object_count,
        logical_bytes,
        packed_bytes,
        split_counts.get("train", 0),
        split_counts.get("val", 0),
        split_counts.get("test", 0),
    )
    if any(
        isinstance(value, bool)
        or not isinstance(value, int)
        or value < 0
        or value > _MAX_RECEIPT_INTEGER
        for value in numeric_values
    ):
        raise ValueError("publication counters are invalid")
    if (
        partition_count <= 0
        or source_object_count <= 0
        or logical_bytes <= 0
        or packed_bytes <= 0
        or sum(split_counts.values()) <= 0
    ):
        raise ValueError("publication receipt counters must be positive")
    return {
        "progress": {
            "total_partitions": partition_count,
            "completed_partitions": partition_count,
            "failed_partitions": 0,
            "source_object_count": source_object_count,
            "processed_object_count": source_object_count,
            "failed_object_count": 0,
        },
        "receipt": {
            "dataset_id": request.dataset_id,
            "dataset_version_id": request.dataset_version_id,
            "version": request.version,
            "manifest_sha256": manifest_digest,
            "manifest_object_key": f"{request.internal_prefix}/{manifest_key}",
            "schema_version": request.schema_version,
            "train_samples": split_counts.get("train", 0),
            "val_samples": split_counts.get("val", 0),
            "test_samples": split_counts.get("test", 0),
            "source_object_count": source_object_count,
            "logical_bytes": logical_bytes,
            "packed_bytes": packed_bytes,
        },
    }


def _encode_result(result: Mapping[str, Any]) -> str:
    encoded = json.dumps(
        result,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    )
    if len(encoded.encode("utf-8")) > TERMINATION_MESSAGE_MAX_BYTES:
        raise ValueError("termination message exceeds the Kubernetes size limit")
    return encoded


def _request_from_arguments(arguments: argparse.Namespace) -> CloudPublishRequest:
    return CloudPublishRequest(
        run_id=arguments.run_id,
        dataset_id=arguments.dataset_id,
        dataset_version_id=arguments.dataset_version_id,
        version=arguments.version,
        schema_version=arguments.schema_version,
        source_bucket=arguments.source_bucket,
        target_bucket=arguments.target_bucket,
        tos_endpoint=arguments.tos_endpoint,
        tos_region=arguments.tos_region,
        source_root=arguments.source_root,
        source_index=arguments.source_index,
        internal_prefix=arguments.internal_prefix,
        output_dir=arguments.output_dir,
    )


def _write_termination_message(path: Path, encoded: str) -> None:
    payload = encoded.encode("utf-8")
    if len(payload) > TERMINATION_MESSAGE_MAX_BYTES:
        raise ValueError("termination message exceeds the Kubernetes size limit")
    with path.open("wb") as destination:
        written = destination.write(payload)
    if written != len(payload):
        raise OSError("termination message write was incomplete")


def main(argv: list[str] | None = None) -> int:
    try:
        arguments = build_argument_parser().parse_args(argv)
        request = _request_from_arguments(arguments)
        storage = TOSStorage(
            source_bucket=request.source_bucket,
            target_bucket=request.target_bucket,
            endpoint=request.tos_endpoint,
            region=request.tos_region,
            source_prefix=request.source_root,
            internal_dataset_prefix=request.internal_prefix,
            irsa_provider=VKEIRSAProvider(),
        )
        result = publish_cloud_dataset(
            request,
            storage=storage,
            pack_config=DEFAULT_PACK_CONFIG,
        )
        encoded = _encode_result(result)
        _write_termination_message(TERMINATION_LOG_PATH, encoded)
    except Exception:
        print(_SANITIZED_FAILURE, file=sys.stderr)
        return 2

    print(encoded)
    return 0


if __name__ == "__main__":
    sys.exit(main())
