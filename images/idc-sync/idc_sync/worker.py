"""One-way, receipt-producing IDC sync worker.

This process intentionally accepts only identifiers and relative paths rendered
by the control plane. It never accepts an endpoint, credential, NFS server or
an arbitrary destination from an end user. ``tosutil cp -u`` is used for the
fast mutable mirror and for content-addressed raw blobs; no command path can
delete objects.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


TERMINATION_LOG = Path("/dev/termination-log")
MAX_RECEIPT_BYTES = 4096
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$")
_PREFIX = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+/-]{0,1023}$")


class SyncError(RuntimeError):
    """A sanitized sync failure safe to place in a Kubernetes receipt."""


@dataclass(frozen=True)
class InventoryEntry:
    relative_path: str
    size_bytes: int
    modified_at_ns: int
    sha256: str
    object_key: str


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(add_help=False)
    value.add_argument("--run-id", required=True)
    value.add_argument("--source-relative-path", required=True)
    value.add_argument("--mirror-prefix", required=True)
    value.add_argument("--internal-prefix", required=True)
    value.add_argument("--bucket", required=True)
    value.add_argument("--tosutil-config", required=True, type=Path)
    value.add_argument("--source-root", default="/data/source", type=Path)
    value.add_argument("--work-dir", default="/work", type=Path)
    value.add_argument("--parallelism", default=16, type=int)
    return value


def _safe_identifier(value: str, field: str) -> str:
    if not _IDENTIFIER.fullmatch(value):
        raise SyncError(f"invalid {field}")
    return value


def _safe_prefix(value: str, field: str) -> str:
    value = value.strip("/")
    if not value or not _PREFIX.fullmatch(value) or any(part in {"", ".", ".."} for part in value.split("/")):
        raise SyncError(f"invalid {field}")
    return value


def _safe_relative_path(value: str) -> str:
    if value.startswith("/"):
        raise SyncError("invalid source relative path")
    value = value.strip("/")
    if not value or any(part in {"", ".", ".."} for part in value.split("/")):
        raise SyncError("invalid source relative path")
    return value


def _source_directory(root: Path, relative_path: str) -> Path:
    resolved_root = root.resolve(strict=True)
    source = (resolved_root / relative_path).resolve(strict=True)
    if source == resolved_root or resolved_root not in source.parents or not source.is_dir():
        raise SyncError("configured source directory is unavailable")
    return source


def _sha256_file(path: Path) -> tuple[str, int, int]:
    before = path.stat(follow_symlinks=False)
    if not path.is_file() or path.is_symlink():
        raise SyncError("source contains unsupported file")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(8 * 1024 * 1024), b""):
            digest.update(chunk)
    after = path.stat(follow_symlinks=False)
    if (before.st_ino, before.st_size, before.st_mtime_ns) != (after.st_ino, after.st_size, after.st_mtime_ns):
        raise SyncError("source changed during inventory")
    return digest.hexdigest(), after.st_size, after.st_mtime_ns


def _walk_files(source: Path) -> Iterable[tuple[str, Path]]:
    for directory, dirnames, filenames in os.walk(source, followlinks=False):
        dirnames[:] = sorted(name for name in dirnames if not (Path(directory) / name).is_symlink())
        for name in sorted(filenames):
            candidate = Path(directory) / name
            if candidate.is_symlink():
                raise SyncError("source contains symlink")
            yield candidate.relative_to(source).as_posix(), candidate


def _run_tosutil(arguments: list[str]) -> None:
    result = subprocess.run(arguments, stdin=subprocess.DEVNULL, stdout=sys.stderr, stderr=sys.stderr, check=False)
    if result.returncode != 0:
        raise SyncError("tosutil transfer failed")


def _mirror_source(source: Path, bucket: str, mirror_prefix: str, config: Path, work_dir: Path, parallelism: int) -> None:
    _run_tosutil([
        "tosutil", "cp", str(source) + "/", f"tos://{bucket}/{mirror_prefix}/", "-r", "-u", "-vchecksum",
        f"-j={parallelism}", f"-nfj={parallelism}", f"-cpd={work_dir / 'checkpoint'}", f"-o={work_dir / 'results'}", f"-conf={config}",
    ])


def _copy_immutable(source: Path, entry: InventoryEntry, bucket: str, config: Path, work_dir: Path) -> None:
    _run_tosutil([
        "tosutil", "cp", str(source), f"tos://{bucket}/{entry.object_key}", "-u", "-vchecksum",
        f"-cpd={work_dir / 'checkpoint'}", f"-o={work_dir / 'results'}", f"-conf={config}",
    ])


def _canonical_inventory(entries: list[InventoryEntry]) -> bytes:
    payload = {"schema": 1, "entries": [entry.__dict__ for entry in entries]}
    return json.dumps(payload, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode("ascii")


def _write_receipt(payload: dict[str, object]) -> None:
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("ascii")
    if len(encoded) > MAX_RECEIPT_BYTES:
        raise SyncError("sync receipt exceeds termination message limit")
    try:
        TERMINATION_LOG.write_bytes(encoded)
    except OSError:
        # A local test may not provide Kubernetes' termination-log mount.
        pass


def run(arguments: list[str] | None = None) -> dict[str, object]:
    request = parser().parse_args(arguments)
    run_id = _safe_identifier(request.run_id, "run id")
    bucket = _safe_identifier(request.bucket, "bucket")
    relative_path = _safe_relative_path(request.source_relative_path)
    mirror_prefix = _safe_prefix(request.mirror_prefix, "mirror prefix")
    internal_prefix = _safe_prefix(request.internal_prefix, "internal prefix")
    if request.parallelism < 1 or request.parallelism > 64:
        raise SyncError("invalid transfer parallelism")
    config = request.tosutil_config.resolve(strict=True)
    if not config.is_file() or config.is_symlink():
        raise SyncError("tosutil configuration is unavailable")
    source = _source_directory(request.source_root, relative_path)
    work_dir = request.work_dir.resolve()
    work_dir.mkdir(parents=True, exist_ok=True)

    _mirror_source(source, bucket, mirror_prefix, config, work_dir, request.parallelism)
    entries: list[InventoryEntry] = []
    source_by_entry: list[tuple[Path, InventoryEntry]] = []
    for relative, candidate in _walk_files(source):
        digest, size, modified_at_ns = _sha256_file(candidate)
        object_key = f"{internal_prefix}/idc-raw/sha256/{digest[:2]}/{digest}"
        entry = InventoryEntry(relative, size, modified_at_ns, digest, object_key)
        entries.append(entry)
        source_by_entry.append((candidate, entry))
    if not entries:
        raise SyncError("source inventory is empty")
    entries.sort(key=lambda entry: entry.relative_path)
    with ThreadPoolExecutor(max_workers=request.parallelism) as pool:
        futures = [pool.submit(_copy_immutable, source, entry, bucket, config, work_dir) for source, entry in source_by_entry]
        for future in futures:
            future.result()

    inventory = _canonical_inventory(entries)
    inventory_digest = hashlib.sha256(inventory).hexdigest()
    inventory_path = work_dir / "inventory.json"
    inventory_path.write_bytes(inventory)
    inventory_key = f"{internal_prefix}/idc-inventories/{run_id}/{inventory_digest}.json"
    _run_tosutil([
        "tosutil", "cp", str(inventory_path), f"tos://{bucket}/{inventory_key}", "-u", "-vchecksum",
        f"-cpd={work_dir / 'checkpoint'}", f"-o={work_dir / 'results'}", f"-conf={config}",
    ])
    receipt: dict[str, object] = {
        "runId": run_id,
        "inventorySha256": inventory_digest,
        "inventoryObjectKey": inventory_key,
        "sourceObjectCount": len(entries),
        "sourceBytes": sum(entry.size_bytes for entry in entries),
        "entries": [entry.__dict__ for entry in entries],
    }
    _write_receipt({key: value for key, value in receipt.items() if key != "entries"})
    return receipt


def main() -> int:
    try:
        receipt = run()
        print(json.dumps({key: value for key, value in receipt.items() if key != "entries"}, sort_keys=True), flush=True)
        return 0
    except (SyncError, OSError, ValueError):
        _write_receipt({"error": "IDC sync failed"})
        print("IDC sync failed", file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
