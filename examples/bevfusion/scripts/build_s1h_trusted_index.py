#!/usr/bin/env python3
"""Convert platform-generated S1H PKLs into a restricted publisher index.

This command is intended for the administrator-controlled publication flow.
It accepts only PKLs produced by the pinned BEVFusion converter and emits the
explicit ``trusted-index-v2`` structure consumed by the dataset publisher.
"""

from __future__ import annotations

import argparse
import json
import math
import pickle
from collections.abc import Iterable, Mapping, Sequence
from pathlib import Path
from typing import Any

import numpy as np

from raytrain_publisher.pack import dump_trusted_index


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


def convert_infos(
    infos: Iterable[Mapping[str, Any]],
    *,
    split: str,
    source_root: Path,
) -> list[dict[str, Any]]:
    """Convert one converter split without retaining camera or internal paths."""

    if split not in VALID_SPLITS:
        raise ValueError("split must be train, val, or test")
    root = Path(source_root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError("source root must be a directory")
    samples = []
    for raw_info in infos:
        samples.append(_convert_info(raw_info, split=split, source_root=root))
    return samples


def merge_splits(
    *,
    train_infos: Iterable[Mapping[str, Any]],
    val_infos: Iterable[Mapping[str, Any]],
    source_root: Path,
) -> list[dict[str, Any]]:
    samples = convert_infos(train_infos, split="train", source_root=source_root)
    samples.extend(convert_infos(val_infos, split="val", source_root=source_root))
    tokens = [sample["token"] for sample in samples]
    if len(tokens) != len(set(tokens)):
        raise ValueError("trusted index contains a duplicate token across splits")
    return samples


def dump_index(samples: list[dict[str, Any]], *, cbgs_seed: int) -> bytes:
    return dump_trusted_index(samples, class_count=len(OBJECT_CLASSES), cbgs_seed=cbgs_seed)


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


def _required_identifier(info: Mapping[str, Any], field: str) -> str:
    value = info.get(field)
    if not isinstance(value, str) or not value or value.strip() != value:
        raise ValueError(f"{field} must be a non-empty normalized string")
    return value


def _relative_lidar_path(value: object, source_root: Path) -> str:
    if not isinstance(value, str) or not value:
        raise ValueError("lidar_path must be a non-empty string")
    candidate = Path(value)
    resolved = (source_root / candidate).resolve(strict=True) if not candidate.is_absolute() else candidate.resolve(strict=True)
    try:
        relative = resolved.relative_to(source_root)
    except ValueError as error:
        raise ValueError("lidar_path must remain inside the source root") from error
    if not resolved.is_file():
        raise ValueError("lidar_path must identify a regular file inside the source root")
    return relative.as_posix()


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
    arguments = parser.parse_args(argv)
    if not arguments.train_pkl:
        parser.error("at least one --train-pkl is required")
    return arguments


def main(argv: Sequence[str] | None = None) -> int:
    arguments = parse_args(argv)
    train_infos = [info for path in arguments.train_pkl for info in load_infos(path)]
    val_infos = [info for path in arguments.val_pkl for info in load_infos(path)]
    samples = merge_splits(
        train_infos=train_infos,
        val_infos=val_infos,
        source_root=arguments.source_root,
    )
    payload = dump_index(samples, cbgs_seed=arguments.cbgs_seed)
    arguments.output.parent.mkdir(parents=True, exist_ok=True)
    arguments.output.write_bytes(payload)
    summary = {
        "class_count": len(OBJECT_CLASSES),
        "index_bytes": len(payload),
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
