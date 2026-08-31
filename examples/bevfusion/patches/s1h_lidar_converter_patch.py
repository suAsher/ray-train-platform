#!/usr/bin/env python3
"""Apply the pinned S1H converter's explicit LiDAR-only publication mode."""

from __future__ import annotations

import argparse
from pathlib import Path


CREATE_SIGNATURE = """    site_name=None,
    min_scene_samples=81,
):"""
CREATE_SIGNATURE_PATCHED = """    site_name=None,
    min_scene_samples=81,
    lidar_only=False,
):"""
FILL_SIGNATURE = "def _fill_trainval_infos(nusc, train_scenes, val_scenes, test=False, max_sweeps=10, site_name=None):"
FILL_SIGNATURE_PATCHED = "def _fill_trainval_infos(nusc, train_scenes, val_scenes, test=False, max_sweeps=10, site_name=None, lidar_only=False):"
FILL_CALL = "max_sweeps=max_sweeps, site_name=site_name\n    )"
FILL_CALL_PATCHED = "max_sweeps=max_sweeps, site_name=site_name, lidar_only=lidar_only\n    )"
INFO_LOCATION = '            "location": location,\n        }'
INFO_LOCATION_PATCHED = '            "location": location,\n            "scene_token": sample["scene_token"],\n        }'
CAMERA_START = "        # obtain 6 image's information per frame\n"
SWEEP_START = "        # obtain sweeps for a single key-frame\n"


def patch_converter_source(source: str) -> str:
    if (
        CREATE_SIGNATURE_PATCHED in source
        and FILL_SIGNATURE_PATCHED in source
        and FILL_CALL_PATCHED in source
        and INFO_LOCATION_PATCHED in source
        and "        if not lidar_only:\n            # obtain 6 image" in source
    ):
        return source
    required = (
        CREATE_SIGNATURE,
        FILL_SIGNATURE,
        FILL_CALL,
        INFO_LOCATION,
        CAMERA_START,
        SWEEP_START,
    )
    if any(marker not in source for marker in required):
        raise ValueError("unexpected S1H converter layout")

    patched = source.replace(CREATE_SIGNATURE, CREATE_SIGNATURE_PATCHED, 1)
    patched = patched.replace(FILL_SIGNATURE, FILL_SIGNATURE_PATCHED, 1)
    patched = patched.replace(FILL_CALL, FILL_CALL_PATCHED, 1)
    patched = patched.replace(INFO_LOCATION, INFO_LOCATION_PATCHED, 1)

    camera_start = patched.index(CAMERA_START)
    sweep_start = patched.index(SWEEP_START, camera_start)
    camera_block = patched[camera_start:sweep_start]
    indented = "".join(
        ("    " + line) if line.strip() else line
        for line in camera_block.splitlines(keepends=True)
    )
    return (
        patched[:camera_start]
        + "        if not lidar_only:\n"
        + indented
        + patched[sweep_start:]
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("repo", type=Path)
    arguments = parser.parse_args()
    converter = arguments.repo / "tools" / "data_converter" / "nuscenes_converter.py"
    original = converter.read_text(encoding="utf-8")
    patched = patch_converter_source(original)
    converter.write_text(patched, encoding="utf-8")
    print(f"patched {converter}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
