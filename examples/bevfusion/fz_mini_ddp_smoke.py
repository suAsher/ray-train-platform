#!/usr/bin/env python3
"""Disposable, two-node BEVFusion training smoke for the FZ mini raw dataset.

This helper is deliberately for platform acceptance only.  Each torchrun rank
builds a private, small NuScenes index under ``/tmp`` from its read-only data
mount, then starts the normal BEVFusion training entrypoint.  It never writes
to the public data space and the validation index is a copy of the selected
training samples, so it must not be used to compare model quality.
"""

from __future__ import annotations

import argparse
import copy
import json
import os
import pickle
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class SmokeIndices:
    train_index: Path
    validation_index: Path
    sample_count: int


def create_smoke_indices(source: Path, destination: Path, sample_limit: int) -> SmokeIndices:
    """Create a small, self-contained train/validation index from a converter output."""
    if sample_limit < 1:
        raise ValueError("sample_limit must be at least 1")
    with source.open("rb") as handle:
        payload: Any = pickle.load(handle)
    if not isinstance(payload, dict) or not isinstance(payload.get("infos"), list):
        raise ValueError("NuScenes index must be a dictionary containing a non-empty infos list")
    infos = payload["infos"][:sample_limit]
    if not infos:
        raise ValueError("NuScenes index must be a dictionary containing a non-empty infos list")

    destination.mkdir(parents=True, exist_ok=True)
    smoke_payload = dict(payload)
    smoke_payload["infos"] = copy.deepcopy(infos)
    train_index = destination / "final_merged_nuscenes_infos_train.pkl"
    validation_index = destination / "final_merged_nuscenes_infos_val.pkl"
    for target in (train_index, validation_index):
        with target.open("wb") as handle:
            pickle.dump(smoke_payload, handle, protocol=pickle.HIGHEST_PROTOCOL)
    return SmokeIndices(train_index=train_index, validation_index=validation_index, sample_count=len(infos))


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--raw-root", default=os.environ.get("PLATFORM_DATASET_PATH", ""))
    parser.add_argument("--config", required=True)
    parser.add_argument("--sample-limit", type=int, default=8)
    parser.add_argument("--work-root", default="/tmp/raytrain-bevfusion-fz-mini")
    parser.add_argument("--output-root", default=os.environ.get("PLATFORM_OUTPUT_PATH", ""))
    return parser.parse_args()


def run(command: list[str]) -> None:
    print("+", " ".join(command), flush=True)
    subprocess.run(command, check=True)


def write_marker(output_root: str, payload: dict[str, Any]) -> None:
    if not output_root:
        return
    path = Path(output_root) / "bevfusion-fz-mini-smoke"
    path.mkdir(parents=True, exist_ok=True)
    rank = os.environ.get("RANK", "0")
    (path / f"rank-{rank}.json").write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8"
    )


def main() -> int:
    arguments = parse_arguments()
    raw_root = Path(arguments.raw_root).resolve()
    if not (raw_root / "samples").is_dir() or not (raw_root / "v1.0-mini").is_dir():
        raise SystemExit(f"FZ mini raw root is incomplete: {raw_root}")

    rank = os.environ.get("RANK", "0")
    work_root = Path(arguments.work_root) / f"rank-{rank}"
    generated = work_root / "generated"
    if work_root.exists():
        shutil.rmtree(work_root)
    generated.mkdir(parents=True)

    run([
        sys.executable,
        "tools/create_data.py",
        "nuscenes",
        "--root-path",
        str(raw_root),
        "--version",
        "v1.0-mini",
        "--out-dir",
        str(generated),
        "--extra-tag",
        "nuscenes",
        "--max-sweeps",
        "1",
        "--site-name",
        "fz",
    ])
    indices = create_smoke_indices(
        generated / "nuscenes_infos_train.pkl", generated, arguments.sample_limit
    )
    train_command = [
        sys.executable,
        "tools/westwell_train.py",
        arguments.config,
        "--launcher",
        "pytorch",
        "--run-dir",
        arguments.output_root or str(work_root / "output"),
        f"dataset_root={generated}/",
        f"eval_dataset_root={generated}/",
        "data.samples_per_gpu=1",
        "data.workers_per_gpu=0",
        "max_epochs=1",
        "evaluation.interval=1",
        "checkpoint_config.interval=1",
        "log_config.interval=1",
    ]
    write_marker(arguments.output_root, {
        "kind": "platform-acceptance-only",
        "rank": rank,
        "worldSize": os.environ.get("WORLD_SIZE", "1"),
        "rawRoot": str(raw_root),
        "indexRoot": str(generated),
        "sampleCount": indices.sample_count,
        "validation": "copied from training subset; do not use for model evaluation",
    })
    run(train_command)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
