#!/usr/bin/env python3
"""Prepare legacy BEVFusion source for a CUDA 12.1 / RTX 4090 build."""

from __future__ import annotations

import pathlib
import shutil
import sys


LEGACY_ARCHES = """            \"-gencode=arch=compute_70,code=sm_70\",\n            \"-gencode=arch=compute_75,code=sm_75\",\n            \"-gencode=arch=compute_80,code=sm_80\",\n            \"-gencode=arch=compute_86,code=sm_86\","""
CANARY_ARCH = '            "-gencode=arch=compute_89,code=sm_89",'


def prepare(source_root: pathlib.Path) -> None:
    setup_path = source_root / "setup.py"
    source = setup_path.read_text(encoding="utf-8")
    if LEGACY_ARCHES not in source:
        raise RuntimeError("legacy CUDA architecture block not found in setup.py")
    if '"-std=c++14"' not in source:
        raise RuntimeError("legacy C++14 compiler flag not found in setup.py")

    setup_path.write_text(
        source.replace(LEGACY_ARCHES, CANARY_ARCH).replace(
            '"-std=c++14"', '"-std=c++17"'
        ),
        encoding="utf-8",
    )
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
