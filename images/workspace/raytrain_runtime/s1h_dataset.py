"""Safe, deterministic bridge from Parquet v1 rows to S1H samples."""

from __future__ import annotations

import dataclasses
import hashlib
import io
import json
import math
import pickle
import pickletools
import re
from collections.abc import Iterable, Iterator, Mapping, Sequence
from typing import Any, overload

import numpy as np


ROW_FIELD_NAMES = (
    "token",
    "scene",
    "split",
    "class_ids",
    "timestamp",
    "points",
    "info",
    "source_digest",
)
TRUSTED_INFO_FORMAT = "trusted-info-v1"
_TRUSTED_FORMAT_KEY = "__raytrain_publisher_format__"
_TRUSTED_PAYLOAD_KEY = "payload"
_VALID_SPLITS = frozenset(("train", "val", "test"))
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
_INT16_MAX = (1 << 15) - 1
_INT64_MIN = -(1 << 63)
_INT64_MAX = (1 << 63) - 1
_MAX_TRUSTED_PICKLE_BYTES = 64 * 1024 * 1024
_MAX_TRUSTED_PICKLE_OPCODES = 1_000_000
_MAX_TRUSTED_CONTAINER_ITEMS = 100_000
_MAX_TRUSTED_DEPTH = 32
_MAX_TRUSTED_STRING_BYTES = 1024 * 1024
_MAX_TRUSTED_BINARY_BYTES = 32 * 1024 * 1024
_FORBIDDEN_PICKLE_OPCODES = frozenset(
    {
        "BINPERSID",
        "BUILD",
        "EXT1",
        "EXT2",
        "EXT4",
        "GLOBAL",
        "INST",
        "NEWOBJ",
        "NEWOBJ_EX",
        "NEXT_BUFFER",
        "OBJ",
        "PERSID",
        "READONLY_BUFFER",
        "REDUCE",
        "STACK_GLOBAL",
    }
)
# Minimal publisher ``info`` allowlist. No other keys or nesting are admitted:
# - boxes: required list[list[finite number]], uniform inner width 7 or 9
# - labels: required list[non-negative int16], one label per box
# - lidar_feature_count: optional integer in [1, 64], default 5
_S1H_INFO_REQUIRED_FIELDS = frozenset(("boxes", "labels"))
_S1H_INFO_OPTIONAL_FIELDS = frozenset(("lidar_feature_count",))
_S1H_INFO_FIELDS = _S1H_INFO_REQUIRED_FIELDS | _S1H_INFO_OPTIONAL_FIELDS
_S1H_BOX_WIDTHS = frozenset((7, 9))


class S1HRowError(ValueError):
    """Public, redacted failure raised for an invalid publisher row."""


class _RowValidationError(ValueError):
    def __init__(self, field: str, reason: str) -> None:
        self.field = field
        self.reason = reason
        super().__init__(f"{field}: {reason}")


class _RestrictedUnpickler(pickle.Unpickler):
    def find_class(self, module: str, name: str) -> object:
        raise pickle.UnpicklingError("forbidden pickle global")

    def persistent_load(self, persistent_id: object) -> object:
        raise pickle.UnpicklingError("forbidden pickle persistent reference")


@dataclasses.dataclass(frozen=True)
class SampleRef:
    """Payload-free identity used to construct a CBGS plan."""

    ordinal: int
    token: str
    class_ids: tuple[int, ...]
    source_digest: str

    def __post_init__(self) -> None:
        if isinstance(self.ordinal, bool) or not isinstance(self.ordinal, int):
            raise ValueError("sample ordinal must be a non-negative integer")
        if self.ordinal < 0:
            raise ValueError("sample ordinal must be a non-negative integer")
        object.__setattr__(self, "token", _validate_identifier("token", self.token))
        object.__setattr__(self, "class_ids", _validate_class_ids(self.class_ids))
        object.__setattr__(
            self,
            "source_digest",
            _validate_digest(self.source_digest),
        )


@dataclasses.dataclass(frozen=True)
class CBGSPlan(Sequence[SampleRef]):
    """One publisher-generated legacy-equivalent reference plan."""

    samples: tuple[SampleRef, ...]
    class_counts: tuple[int, ...]
    seed: int

    @overload
    def __getitem__(self, index: int) -> SampleRef:
        ...

    @overload
    def __getitem__(self, index: slice) -> tuple[SampleRef, ...]:
        ...

    def __getitem__(self, index: Any) -> Any:
        return self.samples[index]

    def __iter__(self) -> Iterator[SampleRef]:
        return iter(self.samples)

    def __len__(self) -> int:
        return len(self.samples)


def iter_sample_refs(rows: Iterable[Mapping[str, Any]]) -> Iterator[SampleRef]:
    """Project rows to lightweight references without touching payload columns."""

    for ordinal, row in enumerate(rows):
        if not isinstance(row, Mapping):
            raise ValueError("sample metadata row must be a mapping")
        try:
            yield SampleRef(
                ordinal=ordinal,
                token=row["token"],
                class_ids=tuple(row["class_ids"]),
                source_digest=row["source_digest"],
            )
        except KeyError:
            raise ValueError(
                "sample metadata row is missing a required field"
            ) from None
        except TypeError:
            raise ValueError("sample metadata row has invalid class IDs") from None


def build_cbgs_plan(
    refs: Iterable[SampleRef],
    *,
    class_count: int,
    seed: int,
) -> CBGSPlan:
    """Build one small publisher-side legacy ``CBGSDataset`` plan.

    The publisher calls this once when creating a version. Training consumes
    the resulting ordered ref Dataset and must never rebuild it per epoch.
    """

    _validate_positive_integer("class_count", class_count)
    if class_count > _INT16_MAX + 1:
        raise ValueError("class_count must not exceed the 32768 int16 classes")
    _validate_non_negative_integer("seed", seed)
    if seed > np.iinfo(np.uint32).max:
        raise ValueError("seed must fit an unsigned 32-bit integer")

    samples = tuple(refs)
    if not samples:
        raise ValueError("CBGS requires at least one sample reference")
    if any(not isinstance(ref, SampleRef) for ref in samples):
        raise ValueError("CBGS accepts only SampleRef values")
    if len({ref.token for ref in samples}) != len(samples):
        raise ValueError("CBGS sample references must have unique tokens")

    class_sample_indices = {class_id: [] for class_id in range(class_count)}
    for index, ref in enumerate(samples):
        for class_id in ref.class_ids:
            if class_id not in class_sample_indices:
                raise ValueError(
                    "sample class ID is outside the configured class range"
                )
            class_sample_indices[class_id].append(index)

    for class_id, indices in class_sample_indices.items():
        if not indices:
            raise ValueError(f"CBGS class {class_id} has no samples")
    duplicated_samples = sum(len(indices) for indices in class_sample_indices.values())
    class_distribution = {
        class_id: len(indices) / duplicated_samples
        for class_id, indices in class_sample_indices.items()
    }
    fraction = 1.0 / class_count
    ratios = [fraction / value for value in class_distribution.values()]
    random = np.random.RandomState(seed)
    selected: list[SampleRef] = []
    class_counts: list[int] = []
    for class_indices, ratio in zip(class_sample_indices.values(), ratios):
        count = int(len(class_indices) * ratio)
        class_counts.append(count)
        selected.extend(samples[index] for index in random.choice(class_indices, count))
    return CBGSPlan(
        samples=tuple(selected),
        class_counts=tuple(class_counts),
        seed=seed,
    )


def decode_s1h_row(row: Mapping[str, Any]) -> dict[str, Any]:
    """Validate one Parquet v1 row and recover the legacy pipeline sample."""

    try:
        normalized = _validate_row(row)
        info = _load_trusted_info(normalized["info"])
        columns, boxes, labels = _validate_s1h_info(info)
        if set(labels.tolist()) != set(normalized["class_ids"]):
            raise _RowValidationError(
                "annotation labels",
                "must match top-level class_ids",
            )
        points = _unpack_points(normalized["points"], columns=columns)
    except _RowValidationError as error:
        raise S1HRowError(
            f"invalid S1H Parquet row: {error.field} {error.reason}"
        ) from None
    except (pickle.UnpicklingError, ValueError):
        raise S1HRowError("invalid S1H Parquet row: trusted info is unsafe") from None

    return {
        "token": normalized["token"],
        "sample_idx": normalized["token"],
        "scene": normalized["scene"],
        "split": normalized["split"],
        "class_ids": normalized["class_ids"],
        "timestamp": normalized["timestamp"],
        "points": points,
        "source_digest": normalized["source_digest"],
        "sweeps": [],
        "ann_info": {
            "gt_bboxes_3d": boxes,
            "gt_labels_3d": labels,
        },
    }


def iter_decoded_samples(
    rows: Iterable[Mapping[str, Any]],
    *,
    pipeline: Any | None = None,
) -> Iterator[Any]:
    """Decode and optionally transform rows one at a time."""

    if pipeline is not None and not callable(pipeline):
        raise ValueError("pipeline must be callable")
    for row in rows:
        sample = decode_s1h_row(row)
        yield pipeline(sample) if pipeline is not None else sample


def _validate_row(row: object) -> dict[str, Any]:
    if not isinstance(row, Mapping):
        raise _RowValidationError("schema", "must be a mapping")
    if set(row) != set(ROW_FIELD_NAMES):
        raise _RowValidationError("schema", "fields do not match lidar-only v1")
    token = _validate_identifier("token", row["token"])
    scene = _validate_identifier("scene", row["scene"])
    split = row["split"]
    if not isinstance(split, str) or split not in _VALID_SPLITS:
        raise _RowValidationError("split", "is invalid")
    class_ids = _validate_class_ids(row["class_ids"])
    timestamp = row["timestamp"]
    if isinstance(timestamp, bool) or not isinstance(timestamp, (int, np.integer)):
        raise _RowValidationError("timestamp", "must be an int64 integer")
    timestamp = int(timestamp)
    if timestamp < _INT64_MIN or timestamp > _INT64_MAX:
        raise _RowValidationError("timestamp", "must fit int64")
    points = _validate_binary("points", row["points"], float32_aligned=True)
    info = _validate_binary("info", row["info"])
    source_digest = _validate_digest(row["source_digest"])
    expected = _compute_source_digest(
        token=token,
        scene=scene,
        split=split,
        class_ids=class_ids,
        timestamp=timestamp,
        points=points,
        info=info,
    )
    if source_digest != expected:
        raise _RowValidationError("source digest", "does not match row payload")
    return {
        "token": token,
        "scene": scene,
        "split": split,
        "class_ids": class_ids,
        "timestamp": timestamp,
        "points": points,
        "info": info,
        "source_digest": source_digest,
    }


def _validate_identifier(field: str, value: object) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise _RowValidationError(field, "is invalid")
    if len(value.encode("utf-8")) > 255:
        raise _RowValidationError(field, "is invalid")
    if value in {".", ".."} or "/" in value or "\\" in value or "://" in value:
        raise _RowValidationError(field, "is invalid")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise _RowValidationError(field, "is invalid")
    return value


def _validate_class_ids(value: object) -> tuple[int, ...]:
    if isinstance(value, np.ndarray):
        values: object = value.tolist()
    else:
        values = value
    if not isinstance(values, Sequence) or isinstance(
        values, (str, bytes, bytearray)
    ):
        raise _RowValidationError("class_ids", "must be an int16 sequence")
    normalized = []
    for class_id in values:
        if isinstance(class_id, bool) or not isinstance(class_id, (int, np.integer)):
            raise _RowValidationError("class_ids", "must contain integers")
        class_value = int(class_id)
        if class_value < 0 or class_value > _INT16_MAX:
            raise _RowValidationError("class_ids", "must fit non-negative int16")
        normalized.append(class_value)
    if len(set(normalized)) != len(normalized):
        raise _RowValidationError("class_ids", "must not contain duplicates")
    return tuple(normalized)


def _validate_binary(
    field: str, value: object, *, float32_aligned: bool = False
) -> bytes:
    if not isinstance(value, (bytes, bytearray, memoryview)):
        raise _RowValidationError(field, "must be bytes-like")
    payload = bytes(value)
    if not payload:
        raise _RowValidationError(field, "must not be empty")
    if float32_aligned and len(payload) % np.dtype(np.float32).itemsize:
        raise _RowValidationError(field, "must contain complete float32 values")
    return payload


def _validate_digest(value: object) -> str:
    if not isinstance(value, str) or not _SHA256_PATTERN.fullmatch(value):
        raise _RowValidationError("source digest", "must be lowercase SHA-256")
    return value


def _compute_source_digest(
    *,
    token: str,
    scene: str,
    split: str,
    class_ids: tuple[int, ...],
    timestamp: int,
    points: bytes,
    info: bytes,
) -> str:
    metadata = json.dumps(
        {
            "token": token,
            "scene": scene,
            "split": split,
            "class_ids": class_ids,
            "timestamp": timestamp,
        },
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    digest = hashlib.sha256(b"raytrain-source-v1\x00")
    for component in (metadata, points, info):
        digest.update(len(component).to_bytes(8, byteorder="big", signed=False))
        digest.update(component)
    return digest.hexdigest()


def _load_trusted_info(payload: bytes) -> dict[str, Any]:
    if not payload or len(payload) > _MAX_TRUSTED_PICKLE_BYTES:
        raise pickle.UnpicklingError("invalid trusted publisher pickle size")
    try:
        for count, (opcode, _argument, _position) in enumerate(
            pickletools.genops(payload), start=1
        ):
            if count > _MAX_TRUSTED_PICKLE_OPCODES:
                raise pickle.UnpicklingError("too many pickle operations")
            if opcode.name in _FORBIDDEN_PICKLE_OPCODES:
                raise pickle.UnpicklingError("forbidden pickle operation")
    except pickle.UnpicklingError:
        raise
    except Exception as error:
        raise pickle.UnpicklingError("invalid trusted publisher pickle") from error

    stream = io.BytesIO(payload)
    try:
        envelope = _RestrictedUnpickler(stream).load()
    except pickle.UnpicklingError:
        raise
    except Exception as error:
        raise pickle.UnpicklingError("invalid trusted publisher pickle") from error
    if stream.read(1):
        raise pickle.UnpicklingError("trusted publisher pickle has trailing data")
    if type(envelope) is not dict or set(envelope) != {
        _TRUSTED_FORMAT_KEY,
        _TRUSTED_PAYLOAD_KEY,
    }:
        raise pickle.UnpicklingError("trusted publisher envelope is required")
    if envelope[_TRUSTED_FORMAT_KEY] != TRUSTED_INFO_FORMAT:
        raise pickle.UnpicklingError("trusted publisher format is unsupported")
    publisher_info = envelope[_TRUSTED_PAYLOAD_KEY]
    if type(publisher_info) is not dict:
        raise pickle.UnpicklingError("trusted publisher payload must be a dictionary")
    _validate_s1h_info_fields(publisher_info)
    try:
        normalized = _normalize_trusted_value(publisher_info)
    except ValueError as error:
        raise pickle.UnpicklingError(
            "trusted publisher structure is invalid"
        ) from error
    if _count_trusted_nodes(normalized) > _MAX_TRUSTED_CONTAINER_ITEMS:
        raise pickle.UnpicklingError("trusted publisher payload has too many values")
    return normalized


def _normalize_trusted_value(value: object, *, depth: int = 0) -> Any:
    if depth > _MAX_TRUSTED_DEPTH:
        raise ValueError("trusted info nesting is too deep")
    if value is None or type(value) is bool:
        return value
    if type(value) is int:
        if value < _INT64_MIN or value > _INT64_MAX:
            raise ValueError("trusted info integer is out of range")
        return value
    if type(value) is float:
        if not math.isfinite(value):
            raise ValueError("trusted info float is not finite")
        return value
    if type(value) is str:
        if len(value.encode("utf-8")) > _MAX_TRUSTED_STRING_BYTES:
            raise ValueError("trusted info string is too large")
        return value
    if type(value) is bytes:
        if len(value) > _MAX_TRUSTED_BINARY_BYTES:
            raise ValueError("trusted info bytes are too large")
        return value
    if type(value) is list:
        if len(value) > _MAX_TRUSTED_CONTAINER_ITEMS:
            raise ValueError("trusted info list is too large")
        return [
            _normalize_trusted_value(item, depth=depth + 1) for item in value
        ]
    if type(value) is dict:
        if len(value) > _MAX_TRUSTED_CONTAINER_ITEMS:
            raise ValueError("trusted info dictionary is too large")
        items = []
        for key, item in value.items():
            trusted_key = _validate_trusted_key(key)
            items.append(
                (trusted_key, _normalize_trusted_value(item, depth=depth + 1))
            )
        return dict(sorted(items, key=lambda pair: pair[0].encode("utf-8")))
    raise ValueError("trusted info value type is unsupported")


def _validate_trusted_key(key: object) -> str:
    if type(key) is not str or not key or len(key.encode("utf-8")) > 255:
        raise ValueError("trusted info key is invalid")
    return key


def _count_trusted_nodes(value: object) -> int:
    if type(value) is list:
        return 1 + sum(_count_trusted_nodes(item) for item in value)
    if type(value) is dict:
        return 1 + sum(_count_trusted_nodes(item) for item in value.values())
    return 1


def _validate_s1h_info(
    info: Mapping[str, Any],
) -> tuple[int, np.ndarray, np.ndarray]:
    """Admit only the lidar fields required by the legacy S1H pipeline."""

    _validate_s1h_info_fields(info)
    columns = _point_columns(info)
    raw_boxes = info["boxes"]
    raw_labels = info["labels"]
    if type(raw_boxes) is not list:
        raise _RowValidationError("box shape", "must be a list of 7 or 9 values")
    if type(raw_labels) is not list:
        raise _RowValidationError("labels", "must be a list of integers")
    if len(raw_boxes) != len(raw_labels):
        raise _RowValidationError(
            "boxes and labels",
            "must contain the same number of entries",
        )

    box_width = 9
    normalized_boxes: list[list[float]] = []
    for raw_box in raw_boxes:
        if type(raw_box) is not list or len(raw_box) not in _S1H_BOX_WIDTHS:
            raise _RowValidationError(
                "box shape",
                "must contain a consistent 7 or 9 values per box",
            )
        if normalized_boxes and len(raw_box) != box_width:
            raise _RowValidationError(
                "box shape",
                "must contain a consistent 7 or 9 values per box",
            )
        box_width = len(raw_box)
        normalized_box = []
        for value in raw_box:
            if type(value) not in (int, float) or not math.isfinite(value):
                raise _RowValidationError(
                    "box values",
                    "must contain only finite numbers",
                )
            normalized_box.append(float(value))
        normalized_boxes.append(normalized_box)

    normalized_labels = []
    for label in raw_labels:
        if type(label) is not int or label < 0 or label > _INT16_MAX:
            raise _RowValidationError(
                "labels",
                "must contain non-negative int16 integers",
            )
        normalized_labels.append(label)

    boxes = np.asarray(normalized_boxes, dtype=np.float32).reshape(-1, box_width)
    labels = np.asarray(normalized_labels, dtype=np.int64)
    return columns, boxes, labels


def _validate_s1h_info_fields(info: Mapping[str, Any]) -> None:
    fields = set(info)
    if not _S1H_INFO_REQUIRED_FIELDS.issubset(fields) or not fields.issubset(
        _S1H_INFO_FIELDS
    ):
        raise _RowValidationError(
            "trusted info allowlist",
            "permits exactly boxes, labels, and optional lidar_feature_count",
        )


def _point_columns(info: Mapping[str, Any]) -> int:
    columns = info.get("lidar_feature_count", 5)
    if isinstance(columns, bool) or not isinstance(columns, int):
        raise _RowValidationError("point shape", "has an invalid feature count")
    if columns < 1 or columns > 64:
        raise _RowValidationError("point shape", "has an invalid feature count")
    return columns


def _unpack_points(payload: bytes, *, columns: int) -> np.ndarray:
    row_bytes = np.dtype(np.float32).itemsize * columns
    if len(payload) % row_bytes:
        raise _RowValidationError("point shape", "does not match feature count")
    return np.frombuffer(payload, dtype=np.float32).reshape(-1, columns).copy()


def _validate_positive_integer(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value <= 0:
        raise ValueError(f"{name} must be a positive integer")
    return value


def _validate_non_negative_integer(name: str, value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError(f"{name} must be a non-negative integer")
    return value
