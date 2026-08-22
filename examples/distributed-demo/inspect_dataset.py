#!/usr/bin/env python3
"""Inspect a selected RayTrain dataset from a real GPU worker.

The report is deliberately small and deterministic.  It proves the data
contract for a submitted directory without copying data or exposing storage
credentials, and it leaves the report beside that task's output artifacts.
"""

from __future__ import annotations

import json
import os
import pathlib
import pickle
import socket
from collections.abc import Iterable
from typing import Any

import ray


EXPECTED_INDEX = "final_merged_nuscenes_infos_train.pkl"
PATH_FIELD_SUFFIXES = ("path", "filename", "file_name")


def relative_paths(value: Any, prefix: str = "") -> Iterable[tuple[str, str]]:
    """Yield likely relative dataset paths from a compact pkl sample."""
    if isinstance(value, dict):
        for key, child in value.items():
            child_prefix = f"{prefix}.{key}" if prefix else str(key)
            if isinstance(child, str) and key.lower().endswith(PATH_FIELD_SUFFIXES):
                yield child_prefix, child
            else:
                yield from relative_paths(child, child_prefix)
    elif isinstance(value, (list, tuple)):
        for index, child in enumerate(value[:8]):
            yield from relative_paths(child, f"{prefix}[{index}]")


def resolve_dataset_path(root: pathlib.Path, raw_path: str) -> pathlib.Path:
    candidate = pathlib.PurePosixPath(raw_path)
    if candidate.is_absolute():
        return pathlib.Path(raw_path)
    return root / pathlib.Path(*candidate.parts)


@ray.remote(num_gpus=1)
def inspect_on_gpu(dataset_root: str, output_root: str) -> dict[str, Any]:
    import torch

    root = pathlib.Path(dataset_root)
    all_pickles = sorted(root.rglob("*.pkl"))
    expected = next((path for path in all_pickles if path.name == EXPECTED_INDEX), None)
    selected = expected or (all_pickles[0] if all_pickles else None)
    result: dict[str, Any] = {
        "datasetRoot": str(root),
        "node": socket.gethostname(),
        "rayNodeId": ray.get_runtime_context().get_node_id(),
        "cudaAvailable": torch.cuda.is_available(),
        "gpu": torch.cuda.get_device_name(0) if torch.cuda.is_available() else None,
        "expectedIndex": EXPECTED_INDEX,
        "expectedIndexPresent": expected is not None,
        "pklFiles": [str(path.relative_to(root)) for path in all_pickles[:100]],
        "pklFileCount": len(all_pickles),
        "sampleReferences": [],
    }
    if selected is None:
        result["contractStatus"] = "MISSING_PKL"
    else:
        result["inspectedPkl"] = str(selected.relative_to(root))
        try:
            with selected.open("rb") as handle:
                payload = pickle.load(handle)
            infos = payload.get("infos", payload) if isinstance(payload, dict) else payload
            first = infos[0] if isinstance(infos, (list, tuple)) and infos else {}
            result["pklTopLevel"] = sorted(payload.keys()) if isinstance(payload, dict) else type(payload).__name__
            result["infoCount"] = len(infos) if isinstance(infos, (list, tuple)) else None
            result["firstInfoFields"] = sorted(first.keys()) if isinstance(first, dict) else type(first).__name__
            result["sampleReferences"] = [
                {
                    "field": field,
                    "value": raw_path,
                    "exists": resolve_dataset_path(root, raw_path).exists(),
                }
                for field, raw_path in list(relative_paths(first))[:40]
            ]
            result["contractStatus"] = "READY" if expected is not None else "MISSING_EXPECTED_INDEX"
        except Exception as error:  # report data incompatibility rather than hide it in worker logs
            result["contractStatus"] = "PKL_READ_ERROR"
            result["pklReadError"] = f"{type(error).__name__}: {error}"
    output = pathlib.Path(output_root)
    output.mkdir(parents=True, exist_ok=True)
    (output / "dataset-inspection.json").write_text(
        json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8"
    )
    return result


def main() -> None:
    dataset_path = os.environ["PLATFORM_DATASET_PATH"]
    output_path = os.environ["PLATFORM_OUTPUT_PATH"]
    ray.init(address="auto")
    result = ray.get(inspect_on_gpu.remote(dataset_path, output_path))
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
