"""One-GPU acceptance for managed Ray Train plus distributed Ray Data staging."""

from __future__ import annotations

import json
import os
import pathlib
import socket

import torch
import torch.distributed as dist


dataset = pathlib.Path(os.environ["PLATFORM_DATASET_PATH"])
output = pathlib.Path(os.environ["PLATFORM_OUTPUT_PATH"])
files = sorted(path for path in dataset.rglob("*") if path.is_file())
if len(files) != 3:
    raise RuntimeError(f"expected three staged validation files, found {len(files)}")

local_rank = int(os.environ["LOCAL_RANK"])
torch.cuda.set_device(local_rank)
if not dist.is_initialized():
    raise RuntimeError("Ray Train did not initialize torch.distributed")

value = torch.tensor([dist.get_rank() + 1], device="cuda", dtype=torch.int64)
dist.all_reduce(value)
cache_devices = sorted({str(path.resolve()).split("/", 3)[2] for path in files})
result = {
    "host": socket.gethostname(),
    "rank": dist.get_rank(),
    "worldSize": dist.get_world_size(),
    "gpu": torch.cuda.get_device_name(local_rank),
    "files": len(files),
    "bytes": sum(path.stat().st_size for path in files),
    "cacheDevices": cache_devices,
    "allReduceSum": int(value.item()),
}
print("RAY_DATA_STAGE_ACCEPTANCE=" + json.dumps(result, sort_keys=True), flush=True)
if dist.get_rank() == 0:
    output.mkdir(parents=True, exist_ok=True)
    (output / "ray-data-stage-acceptance.json").write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
dist.barrier()
