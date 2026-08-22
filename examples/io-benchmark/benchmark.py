#!/usr/bin/env python3
"""Repeatable multi-node read benchmark for Ray Training Platform datasets."""

from __future__ import annotations

import argparse
import json
import os
import statistics
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple


MIB = 1024 * 1024
DEFAULT_CHUNK_SIZE = 4 * MIB


def discover_files(
    root: Path,
    max_files: int,
    max_bytes: Optional[int] = None,
) -> Tuple[List[Path], Dict[str, Any]]:
    """Return a deterministic file manifest without following symbolic links."""
    if max_files <= 0:
        raise ValueError("max_files must be positive")
    if max_bytes is not None and max_bytes <= 0:
        raise ValueError("max_bytes must be positive")

    selected: List[Path] = []
    selected_bytes = 0
    file_limit_reached = False
    byte_limit_reached = False

    for current, directories, filenames in os.walk(str(root), followlinks=False):
        directories[:] = sorted(directories)
        for filename in sorted(filenames):
            path = Path(current) / filename
            if path.is_symlink() or not path.is_file():
                continue
            size = path.stat().st_size
            if len(selected) >= max_files:
                file_limit_reached = True
                break
            if max_bytes is not None and selected_bytes + size > max_bytes:
                byte_limit_reached = True
                break
            selected = [*selected, path]
            selected_bytes += size
        if file_limit_reached or byte_limit_reached:
            break

    return selected, {
        "selected_files": len(selected),
        "selected_bytes": selected_bytes,
        "file_limit_reached": file_limit_reached,
        "byte_limit_reached": byte_limit_reached,
    }


def partition_files(files: Sequence[Path], rank: int, world_size: int) -> List[Path]:
    if world_size <= 0:
        raise ValueError("world_size must be positive")
    if rank < 0 or rank >= world_size:
        raise ValueError("rank must be in [0, world_size)")
    return list(files[rank::world_size])


def limit_partition_bytes(files: Sequence[Path], max_bytes: int) -> List[Path]:
    """Select files without ever exceeding one worker's byte ceiling."""
    if max_bytes <= 0:
        raise ValueError("max_bytes must be positive")
    selected: List[Path] = []
    selected_bytes = 0
    for path in files:
        size = path.stat().st_size
        if selected_bytes + size > max_bytes:
            continue
        selected = [*selected, path]
        selected_bytes += size
    return selected


def require_nonempty_partition(files: Sequence[Path], rank: int, max_bytes: int) -> None:
    if files:
        return
    raise RuntimeError(
        f"worker {rank} selected no files within the {max_bytes}-byte limit; "
        "increase --max-files or --max-bytes-per-worker"
    )


def percentile(values: Sequence[float], quantile: float) -> float:
    if not values:
        return 0.0
    if not 0 <= quantile <= 1:
        raise ValueError("quantile must be in [0, 1]")
    ordered = sorted(values)
    position = (len(ordered) - 1) * quantile
    lower = int(position)
    upper = min(lower + 1, len(ordered) - 1)
    weight = position - lower
    return ordered[lower] * (1 - weight) + ordered[upper] * weight


def read_pass(files: Iterable[Path], chunk_size: int = DEFAULT_CHUNK_SIZE) -> Dict[str, Any]:
    if chunk_size <= 0:
        raise ValueError("chunk_size must be positive")

    total_bytes = 0
    file_count = 0
    latencies_ms: List[float] = []
    started = time.perf_counter()
    for path in files:
        file_started = time.perf_counter()
        file_bytes = 0
        with path.open("rb", buffering=0) as stream:
            while True:
                block = stream.read(chunk_size)
                if not block:
                    break
                file_bytes += len(block)
        latency_ms = (time.perf_counter() - file_started) * 1000
        total_bytes += file_bytes
        file_count += 1
        latencies_ms = [*latencies_ms, latency_ms]
    elapsed = max(time.perf_counter() - started, 1e-9)

    return {
        "files": file_count,
        "bytes": total_bytes,
        "seconds": round(elapsed, 6),
        "mib_per_second": round(total_bytes / elapsed / MIB, 3),
        "files_per_second": round(file_count / elapsed, 3),
        "latency_ms_mean": round(statistics.fmean(latencies_ms), 3) if latencies_ms else 0.0,
        "latency_ms_p50": round(percentile(latencies_ms, 0.50), 3),
        "latency_ms_p95": round(percentile(latencies_ms, 0.95), 3),
    }


def aggregate_report(
    workers: Sequence[Dict[str, Any]],
    dataset_path: str,
    selected_files: int,
    selected_bytes: int,
) -> Dict[str, Any]:
    pass_count = max((len(worker.get("passes", [])) for worker in workers), default=0)
    aggregated_passes: List[Dict[str, Any]] = []
    for pass_index in range(pass_count):
        current = [worker["passes"][pass_index] for worker in workers if len(worker.get("passes", [])) > pass_index]
        wall_seconds = max((float(item["seconds"]) for item in current), default=0.0)
        total_bytes = sum(int(item["bytes"]) for item in current)
        total_files = sum(int(item["files"]) for item in current)
        aggregated_passes = [
            *aggregated_passes,
            {
                "pass": pass_index + 1,
                "files": total_files,
                "bytes": total_bytes,
                "wall_seconds": wall_seconds,
                "aggregate_mib_per_second": round(total_bytes / max(wall_seconds, 1e-9) / MIB, 3),
                "aggregate_files_per_second": round(total_files / max(wall_seconds, 1e-9), 3),
            },
        ]

    return {
        "schema_version": 1,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "dataset_path": dataset_path,
        "workers": len(workers),
        "selected_files": selected_files,
        "selected_bytes": selected_bytes,
        "metadata_scan_wall_seconds": max(
            (float(worker.get("metadata_seconds", 0.0)) for worker in workers),
            default=0.0,
        ),
        "passes": aggregated_passes,
        "worker_results": [
            {
                "worker": f"worker-{index}",
                "metadata_seconds": worker.get("metadata_seconds", 0.0),
                "assigned_files": worker.get("assigned_files", 0),
                "assigned_bytes": worker.get("assigned_bytes", 0),
                "passes": worker.get("passes", []),
            }
            for index, worker in enumerate(sorted(workers, key=lambda item: int(item["rank"])))
        ],
        "interpretation": {
            "pass_1": "首次顺序读取所选文件；不声称为严格冷缓存。",
            "pass_2_plus": "同一 Pod 重复读取同一文件，用于观察客户端、内核或后端缓存影响。",
            "scope": "结果仅代表本次所选文件组合、并发度和运行时集群状态。",
        },
    }


def resolve_dataset_path(root: Path, relative_path: str) -> Path:
    resolved_root = root.resolve()
    selected = (resolved_root / relative_path).resolve()
    try:
        selected.relative_to(resolved_root)
    except ValueError as error:
        raise ValueError("benchmark path must stay inside PLATFORM_DATASET_PATH") from error
    if not selected.is_dir():
        raise ValueError(f"benchmark dataset directory does not exist: {selected}")
    return selected


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Measure real dataset read throughput on every training worker")
    parser.add_argument("--path", default=".", help="directory relative to PLATFORM_DATASET_PATH")
    parser.add_argument("--passes", type=int, default=2, help="number of identical full-read passes")
    parser.add_argument("--max-files", type=int, default=4096, help="maximum files across all workers")
    parser.add_argument(
        "--max-bytes-per-worker",
        type=int,
        default=4 * 1024 * 1024 * 1024,
        help="target maximum selected bytes per worker",
    )
    parser.add_argument("--chunk-size", type=int, default=DEFAULT_CHUNK_SIZE)
    parser.add_argument("--output", default="", help="JSON output path; defaults below PLATFORM_OUTPUT_PATH")
    return parser.parse_args()


def distributed_context() -> Tuple[int, int, Any]:
    rank = int(os.environ.get("RANK", "0"))
    world_size = int(os.environ.get("WORLD_SIZE", "1"))
    if world_size == 1:
        return rank, world_size, None
    try:
        import torch.distributed as distributed
    except ImportError as error:
        raise RuntimeError("torch is required when WORLD_SIZE is greater than one") from error
    distributed.init_process_group(backend="gloo")
    return rank, world_size, distributed


def gather_worker_results(result: Dict[str, Any], rank: int, world_size: int, distributed: Any) -> Optional[List[Dict[str, Any]]]:
    if world_size == 1:
        return [result]
    gathered: Optional[List[Optional[Dict[str, Any]]]] = [None] * world_size if rank == 0 else None
    distributed.gather_object(result, gathered, dst=0)
    distributed.barrier()
    if rank != 0 or gathered is None:
        return None
    return [item for item in gathered if item is not None]


def write_report(report: Dict[str, Any], output_arg: str) -> Path:
    output_root = Path(os.environ.get("PLATFORM_OUTPUT_PATH", ".")).resolve()
    output = Path(output_arg).resolve() if output_arg else output_root / "io-benchmark.json"
    if "PLATFORM_OUTPUT_PATH" in os.environ:
        try:
            output.relative_to(output_root)
        except ValueError as error:
            raise ValueError("benchmark output must stay inside PLATFORM_OUTPUT_PATH") from error
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return output


def main() -> int:
    args = parse_args()
    if args.passes <= 0:
        raise ValueError("passes must be positive")

    rank, world_size, distributed = distributed_context()
    dataset_root = Path(os.environ["PLATFORM_DATASET_PATH"])
    dataset_path = resolve_dataset_path(dataset_root, args.path)

    manifest_holder: List[Optional[Dict[str, Any]]] = [None]
    if rank == 0:
        print(f"RAYTRAIN_IO_PHASE=discover path={dataset_path} max_files={args.max_files}", flush=True)
        metadata_started = time.perf_counter()
        discovered, metadata = discover_files(dataset_path, max_files=args.max_files)
        metadata_seconds = round(time.perf_counter() - metadata_started, 6)
        manifest_holder[0] = {
            "relative_paths": [path.relative_to(dataset_path).as_posix() for path in discovered],
            "metadata_seconds": metadata_seconds,
            "discovered_files": metadata["selected_files"],
            "discovered_bytes": metadata["selected_bytes"],
        }
        print(
            "RAYTRAIN_IO_PHASE=manifest "
            f"files={metadata['selected_files']} bytes={metadata['selected_bytes']} seconds={metadata_seconds}",
            flush=True,
        )
    if distributed is not None:
        distributed.broadcast_object_list(manifest_holder, src=0)
    manifest = manifest_holder[0]
    if manifest is None:
        raise RuntimeError("rank 0 did not publish a benchmark manifest")
    files = [dataset_path / relative for relative in manifest["relative_paths"]]
    if not files:
        raise RuntimeError(f"no regular files selected below {dataset_path}")

    assigned = limit_partition_bytes(
        partition_files(files, rank=rank, world_size=world_size),
        max_bytes=args.max_bytes_per_worker,
    )
    require_nonempty_partition(assigned, rank=rank, max_bytes=args.max_bytes_per_worker)
    passes: List[Dict[str, Any]] = []
    for pass_index in range(args.passes):
        print(
            f"RAYTRAIN_IO_PHASE=read rank={rank} pass={pass_index + 1}/{args.passes} files={len(assigned)}",
            flush=True,
        )
        passes = [*passes, read_pass(assigned, chunk_size=args.chunk_size)]
    worker_result = {
        "rank": rank,
        "metadata_seconds": manifest["metadata_seconds"],
        "assigned_files": len(assigned),
        "assigned_bytes": sum(path.stat().st_size for path in assigned),
        "passes": passes,
    }
    print("RAYTRAIN_IO_WORKER=" + json.dumps(worker_result, ensure_ascii=False), flush=True)

    gathered = gather_worker_results(worker_result, rank, world_size, distributed)
    if rank == 0 and gathered is not None:
        report = aggregate_report(
            gathered,
            dataset_path=str(dataset_path),
            selected_files=sum(int(worker["assigned_files"]) for worker in gathered),
            selected_bytes=sum(int(worker["assigned_bytes"]) for worker in gathered),
        )
        output = write_report(report, args.output)
        print("RAYTRAIN_IO_BENCHMARK_JSON=" + json.dumps(report, ensure_ascii=False), flush=True)
        print(f"RAYTRAIN_IO_BENCHMARK_OUTPUT={output}", flush=True)

    if distributed is not None:
        distributed.destroy_process_group()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
