"""Dataset row schema and serialization helpers."""

from __future__ import annotations

import hashlib
import io
import json
import math
import pickle
import pickletools
import re
from collections.abc import Mapping, Sequence
from typing import Any

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
VALID_SPLITS = frozenset({"train", "val", "test"})
_SHA256_PATTERN = re.compile(r"^[0-9a-f]{64}$")
_INT16_MAX = (1 << 15) - 1
_INT64_MIN = -(1 << 63)
_INT64_MAX = (1 << 63) - 1
_TRUSTED_FORMAT_KEY = "__raytrain_publisher_format__"
TRUSTED_INFO_FORMAT = "trusted-info-v1"
_TRUSTED_PAYLOAD_KEY = "payload"
_MAX_TRUSTED_PICKLE_BYTES = 64 * 1024 * 1024
_MAX_TRUSTED_PICKLE_OPCODES = 1_000_000
_MAX_TRUSTED_CONTAINER_ITEMS = 100_000
_MAX_TRUSTED_DEPTH = 32
_MAX_TRUSTED_STRING_BYTES = 1024 * 1024
_MAX_TRUSTED_BINARY_BYTES = 32 * 1024 * 1024
_SENSITIVE_KEY_FRAGMENTS = (
    "api_key",
    "authorization",
    "credential",
    "password",
    "private_key",
    "secret_access_key",
)
_SENSITIVE_KEYS = frozenset(
    {
        "access_token",
        "ak",
        "bearer_token",
        "id_token",
        "refresh_token",
        "session_token",
        "sk",
    }
)
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


class _RestrictedUnpickler(pickle.Unpickler):
    def find_class(self, module: str, name: str) -> object:
        raise pickle.UnpicklingError("forbidden pickle global")

    def persistent_load(self, persistent_id: object) -> object:
        raise pickle.UnpicklingError("forbidden pickle persistent reference")


def get_arrow_schema() -> object:
    """Build the non-nullable PyArrow schema for Parquet v1 rows."""

    try:
        import pyarrow as pa
    except ModuleNotFoundError as error:
        raise RuntimeError(
            "pyarrow==25.0.1 is required to build the Parquet schema"
        ) from error

    fields = [
        pa.field("token", pa.string(), nullable=False),
        pa.field("scene", pa.string(), nullable=False),
        pa.field("split", pa.string(), nullable=False),
        pa.field("class_ids", pa.list_(pa.int16()), nullable=False),
        pa.field("timestamp", pa.int64(), nullable=False),
        pa.field("points", pa.binary(), nullable=False),
        pa.field("info", pa.binary(), nullable=False),
        pa.field("source_digest", pa.string(), nullable=False),
    ]
    return pa.schema(fields, metadata={b"raytrain.schema_version": b"parquet-v1"})


def dump_trusted_info(info: object) -> bytes:
    """Serialize publisher-owned info without executable globals."""

    if type(info) is not dict:
        raise ValueError("trusted info must be an explicit dictionary")
    normalized = _normalize_trusted_value(info)
    if _count_trusted_nodes(normalized) > _MAX_TRUSTED_CONTAINER_ITEMS:
        raise ValueError("trusted info contains too many values")

    envelope = {
        _TRUSTED_FORMAT_KEY: TRUSTED_INFO_FORMAT,
        _TRUSTED_PAYLOAD_KEY: normalized,
    }
    payload = pickle.dumps(envelope, protocol=pickle.HIGHEST_PROTOCOL)
    if len(payload) > _MAX_TRUSTED_PICKLE_BYTES:
        raise ValueError("trusted info exceeds the publisher size limit")
    return payload


def load_trusted_info(payload: object) -> dict[str, Any]:
    """Load publisher-owned info without executing pickle globals."""

    raw_payload = _validate_pickle_payload(payload)
    _reject_forbidden_pickle_opcodes(raw_payload)

    stream = io.BytesIO(raw_payload)
    try:
        envelope = _RestrictedUnpickler(stream).load()
    except pickle.UnpicklingError:
        raise
    except Exception as error:
        raise pickle.UnpicklingError("invalid trusted publisher pickle") from error
    if stream.read(1):
        raise pickle.UnpicklingError("trusted publisher pickle has trailing data")

    expected_keys = {_TRUSTED_FORMAT_KEY, _TRUSTED_PAYLOAD_KEY}
    if type(envelope) is not dict or set(envelope) != expected_keys:
        raise pickle.UnpicklingError("trusted publisher envelope is required")
    if envelope[_TRUSTED_FORMAT_KEY] != TRUSTED_INFO_FORMAT:
        raise pickle.UnpicklingError("trusted publisher info format is unsupported")
    if type(envelope[_TRUSTED_PAYLOAD_KEY]) is not dict:
        raise pickle.UnpicklingError("trusted publisher info payload must be a dictionary")

    try:
        normalized = _normalize_trusted_value(envelope[_TRUSTED_PAYLOAD_KEY])
    except ValueError as error:
        raise pickle.UnpicklingError("trusted publisher info structure is invalid") from error
    if _count_trusted_nodes(normalized) > _MAX_TRUSTED_CONTAINER_ITEMS:
        raise pickle.UnpicklingError("trusted publisher info contains too many values")
    return normalized


def _validate_pickle_payload(payload: object) -> bytes:
    if not isinstance(payload, (bytes, bytearray, memoryview)):
        raise pickle.UnpicklingError("trusted publisher pickle must be bytes-like")
    raw_payload = bytes(payload)
    if not raw_payload or len(raw_payload) > _MAX_TRUSTED_PICKLE_BYTES:
        raise pickle.UnpicklingError("trusted publisher pickle size is invalid")
    return raw_payload


def _reject_forbidden_pickle_opcodes(payload: bytes) -> None:
    try:
        for opcode_count, (opcode, _argument, _position) in enumerate(
            pickletools.genops(payload), start=1
        ):
            if opcode_count > _MAX_TRUSTED_PICKLE_OPCODES:
                raise pickle.UnpicklingError(
                    "trusted publisher pickle contains too many operations"
                )
            if opcode.name in _FORBIDDEN_PICKLE_OPCODES:
                raise pickle.UnpicklingError(f"forbidden pickle opcode: {opcode.name}")
    except pickle.UnpicklingError:
        raise
    except Exception as error:
        raise pickle.UnpicklingError("invalid trusted publisher pickle stream") from error


def _normalize_trusted_value(value: object, *, depth: int = 0) -> Any:
    if depth > _MAX_TRUSTED_DEPTH:
        raise ValueError("trusted info exceeds the supported nesting depth")
    if value is None or type(value) is bool:
        return value
    if type(value) is int:
        if value < _INT64_MIN or value > _INT64_MAX:
            raise ValueError("trusted info integers must fit int64")
        return value
    if type(value) is float:
        if not math.isfinite(value):
            raise ValueError("trusted info floats must be finite")
        return value
    if type(value) is str:
        if len(value.encode("utf-8")) > _MAX_TRUSTED_STRING_BYTES:
            raise ValueError("trusted info string exceeds the supported size")
        return value
    if type(value) is bytes:
        if len(value) > _MAX_TRUSTED_BINARY_BYTES:
            raise ValueError("trusted info bytes exceed the supported size")
        return value
    if type(value) is list:
        if len(value) > _MAX_TRUSTED_CONTAINER_ITEMS:
            raise ValueError("trusted info list exceeds the supported size")
        return [_normalize_trusted_value(item, depth=depth + 1) for item in value]
    if type(value) is dict:
        if len(value) > _MAX_TRUSTED_CONTAINER_ITEMS:
            raise ValueError("trusted info dictionary exceeds the supported size")
        normalized_items = []
        for key, item in value.items():
            normalized_key = _validate_trusted_key(key)
            normalized_items.append(
                (normalized_key, _normalize_trusted_value(item, depth=depth + 1))
            )
        return dict(sorted(normalized_items, key=lambda pair: pair[0].encode("utf-8")))
    raise ValueError("trusted info contains an unsupported value type")


def _validate_trusted_key(key: object) -> str:
    if type(key) is not str or not key or len(key.encode("utf-8")) > 255:
        raise ValueError("trusted info keys must be short non-empty strings")
    normalized_key = re.sub(r"[^a-z0-9]+", "_", key.lower()).strip("_")
    if normalized_key in _SENSITIVE_KEYS or any(
        fragment in normalized_key for fragment in _SENSITIVE_KEY_FRAGMENTS
    ):
        raise ValueError("trusted info must not contain sensitive credential fields")
    return key


def _count_trusted_nodes(value: object) -> int:
    if type(value) is list:
        return 1 + sum(_count_trusted_nodes(item) for item in value)
    if type(value) is dict:
        return 1 + sum(_count_trusted_nodes(item) for item in value.values())
    return 1


def compute_source_digest(
    *,
    token: str,
    scene: str,
    split: str,
    class_ids: Sequence[int],
    timestamp: int,
    points: bytes,
    info: bytes,
) -> str:
    """Return a stable digest for one source sample."""

    metadata_values = validate_sample_metadata(
        token=token,
        scene=scene,
        split=split,
        class_ids=class_ids,
        timestamp=timestamp,
    )
    metadata = json.dumps(
        metadata_values,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    point_payload = _validate_binary_payload("points", points, float32_aligned=True)
    info_payload = _validate_binary_payload("info", info)

    digest = hashlib.sha256(b"raytrain-source-v1\x00")
    for component in (metadata, point_payload, info_payload):
        digest.update(len(component).to_bytes(8, byteorder="big", signed=False))
        digest.update(component)
    return digest.hexdigest()


def validate_sample_row(row: object) -> dict[str, Any]:
    """Validate and copy one Parquet row."""

    if not isinstance(row, Mapping):
        raise ValueError("row must be a mapping")
    if set(row) != set(ROW_FIELD_NAMES):
        raise ValueError("row fields must exactly match the lidar-only v1 schema")

    metadata = validate_sample_metadata(
        token=row["token"],
        scene=row["scene"],
        split=row["split"],
        class_ids=row["class_ids"],
        timestamp=row["timestamp"],
    )
    token = metadata["token"]
    scene = metadata["scene"]
    split = metadata["split"]
    class_ids = metadata["class_ids"]
    timestamp = metadata["timestamp"]
    points = _validate_binary_payload("points", row["points"], float32_aligned=True)
    info = _validate_binary_payload("info", row["info"])
    try:
        load_trusted_info(info)
    except (pickle.UnpicklingError, ValueError) as error:
        raise ValueError("info must use the trusted publisher pickle structure") from error
    source_digest = row["source_digest"]
    if not isinstance(source_digest, str) or not _SHA256_PATTERN.fullmatch(source_digest):
        raise ValueError("source_digest must be a lowercase SHA-256 hex string")

    expected_digest = compute_source_digest(
        token=token,
        scene=scene,
        split=split,
        class_ids=class_ids,
        timestamp=timestamp,
        points=points,
        info=info,
    )
    if source_digest != expected_digest:
        raise ValueError("source_digest does not match the validated row payload")

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


def validate_sample_metadata(
    *,
    token: object,
    scene: object,
    split: object,
    class_ids: object,
    timestamp: object,
) -> dict[str, Any]:
    """Validate and copy the lightweight metadata used before lidar payload reads."""

    return {
        "token": _validate_identifier("token", token),
        "scene": _validate_identifier("scene", scene),
        "split": _validate_split(split),
        "class_ids": _validate_class_ids(class_ids),
        "timestamp": _validate_timestamp(timestamp),
    }


def _validate_identifier(field_name: str, value: object) -> str:
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError(f"{field_name} must be a non-empty normalized string")
    if len(value.encode("utf-8")) > 255:
        raise ValueError(f"{field_name} must be at most 255 UTF-8 bytes")
    if value in {".", ".."} or "/" in value or "\\" in value or "://" in value:
        raise ValueError(f"{field_name} must not contain path traversal or URI syntax")
    if any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise ValueError(f"{field_name} must not contain control characters")
    return value


def _validate_split(value: object) -> str:
    if not isinstance(value, str) or value not in VALID_SPLITS:
        raise ValueError("split must be one of train, val, or test")
    return value


def _validate_class_ids(value: object) -> tuple[int, ...]:
    if not isinstance(value, Sequence) or isinstance(value, (str, bytes, bytearray)):
        raise ValueError("class_ids must be a sequence of int16 values")

    normalized = []
    for class_id in value:
        if isinstance(class_id, bool) or not isinstance(class_id, (int, np.integer)):
            raise ValueError("class_ids must contain only integers")
        normalized_id = int(class_id)
        if normalized_id < 0 or normalized_id > _INT16_MAX:
            raise ValueError("class_ids values must fit non-negative int16")
        normalized.append(normalized_id)
    return tuple(normalized)


def _validate_timestamp(value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, (int, np.integer)):
        raise ValueError("timestamp must be an int64 integer")
    normalized = int(value)
    if normalized < _INT64_MIN or normalized > _INT64_MAX:
        raise ValueError("timestamp must fit int64")
    return normalized


def _validate_binary_payload(
    field_name: str,
    value: object,
    *,
    float32_aligned: bool = False,
) -> bytes:
    if not isinstance(value, (bytes, bytearray, memoryview)):
        raise ValueError(f"{field_name} must be bytes-like")
    payload = bytes(value)
    if not payload:
        raise ValueError(f"{field_name} must not be empty")
    if float32_aligned and len(payload) % np.dtype(np.float32).itemsize != 0:
        raise ValueError(f"{field_name} must contain complete float32 values")
    return payload


def pack_float32_points(points: np.ndarray) -> bytes:
    """Pack a float32 point matrix into its raw payload."""

    if not isinstance(points, np.ndarray) or points.dtype != np.dtype(np.float32):
        raise ValueError("points must be a float32 NumPy array")
    if points.ndim != 2:
        raise ValueError("points must be a two-dimensional matrix")
    if points.shape[1] <= 0:
        raise ValueError("points must have a positive column count")

    return np.ascontiguousarray(points).tobytes(order="C")


def unpack_float32_points(payload: bytes, *, columns: int) -> np.ndarray:
    """Unpack a raw float32 payload into a point matrix."""

    if isinstance(columns, bool) or not isinstance(columns, int) or columns <= 0:
        raise ValueError("columns must be a positive integer")
    if not isinstance(payload, (bytes, bytearray, memoryview)):
        raise ValueError("point payload must be bytes-like")

    raw_payload = bytes(payload)
    float_size = np.dtype(np.float32).itemsize
    row_size = float_size * columns
    if len(raw_payload) % row_size != 0:
        raise ValueError("point payload length must be divisible by the float32 row size")

    return np.frombuffer(raw_payload, dtype=np.float32).reshape(-1, columns).copy()
