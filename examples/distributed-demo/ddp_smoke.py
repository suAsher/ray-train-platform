#!/usr/bin/env python3
"""Small DDP proof for the RayTrain single-node torchrun profile."""

from __future__ import annotations

import json
import os
import pathlib
import socket

import torch
import torch.distributed as dist


def main() -> None:
    rank = int(os.environ.get("RANK", "0"))
    local_rank = int(os.environ.get("LOCAL_RANK", "0"))
    world_size = int(os.environ.get("WORLD_SIZE", "1"))
    if not torch.cuda.is_available():
        raise RuntimeError("CUDA is unavailable inside the allocated GPU worker")
    torch.cuda.set_device(local_rank)
    if world_size > 1:
        dist.init_process_group(backend="nccl")
    value = torch.tensor([float(rank + 1)], device="cuda")
    if world_size > 1:
        dist.all_reduce(value, op=dist.ReduceOp.SUM)
    result = {
        "rank": rank,
        "localRank": local_rank,
        "worldSize": world_size,
        "host": socket.gethostname(),
        "gpu": torch.cuda.get_device_name(local_rank),
        "reducedRankSum": value.item(),
    }
    output = pathlib.Path(os.environ["PLATFORM_OUTPUT_PATH"])
    output.mkdir(parents=True, exist_ok=True)
    (output / f"ddp-rank-{rank:03d}.json").write_text(json.dumps(result, indent=2), encoding="utf-8")
    if rank == 0:
        (output / "ddp-summary.json").write_text(json.dumps(result, indent=2), encoding="utf-8")
    print(f"DDP_SMOKE rank={rank} local_rank={local_rank} world_size={world_size} reduced={value.item()}", flush=True)
    if world_size > 1:
        dist.destroy_process_group()


if __name__ == "__main__":
    main()
