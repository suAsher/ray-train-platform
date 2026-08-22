#!/usr/bin/env python3
"""Prove one GPU can read selected public data and write governed output.

This intentionally checks one explicit file. Recursive directory walks on an
object-store filesystem turn a smoke test into an expensive bucket listing.
"""

from __future__ import annotations

import json
import os
import socket
from pathlib import Path

import torch

from storage_contract import expected_file


EXPECTED_FILE = os.environ.get("PLATFORM_EXPECTED_DATA_FILE", "final_merged_nuscenes_infos_train.pkl")


def probe(dataset_root: str, output_root: str, relative_path: str) -> dict[str, object]:
    candidate = expected_file(dataset_root, relative_path)
    exists = candidate.is_file()
    result: dict[str, object] = {
        "datasetRoot": dataset_root,
        "expectedFile": relative_path,
        "expectedFileExists": exists,
        "expectedFileSizeBytes": candidate.stat().st_size if exists else 0,
        "node": socket.gethostname(),
        "cudaAvailable": torch.cuda.is_available(),
        "gpu": torch.cuda.get_device_name(0) if torch.cuda.is_available() else None,
        "cudaVisibleDevices": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
    }
    if not exists:
        raise FileNotFoundError(f"selected dataset is missing {relative_path}")
    if not result["cudaAvailable"]:
        raise RuntimeError("CUDA is unavailable inside the allocated GPU worker")

    output = Path(output_root)
    output.mkdir(parents=True, exist_ok=True)
    (output / "storage-gpu-smoke.json").write_text(
        json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8"
    )
    return result


def main() -> None:
    result = probe(
        os.environ["PLATFORM_DATASET_PATH"],
        os.environ["PLATFORM_OUTPUT_PATH"],
        EXPECTED_FILE,
    )
    print("STORAGE_GPU_SMOKE_OK", json.dumps(result, ensure_ascii=False, sort_keys=True), flush=True)


if __name__ == "__main__":
    main()
