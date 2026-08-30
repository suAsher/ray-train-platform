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
PYTORCH_HELPER_INCLUDE = '#include "pytorch_cpp_helper.hpp"'
QUEUE_AND_HELPER_INCLUDES = '#include <queue>\n#include "pytorch_cpp_helper.hpp"'
THC_INCLUDE = "#include <THC/THC.h>\n"
THC_DEVICE_UTILS_INCLUDE = "#include <THC/THCDeviceUtils.cuh>\n"
PYBIND_TORCH_AND_HELPER_INCLUDES = (
    '#include <torch/extension.h>\n#include "pytorch_cpp_helper.hpp"'
)


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
    pixel_group = (
        source_root
        / "mmcv"
        / "ops"
        / "csrc"
        / "pytorch"
        / "cpu"
        / "pixel_group.cpp"
    )
    _replace_exact(
        pixel_group,
        PYTORCH_HELPER_INCLUDE,
        QUEUE_AND_HELPER_INCLUDES,
        1,
        "mmcv pixel_group queue include",
    )
    _replace_exact(
        pixel_group,
        "embedding_dim.dim()",
        "embedding.dim()",
        1,
        "mmcv pixel_group embedding assertion",
    )
    psamask = (
        source_root
        / "mmcv"
        / "ops"
        / "csrc"
        / "pytorch"
        / "cuda"
        / "psamask_cuda.cu"
    )
    _replace_exact(
        psamask,
        THC_INCLUDE,
        "",
        1,
        "mmcv psamask legacy THC include",
    )
    _replace_exact(
        psamask,
        THC_DEVICE_UTILS_INCLUDE,
        "",
        1,
        "mmcv psamask legacy THC device include",
    )
    _replace_exact(
        source_root / "mmcv" / "ops" / "csrc" / "pytorch" / "pybind.cpp",
        PYTORCH_HELPER_INCLUDE,
        PYBIND_TORCH_AND_HELPER_INCLUDES,
        1,
        "mmcv pybind torch extension include",
    )


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: prepare-mmcv-source.py SOURCE_ROOT", file=sys.stderr)
        return 2
    prepare(pathlib.Path(sys.argv[1]).resolve())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
