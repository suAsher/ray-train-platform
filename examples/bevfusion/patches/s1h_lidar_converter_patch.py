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
CAMERA_LOOP = "        for cam in camera_types:\n"
CAMERA_SELECTOR_HELPER = '''def _raytrain_camera_types(sample_data, site_name=None):
    """Select one pinned five-camera family from the current sample."""
    site_families = {
        "default": ("CAM_FRONT", "CAM_FRONT_RIGHT", "CAM_FRONT_LEFT", "CAM_BACK_LEFT", "CAM_BACK_RIGHT"),
        "common": ("CAM_FRONT_MID", "CAM_FRONT_RIGHT", "CAM_FRONT_LEFT", "CAM_REAR_LEFT", "CAM_REAR_RIGHT"),
        "rear-rear": ("CAM_FRONT_MID", "CAM_FRONT_RIGHT", "CAM_FRONT_LEFT", "CAM_REAR_LEFT", "CAM_REAR_REAR"),
        "hactl": ("CAM_MID FRONT", "CAM_FRONT RIGHT", "CAM_FRONT LEFT", "CAM_MID LEFT", "CAM_MID RIGHT"),
        "mxg": ("CAM_MID FRONT", "CAM_FRONT RIGHT", "CAM_FRONT LEFT", "CAM_REAR LEFT", "CAM_REAR RIGHT"),
        "jx": ("CAM_MID FRONT", "CAM_RIGHT FRONT", "CAM_LEFT FRONT", "CAM_LEFT REAR", "CAM_RIGHT REAR"),
        "uk": ("CAM_FRONT_MID", "CAM_FRONT_MID_RIGHT", "CAM_FRONT_LEFT_TOP", "CAM_REAR_LEFT", "CAM_REAR_RIGHT"),
        "mxg128": ("CAM_FRONT_MID", "CAM_FRONT_MID_RIGHT", "CAM_FRONT_MID_LEFT", "CAM_REAR_TOP_LEFT", "CAM_REAR_TOP_RIGHT"),
        "top": ("CAM_FRONT_TOP_MID", "CAM_FRONT_TOP_RIGHT", "CAM_FRONT_TOP_LEFT", "CAM_REAR_TOP_LEFT", "CAM_REAR_TOP_RIGHT"),
    }
    site_family = {
        "gy": "rear-rear",
        "xgcn": "rear-rear",
        "hactl": "hactl",
        "mxg": "mxg",
        "jx": "jx",
        "uk": "uk",
        "mxg128": "mxg128",
        "yt": "top",
        "jk": "top",
    }
    common_sites = {
        "jtg", "nt", "fz", "xm", "qz", "ez", "ml", "xs",
        "jgj", "cl", "yq", "tz", "lz", "zk", "rz", "tpy",
    }
    if site_name in common_sites:
        return list(site_families["common"])
    if site_name in site_family:
        return list(site_families[site_family[site_name]])

    available = frozenset(
        key for key in sample_data if isinstance(key, str) and key.startswith("CAM")
    )
    candidate_order = (
        "default",
        "top",
        "mxg128",
        "uk",
        "rear-rear",
        "hactl",
        "mxg",
        "jx",
        "common",
    )
    for family in candidate_order:
        cameras = site_families[family]
        if all(camera in available for camera in cameras):
            return list(cameras)
    raise KeyError(f"unsupported camera layout, available: {sorted(available)}")


'''


def patch_converter_source(source: str) -> str:
    if (
        CREATE_SIGNATURE_PATCHED in source
        and FILL_SIGNATURE_PATCHED in source
        and FILL_CALL_PATCHED in source
        and INFO_LOCATION_PATCHED in source
        and "        if not lidar_only:\n            # obtain 6 image" in source
        and 'camera_types = _raytrain_camera_types(sample["data"], site_name)' in source
        and "def _raytrain_camera_types(" in source
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

    fill_start = patched.index(FILL_SIGNATURE_PATCHED)
    patched = patched[:fill_start] + CAMERA_SELECTOR_HELPER + patched[fill_start:]

    camera_start = patched.index(CAMERA_START)
    camera_loop = patched.index(CAMERA_LOOP, camera_start)
    patched = (
        patched[:camera_start]
        + CAMERA_START
        + '        camera_types = _raytrain_camera_types(sample["data"], site_name)\n'
        + patched[camera_loop:]
    )
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
