#!/usr/bin/env python3
"""Small managed Ray Train checkpoint/resume acceptance workload.

The integrity helpers intentionally depend only on the Python standard library.
Ray is imported lazily by ``main`` so local unit tests do not need Ray installed.
"""

from __future__ import annotations

import dataclasses
import hashlib
import json
import math
import os
import pathlib
import tempfile
from typing import Any, Dict, Mapping, Optional


MANIFEST_NAME = "manifest.json"
STATE_NAME = "state.json"
MAX_CHECKPOINT_BYTES = 1024 * 1024


@dataclasses.dataclass(frozen=True)
class CheckpointState:
    step: int
    world_size: int
    rank: int
    metrics: Dict[str, float]


def _validate_state(state: CheckpointState) -> None:
    if isinstance(state.step, bool) or not isinstance(state.step, int) or state.step < 0:
        raise ValueError("checkpoint step must be a non-negative integer")
    if isinstance(state.world_size, bool) or not isinstance(state.world_size, int) or state.world_size < 1:
        raise ValueError("checkpoint world size must be positive")
    if isinstance(state.rank, bool) or not isinstance(state.rank, int) or not 0 <= state.rank < state.world_size:
        raise ValueError("checkpoint rank is outside the world size")
    for name, value in state.metrics.items():
        if not isinstance(name, str) or not name or len(name) > 128:
            raise ValueError("checkpoint metric name is invalid")
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(float(value)):
            raise ValueError("checkpoint metric must be finite")


def _encoded_json(payload: Mapping[str, Any]) -> bytes:
    return (json.dumps(payload, allow_nan=False, sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")


def _atomic_write(path: pathlib.Path, payload: bytes) -> None:
    temporary_directory = path.parent.parent
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=".%s-%s." % (path.parent.name, path.name),
        suffix=".tmp",
        dir=str(temporary_directory),
    )
    temporary = pathlib.Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(str(temporary), str(path))
        directory = os.open(str(path.parent), os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def _real_checkpoint_root(root: pathlib.Path, *, create: bool) -> pathlib.Path:
    if root.is_symlink():
        raise ValueError("checkpoint root must not be a symlink")
    if create:
        root.mkdir(parents=True, exist_ok=True)
    if not root.is_dir() or root.is_symlink():
        raise ValueError("checkpoint root must be a real directory")
    return root


def write_checkpoint_atomic(
    checkpoint_root: pathlib.Path,
    *,
    step: int,
    world_size: int,
    rank: int,
    metrics: Mapping[str, float],
) -> Dict[str, Any]:
    root = _real_checkpoint_root(pathlib.Path(checkpoint_root), create=True)
    state = CheckpointState(
        step=step,
        world_size=world_size,
        rank=rank,
        metrics=dict(metrics),
    )
    _validate_state(state)
    state_payload = _encoded_json(
        {
            "step": state.step,
            "worldSize": state.world_size,
            "rank": state.rank,
            "metrics": state.metrics,
        }
    )
    if len(state_payload) > MAX_CHECKPOINT_BYTES:
        raise ValueError("checkpoint state is too large")
    _atomic_write(root / STATE_NAME, state_payload)
    manifest: Dict[str, Any] = {
        "version": 1,
        "complete": True,
        "metadata": {
            "epoch": state.step,
            "step": state.step,
            "worldSize": state.world_size,
            "rank": state.rank,
        },
        "files": [
            {
                "path": STATE_NAME,
                "size": len(state_payload),
                "sha256": hashlib.sha256(state_payload).hexdigest(),
            }
        ],
    }
    _atomic_write(root / MANIFEST_NAME, _encoded_json(manifest))
    return json.loads(json.dumps(manifest))


def _read_bounded(path: pathlib.Path) -> bytes:
    if path.is_symlink() or not path.is_file():
        raise ValueError("checkpoint file is missing or unsafe")
    payload = path.read_bytes()
    if len(payload) > MAX_CHECKPOINT_BYTES:
        raise ValueError("checkpoint file is too large")
    return payload


def load_complete_checkpoint(checkpoint_root: pathlib.Path) -> CheckpointState:
    root = _real_checkpoint_root(pathlib.Path(checkpoint_root), create=False)
    try:
        manifest = json.loads(_read_bounded(root / MANIFEST_NAME))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError("checkpoint manifest is unreadable") from exc
    if not isinstance(manifest, dict) or manifest.get("complete") is not True:
        raise ValueError("checkpoint manifest is not complete")
    if manifest.get("version") != 1 or not isinstance(manifest.get("files"), list):
        raise ValueError("checkpoint manifest format is unsupported")
    entries = manifest["files"]
    if len(entries) != 1 or not isinstance(entries[0], dict) or entries[0].get("path") != STATE_NAME:
        raise ValueError("checkpoint manifest file set is invalid")
    state_payload = _read_bounded(root / STATE_NAME)
    if entries[0].get("size") != len(state_payload) or entries[0].get("sha256") != hashlib.sha256(state_payload).hexdigest():
        raise ValueError("checkpoint state digest does not match the manifest")
    try:
        raw = json.loads(state_payload)
        state = CheckpointState(
            step=raw["step"],
            world_size=raw["worldSize"],
            rank=raw["rank"],
            metrics=dict(raw["metrics"]),
        )
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
        raise ValueError("checkpoint state is invalid") from exc
    _validate_state(state)
    return state


def resume_start_step(state: Optional[CheckpointState]) -> int:
    return 0 if state is None else state.step + 1


def _resume_state() -> Optional[CheckpointState]:
    candidate = os.environ.get("RAYTRAIN_RESUME_CHECKPOINT_PATH") or os.environ.get("PLATFORM_CHECKPOINT_PATH")
    return load_complete_checkpoint(pathlib.Path(candidate)) if candidate else None


def main() -> None:
    from ray import train
    from raytrain_runtime.reporting import report_metrics

    context = train.get_context()
    rank = int(context.get_world_rank())
    world_size = int(context.get_world_size())
    start = resume_start_step(_resume_state())
    steps = int(os.environ.get("RAY_TRAIN_SMOKE_STEPS", "3"))
    if steps < 1 or steps > 100:
        raise ValueError("RAY_TRAIN_SMOKE_STEPS must be between 1 and 100")
    checkpoint_root = pathlib.Path(
        os.environ.get("RAYTRAIN_CHECKPOINT_OUTPUT_PATH", tempfile.gettempdir())
    )
    for step in range(start, start + steps):
        metrics = {
            "step": float(step),
            "step_time": 0.01,
            "data_time": 0.001,
            "nccl_duration": 0.002,
            "loss": 1.0 / float(step + 1),
        }
        checkpoint_path = None
        if rank == 0:
            path = checkpoint_root / ("checkpoint-step-%09d" % step)
            write_checkpoint_atomic(
                path,
                step=step,
                world_size=world_size,
                rank=rank,
                metrics=metrics,
            )
            checkpoint_path = str(path)
        report_metrics(metrics, checkpoint_path, world_rank=rank, train_api=train)
        print(
            "RAY_TRAIN_CHECKPOINT_SMOKE rank=%d world_size=%d step=%d resumed=%s"
            % (rank, world_size, step, start > 0),
            flush=True,
        )


if __name__ == "__main__":
    main()
