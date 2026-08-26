#!/usr/bin/env python3
"""Run a deterministic BEVFusion subset, optionally staged to node NVMe."""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import pickle
import shutil
import sys
import time
from pathlib import Path
from typing import Iterable


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", required=True)
    parser.add_argument("--train", required=True)
    parser.add_argument("--val", required=True)
    parser.add_argument("--train-limit", type=int, default=4096)
    parser.add_argument("--val-limit", type=int, default=512)
    parser.add_argument("--stage-cache", action="store_true")
    parser.add_argument("--copy-workers", type=int, default=32)
    parser.add_argument("--timeout", type=int, default=14400)
    return parser.parse_args()


def load_subset(path: Path, limit: int) -> tuple[object, list[dict]]:
    with path.open("rb") as stream:
        payload = pickle.load(stream)
    infos = payload.get("infos", payload) if isinstance(payload, dict) else payload
    if not isinstance(infos, list):
        raise TypeError(f"annotation does not contain an info list: {path}")
    selected = list(infos[:limit])
    if isinstance(payload, dict):
        subset_payload = {**payload, "infos": selected}
    else:
        subset_payload = selected
    return subset_payload, selected


def recorded_paths(info: dict) -> Iterable[str]:
    if info.get("lidar_path"):
        yield info["lidar_path"]
    for sweep in info.get("sweeps", []):
        if sweep.get("data_path"):
            yield sweep["data_path"]
    for camera in info.get("cams", {}).values():
        if camera.get("data_path"):
            yield camera["data_path"]


def within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def resolve_files(source_root: Path, infos: list[dict]) -> list[Path]:
    sys.path.insert(0, str(Path(__file__).resolve().parent / "mmdet3d" / "datasets"))
    from platform_paths import discover_path_rewrite, safe_path_parts

    recorded_unique: dict[str, str] = {}
    for info in infos:
        for recorded in recorded_paths(info):
            recorded_unique[recorded] = recorded
    rewrite = None
    for recorded in recorded_unique.values():
        rewrite = discover_path_rewrite(recorded, str(source_root))
        if rewrite is not None:
            break
    if rewrite is None:
        raise FileNotFoundError("could not discover a mounted path rewrite from annotations")
    prefix, drop = rewrite
    unique: dict[str, Path] = {}
    for recorded in recorded_unique.values():
        parts = safe_path_parts(recorded)
        if parts is None or drop >= len(parts):
            raise ValueError(f"unsafe or incompatible recorded path: {recorded}")
        candidate = source_root.joinpath(*prefix, *parts[drop:])
        if not within(candidate, source_root):
            raise ValueError(f"resolved path escapes dataset root: {candidate}")
        unique[str(candidate)] = candidate
    print(
        "RAYTRAIN_BEV_CACHE_MANIFEST="
        + json.dumps({"files": len(unique), "prefix": list(prefix), "drop": drop}),
        flush=True,
    )
    return sorted(unique.values(), key=lambda item: item.as_posix())


def copy_one(source: Path, source_root: Path, destination_root: Path) -> int:
    destination = destination_root / source.relative_to(source_root)
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.tmp-{os.getpid()}")
    shutil.copyfile(source, temporary)
    temporary.replace(destination)
    return destination.stat().st_size


def write_pickle(path: Path, payload: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp-{os.getpid()}")
    with temporary.open("wb") as stream:
        pickle.dump(payload, stream, protocol=pickle.HIGHEST_PROTOCOL)
    temporary.replace(path)


def wait_ready(ready: Path, timeout: int) -> dict:
    deadline = time.monotonic() + timeout
    while not ready.is_file():
        if time.monotonic() >= deadline:
            raise TimeoutError(f"timed out waiting for benchmark preparation: {ready}")
        time.sleep(1)
    return json.loads(ready.read_text(encoding="utf-8"))


def prepare(args: argparse.Namespace) -> tuple[Path, Path, Path, dict]:
    source_root = Path(os.environ["PLATFORM_DATASET_PATH"]).resolve()
    local_rank = int(os.environ.get("LOCAL_RANK", "0"))
    cache_value = os.environ.get("PLATFORM_CACHE_PATH", "").strip()
    if args.stage_cache and not cache_value:
        raise RuntimeError("--stage-cache requires PLATFORM_CACHE_PATH")

    base = Path(cache_value).resolve() if args.stage_cache else Path("/tmp/raytrain-bev-benchmark")
    destination_root = base / "dataset" if args.stage_cache else source_root
    annotation_root = base / "annotations"
    train_output = annotation_root / "train.pkl"
    val_output = annotation_root / "val.pkl"
    ready = base / ".benchmark.ready"

    if local_rank != 0:
        payload = wait_ready(ready, args.timeout)
        return destination_root, train_output, val_output, payload
    if ready.is_file() and train_output.is_file() and val_output.is_file():
        return destination_root, train_output, val_output, json.loads(ready.read_text(encoding="utf-8"))

    started = time.perf_counter()
    train_payload, train_infos = load_subset(source_root / args.train, args.train_limit)
    val_payload, val_infos = load_subset(source_root / args.val, args.val_limit)
    write_pickle(train_output, train_payload)
    write_pickle(val_output, val_payload)
    payload = {
        "cache": args.stage_cache,
        "train_samples": len(train_infos),
        "val_samples": len(val_infos),
        "files": 0,
        "bytes": 0,
        "gib": 0.0,
        "copy_seconds": 0.0,
        "mib_per_second": 0.0,
    }
    if args.stage_cache:
        files = resolve_files(source_root, [*train_infos, *val_infos])
        copy_started = time.perf_counter()
        copied_bytes = 0
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.copy_workers) as executor:
            futures = [executor.submit(copy_one, path, source_root, destination_root) for path in files]
            for completed, future in enumerate(concurrent.futures.as_completed(futures), start=1):
                copied_bytes += future.result()
                if completed % 5000 == 0:
                    print(
                        f"RAYTRAIN_BEV_CACHE_PROGRESS files={completed}/{len(files)} bytes={copied_bytes}",
                        flush=True,
                    )
        copy_seconds = max(time.perf_counter() - copy_started, 1e-9)
        payload = {
            **payload,
            "files": len(files),
            "bytes": copied_bytes,
            "gib": round(copied_bytes / 1024**3, 3),
            "copy_seconds": round(copy_seconds, 3),
            "mib_per_second": round(copied_bytes / copy_seconds / 1024**2, 3),
        }
    payload = {**payload, "prepare_seconds": round(time.perf_counter() - started, 3)}
    ready.write_text(json.dumps(payload) + "\n", encoding="utf-8")
    return destination_root, train_output, val_output, payload


def main() -> int:
    args = parse_args()
    dataset_root, train_annotation, val_annotation, payload = prepare(args)
    local_rank = int(os.environ.get("LOCAL_RANK", "0"))
    if local_rank == 0:
        print("RAYTRAIN_BEV_BENCHMARK_PREP=" + json.dumps(payload), flush=True)
    os.environ["PLATFORM_DATASET_PATH"] = str(dataset_root)
    output_path = os.environ["PLATFORM_OUTPUT_PATH"]
    command = [
        "python3",
        "tools/westwell_train.py",
        args.config,
        "--launcher",
        "pytorch",
        "--run-dir",
        output_path,
        f"dataset_root={dataset_root}/",
        f"eval_dataset_root={dataset_root}/",
        f"data.train.dataset.ann_file={train_annotation}",
        f"data.val.ann_file={val_annotation}",
        f"data.test.ann_file={val_annotation}",
        "data.samples_per_gpu=1",
        "data.workers_per_gpu=4",
        "optimizer.lr=1.0e-5",
        "runner.max_epochs=1",
        "log_config.interval=10",
        "evaluation.interval=1",
    ]
    os.execvp(command[0], command)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
