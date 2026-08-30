#!/usr/bin/env python3
"""Backport the minimal MMCV 1.x Torch 2.x build compatibility fixes.

The replacements mirror changes released later on the upstream MMCV 1.x
branch. They deliberately fail closed if the pinned 1.4.0 source shape changes.
"""

from __future__ import annotations

import pathlib
import sys


TORCH_EXTENSION_INCLUDE = "#include <torch/extension.h>"
TORCH_TYPES_INCLUDE = "#include <torch/types.h>"
CPP14_FLAG = "'-std=c++14'"
CPP17_FLAG = "'-std=c++17'"


def _replace_exact(
    path: pathlib.Path,
    old: str,
    new: str,
    expected: int,
    label: str,
) -> None:
    source = path.read_text(encoding="utf-8")
    actual = source.count(old)
    if actual != expected:
        raise RuntimeError(
            f"{label}: expected {expected} occurrence(s), found {actual} in {path}"
        )
    path.write_text(source.replace(old, new), encoding="utf-8")


def prepare(source_root: pathlib.Path) -> None:
    helper = (
        source_root
        / "mmcv"
        / "ops"
        / "csrc"
        / "common"
        / "pytorch_cpp_helper.hpp"
    )
    _replace_exact(
        helper,
        TORCH_EXTENSION_INCLUDE,
        TORCH_TYPES_INCLUDE,
        1,
        "mmcv helper include",
    )
    _replace_exact(
        source_root / "setup.py",
        CPP14_FLAG,
        CPP17_FLAG,
        2,
        "mmcv compiler standard",
    )


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: prepare-mmcv-source.py SOURCE_ROOT", file=sys.stderr)
        return 2
    prepare(pathlib.Path(sys.argv[1]).resolve())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
