#!/usr/bin/env python3
"""Run an immutable RayTrain command on the GPUs it reserved.

This program is called by the RayJob driver, which intentionally runs on the
CPU-only head.  It moves a normal shell command to Ray GPU worker Pods and
records the actual rank topology beside the task output.  No storage
credentials are accepted or emitted here; governed paths arrive as environment
variables from the platform renderer.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import socket
import subprocess
import sys
import time
from typing import Any


def arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="RayTrain GPU command launcher")
    parser.add_argument("--mode", choices=("single_gpu", "torchrun", "ray_train"), required=True)
    parser.add_argument("--workers", type=int, required=True)
    parser.add_argument("--gpus-per-worker", type=int, required=True)
    parser.add_argument("--print-plan", action="store_true")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    parsed = parser.parse_args()
    if parsed.workers < 1 or parsed.gpus_per_worker < 1:
        parser.error("workers and gpus-per-worker must be positive")
    if not parsed.command or parsed.command[0] != "--" or len(parsed.command) == 1:
        parser.error("a command must follow --")
    parsed.command = parsed.command[1:]
    if parsed.mode == "single_gpu" and (parsed.workers != 1 or parsed.gpus_per_worker != 1):
        parser.error("single_gpu requires one worker and one GPU")
    if parsed.mode == "torchrun" and (parsed.workers != 1 or parsed.gpus_per_worker < 2):
        parser.error("torchrun requires one worker and at least two GPUs")
    if parsed.mode == "ray_train" and parsed.workers < 2:
        parser.error("ray_train requires at least two workers")
    return parsed


def execution_plan(parsed: argparse.Namespace) -> dict[str, Any]:
    command = list(parsed.command)
    if parsed.mode == "torchrun":
        # A Ray GPU worker Pod can share a physical host with another job.
        # --standalone gives each launch an isolated local rendezvous instead
        # of contending for torchrun's fixed default port 29500.
        command = torchrun_command(
            ["--standalone", f"--nproc_per_node={parsed.gpus_per_worker}"], command
        )
    plan = {
        "mode": parsed.mode,
        "workers": parsed.workers,
        "gpusPerWorker": parsed.gpus_per_worker,
        "worldSize": parsed.workers * parsed.gpus_per_worker,
        "command": command,
        "placementStrategy": "STRICT_SPREAD" if parsed.mode == "ray_train" else "PACK",
    }
    if parsed.mode == "ray_train":
        # The launcher actor uses one logical Ray CPU in addition to its GPU
        # bundle. Include that CPU in the placement-group bundle so scheduling
        # cannot wait forever for a resource absent from the reserved node.
        plan["placementBundles"] = [
            {"CPU": 1, "GPU": parsed.gpus_per_worker} for _ in range(parsed.workers)
        ]
    return plan


def torchrun_command(options: list[str], command: list[str]) -> list[str]:
    """Build a torchrun command without turning `python` into a script path.

    Most users enter `python train.py ...` in the Portal or rayctl. torchrun
    normally treats its first positional argument as a Python script, so that
    form would try to open a file literally named `python`. --no_python makes
    torchrun execute the supplied command verbatim; it also supports `python
    -m module`, shell wrappers, and other executable launchers. A direct
    `train.py` entrypoint retains torchrun's normal Python-script mode. The
    underscore spelling is accepted by the current PyTorch releases and by
    the PyTorch 1.x runtime used by the existing BEVFusion image; the newer
    dashed-only spelling would make that production image fail before the
    training script starts.
    """
    if not command:
        raise ValueError("torchrun requires a command")
    no_python = not command[0].endswith(".py")
    return ["torchrun", *options, *(["--no_python"] if no_python else []), *command]


def write_topology(payload: dict[str, Any]) -> None:
    output = os.environ.get("PLATFORM_OUTPUT_PATH", "").strip()
    if not output:
        return
    destination = pathlib.Path(output)
    destination.mkdir(parents=True, exist_ok=True)
    (destination / "raytrain-topology.json").write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8"
    )


def run_single_or_torchrun(parsed: argparse.Namespace) -> int:
    import ray

    command = execution_plan(parsed)["command"]

    @ray.remote(num_gpus=parsed.gpus_per_worker)
    def run_on_gpu(argv: list[str]) -> dict[str, Any]:
        completed = subprocess.run(argv, check=False)
        return {
            "returncode": completed.returncode,
            "host": socket.gethostname(),
            "nodeIP": ray.util.get_node_ip_address(),
            "visibleDevices": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
        }

    ray.init(address=os.environ.get("RAY_ADDRESS", "auto"))
    result = ray.get(run_on_gpu.remote(command))
    write_topology({**execution_plan(parsed), "nodes": [result]})
    return int(result["returncode"])


def run_distributed(parsed: argparse.Namespace) -> int:
    import ray
    from ray.util.placement_group import placement_group, remove_placement_group

    @ray.remote
    class NodeLauncher:
        def __init__(self) -> None:
            self.process: subprocess.Popen[str] | None = None
            self.ip = ray.util.get_node_ip_address()

        def endpoint(self) -> dict[str, str]:
            return {"host": socket.gethostname(), "ip": self.ip}

        def start(self, argv: list[str], node_rank: int, master_addr: str, master_port: int) -> dict[str, Any]:
            env = dict(os.environ)
            torchrun = torchrun_command(
                [
                    f"--nnodes={parsed.workers}",
                    f"--nproc_per_node={parsed.gpus_per_worker}",
                    f"--node_rank={node_rank}",
                    f"--master_addr={master_addr}",
                    f"--master_port={master_port}",
                ],
                argv,
            )
            self.process = subprocess.Popen(torchrun, env=env, text=True)
            return {"host": socket.gethostname(), "ip": self.ip, "nodeRank": node_rank, "pid": self.process.pid}

        def wait(self) -> int:
            if self.process is None:
                return 1
            return self.process.wait()

    ray.init(address=os.environ.get("RAY_ADDRESS", "auto"))
    group = placement_group(execution_plan(parsed)["placementBundles"], strategy="STRICT_SPREAD")
    try:
        ray.get(group.ready())
        launchers = [
            NodeLauncher.options(
                num_gpus=parsed.gpus_per_worker,
                placement_group=group,
                placement_group_bundle_index=index,
            ).remote()
            for index in range(parsed.workers)
        ]
        master = ray.get(launchers[0].endpoint.remote())
        # The port is private to the worker Pod network. A randomized high port
        # avoids sharing a well-known rendezvous port with another RayJob.
        master_port = 20000 + (os.getpid() % 20000)
        starts = [
            launchers[index].start.remote(parsed.command, index, master["ip"], master_port)
            for index in range(parsed.workers)
        ]
        nodes = ray.get(starts)
        time.sleep(1)
        return_codes = ray.get([launcher.wait.remote() for launcher in launchers])
        write_topology({
            **execution_plan(parsed),
            "master": {"address": master["ip"], "port": master_port},
            "nodes": nodes,
            "returnCodes": return_codes,
        })
        return 0 if all(code == 0 for code in return_codes) else 1
    finally:
        remove_placement_group(group)


def main() -> int:
    parsed = arguments()
    plan = execution_plan(parsed)
    if parsed.print_plan:
        print(json.dumps(plan, sort_keys=True))
        return 0
    if parsed.mode in {"single_gpu", "torchrun"}:
        return run_single_or_torchrun(parsed)
    return run_distributed(parsed)


if __name__ == "__main__":
    raise SystemExit(main())
