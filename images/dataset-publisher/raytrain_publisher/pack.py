"""Publish trusted lidar-only dataset indexes as immutable Parquet shards."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import tempfile
from collections.abc import Iterable, Mapping
from dataclasses import dataclass
from itertools import groupby
from pathlib import Path
from typing import Any

import numpy as np

from .schema import (
    compute_source_digest,
    dump_trusted_info,
    get_arrow_schema,
    load_trusted_info,
    pack_float32_points,
    validate_sample_metadata,
    validate_sample_row,
)
from .input_security import (
    input_file_size as _input_file_size,
    read_input_bytes as _read_input_bytes,
    resolve_input_path,
    validate_lidar_only_info as _validate_lidar_only_info,
)


TRUSTED_INDEX_FORMAT = "trusted-index-v2"
MIB = 1024 * 1024
MIN_SHARD_BYTES = 256 * MIB
MAX_SHARD_BYTES = 512 * MIB
DEFAULT_SHARD_BYTES = 384 * MIB
MIN_ROW_GROUP_BYTES = 32 * MIB
MAX_ROW_GROUP_BYTES = 128 * MIB
DEFAULT_ROW_GROUP_BYTES = 64 * MIB
_MAX_INDEX_BYTES = 64 * MIB
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
_INDEX_REQUIRED_FIELDS = frozenset(
    {"token", "scene", "split", "class_ids", "timestamp", "lidar_path", "info"}
)
_INDEX_OPTIONAL_FIELDS = frozenset({"point_columns"})
_MAX_CLASS_COUNT = 1 << 15
_MAX_CBGS_SEED = int(np.iinfo(np.uint32).max)


@dataclass(frozen=True)
class PackConfig:
    target_shard_bytes: int = DEFAULT_SHARD_BYTES
    target_row_group_bytes: int = DEFAULT_ROW_GROUP_BYTES
    compression: str = "zstd"

    def __post_init__(self) -> None:
        _validate_positive_size("target_shard_bytes", self.target_shard_bytes)
        _validate_positive_size("target_row_group_bytes", self.target_row_group_bytes)
        if self.target_row_group_bytes > self.target_shard_bytes:
            raise ValueError("target row group bytes must not exceed target shard bytes")
        if self.compression not in {"zstd", "none"}:
            raise ValueError("compression must be zstd or none")


@dataclass(frozen=True)
class TrustedIndexDocument:
    """Publisher-owned sample order and immutable CBGS contract."""

    samples: tuple[dict[str, Any], ...]
    class_count: int
    cbgs_seed: int


@dataclass(frozen=True)
class ShardPlan:
    rows: tuple[dict[str, Any], ...]
    scenes: tuple[str, ...]
    estimated_bytes: int
    digest: str


def estimate_row_bytes(row: Mapping[str, Any]) -> int:
    """Estimate uncompressed row bytes for deterministic boundary planning."""

    normalized = validate_sample_row(row)
    return (
        len(normalized["token"].encode("utf-8"))
        + len(normalized["scene"].encode("utf-8"))
        + len(normalized["split"].encode("utf-8"))
        + len(normalized["class_ids"]) * 2
        + 8
        + len(normalized["points"])
        + len(normalized["info"])
        + len(normalized["source_digest"])
        + 64
    )


def plan_shards(
    rows: Iterable[Mapping[str, Any]],
    *,
    target_shard_bytes: int = DEFAULT_SHARD_BYTES,
) -> tuple[ShardPlan, ...]:
    """Plan content-addressed shards without splitting a scene."""

    _validate_positive_size("target_shard_bytes", target_shard_bytes)
    ordered = _validated_sorted_rows(rows)
    plans = []
    current_rows: tuple[dict[str, Any], ...] = ()
    current_size = 0
    for _scene, grouped in groupby(ordered, key=lambda row: row["scene"]):
        scene_rows = tuple(grouped)
        scene_size = sum(estimate_row_bytes(row) for row in scene_rows)
        if current_rows and current_size + scene_size > target_shard_bytes:
            plans.append(_make_shard_plan(current_rows))
            current_rows = ()
            current_size = 0
        current_rows = current_rows + scene_rows
        current_size += scene_size
    if current_rows:
        plans.append(_make_shard_plan(current_rows))
    return tuple(plans)


def iter_prepared_shard_plans(
    samples: Iterable[Mapping[str, Any]],
    *,
    input_root: Path,
    target_shard_bytes: int = DEFAULT_SHARD_BYTES,
) -> Iterable[ShardPlan]:
    """Yield bounded prepared shards while retaining only lightweight index metadata.

    The trusted index is capped separately and is safe to sort in memory. Point payloads
    are loaded one scene at a time and released after each yielded shard is written.
    """

    _validate_positive_size("target_shard_bytes", target_shard_bytes)
    root = Path(input_root).resolve(strict=True)
    ordered_samples = _validated_sorted_index_samples(samples)
    current_rows: tuple[dict[str, Any], ...] = ()
    current_size = 0

    for _scene, grouped_samples in groupby(
        ordered_samples, key=lambda sample: sample["scene"]
    ):
        scene_samples = tuple(grouped_samples)
        estimated_scene_size = sum(
            _estimate_index_sample_bytes(sample, input_root=root)
            for sample in scene_samples
        )
        if estimated_scene_size > MAX_SHARD_BYTES:
            raise ValueError("one scene exceeds the maximum immutable shard size")
        if current_rows and current_size + estimated_scene_size > target_shard_bytes:
            rows_to_publish = current_rows
            current_rows = ()
            current_size = 0
            yield _make_shard_plan(rows_to_publish)

        scene_rows = tuple(
            _prepare_row(sample, input_root=root) for sample in scene_samples
        )
        current_rows = current_rows + scene_rows
        current_size += sum(estimate_row_bytes(row) for row in scene_rows)

    if current_rows:
        yield _make_shard_plan(current_rows)


def plan_row_groups(
    rows: Iterable[Mapping[str, Any]],
    *,
    target_row_group_bytes: int = DEFAULT_ROW_GROUP_BYTES,
) -> tuple[tuple[dict[str, Any], ...], ...]:
    """Plan row groups by estimated uncompressed bytes."""

    _validate_positive_size("target_row_group_bytes", target_row_group_bytes)
    groups = []
    current: tuple[dict[str, Any], ...] = ()
    current_size = 0
    for row in rows:
        normalized = validate_sample_row(row)
        row_size = estimate_row_bytes(normalized)
        if current and current_size + row_size > target_row_group_bytes:
            groups.append(current)
            current = ()
            current_size = 0
        current = current + (normalized,)
        current_size += row_size
    if current:
        groups.append(current)
    return tuple(groups)


def _validate_positive_size(field_name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError(f"{field_name} must be a positive integer")
    return value


def _validated_sorted_rows(
    rows: Iterable[Mapping[str, Any]],
) -> tuple[dict[str, Any], ...]:
    normalized = tuple(validate_sample_row(row) for row in rows)
    tokens = [row["token"] for row in normalized]
    if len(tokens) != len(set(tokens)):
        raise ValueError("rows contain a duplicate token")
    return tuple(
        sorted(
            normalized,
            key=lambda row: (
                row["scene"].encode("utf-8"),
                row["timestamp"],
                row["token"].encode("utf-8"),
            ),
        )
    )


def _make_shard_plan(rows: tuple[dict[str, Any], ...]) -> ShardPlan:
    digest = hashlib.sha256(b"raytrain-shard-plan-v1\x00")
    for row in rows:
        component = row["source_digest"].encode("ascii")
        digest.update(len(component).to_bytes(8, byteorder="big", signed=False))
        digest.update(component)
    scenes = tuple(dict.fromkeys(row["scene"] for row in rows))
    return ShardPlan(
        rows=rows,
        scenes=scenes,
        estimated_bytes=sum(estimate_row_bytes(row) for row in rows),
        digest=digest.hexdigest(),
    )


def build_partition_summary(
    plans: Iterable[ShardPlan],
    shard_records: Iterable[Mapping[str, Any]],
) -> dict[str, Any]:
    """Build a path-free per-scene summary for manifest consumers."""

    plan_values = tuple(plans)
    record_values = tuple(dict(record) for record in shard_records)
    if len(plan_values) != len(record_values):
        raise ValueError("partition plans and shard records must have equal lengths")

    partitions: dict[str, dict[str, Any]] = {}
    for plan, record in zip(plan_values, record_values):
        _validate_shard_record(plan, record)
        for scene, scene_rows in groupby(plan.rows, key=lambda row: row["scene"]):
            rows = tuple(scene_rows)
            if scene in partitions:
                raise ValueError("a scene must not span multiple shards")
            split_counts = {
                split: sum(1 for row in rows if row["split"] == split)
                for split in sorted({row["split"] for row in rows})
            }
            partitions[scene] = {
                "scene": scene,
                "row_count": len(rows),
                "splits": split_counts,
                "class_ids": sorted(
                    {class_id for row in rows for class_id in row["class_ids"]}
                ),
                "source_digest": _digest_row_sources(rows),
                "shards": [record["path"]],
            }
    return {
        "schema_version": "parquet-v1",
        "partitions": [partitions[scene] for scene in sorted(partitions, key=str.encode)],
    }


def build_manifest(
    *,
    dataset_id: str,
    version_id: str,
    config: PackConfig,
    plans: Iterable[ShardPlan],
    shard_records: Iterable[Mapping[str, Any]],
    partition_summary: Mapping[str, Any],
) -> dict[str, Any]:
    """Build a deterministic immutable publication manifest."""

    normalized_dataset_id = _validate_publication_id("dataset_id", dataset_id)
    normalized_version_id = _validate_publication_id("version_id", version_id)
    plan_values = tuple(plans)
    records = tuple(dict(record) for record in shard_records)
    if len(plan_values) != len(records):
        raise ValueError("manifest plans and shard records must have equal lengths")
    for plan, record in zip(plan_values, records):
        _validate_shard_record(plan, record)

    return _build_manifest_from_counts(
        dataset_id=normalized_dataset_id,
        version_id=normalized_version_id,
        config=config,
        records=records,
        partition_summary=partition_summary,
        row_count=sum(len(plan.rows) for plan in plan_values),
        scene_count=len({scene for plan in plan_values for scene in plan.scenes}),
    )


def _build_manifest_from_counts(
    *,
    dataset_id: str,
    version_id: str,
    config: PackConfig,
    records: tuple[dict[str, Any], ...],
    partition_summary: Mapping[str, Any],
    row_count: int,
    scene_count: int,
) -> dict[str, Any]:
    summary_bytes = _canonical_json_bytes(partition_summary)
    base_manifest = {
        "dataset_id": dataset_id,
        "version_id": version_id,
        "schema_version": "parquet-v1",
        "format": "parquet",
        "compression": config.compression,
        "target_shard_bytes": config.target_shard_bytes,
        "target_row_group_bytes": config.target_row_group_bytes,
        "row_count": row_count,
        "scene_count": scene_count,
        "partition_summary": "partition-summary.json",
        "partition_summary_digest": hashlib.sha256(summary_bytes).hexdigest(),
        "shards": [dict(record) for record in records],
    }
    return {
        **base_manifest,
        "manifest_digest": hashlib.sha256(_canonical_json_bytes(base_manifest)).hexdigest(),
    }


def publish_dataset(
    *,
    input_root: Path,
    index_path: object,
    output_dir: Path,
    dataset_id: str,
    version_id: str,
    config: PackConfig | None = None,
) -> dict[str, Any]:
    """Publish one trusted index and atomically commit its JSON manifest."""

    effective_config = config or PackConfig()
    pa, parquet = _require_pyarrow()
    samples = load_trusted_index(
        _read_input_bytes(
            Path(input_root),
            index_path,
            maximum_bytes=_MAX_INDEX_BYTES,
            field_name="trusted publisher index",
        )
    )
    if not samples:
        raise ValueError("trusted publisher index must contain at least one sample")
    output_path = _prepare_output_directory(Path(output_dir))
    records: list[dict[str, Any]] = []
    partitions: list[dict[str, Any]] = []
    row_count = 0
    scene_count = 0
    for plan in iter_prepared_shard_plans(
        samples,
        input_root=Path(input_root),
        target_shard_bytes=effective_config.target_shard_bytes,
    ):
        record = _write_parquet_shard(
            output_path, plan, config=effective_config, pa=pa, parquet=parquet
        )
        shard_summary = build_partition_summary((plan,), (record,))
        records.append(record)
        partitions.extend(shard_summary["partitions"])
        row_count += len(plan.rows)
        scene_count += len(plan.scenes)

    summary = {
        "schema_version": "parquet-v1",
        "partitions": sorted(partitions, key=lambda item: item["scene"].encode("utf-8")),
    }
    manifest = _build_manifest_from_counts(
        dataset_id=_validate_publication_id("dataset_id", dataset_id),
        version_id=_validate_publication_id("version_id", version_id),
        config=effective_config,
        records=tuple(records),
        partition_summary=summary,
        row_count=row_count,
        scene_count=scene_count,
    )
    _write_json_atomically(output_path / "partition-summary.json", summary)
    _write_json_atomically(output_path / "manifest.json", manifest)
    return manifest


def _digest_row_sources(rows: Iterable[Mapping[str, Any]]) -> str:
    digest = hashlib.sha256(b"raytrain-partition-v1\x00")
    for row in rows:
        source = row["source_digest"].encode("ascii")
        digest.update(len(source).to_bytes(8, byteorder="big", signed=False))
        digest.update(source)
    return digest.hexdigest()


def _validate_shard_record(plan: ShardPlan, record: Mapping[str, Any]) -> None:
    required = {
        "path",
        "sha256",
        "logical_digest",
        "size_bytes",
        "row_count",
        "row_group_count",
        "scenes",
    }
    if set(record) != required:
        raise ValueError("shard record fields are invalid")
    path = record["path"]
    if (
        not isinstance(path, str)
        or Path(path).name != path
        or not path.startswith("sha256-")
        or not path.endswith(".parquet")
    ):
        raise ValueError("shard path must be a generated content-addressed filename")
    if not isinstance(record["sha256"], str) or not _SHA256_PATTERN.fullmatch(
        record["sha256"]
    ):
        raise ValueError("shard sha256 is invalid")
    if record["logical_digest"] != plan.digest:
        raise ValueError("shard logical digest does not match its rows")
    if record["row_count"] != len(plan.rows) or record["scenes"] != list(plan.scenes):
        raise ValueError("shard summary does not match its rows")
    for numeric_field in ("size_bytes", "row_count", "row_group_count"):
        if isinstance(record[numeric_field], bool) or not isinstance(record[numeric_field], int):
            raise ValueError("shard numeric metadata is invalid")
        if record[numeric_field] <= 0:
            raise ValueError("shard numeric metadata must be positive")


def _validate_publication_id(field_name: str, value: object) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError(f"{field_name} must be a non-empty normalized string")
    if len(value.encode("utf-8")) > 255:
        raise ValueError(f"{field_name} exceeds the size limit")
    if value in {".", ".."} or "/" in value or "\\" in value or "://" in value:
        raise ValueError(f"{field_name} must not contain path or URI syntax")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise ValueError(f"{field_name} must not contain control characters")
    return value


def _canonical_json_bytes(value: object) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")


def _require_pyarrow() -> tuple[object, object]:
    try:
        import pyarrow as pa
        import pyarrow.parquet as parquet
    except ModuleNotFoundError as error:
        raise RuntimeError(
            "pyarrow==25.0.1 is required to publish Parquet datasets"
        ) from error
    return pa, parquet


def _prepare_output_directory(output_dir: Path) -> Path:
    try:
        output_dir.mkdir(parents=True, exist_ok=True, mode=0o750)
        resolved = output_dir.resolve(strict=True)
    except OSError as error:
        raise ValueError("output directory is not accessible") from error
    if not resolved.is_dir():
        raise ValueError("output directory must be a directory")
    return resolved


def _write_parquet_shard(
    output_dir: Path,
    plan: ShardPlan,
    *,
    config: PackConfig,
    pa: object,
    parquet: object,
) -> dict[str, Any]:
    schema = get_arrow_schema()
    row_groups = plan_row_groups(
        plan.rows,
        target_row_group_bytes=config.target_row_group_bytes,
    )
    temporary_path = _temporary_path(output_dir, ".parquet.tmp")
    compression = None if config.compression == "none" else config.compression
    try:
        with parquet.ParquetWriter(
            temporary_path,
            schema,
            compression=compression,
            use_dictionary=["token", "scene", "split"],
            version="2.6",
            write_statistics=True,
        ) as writer:
            for group in row_groups:
                table = pa.Table.from_pylist(list(group), schema=schema)
                writer.write_table(table, row_group_size=len(group))
        file_digest = _sha256_file(temporary_path)
        final_name = f"sha256-{file_digest}.parquet"
        final_path = output_dir / final_name
        if final_path.exists():
            if _sha256_file(final_path) != file_digest:
                raise ValueError("existing content-addressed shard failed checksum validation")
            temporary_path.unlink()
        else:
            os.replace(temporary_path, final_path)
    except Exception:
        if temporary_path.exists():
            temporary_path.unlink()
        raise
    return {
        "path": final_name,
        "sha256": file_digest,
        "logical_digest": plan.digest,
        "size_bytes": final_path.stat().st_size,
        "row_count": len(plan.rows),
        "row_group_count": len(row_groups),
        "scenes": list(plan.scenes),
    }


def _temporary_path(directory: Path, suffix: str) -> Path:
    descriptor, raw_path = tempfile.mkstemp(prefix=".raytrain-", suffix=suffix, dir=directory)
    os.close(descriptor)
    return Path(raw_path)


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _write_json_atomically(path: Path, value: object) -> None:
    content = _canonical_json_bytes(value) + b"\n"
    if path.exists():
        if path.read_bytes() != content:
            raise ValueError("immutable publication metadata already exists with different content")
        return
    temporary_path = _temporary_path(path.parent, ".json.tmp")
    try:
        with temporary_path.open("wb") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        try:
            os.link(temporary_path, path)
        except FileExistsError:
            if path.read_bytes() != content:
                raise ValueError(
                    "immutable publication metadata already exists with different content"
                )
    except Exception:
        raise
    finally:
        temporary_path.unlink(missing_ok=True)


def build_argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Publish a trusted lidar-only index as immutable Parquet shards."
    )
    parser.add_argument("--input-root", required=True, type=Path)
    parser.add_argument("--index", "--info-pickle", dest="index_path", required=True)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--dataset-id", required=True)
    parser.add_argument("--version-id", required=True)
    parser.add_argument(
        "--target-shard-bytes",
        type=_shard_cli_size,
        default=DEFAULT_SHARD_BYTES,
    )
    parser.add_argument(
        "--target-row-group-bytes",
        type=_row_group_cli_size,
        default=DEFAULT_ROW_GROUP_BYTES,
    )
    parser.add_argument("--compression", choices=("zstd", "none"), default="zstd")
    return parser


def _positive_cli_size(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as error:
        raise argparse.ArgumentTypeError("size must be a positive integer") from error
    if parsed <= 0:
        raise argparse.ArgumentTypeError("size must be a positive integer")
    return parsed


def _bounded_cli_size(value: str, *, minimum: int, maximum: int, label: str) -> int:
    parsed = _positive_cli_size(value)
    if parsed < minimum or parsed > maximum:
        raise argparse.ArgumentTypeError(
            f"{label} must be between {minimum} and {maximum} bytes"
        )
    return parsed


def _shard_cli_size(value: str) -> int:
    return _bounded_cli_size(
        value,
        minimum=MIN_SHARD_BYTES,
        maximum=MAX_SHARD_BYTES,
        label="shard size",
    )


def _row_group_cli_size(value: str) -> int:
    return _bounded_cli_size(
        value,
        minimum=MIN_ROW_GROUP_BYTES,
        maximum=MAX_ROW_GROUP_BYTES,
        label="row group size",
    )


def main(argv: list[str] | None = None) -> int:
    parser = build_argument_parser()
    arguments = parser.parse_args(argv)
    try:
        config = PackConfig(
            target_shard_bytes=arguments.target_shard_bytes,
            target_row_group_bytes=arguments.target_row_group_bytes,
            compression=arguments.compression,
        )
        manifest = publish_dataset(
            input_root=arguments.input_root,
            index_path=arguments.index_path,
            output_dir=arguments.output_dir,
            dataset_id=arguments.dataset_id,
            version_id=arguments.version_id,
            config=config,
        )
    except RuntimeError as error:
        print(str(error), file=sys.stderr)
        return 2
    except (OSError, ValueError):
        print(
            "dataset publication failed: invalid input or immutable output conflict",
            file=sys.stderr,
        )
        return 2

    print(
        json.dumps(
            {
                "manifest_digest": manifest["manifest_digest"],
                "row_count": manifest["row_count"],
                "scene_count": manifest["scene_count"],
            },
            separators=(",", ":"),
            sort_keys=True,
        )
    )
    return 0


def dump_trusted_index(
    samples: object,
    *,
    class_count: int,
    cbgs_seed: int = 0,
) -> bytes:
    """Serialize the publisher's explicit sample-index structure."""

    if type(samples) is not list:
        raise ValueError("trusted index samples must be an explicit list")
    _validate_cbgs_contract(class_count=class_count, seed=cbgs_seed)
    return dump_trusted_info(
        {
            "index_format": TRUSTED_INDEX_FORMAT,
            "class_count": class_count,
            "cbgs_seed": cbgs_seed,
            "samples": samples,
        }
    )


def load_trusted_index(payload: object) -> list[dict[str, Any]]:
    """Load an index through the restricted trusted-info unpickler."""

    document = load_trusted_index_document(payload)
    return [dict(sample) for sample in document.samples]


def load_trusted_index_document(payload: object) -> TrustedIndexDocument:
    """Load the explicit v2 index and its publisher-side CBGS policy."""

    envelope = load_trusted_info(payload)
    if set(envelope) != {
        "index_format",
        "class_count",
        "cbgs_seed",
        "samples",
    }:
        raise ValueError("trusted publisher index structure is invalid")
    if envelope["index_format"] != TRUSTED_INDEX_FORMAT:
        raise ValueError("trusted publisher index format is unsupported")
    class_count, cbgs_seed = _validate_cbgs_contract(
        class_count=envelope["class_count"],
        seed=envelope["cbgs_seed"],
    )
    samples = envelope["samples"]
    if type(samples) is not list or any(type(sample) is not dict for sample in samples):
        raise ValueError("trusted publisher index samples must be explicit dictionaries")
    return TrustedIndexDocument(
        samples=tuple(dict(sample) for sample in samples),
        class_count=class_count,
        cbgs_seed=cbgs_seed,
    )


def build_cbgs_sample_plan(
    samples: Iterable[Mapping[str, Any]],
    *,
    class_count: int,
    seed: int,
) -> tuple[Mapping[str, Any], ...]:
    """Build the legacy-equivalent train order from lightweight references.

    The returned values reference caller-owned sample metadata and may repeat.
    Parquet payload rows remain stored once.
    """

    class_count, seed = _validate_cbgs_contract(
        class_count=class_count,
        seed=seed,
    )
    original = tuple(samples)
    normalized = tuple(_validate_index_sample(sample) for sample in original)
    tokens = tuple(sample["token"] for sample in normalized)
    if len(tokens) != len(set(tokens)):
        raise ValueError("trusted index contains a duplicate token")

    train_pairs = tuple(
        (source, validated)
        for source, validated in zip(original, normalized)
        if validated["split"] == "train"
    )
    if not train_pairs:
        raise ValueError("CBGS requires at least one train sample")
    class_sample_indices = {class_id: [] for class_id in range(class_count)}
    for index, (_source, sample) in enumerate(train_pairs):
        for class_id in sample["class_ids"]:
            if class_id not in class_sample_indices:
                raise ValueError("sample class ID is outside the configured class range")
            class_sample_indices[class_id].append(index)
    for class_id, indexes in class_sample_indices.items():
        if not indexes:
            raise ValueError(f"CBGS class {class_id} has no samples")

    duplicated_samples = sum(len(indexes) for indexes in class_sample_indices.values())
    fraction = 1.0 / class_count
    random = np.random.RandomState(seed)
    selected: list[Mapping[str, Any]] = []
    for indexes in class_sample_indices.values():
        distribution = len(indexes) / duplicated_samples
        count = int(len(indexes) * (fraction / distribution))
        selected.extend(
            train_pairs[int(index)][0] for index in random.choice(indexes, count)
        )
    return tuple(selected)


def _validate_cbgs_contract(*, class_count: object, seed: object) -> tuple[int, int]:
    if (
        isinstance(class_count, bool)
        or not isinstance(class_count, int)
        or class_count <= 0
        or class_count > _MAX_CLASS_COUNT
    ):
        raise ValueError("class_count must be between 1 and 32768")
    if (
        isinstance(seed, bool)
        or not isinstance(seed, int)
        or seed < 0
        or seed > _MAX_CBGS_SEED
    ):
        raise ValueError("CBGS seed must fit an unsigned 32-bit integer")
    return class_count, seed


def prepare_rows(
    samples: Iterable[Mapping[str, Any]],
    *,
    input_root: Path,
) -> list[dict[str, Any]]:
    """Validate, load, and deterministically order lidar-only samples."""

    prepared = []
    seen_tokens = set()
    for sample in samples:
        normalized = _validate_index_sample(sample)
        token = normalized["token"]
        if token in seen_tokens:
            raise ValueError("trusted index contains a duplicate token")
        seen_tokens.add(token)
        prepared.append(_prepare_row(normalized, input_root=Path(input_root)))
    return sorted(
        prepared,
        key=lambda row: (row["scene"].encode("utf-8"), row["timestamp"], row["token"].encode("utf-8")),
    )


def _validate_index_sample(sample: object) -> dict[str, Any]:
    if not isinstance(sample, Mapping):
        raise ValueError("trusted index sample must be a mapping")
    fields = set(sample)
    if not _INDEX_REQUIRED_FIELDS.issubset(fields) or not fields.issubset(
        _INDEX_REQUIRED_FIELDS | _INDEX_OPTIONAL_FIELDS
    ):
        raise ValueError("trusted index sample fields must match the lidar-only structure")
    point_columns = sample.get("point_columns", 5)
    if isinstance(point_columns, bool) or not isinstance(point_columns, int) or point_columns <= 0:
        raise ValueError("point_columns must be a positive integer")
    info = sample["info"]
    if type(info) is not dict:
        raise ValueError("info must be an explicit dictionary")
    _validate_lidar_only_info(info)
    declared_columns = info.get("lidar_feature_count")
    if declared_columns is not None and declared_columns != point_columns:
        raise ValueError("point_columns must match info lidar_feature_count")
    metadata = validate_sample_metadata(
        token=sample["token"],
        scene=sample["scene"],
        split=sample["split"],
        class_ids=sample["class_ids"],
        timestamp=sample["timestamp"],
    )
    if set(info["labels"]) != set(metadata["class_ids"]):
        raise ValueError("info labels must match top-level class_ids")
    return {**sample, **metadata, "point_columns": point_columns}


def _validated_sorted_index_samples(
    samples: Iterable[Mapping[str, Any]],
) -> tuple[dict[str, Any], ...]:
    normalized = tuple(_validate_index_sample(sample) for sample in samples)
    tokens = [sample["token"] for sample in normalized]
    if len(tokens) != len(set(tokens)):
        raise ValueError("trusted index contains a duplicate token")
    return tuple(
        sorted(
            normalized,
            key=lambda sample: (
                sample["scene"].encode("utf-8"),
                sample["timestamp"],
                sample["token"].encode("utf-8"),
            ),
        )
    )


def _estimate_index_sample_bytes(
    sample: Mapping[str, Any], *, input_root: Path
) -> int:
    info_payload = dump_trusted_info(sample["info"])
    return (
        len(sample["token"].encode("utf-8"))
        + len(sample["scene"].encode("utf-8"))
        + len(sample["split"].encode("utf-8"))
        + len(sample["class_ids"]) * 2
        + 8
        + _input_file_size(
            input_root, sample["lidar_path"], field_name="lidar input"
        )
        + len(info_payload)
        + 64
        + 64
    )


def _prepare_row(sample: Mapping[str, Any], *, input_root: Path) -> dict[str, Any]:
    point_columns = sample["point_columns"]
    point_bytes = _read_input_bytes(
        input_root,
        sample["lidar_path"],
        maximum_bytes=MAX_SHARD_BYTES,
        field_name="lidar input",
    )
    points = np.frombuffer(point_bytes, dtype=np.float32)
    if points.size == 0 or points.size % point_columns != 0:
        raise ValueError("point_columns must evenly divide the non-empty lidar payload")
    point_payload = pack_float32_points(points.reshape(-1, point_columns))
    info_payload = dump_trusted_info(sample["info"])
    digest = compute_source_digest(
        token=sample["token"],
        scene=sample["scene"],
        split=sample["split"],
        class_ids=sample["class_ids"],
        timestamp=sample["timestamp"],
        points=point_payload,
        info=info_payload,
    )
    return validate_sample_row(
        {
            "token": sample["token"],
            "scene": sample["scene"],
            "split": sample["split"],
            "class_ids": sample["class_ids"],
            "timestamp": sample["timestamp"],
            "points": point_payload,
            "info": info_payload,
            "source_digest": digest,
        }
    )


if __name__ == "__main__":
    sys.exit(main())
