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
import contextlib
import ctypes
import json
import os
import pathlib
import signal
import socket
import subprocess
import sys
import tempfile
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


POLL_INTERVAL = 0.1
TERM_GRACE = 5.0
KILL_GRACE = 2.0


def runtime_event(event: str, **fields: Any) -> None:
    # Callers supply only lifecycle metadata, never argv, environment, or raw
    # exception messages (which can contain user command arguments).
    print("[raytrain-runtime] " + json.dumps({"event": event, **fields}, sort_keys=True), flush=True)


def process_identity(pid: int) -> tuple[str, int, int, str] | None:
    try:
        fields = pathlib.Path("/proc", str(pid), "stat").read_text().rsplit(")", 1)[1].split()
        return fields[19], int(fields[1]), int(fields[2]), fields[0]
    except (FileNotFoundError, ProcessLookupError, PermissionError):
        return None


def descendants(parent: int) -> dict[int, tuple[str, int, int, str]]:
    snapshot = {}
    for path in pathlib.Path("/proc").iterdir():
        if path.name.isdigit():
            current = process_identity(int(path.name))
            if current is not None:
                snapshot[int(path.name)] = current
    owned = {}
    frontier = {parent}
    while frontier:
        children = {pid: identity for pid, identity in snapshot.items()
                    if identity[1] in frontier and pid not in owned}
        owned = {**owned, **children}
        frontier = set(children)
    return owned


def signal_owned(owned: dict[int, tuple[str, int, int, str]], signum: int) -> None:
    for pid, identity in owned.items():
        current = process_identity(pid)
        if current is None or current[0] != identity[0] or current[3] == "Z":
            continue
        try:
            # A descendant's independent session (e.g. torchrun elastic) is
            # still owned. Verify each PID's birth before signalling it; never
            # signal the Ray worker's process group or all processes in a Pod.
            os.kill(pid, signum)
        except ProcessLookupError:
            pass


def reap_adopted(owned: dict[int, tuple[str, int, int, str]], root_pid: int) -> None:
    for pid in owned:
        if pid != root_pid:
            try:
                os.waitpid(pid, os.WNOHANG)
            except (ChildProcessError, ProcessLookupError):
                pass


def clean_descendants(process: subprocess.Popen[str], term_grace: float, kill_grace: float) -> bool:
    # Only the dedicated supervisor enables subreaping. Orphaned children,
    # including new sessions, reparent here rather than to the shared Ray actor.
    for signum, grace in ((signal.SIGTERM, term_grace), (signal.SIGKILL, kill_grace)):
        deadline = time.monotonic() + grace
        while True:
            process.poll()  # Preserve/reap the root's real return code first.
            owned = descendants(os.getpid())
            signal_owned(owned, signum)
            reap_adopted(owned, process.pid)
            if not any(identity[3] != "Z" for identity in owned.values()):
                return True
            if time.monotonic() >= deadline:
                break
            time.sleep(0.02)
    process.poll()
    owned = descendants(os.getpid())
    reap_adopted(owned, process.pid)
    return not any(identity[3] != "Z" for identity in owned.values())


def supervisor_main() -> int:
    config = json.load(sys.stdin)
    cancelled = 0

    def cancel(signum: int, _frame: Any) -> None:
        nonlocal cancelled
        cancelled = cancelled or signum

    signal.signal(signal.SIGTERM, cancel)
    signal.signal(signal.SIGINT, cancel)
    libc = ctypes.CDLL(None, use_errno=True)
    # PR_SET_CHILD_SUBREAPER and PR_SET_PDEATHSIG are Linux-only. Fail closed
    # before starting user code when supervision cannot be established.
    for option, value in ((36, 1), (1, signal.SIGTERM)):
        if libc.prctl(option, value, 0, 0, 0) != 0:
            raise OSError(ctypes.get_errno(), "cannot establish process supervision")
    if os.getppid() != config["parentPID"]:
        cancelled = signal.SIGTERM
    process = None
    returncode = 128 + cancelled if cancelled else 127
    cleaned = True
    try:
        if not cancelled:
            process = subprocess.Popen(config["command"], start_new_session=True, text=True)
            while process.poll() is None and not cancelled:
                time.sleep(0.02)
            returncode = process.poll()
    except OSError as error:
        runtime_event("launch_error", nodeRank=config["nodeRank"], errorType=type(error).__name__)
    finally:
        if process is not None:
            cleaned = clean_descendants(process, config["termGrace"], config["killGrace"])
            if returncode is None:
                returncode = process.poll()
        if returncode is None or (returncode == 0 and not cleaned):
            returncode = 1
        destination = pathlib.Path(config["statusPath"])
        destination.write_text(json.dumps({"returncode": returncode, "cleanupComplete": cleaned}))
    return 0


class ProcessTree:
    """One owned Linux process tree, isolated from the shared Ray worker."""

    def __init__(self, node_rank: int, term_grace: float = TERM_GRACE,
                 kill_grace: float = KILL_GRACE) -> None:
        self.node_rank, self.term_grace, self.kill_grace = node_rank, term_grace, kill_grace
        self.process: subprocess.Popen[str] | None = None
        self.directory: tempfile.TemporaryDirectory[str] | None = None
        self.returncode: int | None = None
        self.cleanup_complete = True

    def start(self, argv: list[str]) -> dict[str, Any]:
        if self.process is not None:
            raise RuntimeError("process already started")
        self.directory = tempfile.TemporaryDirectory(prefix="raytrain-supervisor-")
        self.process = subprocess.Popen(
            [sys.executable, os.path.abspath(__file__), "--raytrain-supervisor"],
            stdin=subprocess.PIPE, start_new_session=True, text=True,
        )
        config = {"command": argv, "parentPID": os.getpid(), "nodeRank": self.node_rank,
                  "termGrace": self.term_grace, "killGrace": self.kill_grace,
                  "statusPath": str(pathlib.Path(self.directory.name) / "result.json")}
        try:
            self.process.stdin.write(json.dumps(config))
            self.process.stdin.close()
        except BrokenPipeError:
            pass  # poll records the failed supervisor's exit without exposing argv.
        runtime_event("start", nodeRank=self.node_rank, pid=self.process.pid)
        return {"pid": self.process.pid, "nodeRank": self.node_rank, "host": socket.gethostname()}

    def poll(self) -> int | None:
        if self.returncode is not None or self.process is None:
            return self.returncode
        status = self.process.poll()
        if status is None:
            return None
        self.returncode = status if status != 0 else 1
        self.cleanup_complete = False
        try:
            result = json.loads((pathlib.Path(self.directory.name) / "result.json").read_text())
            self.returncode = int(result["returncode"])
            self.cleanup_complete = result["cleanupComplete"] is True
        except (OSError, ValueError, KeyError, TypeError):
            pass  # A lost supervisor is a failed, incomplete cleanup, never success.
        runtime_event("exit", nodeRank=self.node_rank, pid=self.process.pid, returncode=self.returncode)
        runtime_event("cleanup", nodeRank=self.node_rank, complete=self.cleanup_complete)
        self.directory.cleanup()
        return self.returncode

    def stop(self, reason: str) -> int | None:
        if self.process is None or self.poll() is not None:
            return self.returncode
        runtime_event("cleanup_start", nodeRank=self.node_rank, reason=reason)
        try:
            self.process.send_signal(signal.SIGTERM)
            self.process.wait(timeout=self.term_grace + self.kill_grace + 2)
        except subprocess.TimeoutExpired:
            # This only kills our isolated supervisor. A supervisor stuck in
            # kernel I/O or killed with SIGKILL cannot guarantee tree cleanup.
            self.process.kill()
            try:
                self.process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                runtime_event("cleanup", nodeRank=self.node_rank, complete=False)
                return None
        return self.poll()


class DriverCancelled(BaseException):
    def __init__(self, signum: int) -> None:
        self.returncode = 128 + signum


@contextlib.contextmanager
def cancellation_signals():
    def cancel(signum: int, _frame: Any) -> None:
        raise DriverCancelled(signum)

    previous = {signum: signal.signal(signum, cancel) for signum in (signal.SIGTERM, signal.SIGINT)}
    try:
        yield
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


def stop_launchers(ray: Any, launchers: list[Any], reason: str) -> bool:
    pending = []
    complete = True
    for launcher in launchers:
        try:
            pending.append(launcher.stop.remote(reason))
        except Exception as error:
            runtime_event("cleanup", complete=False, errorType=type(error).__name__)
            complete = False
    deadline = time.monotonic() + TERM_GRACE + KILL_GRACE + 5
    while pending and time.monotonic() < deadline:
        try:
            ready, pending = ray.wait(pending, num_returns=1, timeout=max(0, deadline - time.monotonic()))
        except Exception as error:
            runtime_event("cleanup", complete=False, errorType=type(error).__name__)
            complete = False
            break
        for ref in ready:
            try:
                if ray.get(ref) is None:
                    complete = False
            except Exception as error:
                runtime_event("cleanup", complete=False, errorType=type(error).__name__)
                complete = False
    if pending:
        runtime_event("cleanup", complete=False, reason="actor_timeout")
    for launcher in launchers:
        try:
            ray.kill(launcher, no_restart=True)
        except Exception as error:
            runtime_event("cleanup", complete=False, errorType=type(error).__name__)
            complete = False
    return complete and not pending


def monitor_launchers(ray: Any, launchers: list[Any]) -> int:
    pending = {launcher.poll.remote(): index for index, launcher in enumerate(launchers)}
    while pending:
        ready, _ = ray.wait(list(pending), num_returns=1, timeout=POLL_INTERVAL)
        for ref in ready:
            index = pending.pop(ref)
            code = ray.get(ref)
            if code is None:
                time.sleep(POLL_INTERVAL)
                pending[launchers[index].poll.remote()] = index
            elif code != 0:
                return int(code)
    return 0


def run_launchers(parsed: argparse.Namespace, distributed: bool) -> int:
    import ray
    from ray.util.placement_group import placement_group, remove_placement_group

    @ray.remote(num_cpus=1)
    class NodeLauncher:
        def __init__(self, node_rank: int) -> None:
            self.tree = ProcessTree(node_rank)

        def endpoint(self) -> dict[str, str]:
            return {"host": socket.gethostname(), "ip": ray.util.get_node_ip_address()}

        def start(self, argv: list[str]) -> dict[str, Any]:
            return {**self.tree.start(argv), "ip": ray.util.get_node_ip_address()}

        def poll(self) -> int | None:
            return self.tree.poll()

        def stop(self, reason: str) -> int | None:
            return self.tree.stop(reason)

    group, launchers, nodes = None, [], []
    returncode = 1
    with cancellation_signals():
        try:
            ray.init(address=os.environ.get("RAY_ADDRESS", "auto"))
            if distributed:
                group = placement_group(execution_plan(parsed)["placementBundles"], strategy="STRICT_SPREAD")
                ray.get(group.ready())
            for index in range(parsed.workers):
                options = {"num_gpus": parsed.gpus_per_worker}
                if group is not None:
                    options = {**options, "placement_group": group, "placement_group_bundle_index": index}
                launchers.append(NodeLauncher.options(**options).remote(index))
            master = ray.get(launchers[0].endpoint.remote())
            master_port = 20000 + (os.getpid() % 20000)
            starts = []
            for index, launcher in enumerate(launchers):
                command = execution_plan(parsed)["command"]
                if distributed:
                    command = torchrun_command([
                        f"--nnodes={parsed.workers}", f"--nproc_per_node={parsed.gpus_per_worker}",
                        f"--node_rank={index}", f"--master_addr={master['ip']}",
                        f"--master_port={master_port}",
                    ], parsed.command)
                starts.append(launcher.start.remote(command))
            while starts:
                ready, starts = ray.wait(starts, num_returns=1)
                nodes.extend(ray.get(ref) for ref in ready)
            returncode = monitor_launchers(ray, launchers)
        except DriverCancelled as error:
            returncode = error.returncode
        except KeyboardInterrupt:
            returncode = 130
        except Exception as error:
            runtime_event("driver_error", errorType=type(error).__name__)
        finally:
            # Repeated cancellation must not interrupt the bounded cleanup.
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
            signal.signal(signal.SIGINT, signal.SIG_IGN)
            complete = stop_launchers(ray, launchers, "completed" if returncode == 0 else "failed_or_cancelled")
            if returncode == 0 and not complete:
                returncode = 1
            if group is not None:
                try:
                    remove_placement_group(group)
                except Exception as error:
                    runtime_event("cleanup", complete=False, errorType=type(error).__name__)
                    if returncode == 0:
                        returncode = 1
            try:
                plan = {key: value for key, value in execution_plan(parsed).items() if key != "command"}
                write_topology({**plan, "nodes": nodes, "returncode": returncode})
            except Exception as error:
                runtime_event("topology_error", errorType=type(error).__name__)
    return returncode


def run_single_or_torchrun(parsed: argparse.Namespace) -> int:
    return run_launchers(parsed, distributed=False)


def run_distributed(parsed: argparse.Namespace) -> int:
    return run_launchers(parsed, distributed=True)


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
    if sys.argv[1:] == ["--raytrain-supervisor"]:
        try:
            raise SystemExit(supervisor_main())
        except Exception as error:
            runtime_event("supervisor_error", errorType=type(error).__name__)
            raise SystemExit(1)
    code = main()
    raise SystemExit(128 - code if code < 0 else code)
