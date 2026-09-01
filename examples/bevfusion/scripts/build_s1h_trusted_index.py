#!/usr/bin/env python3
"""Convert platform-generated S1H PKLs into a restricted publisher index.

This command is intended for the administrator-controlled publication flow.
It accepts only PKLs produced by the pinned BEVFusion converter and emits the
explicit ``trusted-index-v2`` structure consumed by the dataset publisher.
"""

from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from functools import partial
import hashlib
import json
import math
import pickle
from collections.abc import Iterable, Mapping, Sequence
from pathlib import Path
from typing import Any

import numpy as np

from raytrain_publisher.pack import dump_trusted_index, dump_trusted_index_manifest


OBJECT_CLASSES = (
    "Car",
    "Truck_head",
    "Vehicle",
    "Rtgc",
    "Qc",
    "Twistlock_station",
    "Fork_lift",
    "Cone",
    "Pedestrian",
    "IGV",
)
CLASS_TO_ID = {name: index for index, name in enumerate(OBJECT_CLASSES)}
NAME_MAPPING = {
    "Cone": "Cone",
    "cone": "Cone",
    "pedestrian": "Pedestrian",
    "Person": "Pedestrian",
    "People": "Pedestrian",
    "Car": "Car",
    "car": "Car",
    "truck_head": "Truck_head",
    "Truck_head": "Truck_head",
    "truck_trailer.wo_container": "Vehicle",
    "Truck_trailer.wo_container": "Vehicle",
    "truck_trailer.w_container": "Vehicle",
    "Truck_trailer.w_container": "Vehicle",
    "Vehicle": "Vehicle",
    "vehicle": "Vehicle",
    "igv.w_container": "IGV",
    "igv.w_containerer": "IGV",
    "igv.wo_container": "IGV",
    "igv.wo_containerer": "IGV",
    "Bus": "Vehicle",
    "bus": "Vehicle",
    "Van": "Vehicle",
    "van": "Vehicle",
    "Goods_vehicle": "Vehicle",
    "goods_vehicle": "Vehicle",
    "RTG": "Rtgc",
    "Rtg": "Rtgc",
    "Qc": "Qc",
    "QC": "Qc",
    "lock_station": "Twistlock_station",
    "Lock_station": "Twistlock_station",
    "Twistlock_st": "Twistlock_station",
    "Twistlock_stati": "Twistlock_station",
    "Fork_lift": "Fork_lift",
    "Forklift": "Fork_lift",
    "Lift": "Fork_lift",
    "lift": "Fork_lift",
    "Fork_truck": "Fork_lift",
    "fork_truck": "Fork_lift",
    "aerial_lift": "Fork_lift",
}
VALID_SPLITS = frozenset(("train", "val", "test"))
POINT_COLUMNS = 4
DEFAULT_SAMPLES_PER_PART = 2000
MULTIMODAL_SCHEMA_VERSION = "s1h-multimodal-webdataset-v2"


def convert_infos(
    infos: Iterable[Mapping[str, Any]],
    *,
    split: str,
    source_root: Path,
    workers: int = 1,
) -> list[dict[str, Any]]:
    """Convert one converter split without retaining camera or internal paths."""

    if split not in VALID_SPLITS:
        raise ValueError("split must be train, val, or test")
    root = Path(source_root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError("source root must be a directory")
    if workers < 1 or workers > 64:
        raise ValueError("workers must be between 1 and 64")
    convert = partial(_convert_info, split=split, source_root=root)
    if workers == 1:
        return [convert(raw_info) for raw_info in infos]
    with ThreadPoolExecutor(max_workers=workers) as executor:
        return list(executor.map(convert, infos))


def merge_splits(
    *,
    train_infos: Iterable[Mapping[str, Any]],
    val_infos: Iterable[Mapping[str, Any]],
    source_root: Path,
    workers: int = 1,
) -> list[dict[str, Any]]:
    samples = convert_infos(
        train_infos,
        split="train",
        source_root=source_root,
        workers=workers,
    )
    samples.extend(
        convert_infos(
            val_infos,
            split="val",
            source_root=source_root,
            workers=workers,
        )
    )
    tokens = [sample["token"] for sample in samples]
    if len(tokens) != len(set(tokens)):
        raise ValueError("trusted index contains a duplicate token across splits")
    return samples


def convert_multimodal_infos(
    infos: Iterable[Mapping[str, Any]],
    *,
    split: str,
    source_root: Path,
    workers: int = 1,
) -> list[dict[str, Any]]:
    """Build path-free S1H fusion descriptors for the v2 publisher.

    The returned descriptor intentionally contains only relative object keys in
    ``payloads``.  Calibration and annotation metadata is copied into a small,
    JSON-compatible structure; the v2 publisher later fetches the referenced
    bytes and writes them into immutable WebDataset shards.
    """

    if split not in VALID_SPLITS:
        raise ValueError("split must be train, val, or test")
    root = Path(source_root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError("source root must be a directory")
    if workers < 1 or workers > 64:
        raise ValueError("workers must be between 1 and 64")
    convert = partial(_convert_multimodal_info, split=split, source_root=root)
    if workers == 1:
        return [convert(raw_info) for raw_info in infos]
    with ThreadPoolExecutor(max_workers=workers) as executor:
        return list(executor.map(convert, infos))


def dump_index(samples: list[dict[str, Any]], *, cbgs_seed: int) -> bytes:
    return dump_trusted_index(samples, class_count=len(OBJECT_CLASSES), cbgs_seed=cbgs_seed)


def write_index_bundle(
    samples: list[dict[str, Any]],
    *,
    output: Path,
    cbgs_seed: int,
    samples_per_part: int = DEFAULT_SAMPLES_PER_PART,
) -> dict[str, int]:
    """Write bounded v2 parts first and the small root manifest last."""

    if not samples:
        raise ValueError("trusted index requires at least one sample")
    if samples_per_part < 1 or samples_per_part > 5000:
        raise ValueError("samples_per_part must be between 1 and 5000")
    target = Path(output)
    target.parent.mkdir(parents=True, exist_ok=True)
    parts_directory_name = f"{target.stem}.parts"
    parts_directory = target.parent / parts_directory_name
    parts_directory.mkdir(parents=True, exist_ok=True)
    records = []
    total_part_bytes = 0
    initial_chunks = (
        samples[offset : offset + samples_per_part]
        for offset in range(0, len(samples), samples_per_part)
    )
    encoded_parts = (
        encoded
        for chunk in initial_chunks
        for encoded in _encode_bounded_parts(chunk, cbgs_seed=cbgs_seed)
    )
    for chunk, payload in encoded_parts:
        digest = hashlib.sha256(payload).hexdigest()
        filename = f"sha256-{digest}.pkl"
        part_path = parts_directory / filename
        if part_path.exists() and part_path.read_bytes() != payload:
            raise ValueError("content-addressed trusted index part conflicts")
        if not part_path.exists():
            part_path.write_bytes(payload)
        records.append(
            {
                "key": f"{parts_directory_name}/{filename}",
                "sha256": digest,
                "sample_count": len(chunk),
            }
        )
        total_part_bytes += len(payload)
    manifest = dump_trusted_index_manifest(
        records,
        class_count=len(OBJECT_CLASSES),
        cbgs_seed=cbgs_seed,
        sample_count=len(samples),
    )
    target.write_bytes(manifest)
    return {
        "index_bytes": len(manifest),
        "part_bytes": total_part_bytes,
        "part_count": len(records),
    }


def _encode_bounded_parts(
    samples: list[dict[str, Any]], *, cbgs_seed: int
) -> Iterable[tuple[list[dict[str, Any]], bytes]]:
    """Bisect only serialization-size failures until every part is bounded."""

    try:
        yield samples, dump_index(samples, cbgs_seed=cbgs_seed)
        return
    except ValueError as error:
        message = str(error).lower()
        is_size_error = any(
            fragment in message
            for fragment in ("exceeds", "too many", "size limit", "supported size")
        )
        if not is_size_error or len(samples) == 1:
            raise
    midpoint = len(samples) // 2
    yield from _encode_bounded_parts(samples[:midpoint], cbgs_seed=cbgs_seed)
    yield from _encode_bounded_parts(samples[midpoint:], cbgs_seed=cbgs_seed)


def _convert_info(
    raw_info: Mapping[str, Any],
    *,
    split: str,
    source_root: Path,
) -> dict[str, Any]:
    if not isinstance(raw_info, Mapping):
        raise ValueError("S1H info must be a mapping")
    token = _required_identifier(raw_info, "token")
    scene = _required_identifier(raw_info, "scene_token")
    timestamp = raw_info.get("timestamp")
    if isinstance(timestamp, bool) or not isinstance(timestamp, (int, np.integer)):
        raise ValueError("timestamp must be an integer")

    lidar_path = _relative_lidar_path(raw_info.get("lidar_path"), source_root)
    boxes = np.asarray(raw_info.get("gt_boxes"))
    names = np.asarray(raw_info.get("gt_names"))
    if boxes.ndim != 2 or boxes.shape[1] not in (7, 9):
        raise ValueError("gt_boxes must have shape [N, 7] or [N, 9]")
    if names.ndim != 1 or len(names) != len(boxes):
        raise ValueError("gt_names must align with gt_boxes")

    selected_boxes: list[list[float]] = []
    labels: list[int] = []
    for raw_box, raw_name in zip(boxes, names):
        mapped_name = NAME_MAPPING.get(str(raw_name))
        label = CLASS_TO_ID.get(mapped_name) if mapped_name is not None else None
        if label is None:
            continue
        box = [float(value) for value in raw_box.tolist()]
        if any(not math.isfinite(value) for value in box):
            raise ValueError("gt_boxes must contain finite values")
        selected_boxes.append(box)
        labels.append(label)

    return {
        "token": token,
        "scene": scene,
        "split": split,
        "class_ids": sorted(set(labels)),
        "timestamp": int(timestamp),
        "lidar_path": lidar_path,
        "point_columns": POINT_COLUMNS,
        "info": {
            "boxes": selected_boxes,
            "labels": labels,
            "lidar_feature_count": POINT_COLUMNS,
        },
    }


def _convert_multimodal_info(
    raw_info: Mapping[str, Any],
    *,
    split: str,
    source_root: Path,
) -> dict[str, Any]:
    """Normalize one BEVFusion sample without serializing source locations."""

    base = _convert_info(raw_info, split=split, source_root=source_root)
    sweeps = raw_info.get("sweeps")
    cameras = raw_info.get("cams")
    if type(cameras) is not dict or not cameras:
        raise ValueError("multimodal camera metadata is required")
    if type(sweeps) is not list:
        raise ValueError("multimodal sweeps must be an explicit list")

    normalized_sweeps = []
    sweep_paths = []
    for index, sweep in enumerate(sweeps):
        if type(sweep) is not dict:
            raise ValueError("multimodal sweep metadata must be a dictionary")
        path_value = sweep.get("data_path", sweep.get("lidar_path"))
        sweep_paths.append(
            _relative_payload_path(
                path_value,
                source_root,
                field_name="multimodal sweep payload",
            )
        )
        normalized_sweeps.append(
            {
                "timestamp": _required_timestamp(sweep, "multimodal sweep timestamp"),
                "sensor2lidar_rotation": _matrix(
                    sweep.get("sensor2lidar_rotation"),
                    field_name="multimodal sweep sensor2lidar_rotation",
                ),
                "sensor2lidar_translation": _vector(
                    sweep.get("sensor2lidar_translation"),
                    field_name="multimodal sweep sensor2lidar_translation",
                ),
            }
        )

    normalized_cameras: dict[str, dict[str, list[list[float]] | list[float]]] = {}
    camera_paths: dict[str, str] = {}
    for camera_name in sorted(cameras, key=lambda value: str(value).encode("utf-8")):
        if not isinstance(camera_name, str) or not camera_name or camera_name.strip() != camera_name:
            raise ValueError("multimodal camera name must be a non-empty normalized string")
        camera = cameras[camera_name]
        if type(camera) is not dict:
            raise ValueError("multimodal camera metadata must be a dictionary")
        camera_paths[camera_name] = _relative_payload_path(
            camera.get("data_path"),
            source_root,
            field_name="multimodal camera payload",
        )
        normalized_cameras[camera_name] = {
            "sensor2lidar_rotation": _matrix(
                camera.get("sensor2lidar_rotation"),
                field_name="multimodal camera sensor2lidar_rotation",
            ),
            "sensor2lidar_translation": _vector(
                camera.get("sensor2lidar_translation"),
                field_name="multimodal camera sensor2lidar_translation",
            ),
            "sensor2ego_rotation": _matrix(
                camera.get("sensor2ego_rotation"),
                field_name="multimodal camera sensor2ego_rotation",
            ),
            "sensor2ego_translation": _vector(
                camera.get("sensor2ego_translation"),
                field_name="multimodal camera sensor2ego_translation",
            ),
            "camera_intrinsics": _matrix(
                camera.get("camera_intrinsics"),
                field_name="multimodal camera_intrinsics",
            ),
        }

    selected_indexes = _selected_annotation_indexes(raw_info)
    info = {
        "boxes": base["info"]["boxes"],
        "labels": base["info"]["labels"],
        "lidar_feature_count": POINT_COLUMNS,
        "lidar2ego_rotation": _matrix(
            raw_info.get("lidar2ego_rotation"),
            field_name="multimodal lidar2ego_rotation",
        ),
        "lidar2ego_translation": _vector(
            raw_info.get("lidar2ego_translation"),
            field_name="multimodal lidar2ego_translation",
        ),
        "ego2global_rotation": _matrix(
            raw_info.get("ego2global_rotation"),
            field_name="multimodal ego2global_rotation",
        ),
        "ego2global_translation": _vector(
            raw_info.get("ego2global_translation"),
            field_name="multimodal ego2global_translation",
        ),
        "sweeps": normalized_sweeps,
        "cams": normalized_cameras,
    }
    num_lidar_points = raw_info.get("num_lidar_pts")
    if num_lidar_points is not None:
        info["num_lidar_pts"] = _selected_finite_vector(
            num_lidar_points,
            selected_indexes,
            field_name="multimodal num_lidar_pts",
        )
    velocity = raw_info.get("gt_velocity")
    if velocity is not None:
        info["gt_velocity"] = _selected_finite_matrix(
            velocity,
            selected_indexes,
            columns=2,
            field_name="multimodal gt_velocity",
        )

    return {
        "token": base["token"],
        "scene": base["scene"],
        "split": base["split"],
        "class_ids": base["class_ids"],
        "timestamp": base["timestamp"],
        "schema_version": MULTIMODAL_SCHEMA_VERSION,
        "payloads": {
            "lidar": base["lidar_path"],
            "sweeps": sweep_paths,
            "cameras": camera_paths,
        },
        "info": info,
    }


def _required_identifier(info: Mapping[str, Any], field: str) -> str:
    value = info.get(field)
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError(f"{field} must be a non-empty normalized string")
    return value


def _relative_lidar_path(value: object, source_root: Path) -> str:
    return _relative_payload_path(value, source_root, field_name="lidar_path")


def _relative_payload_path(value: object, source_root: Path, *, field_name: str) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError(f"{field_name} must be a non-empty string")
    candidate = Path(value)
    try:
        resolved = (source_root / candidate).resolve(strict=True) if not candidate.is_absolute() else candidate.resolve(strict=True)
    except OSError as error:
        raise ValueError(f"{field_name} is not accessible inside the source root") from error
    try:
        relative = resolved.relative_to(source_root)
    except ValueError as error:
        raise ValueError(f"{field_name} must remain inside the source root") from error
    if not resolved.is_file():
        raise ValueError(f"{field_name} must identify a regular file inside the source root")
    return relative.as_posix()


def _required_timestamp(value: Mapping[str, Any], field_name: str) -> int:
    timestamp = value.get("timestamp")
    if isinstance(timestamp, bool) or not isinstance(timestamp, (int, np.integer)):
        raise ValueError(f"{field_name} must be an integer")
    return int(timestamp)


def _matrix(value: object, *, field_name: str) -> list[list[float]]:
    matrix = np.asarray(value)
    if matrix.shape != (3, 3):
        raise ValueError(f"{field_name} must have shape [3, 3]")
    try:
        normalized = matrix.astype(np.float64)
    except (TypeError, ValueError) as error:
        raise ValueError(f"{field_name} must be numeric") from error
    if not np.isfinite(normalized).all():
        raise ValueError(f"{field_name} must contain finite values")
    return [[float(item) for item in row] for row in normalized.tolist()]


def _vector(value: object, *, field_name: str) -> list[float]:
    vector = np.asarray(value)
    if vector.shape != (3,):
        raise ValueError(f"{field_name} must have shape [3]")
    try:
        normalized = vector.astype(np.float64)
    except (TypeError, ValueError) as error:
        raise ValueError(f"{field_name} must be numeric") from error
    if not np.isfinite(normalized).all():
        raise ValueError(f"{field_name} must contain finite values")
    return [float(item) for item in normalized.tolist()]


def _selected_annotation_indexes(raw_info: Mapping[str, Any]) -> list[int]:
    names = np.asarray(raw_info.get("gt_names"))
    return [
        index
        for index, raw_name in enumerate(names)
        if NAME_MAPPING.get(str(raw_name)) in CLASS_TO_ID
    ]


def _selected_finite_vector(
    value: object,
    indexes: list[int],
    *,
    field_name: str,
) -> list[float]:
    values = np.asarray(value)
    if values.ndim != 1 or len(values) < (max(indexes) + 1 if indexes else 0):
        raise ValueError(f"{field_name} must align with gt_names")
    try:
        normalized = values.astype(np.float64)
    except (TypeError, ValueError) as error:
        raise ValueError(f"{field_name} must be numeric") from error
    selected = normalized[indexes]
    if not np.isfinite(selected).all():
        raise ValueError(f"{field_name} must contain finite values")
    return [float(item) for item in selected.tolist()]


def _selected_finite_matrix(
    value: object,
    indexes: list[int],
    *,
    columns: int,
    field_name: str,
) -> list[list[float]]:
    values = np.asarray(value)
    if values.ndim != 2 or values.shape[1] != columns or len(values) < (max(indexes) + 1 if indexes else 0):
        raise ValueError(f"{field_name} must align with gt_names and have shape [N, {columns}]")
    try:
        normalized = values.astype(np.float64)
    except (TypeError, ValueError) as error:
        raise ValueError(f"{field_name} must be numeric") from error
    selected = normalized[indexes]
    if not np.isfinite(selected).all():
        raise ValueError(f"{field_name} must contain finite values")
    return [[float(item) for item in row] for row in selected.tolist()]


def load_infos(path: Path) -> list[Mapping[str, Any]]:
    with Path(path).open("rb") as stream:
        payload = pickle.load(stream)
    infos = payload.get("infos") if isinstance(payload, Mapping) else None
    if not isinstance(infos, list) or any(not isinstance(info, Mapping) for info in infos):
        raise ValueError("converter PKL must contain an explicit infos list")
    return infos


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--train-pkl", type=Path, action="append", default=[])
    parser.add_argument("--val-pkl", type=Path, action="append", default=[])
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--summary", type=Path)
    parser.add_argument("--cbgs-seed", type=int, default=0)
    parser.add_argument("--workers", type=int, default=1)
    parser.add_argument("--samples-per-part", type=int, default=DEFAULT_SAMPLES_PER_PART)
    arguments = parser.parse_args(argv)
    if not arguments.train_pkl:
        parser.error("at least one --train-pkl is required")
    if arguments.workers < 1 or arguments.workers > 64:
        parser.error("--workers must be between 1 and 64")
    if arguments.samples_per_part < 1 or arguments.samples_per_part > 5000:
        parser.error("--samples-per-part must be between 1 and 5000")
    return arguments


def main(argv: Sequence[str] | None = None) -> int:
    arguments = parse_args(argv)
    train_infos = [info for path in arguments.train_pkl for info in load_infos(path)]
    val_infos = [info for path in arguments.val_pkl for info in load_infos(path)]
    samples = merge_splits(
        train_infos=train_infos,
        val_infos=val_infos,
        source_root=arguments.source_root,
        workers=arguments.workers,
    )
    bundle = write_index_bundle(
        samples,
        output=arguments.output,
        cbgs_seed=arguments.cbgs_seed,
        samples_per_part=arguments.samples_per_part,
    )
    summary = {
        "class_count": len(OBJECT_CLASSES),
        **bundle,
        "samples": len(samples),
        "train_samples": sum(sample["split"] == "train" for sample in samples),
        "val_samples": sum(sample["split"] == "val" for sample in samples),
    }
    if arguments.summary is not None:
        arguments.summary.parent.mkdir(parents=True, exist_ok=True)
        arguments.summary.write_text(
            json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
    print(json.dumps(summary, separators=(",", ":"), sort_keys=True), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
