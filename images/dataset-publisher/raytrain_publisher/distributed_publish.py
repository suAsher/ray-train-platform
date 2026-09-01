"""Recoverable, Indexed publication of immutable Parquet dataset versions.

The Kubernetes controller invokes three idempotent phases.  ``pack`` maps one
completion index to a deterministic subset of the trusted index, writes only
content-addressed Parquet shards plus one immutable receipt, and can therefore
be retried by Kubernetes without a shared mutable work directory.  ``finalize``
accepts a version only when every expected receipt is present and valid.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import tempfile
from collections import Counter
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .cloud_publish import (
    DEFAULT_PACK_CONFIG,
    DEFAULT_TERMINATION_LOG_PATH,
    CloudPublishError,
    CloudPublishRequest,
    _build_result,
    _encode_result,
    _inspect_remote_samples,
    _manifest_schema,
    _plan_remote_shards,
    _publish_remote_shards,
    _put_verified_file,
    _sha256_file,
    _validate_pack_config,
    _validate_request,
    _validated_remote_samples,
    _write_termination_message,
    build_reference_manifest_rows,
    load_cloud_trusted_index,
)
from .pack import MAX_SHARD_BYTES, _prepare_output_directory, _require_pyarrow
from .schema import dump_trusted_info
from .tos_storage import MAX_INDEX_BYTES, TOSStorage, TOSStorageError
from .irsa import VKEIRSAProvider


_MAX_PARTITIONS = 100_000
_RECEIPT_FORMAT = "raytrain-partition-receipt-v1"
_JSON_CONTENT_TYPE = "application/json"


@dataclass(frozen=True)
class PublicationPartitionPlan:
    """Deterministic, path-safe work descriptor for one Indexed partition."""

    ordinal: int
    input_fingerprint: str
    sample_count: int
    reused: bool


def build_partition_plan(
    *,
    samples: tuple[Mapping[str, Any], ...] | list[Mapping[str, Any]],
    partition_count: int,
    verified_base_receipts: Mapping[int, str] | None = None,
) -> tuple[PublicationPartitionPlan, ...]:
    """Plan stable partitions and reuse only a receipt with the same input.

    This pure function is also the controller-facing incremental contract: a
    changed source tuple affects only its partition fingerprint.
    """

    count = _validated_partition_count(partition_count)
    grouped: list[list[dict[str, Any]]] = [[] for _ in range(count)]
    for sample in samples:
        if not isinstance(sample, Mapping):
            raise ValueError("partition sample is invalid")
        token, source_key, size, digest = (sample.get(name) for name in ("token", "source_key", "size", "sha256"))
        if not isinstance(token, str) or not token or not isinstance(source_key, str) or not source_key or isinstance(size, bool) or not isinstance(size, int) or size < 0 or not isinstance(digest, str) or len(digest) != 64:
            raise ValueError("partition sample is invalid")
        grouped[_sample_partition(token, count)].append({"token": token, "source_key": source_key, "size": size, "sha256": digest})
    receipts = dict(verified_base_receipts or {})
    plans = []
    for ordinal, members in enumerate(grouped):
        payload = _canonical_json({"ordinal": ordinal, "samples": sorted(members, key=lambda value: value["token"])})
        fingerprint = hashlib.sha256(payload).hexdigest()
        plans.append(PublicationPartitionPlan(ordinal=ordinal, input_fingerprint=fingerprint, sample_count=len(members), reused=receipts.get(ordinal) == fingerprint))
    return tuple(plans)


class _ArgumentParser(argparse.ArgumentParser):
    def error(self, _message: str) -> None:
        raise CloudPublishError("dataset cloud publication failed")


def build_argument_parser() -> argparse.ArgumentParser:
    parser = _ArgumentParser(description="Run one distributed immutable dataset publication phase")
    parser.add_argument("--phase", choices=("plan", "pack", "finalize"), required=True)
    parser.add_argument("--partition-count", type=int, required=True)
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


def _request(arguments: argparse.Namespace) -> CloudPublishRequest:
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


def _validated_partition_count(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= _MAX_PARTITIONS:
        raise ValueError("partition count is invalid")
    return value


def _partition_ordinal(value: str, count: int) -> int:
    if not value or not value.isdecimal():
        raise ValueError("partition ordinal is invalid")
    ordinal = int(value)
    if ordinal < 0 or ordinal >= count:
        raise ValueError("partition ordinal is invalid")
    return ordinal


def _sample_partition(token: str, count: int) -> int:
    return int.from_bytes(hashlib.sha256(token.encode("utf-8")).digest()[:8], "big") % count


def _receipt_key(request: CloudPublishRequest, ordinal: int) -> str:
    return f"{request.dataset_id}/publication/{request.dataset_version_id}/partitions/{ordinal:05d}.json"


def _shared_receipt_key(request: CloudPublishRequest, fingerprint: str) -> str:
    return f"{request.dataset_id}/publication/receipts/sha256-{fingerprint}.json"


def _canonical_json(payload: Mapping[str, Any]) -> bytes:
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode("utf-8")


def _put_verified_bytes(storage: Any, *, key: str, payload: bytes) -> None:
    digest = hashlib.sha256(payload).hexdigest()
    try:
        storage.put_immutable(key, payload, sha256=digest, size=len(payload), maximum_bytes=MAX_INDEX_BYTES, content_type=_JSON_CONTENT_TYPE)
    except TOSStorageError:
        storage.verify_immutable(key, expected_size=len(payload), expected_sha256=digest)
        return
    storage.verify_immutable(key, expected_size=len(payload), expected_sha256=digest)


def _partition_fingerprint(samples: tuple[dict[str, Any], ...], sources: Mapping[str, Any]) -> str:
    items = []
    for sample in samples:
        source = sources[sample["lidar_path"]]
        items.append({
            "token": sample["token"], "scene": sample["scene"], "split": sample["split"],
            "class_ids": list(sample["class_ids"]), "timestamp": sample["timestamp"],
            "lidar_path": sample["lidar_path"], "point_columns": sample.get("point_columns"),
            "info_sha256": hashlib.sha256(dump_trusted_info(sample["info"])).hexdigest(),
            "source_size": source.size, "source_sha256": source.sha256 or "",
        })
    return hashlib.sha256(_canonical_json({"samples": items})).hexdigest()


def _version_receipt(request: CloudPublishRequest, *, ordinal: int, partition_count: int, fingerprint: str, shared_key: str) -> bytes:
    return _canonical_json({
        "format": _RECEIPT_FORMAT, "dataset_id": request.dataset_id, "dataset_version_id": request.dataset_version_id,
        "partition_ordinal": ordinal, "partition_count": partition_count, "input_fingerprint": fingerprint,
        "shared_receipt_key": shared_key,
    })


def _load_receipt(storage: Any, *, request: CloudPublishRequest, ordinal: int, partition_count: int) -> dict[str, Any]:
    payload = storage.get_immutable(_receipt_key(request, ordinal), maximum_bytes=MAX_INDEX_BYTES)
    try:
        receipt = json.loads(payload)
    except Exception as exc:
        raise ValueError("partition receipt is invalid") from exc
    if not isinstance(receipt, dict) or receipt.get("format") != _RECEIPT_FORMAT:
        raise ValueError("partition receipt is invalid")
    expected = {
        "dataset_id": request.dataset_id,
        "dataset_version_id": request.dataset_version_id,
        "partition_ordinal": ordinal,
        "partition_count": partition_count,
    }
    if any(receipt.get(key) != value for key, value in expected.items()):
        raise ValueError("partition receipt identity is invalid")
    fingerprint, shared_key = receipt.get("input_fingerprint"), receipt.get("shared_receipt_key")
    if not isinstance(fingerprint, str) or len(fingerprint) != 64 or shared_key != _shared_receipt_key(request, fingerprint):
        raise ValueError("partition receipt payload is invalid")
    shared_payload = storage.get_immutable(shared_key, maximum_bytes=MAX_INDEX_BYTES)
    try:
        shared = json.loads(shared_payload)
    except Exception as exc:
        raise ValueError("shared partition receipt is invalid") from exc
    if not isinstance(shared, dict) or shared.get("format") != _RECEIPT_FORMAT or shared.get("dataset_id") != request.dataset_id or shared.get("input_fingerprint") != fingerprint:
        raise ValueError("shared partition receipt is invalid")
    if not isinstance(shared.get("locators"), list) or not isinstance(shared.get("sources"), list) or not isinstance(shared.get("packed_bytes"), int):
        raise ValueError("shared partition receipt payload is invalid")
    return shared


def run_plan(request: CloudPublishRequest, *, storage: Any, partition_count: int) -> None:
    """Fail early if the trusted index cannot be read before scheduling work."""

    index = load_cloud_trusted_index(storage=storage, source_root=request.source_root, source_index=request.source_index)
    if not index.samples:
        raise ValueError("trusted publisher index must contain at least one sample")
    # Partition membership is deterministic and can be recomputed after a
    # controller restart; no mutable plan object is required.
    _ = sum(1 for sample in index.samples if _sample_partition(sample["token"], partition_count) >= 0)


def run_pack(request: CloudPublishRequest, *, storage: Any, partition_count: int, ordinal: int) -> None:
    index = load_cloud_trusted_index(storage=storage, source_root=request.source_root, source_index=request.source_index)
    selected = [sample for sample in index.samples if _sample_partition(sample["token"], partition_count) == ordinal]
    samples = _validated_remote_samples(selected, source_root=request.source_root)
    # Never list the whole source prefix for every Indexed completion.  HEAD
    # only the selected payload objects, bounded by the validated index.
    if selected:
        remote_samples, sources = _inspect_remote_samples(samples, storage=storage, prefer_listing=False)
    else:
        remote_samples, sources = (), {}
    # A partition's receipt is content-addressed by trusted metadata plus source
    # HEAD identities. New immutable versions therefore reuse unchanged work
    # without rereading or repacking payload bytes.
    fingerprint = _partition_fingerprint(samples, sources)
    shared_key = _shared_receipt_key(request, fingerprint)
    try:
        cached_payload = storage.get_immutable(shared_key, maximum_bytes=MAX_INDEX_BYTES)
        shared = json.loads(cached_payload)
        if not isinstance(shared, dict) or shared.get("format") != _RECEIPT_FORMAT or shared.get("input_fingerprint") != fingerprint:
            raise ValueError("shared partition receipt is invalid")
    except TOSStorageError:
        shared = None
    if shared is not None:
        _put_verified_bytes(storage, key=_receipt_key(request, ordinal), payload=_version_receipt(request, ordinal=ordinal, partition_count=partition_count, fingerprint=fingerprint, shared_key=shared_key))
        return
    if not selected:
        shared = {"format": _RECEIPT_FORMAT, "dataset_id": request.dataset_id, "input_fingerprint": fingerprint, "locators": [], "sources": [], "packed_bytes": 0}
        _put_verified_bytes(storage, key=shared_key, payload=_canonical_json(shared))
        _put_verified_bytes(storage, key=_receipt_key(request, ordinal), payload=_version_receipt(request, ordinal=ordinal, partition_count=partition_count, fingerprint=fingerprint, shared_key=shared_key))
        return
    pa, parquet = _require_pyarrow()
    remote_shards = _plan_remote_shards(remote_samples, target_shard_bytes=DEFAULT_PACK_CONFIG.target_shard_bytes)
    output_dir = _prepare_output_directory(request.output_dir)
    with tempfile.TemporaryDirectory(prefix=".raytrain-distributed-pack-", dir=output_dir) as temp:
        published = _publish_remote_shards(remote_shards, request=request, storage=storage, pack_config=DEFAULT_PACK_CONFIG, publication_root=Path(temp), pa=pa, parquet=parquet)
    locators = [locator for shard in published for locator in shard.manifest_rows]
    if len(locators) != len(selected) or len({locator["token"] for locator in locators}) != len(selected):
        raise ValueError("partition locator output is invalid")
    shared = {
        "format": _RECEIPT_FORMAT,
        "dataset_id": request.dataset_id,
        "input_fingerprint": fingerprint,
        "locators": locators,
        "sources": [{"key": key, "size": value.size} for key, value in sorted(sources.items())],
        "packed_bytes": sum(shard.size for shard in published),
    }
    _put_verified_bytes(storage, key=shared_key, payload=_canonical_json(shared))
    _put_verified_bytes(storage, key=_receipt_key(request, ordinal), payload=_version_receipt(request, ordinal=ordinal, partition_count=partition_count, fingerprint=fingerprint, shared_key=shared_key))


def run_finalize(request: CloudPublishRequest, *, storage: Any, partition_count: int) -> dict[str, Any]:
    index = load_cloud_trusted_index(storage=storage, source_root=request.source_root, source_index=request.source_index)
    locators: dict[str, Mapping[str, Any]] = {}
    sources: dict[str, int] = {}
    packed_bytes = 0
    for ordinal in range(partition_count):
        receipt = _load_receipt(storage, request=request, ordinal=ordinal, partition_count=partition_count)
        packed_bytes += receipt.get("packed_bytes", 0)
        for source in receipt["sources"]:
            if not isinstance(source, dict) or not isinstance(source.get("key"), str) or not isinstance(source.get("size"), int) or source["size"] <= 0:
                raise ValueError("partition source receipt is invalid")
            prior = sources.setdefault(source["key"], source["size"])
            if prior != source["size"]:
                raise ValueError("partition source receipt conflicts")
        for locator in receipt["locators"]:
            if not isinstance(locator, dict) or not isinstance(locator.get("token"), str) or locator["token"] in locators:
                raise ValueError("partition locator receipt is invalid")
            locators[locator["token"]] = locator
    rows = build_reference_manifest_rows(index.samples, locators, class_count=index.class_count, cbgs_seed=index.cbgs_seed)
    pa, parquet = _require_pyarrow()
    output_dir = _prepare_output_directory(request.output_dir)
    with tempfile.TemporaryDirectory(prefix=".raytrain-distributed-finalize-", dir=output_dir) as temp:
        manifest_path = Path(temp) / "reference-manifest.parquet"
        parquet.write_table(pa.Table.from_pylist(list(rows), schema=_manifest_schema(pa, request.schema_version)), manifest_path, compression="zstd", use_dictionary=["token", "split", "shard_path"], version="2.6", write_statistics=True, row_group_size=min(len(rows), 65_536))
        manifest_size = manifest_path.stat().st_size
        if manifest_size <= 0 or manifest_size > MAX_SHARD_BYTES:
            raise ValueError("reference manifest exceeds the immutable object bound")
        digest = _sha256_file(manifest_path)
        manifest_key = f"{request.dataset_id}/manifests/{request.dataset_version_id}.parquet"
        _put_verified_file(storage, key=manifest_key, path=manifest_path, sha256=digest, size=manifest_size)
    split_counts = Counter(row["split"] for row in rows)
    return _build_result(request=request, partition_count=partition_count, source_object_count=len(sources), logical_bytes=sum(sources.values()), packed_bytes=packed_bytes + manifest_size, split_counts=split_counts, manifest_digest=digest, manifest_key=manifest_key)


def main(argv: list[str] | None = None) -> int:
    try:
        arguments = build_argument_parser().parse_args(argv)
        request = _validate_request(_request(arguments))
        count = _validated_partition_count(arguments.partition_count)
        storage = TOSStorage(source_bucket=request.source_bucket, target_bucket=request.target_bucket, endpoint=request.tos_endpoint, region=request.tos_region, source_prefix=request.source_root, internal_dataset_prefix=request.internal_prefix, irsa_provider=VKEIRSAProvider())
        if arguments.phase == "plan":
            run_plan(request, storage=storage, partition_count=count)
        elif arguments.phase == "pack":
            ordinal = _partition_ordinal(os.environ.get("DATASET_PUBLISHER_PARTITION_ORDINAL", ""), count)
            run_pack(request, storage=storage, partition_count=count, ordinal=ordinal)
        else:
            result = run_finalize(request, storage=storage, partition_count=count)
            _write_termination_message(DEFAULT_TERMINATION_LOG_PATH, _encode_result(result))
            print(_encode_result(result))
    except Exception:
        print("dataset cloud publication failed", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
