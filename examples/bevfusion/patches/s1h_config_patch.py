#!/usr/bin/env python3
"""Apply the two reproducibility fixes required by the bev_3dod_s1h branch."""

from __future__ import annotations

import argparse
import os
from pathlib import Path


OLD_CHECKPOINT = "load_from: /storage/run_dir/lidar_0706/epoch_20.pth"
PORTABLE_CHECKPOINT = "load_from: null"
UNDEFINED_RANGE = "post_center_range: ${post_center_range}"
EXPLICIT_RANGE = "post_center_range: [-61.2, -61.2, -10.0, 61.2, 61.2, 10.0]"


def replace_once_or_confirm(source: str, old: str, new: str, label: str) -> str:
    if old not in source:
        if new in source:
            return source
        raise ValueError(f"{label}: expected source setting was not found")
    if source.count(old) != 1:
        raise ValueError(f"{label}: expected exactly one source setting")
    return source.replace(old, new, 1)


def patch_default_config(source: str) -> str:
    return replace_once_or_confirm(
        source, OLD_CHECKPOINT, PORTABLE_CHECKPOINT, "checkpoint config"
    )


def patch_transfusion_config(source: str) -> str:
    return replace_once_or_confirm(
        source, UNDEFINED_RANGE, EXPLICIT_RANGE, "post-center-range config"
    )


def write_if_changed(path: Path, contents: str) -> bool:
    current = path.read_text(encoding="utf-8")
    if current == contents:
        return False
    temporary = path.with_name(path.name + ".raytrain.tmp")
    temporary.write_text(contents, encoding="utf-8")
    os.replace(temporary, path)
    return True


def apply(repo: Path) -> list[Path]:
    targets = (
        (repo / "configs/default.yaml", patch_default_config),
        (repo / "configs/westwell/det/transfusion/default.yaml", patch_transfusion_config),
    )
    changed: list[Path] = []
    for path, transform in targets:
        if not path.is_file():
            raise FileNotFoundError(f"required s1h config does not exist: {path}")
        updated = transform(path.read_text(encoding="utf-8"))
        if write_if_changed(path, updated):
            changed.append(path)
    return changed


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("repo", type=Path, help="bev_3dod_s1h checkout")
    arguments = parser.parse_args()
    repo = arguments.repo.resolve()
    changed = apply(repo)
    for path in changed:
        print(path.relative_to(repo))
    if not changed:
        print("already patched")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
