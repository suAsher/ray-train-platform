#!/usr/bin/env python3
"""Run a deterministic BEVFusion subset, optionally staged to node NVMe."""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
import pickle
import shutil
import sys
import time
from pathlib import Path, PurePosixPath
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
    parser.add_argument("--samples-per-gpu", type=int, default=1)
    parser.add_argument("--workers-per-gpu", type=int, default=4)
    parser.add_argument("--learning-rate", type=float, default=1.0e-5)
    parser.add_argument("--recorded-root", default="")
    return parser.parse_args()


def load_subset(path: Path, limit: int) -> tuple[object, list[dict]]:
    with path.open("rb") as stream:
        payload = pickle.load(stream)
    infos = payload.get("infos", payload) if isinstance(payload, dict) else payload
    if not isinstance(infos, list):
        raise TypeError(f"annotation does not contain an info list: {path}")
    selected = list(infos if limit <= 0 else infos[:limit])
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


def rewrite_info_paths(info: dict, mapping: dict[str, str]) -> dict:
    """Return an info record whose data files point at staged cache copies."""
    rewritten = dict(info)
    lidar_path = info.get("lidar_path")
    if lidar_path in mapping:
        rewritten["lidar_path"] = mapping[lidar_path]

    rewritten["sweeps"] = [
        {
            **sweep,
            "data_path": mapping.get(sweep.get("data_path"), sweep.get("data_path")),
        }
        if isinstance(sweep, dict) and sweep.get("data_path")
        else sweep
        for sweep in info.get("sweeps", [])
    ]
    rewritten["cams"] = {
        name: {
            **camera,
            "data_path": mapping.get(camera.get("data_path"), camera.get("data_path")),
        }
        if isinstance(camera, dict) and camera.get("data_path")
        else camera
        for name, camera in info.get("cams", {}).items()
    }
    return rewritten


def replace_payload_infos(payload: object, infos: list[dict]) -> object:
    if isinstance(payload, dict):
        return {**payload, "infos": infos}
    return infos


def within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def map_explicit_recorded_path(source_root: Path, recorded_path: str, recorded_root: str) -> Path:
    """Map a known absolute PKL prefix without resolving symlink targets."""
    recorded = PurePosixPath(recorded_path)
    root = PurePosixPath(recorded_root)
    if not recorded.is_absolute() or not root.is_absolute():
        raise ValueError("recorded path and --recorded-root must be absolute")
    if any(part in (".", "..") for part in (*recorded.parts, *root.parts)):
        raise ValueError(f"unsafe recorded path: {recorded_path}")
    try:
        relative = recorded.relative_to(root)
    except ValueError as exc:
        raise ValueError(
            f"recorded path is outside --recorded-root: {recorded_path}"
        ) from exc
    candidate = source_root.joinpath(*relative.parts)
    if not within(candidate, source_root):
        raise ValueError(f"mapped path escapes dataset root: {candidate}")
    return candidate


def resolve_files(
    source_root: Path, infos: list[dict], recorded_root: str = ""
) -> dict[str, Path]:
    recorded_unique: dict[str, str] = {}
    for info in infos:
        for recorded in recorded_paths(info):
            recorded_unique[recorded] = recorded
    if recorded_root:
        resolved = {
            recorded: map_explicit_recorded_path(source_root, recorded, recorded_root)
            for recorded in recorded_unique.values()
        }
        print(
            "RAYTRAIN_BEV_CACHE_MANIFEST="
            + json.dumps(
                {
                    "files": len(set(resolved.values())),
                    "recorded_root": recorded_root,
                }
            ),
            flush=True,
        )
        return resolved
    sys.path.insert(0, str(Path(__file__).resolve().parent / "mmdet3d" / "datasets"))
    from platform_paths import discover_path_rewrite, safe_path_parts

    rewrite = None
    for recorded in recorded_unique.values():
        rewrite = discover_path_rewrite(recorded, str(source_root))
        if rewrite is not None:
            break
    if rewrite is None:
        raise FileNotFoundError("could not discover a mounted path rewrite from annotations")
    prefix, drop = rewrite
    resolved: dict[str, Path] = {}
    for recorded in recorded_unique.values():
        parts = safe_path_parts(recorded)
        if parts is None or drop >= len(parts):
            raise ValueError(f"unsafe or incompatible recorded path: {recorded}")
        candidate = source_root.joinpath(*prefix, *parts[drop:])
        if not within(candidate, source_root):
            raise ValueError(f"resolved path escapes dataset root: {candidate}")
        resolved[recorded] = candidate
    unique_files = {str(candidate) for candidate in resolved.values()}
    print(
        "RAYTRAIN_BEV_CACHE_MANIFEST="
        + json.dumps({"files": len(unique_files), "prefix": list(prefix), "drop": drop}),
        flush=True,
    )
    return resolved


def cache_root_index(relative: Path, root_count: int) -> int:
    digest = hashlib.sha256(relative.as_posix().encode("utf-8")).digest()
    return int.from_bytes(digest[:8], "big") % root_count


def copy_one(source: Path, source_root: Path, cache_roots: list[Path]) -> tuple[int, int, str, str]:
    relative = source.relative_to(source_root)
    index = cache_root_index(relative, len(cache_roots))
    destination = cache_roots[index] / "data" / relative
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.tmp-{os.getpid()}")
    shutil.copyfile(source, temporary)
    temporary.replace(destination)
    return index, destination.stat().st_size, str(source), str(destination)


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
    cache_value = os.environ.get("PLATFORM_CACHE_PATHS", "").strip()
    if not cache_value:
        cache_value = os.environ.get("PLATFORM_CACHE_PATH", "").strip()
    cache_roots = [Path(item).resolve() for item in cache_value.split(":") if item]
    if args.stage_cache and not cache_roots:
        raise RuntimeError("--stage-cache requires PLATFORM_CACHE_PATHS")
    if len(set(cache_roots)) != len(cache_roots):
        raise RuntimeError("PLATFORM_CACHE_PATHS contains duplicate roots")

    base = cache_roots[0] if args.stage_cache else Path("/tmp/raytrain-bev-benchmark")
    destination_root = base if args.stage_cache else source_root
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
        recorded_sources = resolve_files(
            source_root, [*train_infos, *val_infos], args.recorded_root
        )
        files = sorted(set(recorded_sources.values()), key=lambda item: item.as_posix())
        for cache_root in cache_roots:
            (cache_root / "data").mkdir(parents=True, exist_ok=True)
        copy_started = time.perf_counter()
        copied_bytes = 0
        root_files = [0 for _ in cache_roots]
        root_bytes = [0 for _ in cache_roots]
        source_destinations: dict[str, str] = {}
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.copy_workers) as executor:
            futures = [executor.submit(copy_one, path, source_root, cache_roots) for path in files]
            for completed, future in enumerate(concurrent.futures.as_completed(futures), start=1):
                root_index, file_bytes, source, destination = future.result()
                copied_bytes += file_bytes
                root_files[root_index] += 1
                root_bytes[root_index] += file_bytes
                source_destinations[source] = destination
                if completed % 5000 == 0:
                    print(
                        f"RAYTRAIN_BEV_CACHE_PROGRESS files={completed}/{len(files)} bytes={copied_bytes}",
                        flush=True,
                    )
        recorded_destinations = {
            recorded: source_destinations[str(source)]
            for recorded, source in recorded_sources.items()
        }
        train_infos = [rewrite_info_paths(info, recorded_destinations) for info in train_infos]
        val_infos = [rewrite_info_paths(info, recorded_destinations) for info in val_infos]
        train_payload = replace_payload_infos(train_payload, train_infos)
        val_payload = replace_payload_infos(val_payload, val_infos)
        copy_seconds = max(time.perf_counter() - copy_started, 1e-9)
        payload = {
            **payload,
            "files": len(files),
            "bytes": copied_bytes,
            "gib": round(copied_bytes / 1024**3, 3),
            "copy_seconds": round(copy_seconds, 3),
            "mib_per_second": round(copied_bytes / copy_seconds / 1024**2, 3),
            "roots": [
                {"path": str(root), "files": root_files[index], "bytes": root_bytes[index]}
                for index, root in enumerate(cache_roots)
            ],
        }
    write_pickle(train_output, train_payload)
    write_pickle(val_output, val_payload)
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
        "raytrain-bevfusion-prepare",
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
        f"data.samples_per_gpu={args.samples_per_gpu}",
        f"data.workers_per_gpu={args.workers_per_gpu}",
        f"optimizer.lr={args.learning_rate}",
        "runner.max_epochs=1",
        "log_config.interval=10",
        "evaluation.interval=1",
    ]
    os.execvp(command[0], command)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
