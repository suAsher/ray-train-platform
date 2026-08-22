"""Fast runtime contract check to run before an expensive BEVFusion job."""

from __future__ import annotations

import json
import os
from pathlib import Path

import mmdet3d
import torch

from mmdet3d.datasets.platform_paths import DatasetPathResolver


def require_directory(variable: str) -> Path:
    value = os.environ.get(variable, "").strip()
    if not value:
        raise RuntimeError(f"{variable} is not set")
    path = Path(value)
    if not path.is_dir():
        raise RuntimeError(f"{variable} is not a directory: {path}")
    return path


def main() -> None:
    dataset = require_directory("PLATFORM_DATASET_PATH")
    output = require_directory("PLATFORM_OUTPUT_PATH")
    annotation = dataset / "platform-validation/annotations/fz-0429-platform-smoke-128"
    if not annotation.is_dir():
        raise RuntimeError(f"validation annotation directory is missing: {annotation}")

    result = {
        "cuda": torch.cuda.is_available(),
        "cudaDeviceCount": torch.cuda.device_count(),
        "dataset": str(dataset),
        "mmdet3dSource": str(Path(mmdet3d.__file__).resolve()),
        "output": str(output),
        "pathResolverActive": DatasetPathResolver(str(dataset)).active,
        "torch": torch.__version__,
    }
    if not result["cuda"] or result["cudaDeviceCount"] < 1:
        raise RuntimeError(f"GPU runtime is unavailable: {result}")

    destination = output / "platform-preflight.json"
    destination.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print("BEVFUSION_PLATFORM_PREFLIGHT_OK", json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
