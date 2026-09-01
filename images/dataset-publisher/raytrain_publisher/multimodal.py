"""Deterministic S1H camera + LiDAR WebDataset shard construction.

This module is intentionally separate from the legacy LiDAR-only Parquet v1
writer.  A caller must opt into the v2 contract, which prevents the existing
publisher from accidentally treating a fusion sample as a LiDAR-only row.
"""

from __future__ import annotations

import hashlib
import io
import json
import math
import posixpath
from collections.abc import Iterable, Mapping
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
import re
import tarfile
from typing import Any

from .input_security import read_input_bytes
from .pack import MAX_SHARD_BYTES
from .schema import validate_sample_metadata
from .tos_storage import MAX_INDEX_BYTES


MULTIMODAL_SCHEMA_VERSION = "s1h-multimodal-webdataset-v2"
MULTIMODAL_INDEX_FORMAT = "trusted-index-v3"
MULTIMODAL_INDEX_MANIFEST_FORMAT = "trusted-index-sharded-v2"
_PAYLOAD_FIELDS = frozenset({"lidar", "sweeps", "cameras"})
_INFO_REQUIRED_FIELDS = frozenset(
    {
        "boxes",
        "labels",
        "lidar_feature_count",
        "lidar2ego_rotation",
        "lidar2ego_translation",
        "ego2global_rotation",
        "ego2global_translation",
        "sweeps",
        "cams",
    }
)
_INFO_OPTIONAL_FIELDS = frozenset({"num_lidar_pts", "gt_velocity"})
_PATHLIKE_INFO_KEY = re.compile(r"(?:^|_)(?:path|root|uri|url|bucket|endpoint|file)(?:_|$)")
_SAFE_CAMERA_SUFFIX = re.compile(r"^\.[a-z0-9]{1,12}$")


@dataclass(frozen=True)
class MultimodalPackedShard:
    """Content-addressed TAR payload and safe per-sample manifest rows."""

    payload: bytes
    sha256: str
    members: tuple[dict[str, Any], ...]


@dataclass(frozen=True)
class MultimodalIndexDocument:
    """Verified path-free S1H fusion publication descriptors."""

    samples: tuple[dict[str, Any], ...]
    class_count: int
    cbgs_seed: int


def load_cloud_multimodal_index(
    *, storage: Any, source_root: str, source_index: str
) -> MultimodalIndexDocument:
    """Load a bounded JSON root and all digest-addressed v3 index parts."""

    _clean_relative_path(source_root, field_name="multimodal source root")
    _clean_relative_path(source_index, field_name="multimodal source index")
    root = _load_json_document(
        storage.get_index(source_index, maximum_bytes=MAX_INDEX_BYTES),
        field_name="multimodal index manifest",
    )
    required = {
        "format",
        "sample_schema_version",
        "class_count",
        "cbgs_seed",
        "sample_count",
        "parts",
    }
    if set(root) != required or root["format"] != MULTIMODAL_INDEX_MANIFEST_FORMAT:
        raise ValueError("multimodal index manifest is invalid")
    class_count, cbgs_seed = _validate_index_policy(
        root["class_count"], root["cbgs_seed"]
    )
    if root["sample_schema_version"] != MULTIMODAL_SCHEMA_VERSION:
        raise ValueError("multimodal index schema is unsupported")
    sample_count = root["sample_count"]
    parts = root["parts"]
    if (
        isinstance(sample_count, bool)
        or not isinstance(sample_count, int)
        or sample_count < 1
        or type(parts) is not list
        or not parts
    ):
        raise ValueError("multimodal index manifest is invalid")

    manifest_directory = posixpath.dirname(source_index)
    part_directory = f"{posixpath.splitext(posixpath.basename(source_index))[0]}.parts"
    samples: list[dict[str, Any]] = []
    seen_tokens: set[str] = set()
    declared_count = 0
    for part in parts:
        if type(part) is not dict or set(part) != {"key", "sha256", "sample_count"}:
            raise ValueError("multimodal index part is invalid")
        digest, count, key = part["sha256"], part["sample_count"], part["key"]
        if (
            not isinstance(digest, str)
            or not re.fullmatch(r"[0-9a-f]{64}", digest)
            or isinstance(count, bool)
            or not isinstance(count, int)
            or count < 1
            or key != f"{part_directory}/sha256-{digest}.json"
        ):
            raise ValueError("multimodal index part is invalid")
        full_key = posixpath.join(manifest_directory, key)
        _clean_relative_path(full_key, field_name="multimodal index part")
        payload = storage.get_index(full_key, maximum_bytes=MAX_INDEX_BYTES)
        if hashlib.sha256(payload).hexdigest() != digest:
            raise ValueError("multimodal index part digest verification failed")
        document = _load_json_document(payload, field_name="multimodal index part")
        if (
            set(document)
            != {
                "format",
                "sample_schema_version",
                "class_count",
                "cbgs_seed",
                "samples",
            }
            or document["format"] != MULTIMODAL_INDEX_FORMAT
            or document["sample_schema_version"] != MULTIMODAL_SCHEMA_VERSION
            or document["class_count"] != class_count
            or document["cbgs_seed"] != cbgs_seed
            or type(document["samples"]) is not list
            or len(document["samples"]) != count
        ):
            raise ValueError("multimodal index part contract is invalid")
        for value in document["samples"]:
            sample = _validate_sample(value)
            if sample["token"] in seen_tokens:
                raise ValueError("multimodal index contains a duplicate token")
            seen_tokens.add(sample["token"])
            samples.append(sample)
        declared_count += count
    if declared_count != sample_count or len(samples) != sample_count:
        raise ValueError("multimodal index sample count is invalid")
    return MultimodalIndexDocument(
        samples=tuple(samples), class_count=class_count, cbgs_seed=cbgs_seed
    )


def _load_json_document(payload: object, *, field_name: str) -> dict[str, Any]:
    if not isinstance(payload, (bytes, bytearray)):
        raise ValueError(f"{field_name} is invalid")
    try:
        value = json.loads(bytes(payload))
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise ValueError(f"{field_name} is invalid") from None
    if type(value) is not dict:
        raise ValueError(f"{field_name} is invalid")
    return value


def _validate_index_policy(class_count: object, cbgs_seed: object) -> tuple[int, int]:
    if (
        isinstance(class_count, bool)
        or not isinstance(class_count, int)
        or not 1 <= class_count <= 32768
        or isinstance(cbgs_seed, bool)
        or not isinstance(cbgs_seed, int)
        or cbgs_seed < 0
    ):
        raise ValueError("multimodal index policy is invalid")
    return class_count, cbgs_seed


def _clean_relative_path(value: object, *, field_name: str) -> str:
    if (
        not isinstance(value, str)
        or not value
        or value.startswith("/")
        or "\\" in value
        or "://" in value
        or posixpath.normpath(value) != value
        or any(part in {"", ".", ".."} for part in value.split("/"))
    ):
        raise ValueError(f"{field_name} is invalid")
    return value


def pack_multimodal_shard(
    samples: Iterable[Mapping[str, Any]], *, input_root: Path
) -> MultimodalPackedShard:
    """Package bounded fusion sample bytes into a deterministic TAR payload.

    The resulting metadata has TAR member names only.  It never embeds raw
    object keys or the mounted source root, so consumers cannot discover the
    governed source layout from a released dataset version.
    """

    root = Path(input_root)
    normalized = _validated_sorted_samples(samples)
    if not normalized:
        raise ValueError("multimodal shard requires at least one sample")

    archive_members: list[tuple[str, bytes]] = []
    manifest_members: list[dict[str, Any]] = []
    total_bytes = 0
    for sample in normalized:
        payload_entries = _read_sample_payloads(sample, input_root=root)
        total_bytes += sum(len(payload) for _kind, _name, payload in payload_entries)
        if total_bytes > MAX_SHARD_BYTES:
            raise ValueError("multimodal shard exceeds the immutable object bound")
        payload_members = {kind: name for kind, name, _payload in payload_entries}
        source_digest = _source_digest(sample, payload_entries)
        metadata = {
            "token": sample["token"],
            "scene": sample["scene"],
            "split": sample["split"],
            "class_ids": list(sample["class_ids"]),
            "timestamp": sample["timestamp"],
            "info": sample["info"],
            "payload_members": {
                "lidar": payload_members["lidar"],
                "sweeps": [
                    payload_members[f"sweep:{index}"]
                    for index in range(len(sample["payloads"]["sweeps"]))
                ],
                "cameras": {
                    camera: payload_members[f"camera:{camera}"]
                    for camera in sorted(sample["payloads"]["cameras"], key=str.encode)
                },
            },
            "source_digest": source_digest,
        }
        metadata_name = f"{sample['token']}/metadata.json"
        metadata_payload = _canonical_json(metadata)
        archive_members.extend((name, payload) for _kind, name, payload in payload_entries)
        archive_members.append((metadata_name, metadata_payload))
        manifest_members.append(
            {
                "token": sample["token"],
                "scene": sample["scene"],
                "split": sample["split"],
                "class_ids": list(sample["class_ids"]),
                "timestamp": sample["timestamp"],
                "metadata_member": metadata_name,
                "source_digest": source_digest,
            }
        )

    payload = _write_tar(archive_members)
    if len(payload) > MAX_SHARD_BYTES:
        raise ValueError("multimodal shard exceeds the immutable object bound")
    return MultimodalPackedShard(
        payload=payload,
        sha256=hashlib.sha256(payload).hexdigest(),
        members=tuple(manifest_members),
    )


def _validated_sorted_samples(
    samples: Iterable[Mapping[str, Any]],
) -> tuple[dict[str, Any], ...]:
    normalized = tuple(_validate_sample(sample) for sample in samples)
    tokens = [sample["token"] for sample in normalized]
    if len(tokens) != len(set(tokens)):
        raise ValueError("multimodal samples contain a duplicate token")
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


def _validate_sample(sample: Mapping[str, Any]) -> dict[str, Any]:
    if not isinstance(sample, Mapping):
        raise ValueError("multimodal sample must be a mapping")
    required = {"schema_version", "token", "scene", "split", "class_ids", "timestamp", "payloads", "info"}
    if set(sample) != required:
        raise ValueError("multimodal sample fields must exactly match the v2 schema")
    if sample["schema_version"] != MULTIMODAL_SCHEMA_VERSION:
        raise ValueError("multimodal sample schema version is unsupported")
    metadata = validate_sample_metadata(
        token=sample["token"],
        scene=sample["scene"],
        split=sample["split"],
        class_ids=sample["class_ids"],
        timestamp=sample["timestamp"],
    )
    payloads = _validate_payloads(sample["payloads"])
    info = _validate_info(sample["info"], class_ids=metadata["class_ids"], sweep_count=len(payloads["sweeps"]), cameras=payloads["cameras"])
    return {**metadata, "schema_version": MULTIMODAL_SCHEMA_VERSION, "payloads": payloads, "info": info}


def _validate_payloads(value: object) -> dict[str, Any]:
    if type(value) is not dict or set(value) != _PAYLOAD_FIELDS:
        raise ValueError("multimodal payload fields must be lidar, sweeps, and cameras")
    lidar = _relative_key(value["lidar"], field_name="multimodal lidar payload")
    sweeps = value["sweeps"]
    if type(sweeps) is not list:
        raise ValueError("multimodal sweep payloads must be a list")
    normalized_sweeps = [
        _relative_key(item, field_name="multimodal sweep payload") for item in sweeps
    ]
    cameras = value["cameras"]
    if type(cameras) is not dict or not cameras:
        raise ValueError("multimodal camera payloads must be a non-empty dictionary")
    normalized_cameras: dict[str, str] = {}
    for camera, key in cameras.items():
        if not isinstance(camera, str) or not camera or camera.strip() != camera:
            raise ValueError("multimodal camera name must be normalized")
        normalized_cameras[camera] = _relative_key(
            key, field_name="multimodal camera payload"
        )
    return {"lidar": lidar, "sweeps": normalized_sweeps, "cameras": normalized_cameras}


def _relative_key(value: object, *, field_name: str) -> str:
    if not isinstance(value, str) or not value or "\x00" in value or "\\" in value or "://" in value:
        raise ValueError(f"{field_name} must be a normalized relative object key")
    candidate = PurePosixPath(value)
    if candidate.is_absolute() or not candidate.parts or any(part in {"", ".", ".."} for part in candidate.parts):
        raise ValueError(f"{field_name} must remain inside the source root")
    return candidate.as_posix()


def _validate_info(
    value: object,
    *,
    class_ids: tuple[int, ...],
    sweep_count: int,
    cameras: Mapping[str, str],
) -> dict[str, Any]:
    if type(value) is not dict or not _INFO_REQUIRED_FIELDS.issubset(value) or not set(value).issubset(_INFO_REQUIRED_FIELDS | _INFO_OPTIONAL_FIELDS):
        raise ValueError("multimodal info fields must match the v2 schema")
    _reject_pathlike_info(value)
    boxes = _finite_matrix(value["boxes"], columns=(7, 9), field_name="multimodal boxes")
    labels = value["labels"]
    if type(labels) is not list or len(labels) != len(boxes) or any(isinstance(label, bool) or not isinstance(label, int) or label < 0 or label > 32767 for label in labels):
        raise ValueError("multimodal labels must align with boxes")
    if set(labels) != set(class_ids):
        raise ValueError("multimodal labels must match top-level class IDs")
    feature_count = value["lidar_feature_count"]
    if isinstance(feature_count, bool) or not isinstance(feature_count, int) or not 1 <= feature_count <= 64:
        raise ValueError("multimodal lidar feature count is invalid")
    normalized: dict[str, Any] = {
        "boxes": boxes,
        "labels": list(labels),
        "lidar_feature_count": feature_count,
        "lidar2ego_rotation": _finite_matrix(value["lidar2ego_rotation"], columns=(3,), rows=3, field_name="multimodal lidar2ego_rotation"),
        "lidar2ego_translation": _finite_vector(value["lidar2ego_translation"], length=3, field_name="multimodal lidar2ego_translation"),
        "ego2global_rotation": _finite_matrix(value["ego2global_rotation"], columns=(3,), rows=3, field_name="multimodal ego2global_rotation"),
        "ego2global_translation": _finite_vector(value["ego2global_translation"], length=3, field_name="multimodal ego2global_translation"),
    }
    sweeps = value["sweeps"]
    if type(sweeps) is not list or len(sweeps) != sweep_count:
        raise ValueError("multimodal sweep metadata must align with payloads")
    normalized["sweeps"] = [_validate_sweep(item) for item in sweeps]
    camera_info = value["cams"]
    if type(camera_info) is not dict or set(camera_info) != set(cameras):
        raise ValueError("multimodal camera metadata must align with payloads")
    normalized["cams"] = {camera: _validate_camera(camera_info[camera]) for camera in sorted(cameras, key=str.encode)}
    if "num_lidar_pts" in value:
        normalized["num_lidar_pts"] = _finite_vector(value["num_lidar_pts"], length=len(boxes), field_name="multimodal num_lidar_pts")
    if "gt_velocity" in value:
        normalized["gt_velocity"] = _finite_matrix(value["gt_velocity"], columns=(2,), rows=len(boxes), field_name="multimodal gt_velocity")
    return normalized


def _validate_sweep(value: object) -> dict[str, Any]:
    if type(value) is not dict or set(value) != {"timestamp", "sensor2lidar_rotation", "sensor2lidar_translation"}:
        raise ValueError("multimodal sweep metadata is invalid")
    timestamp = value["timestamp"]
    if isinstance(timestamp, bool) or not isinstance(timestamp, int):
        raise ValueError("multimodal sweep timestamp is invalid")
    return {"timestamp": timestamp, "sensor2lidar_rotation": _finite_matrix(value["sensor2lidar_rotation"], columns=(3,), rows=3, field_name="multimodal sweep rotation"), "sensor2lidar_translation": _finite_vector(value["sensor2lidar_translation"], length=3, field_name="multimodal sweep translation")}


def _validate_camera(value: object) -> dict[str, Any]:
    fields = {"sensor2lidar_rotation", "sensor2lidar_translation", "sensor2ego_rotation", "sensor2ego_translation", "camera_intrinsics"}
    if type(value) is not dict or set(value) != fields:
        raise ValueError("multimodal camera metadata is invalid")
    return {
        "sensor2lidar_rotation": _finite_matrix(value["sensor2lidar_rotation"], columns=(3,), rows=3, field_name="multimodal camera rotation"),
        "sensor2lidar_translation": _finite_vector(value["sensor2lidar_translation"], length=3, field_name="multimodal camera translation"),
        "sensor2ego_rotation": _finite_matrix(value["sensor2ego_rotation"], columns=(3,), rows=3, field_name="multimodal camera ego rotation"),
        "sensor2ego_translation": _finite_vector(value["sensor2ego_translation"], length=3, field_name="multimodal camera ego translation"),
        "camera_intrinsics": _finite_matrix(value["camera_intrinsics"], columns=(3,), rows=3, field_name="multimodal camera intrinsics"),
    }


def _finite_vector(value: object, *, length: int, field_name: str) -> list[float]:
    if type(value) is not list or len(value) != length:
        raise ValueError(f"{field_name} has an invalid shape")
    return [_finite_number(item, field_name=field_name) for item in value]


def _finite_matrix(value: object, *, columns: tuple[int, ...], field_name: str, rows: int | None = None) -> list[list[float]]:
    if type(value) is not list or (rows is not None and len(value) != rows):
        raise ValueError(f"{field_name} has an invalid shape")
    normalized = []
    for row in value:
        if type(row) is not list or len(row) not in columns:
            raise ValueError(f"{field_name} has an invalid shape")
        normalized.append([_finite_number(item, field_name=field_name) for item in row])
    return normalized


def _finite_number(value: object, *, field_name: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(float(value)):
        raise ValueError(f"{field_name} must contain finite numeric values")
    return float(value)


def _reject_pathlike_info(value: object) -> None:
    if type(value) is dict:
        for key, item in value.items():
            if type(key) is not str or _PATHLIKE_INFO_KEY.search(key.lower()):
                raise ValueError("multimodal info must not contain source locations")
            _reject_pathlike_info(item)
    elif type(value) is list:
        for item in value:
            _reject_pathlike_info(item)
    elif type(value) is str and ("://" in value or "/" in value or "\\" in value):
        raise ValueError("multimodal info must not contain source locations")


def _read_sample_payloads(sample: Mapping[str, Any], *, input_root: Path) -> list[tuple[str, str, bytes]]:
    entries: list[tuple[str, str, bytes]] = []
    token = sample["token"]
    entries.append(("lidar", f"{token}/lidar.bin", _read_payload(input_root, sample["payloads"]["lidar"], "multimodal lidar payload")))
    for index, source_key in enumerate(sample["payloads"]["sweeps"]):
        entries.append((f"sweep:{index}", f"{token}/sweeps/{index:03d}.bin", _read_payload(input_root, source_key, "multimodal sweep payload")))
    for camera in sorted(sample["payloads"]["cameras"], key=str.encode):
        source_key = sample["payloads"]["cameras"][camera]
        suffix = PurePosixPath(source_key).suffix.lower()
        if not _SAFE_CAMERA_SUFFIX.fullmatch(suffix):
            suffix = ".bin"
        entries.append((f"camera:{camera}", f"{token}/cameras/{camera}{suffix}", _read_payload(input_root, source_key, "multimodal camera payload")))
    return entries


def _read_payload(input_root: Path, source_key: str, field_name: str) -> bytes:
    return read_input_bytes(input_root, source_key, maximum_bytes=MAX_SHARD_BYTES, field_name=field_name)


def _source_digest(sample: Mapping[str, Any], entries: list[tuple[str, str, bytes]]) -> str:
    digest = hashlib.sha256(b"raytrain-s1h-multimodal-source-v2\x00")
    metadata = {key: sample[key] for key in ("schema_version", "token", "scene", "split", "class_ids", "timestamp", "info")}
    for component in [_canonical_json(metadata), *[kind.encode("utf-8") + b"\x00" + payload for kind, _name, payload in entries]]:
        digest.update(len(component).to_bytes(8, byteorder="big", signed=False))
        digest.update(component)
    return digest.hexdigest()


def _write_tar(entries: Iterable[tuple[str, bytes]]) -> bytes:
    output = io.BytesIO()
    ordered = sorted(entries, key=lambda item: item[0].encode("utf-8"))
    with tarfile.open(fileobj=output, mode="w:", format=tarfile.USTAR_FORMAT) as archive:
        for name, payload in ordered:
            info = tarfile.TarInfo(name=name)
            info.size = len(payload)
            info.mode = 0o444
            info.mtime = 0
            info.uid = 0
            info.gid = 0
            info.uname = ""
            info.gname = ""
            archive.addfile(info, io.BytesIO(payload))
    return output.getvalue()


def _canonical_json(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode("utf-8")
