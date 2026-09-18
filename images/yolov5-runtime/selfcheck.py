#!/usr/bin/env python3
"""Build-time import contract for the RayTrain YOLOv5 runtime."""

from __future__ import annotations

import importlib
import os
import sys

import torch
import torchvision


def require(module: str) -> None:
    importlib.import_module(module)


def main() -> None:
    assert sys.version_info[:2] == (3, 10), sys.version
    assert torch.__version__.split("+")[0] == "2.4.1", torch.__version__
    assert torch.version.cuda == "12.1", torch.version.cuda
    assert torchvision.__version__.split("+")[0] == "0.19.1", torchvision.__version__

    for module in (
        "cv2",
        "git",
        "matplotlib",
        "numpy",
        "pandas",
        "PIL",
        "pycocotools",
        "scipy",
        "seaborn",
        "thop",
        "ultralytics",
        "yaml",
    ):
        require(module)

    os.makedirs("/home/ray/.config/Ultralytics", exist_ok=True)
    print("raytrain-yolov5-selfcheck PASS")


if __name__ == "__main__":
    main()
