#!/usr/bin/env python3
"""Prepare legacy BEVFusion source for a CUDA 12.1 / RTX 4090 build."""

from __future__ import annotations

import pathlib
import shutil
import sys


LEGACY_ARCHES = """            \"-gencode=arch=compute_70,code=sm_70\",\n            \"-gencode=arch=compute_75,code=sm_75\",\n            \"-gencode=arch=compute_80,code=sm_80\",\n            \"-gencode=arch=compute_86,code=sm_86\","""
CANARY_ARCH = '            "-gencode=arch=compute_89,code=sm_89",'
THC_INCLUDE = "#include <THC/THC.h>\n"
THC_STATE_DECLARATION = "extern THCState *state;\n"
THC_BINDING_PATHS = (
    pathlib.Path("mmdet3d/ops/ball_query/src/ball_query.cpp"),
    pathlib.Path(
        "mmdet3d/ops/furthest_point_sample/src/furthest_point_sample.cpp"
    ),
    pathlib.Path("mmdet3d/ops/gather_points/src/gather_points.cpp"),
    pathlib.Path("mmdet3d/ops/group_points/src/group_points.cpp"),
    pathlib.Path("mmdet3d/ops/interpolate/src/interpolate.cpp"),
    pathlib.Path("mmdet3d/ops/knn/src/knn.cpp"),
)


def _replace_once(
    source: str,
    old: str,
    new: str,
    label: str,
    path: pathlib.Path,
) -> str:
    actual = source.count(old)
    if actual != 1:
        raise RuntimeError(
            f"{label}: expected 1 occurrence, found {actual} in {path}"
        )
    return source.replace(old, new)


def prepare(source_root: pathlib.Path) -> None:
    setup_path = source_root / "setup.py"
    source = setup_path.read_text(encoding="utf-8")
    if LEGACY_ARCHES not in source:
        raise RuntimeError("legacy CUDA architecture block not found in setup.py")
    if '"-std=c++14"' not in source:
        raise RuntimeError("legacy C++14 compiler flag not found in setup.py")

    updated_setup = source.replace(LEGACY_ARCHES, CANARY_ARCH).replace(
        '"-std=c++14"', '"-std=c++17"'
    )
    binding_updates: list[tuple[pathlib.Path, str]] = []
    for relative_path in THC_BINDING_PATHS:
        binding_path = source_root / relative_path
        binding_source = binding_path.read_text(encoding="utf-8")
        binding_source = _replace_once(
            binding_source,
            THC_INCLUDE,
            "",
            "legacy BEVFusion THC include",
            binding_path,
        )
        binding_source = _replace_once(
            binding_source,
            THC_STATE_DECLARATION,
            "",
            "legacy BEVFusion THCState declaration",
            binding_path,
        )
        binding_updates.append((binding_path, binding_source))

    setup_path.write_text(updated_setup, encoding="utf-8")
    for binding_path, binding_source in binding_updates:
        binding_path.write_text(binding_source, encoding="utf-8")
    for build_dir in (source_root / "build", source_root / "dist"):
        shutil.rmtree(build_dir, ignore_errors=True)
    for extension in source_root.rglob("*.so"):
        extension.unlink()
    for cache_dir in source_root.rglob("__pycache__"):
        shutil.rmtree(cache_dir, ignore_errors=True)


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: prepare-canary-source.py SOURCE_ROOT", file=sys.stderr)
        return 2
    prepare(pathlib.Path(sys.argv[1]).resolve())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
