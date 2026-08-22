#!/usr/bin/env python3
"""Validate BEVFusion annotation paths against the platform data mount."""

from __future__ import annotations

import argparse
import os
import pickle
import sys
from pathlib import Path
from typing import Any, Dict, Iterable, Optional, Sequence


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--train", required=True, help="train pkl relative to PLATFORM_DATASET_PATH")
    parser.add_argument("--val", required=True, help="validation pkl relative to PLATFORM_DATASET_PATH")
    parser.add_argument("--samples", type=int, default=128, help="records to inspect per split")
    parser.add_argument("--max-train", type=int, help="train records to inspect")
    parser.add_argument("--max-val", type=int, help="validation records to inspect")
    return parser.parse_args(argv)


def split_sample_limit(args: argparse.Namespace, split: str) -> int:
    specific = args.max_train if split == "train" else args.max_val
    limit = args.samples if specific is None else specific
    if limit < 1:
        raise ValueError("sample limits must be positive")
    return limit


def resolve_annotation_path(root: Path, relative: str) -> Path:
    """Resolve an annotation path without allowing escape from the data root."""
    requested = Path(relative)
    if requested.is_absolute():
        raise ValueError("annotation paths must be relative to PLATFORM_DATASET_PATH")
    resolved_root = root.resolve()
    resolved = (resolved_root / requested).resolve()
    try:
        inside_root = os.path.commonpath((str(resolved_root), str(resolved))) == str(resolved_root)
    except ValueError:
        inside_root = False
    if not inside_root:
        raise ValueError("annotation paths must stay inside PLATFORM_DATASET_PATH")
    return resolved


def resolver_module_dir(script_path: Path) -> Path:
    """Locate platform_paths.py both in an algorithm checkout and this repo."""
    candidates = (
        script_path.parent.parent / "mmdet3d" / "datasets",
        script_path.parent / "patches",
    )
    for candidate in candidates:
        if (candidate / "platform_paths.py").is_file():
            return candidate
    raise FileNotFoundError("platform_paths.py is not installed in mmdet3d/datasets")


def load_infos(path: Path) -> Iterable[Dict[str, Any]]:
    with path.open("rb") as stream:
        payload = pickle.load(stream)
    if isinstance(payload, dict):
        payload = payload.get("infos", payload)
    if not isinstance(payload, list):
        raise TypeError(f"{path} does not contain a list of infos")
    return payload


def candidate_paths(info: Dict[str, Any]) -> Iterable[str]:
    lidar = info.get("lidar_path")
    if lidar:
        yield lidar
    for sweep in info.get("sweeps", [])[:1]:
        if sweep.get("data_path"):
            yield sweep["data_path"]
    for camera in info.get("cams", {}).values():
        if camera.get("data_path"):
            yield camera["data_path"]


def main() -> int:
    args = parse_args()
    root = Path(os.environ["PLATFORM_DATASET_PATH"]).resolve()
    module_dir = resolver_module_dir(Path(__file__).resolve())
    sys.path.insert(0, str(module_dir))
    from platform_paths import DatasetPathResolver  # pylint: disable=import-outside-toplevel

    checked = 0
    missing = []
    for split, relative in (("train", args.train), ("val", args.val)):
        annotation = resolve_annotation_path(root, relative)
        infos = load_infos(annotation)
        print(f"ANNOTATION path={annotation} samples={len(infos)}", flush=True)
        resolver = DatasetPathResolver(str(root))
        for info in infos[: split_sample_limit(args, split)]:
            for recorded in candidate_paths(info):
                resolved = resolver.resolve(recorded)
                checked += 1
                if not os.path.exists(resolved):
                    missing.append((recorded, resolved))
    print(f"PATH_CHECK checked={checked} missing={len(missing)}", flush=True)
    for recorded, resolved in missing[:10]:
        print(f"MISSING recorded={recorded} resolved={resolved}", flush=True)
    if missing:
        return 2
    print("BEVFUSION_PLATFORM_DATA_PREFLIGHT_OK", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
