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
import urllib.error
import urllib.request
from datetime import UTC, datetime
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
    value.add_argument("--previous-inventory-key", default="")
    # callback URL and token are rendered only by the platform controller. They
    # are intentionally not accepted from the public API.
    value.add_argument("--callback-url", required=True)
    value.add_argument("--callback-token", default=os.environ.get("IDC_SYNC_CALLBACK_TOKEN", ""))
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
    payload = {"schema": 1, "entries": [_entry_payload(entry) for entry in entries]}
    return json.dumps(payload, sort_keys=True, ensure_ascii=True, separators=(",", ":")).encode("ascii")


def _entry_payload(entry: InventoryEntry) -> dict[str, object]:
    return {
        "relativePath": entry.relative_path,
        "sizeBytes": entry.size_bytes,
        "modifiedAt": _timestamp(entry.modified_at_ns),
        "modifiedAtNs": entry.modified_at_ns,
        "sha256": entry.sha256,
        "objectKey": entry.object_key,
    }


def _timestamp(modified_at_ns: int) -> str:
    return datetime.fromtimestamp(modified_at_ns / 1_000_000_000, tz=UTC).isoformat().replace("+00:00", "Z")


def _load_previous_inventory(bucket: str, key: str, config: Path, work_dir: Path) -> dict[str, InventoryEntry]:
    if not key:
        return {}
    key = _safe_prefix(key, "previous inventory key")
    if "/idc-inventories/" not in key or not key.endswith(".json"):
        raise SyncError("invalid previous inventory key")
    path = work_dir / "previous-inventory.json"
    _run_tosutil([
        "tosutil", "cp", f"tos://{bucket}/{key}", str(path), "-vchecksum",
        f"-cpd={work_dir / 'checkpoint'}", f"-o={work_dir / 'results'}", f"-conf={config}",
    ])
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SyncError("previous inventory is unavailable") from exc
    if payload.get("schema") != 1 or not isinstance(payload.get("entries"), list):
        raise SyncError("previous inventory is invalid")
    result: dict[str, InventoryEntry] = {}
    for item in payload["entries"]:
        if not isinstance(item, dict):
            raise SyncError("previous inventory is invalid")
        relative = _safe_relative_path(str(item.get("relativePath", "")))
        try:
            size = int(item["sizeBytes"])
            modified_at_ns = int(item.get("modifiedAtNs", 0))
            digest = str(item["sha256"])
            object_key = str(item["objectKey"])
        except (KeyError, TypeError, ValueError) as exc:
            raise SyncError("previous inventory is invalid") from exc
        if modified_at_ns <= 0:
            try:
                modified_at_ns = int(datetime.fromisoformat(str(item["modifiedAt"]).replace("Z", "+00:00")).timestamp() * 1_000_000_000)
            except (KeyError, TypeError, ValueError) as exc:
                raise SyncError("previous inventory is invalid") from exc
        if size < 0 or modified_at_ns <= 0 or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise SyncError("previous inventory is invalid")
        if "/idc-raw/sha256/" not in object_key or not object_key.endswith("/" + digest):
            raise SyncError("previous inventory is invalid")
        if relative in result:
            raise SyncError("previous inventory is invalid")
        result[relative] = InventoryEntry(relative, size, modified_at_ns, digest, object_key)
    return result


def _write_receipt(payload: dict[str, object]) -> None:
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("ascii")
    if len(encoded) > MAX_RECEIPT_BYTES:
        raise SyncError("sync receipt exceeds termination message limit")
    try:
        TERMINATION_LOG.write_bytes(encoded)
    except OSError:
        # A local test may not provide Kubernetes' termination-log mount.
        pass


def _post_callback(url: str, token: str, payload: dict[str, object]) -> None:
    body = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")
    request = urllib.request.Request(url, data=body, method="POST")
    request.add_header("Authorization", "Bearer " + token)
    request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            if response.status // 100 != 2:
                raise SyncError("sync receipt was rejected")
    except (urllib.error.URLError, TimeoutError):
        raise SyncError("sync receipt delivery failed") from None


def _report_entries(callback_url: str, callback_token: str, run_id: str, entries: list[InventoryEntry]) -> None:
    # 1,000 entries stay well below the API's bounded internal request size
    # while keeping the number of control-plane calls practical for large data.
    for start in range(0, len(entries), 1000):
        _post_callback(callback_url + "/entries", callback_token, {
            "runId": run_id,
            "entries": [_entry_payload(entry) for entry in entries[start : start + 1000]],
        })


def run(arguments: list[str] | None = None) -> dict[str, object]:
    request = parser().parse_args(arguments)
    run_id = _safe_identifier(request.run_id, "run id")
    bucket = _safe_identifier(request.bucket, "bucket")
    relative_path = _safe_relative_path(request.source_relative_path)
    mirror_prefix = _safe_prefix(request.mirror_prefix, "mirror prefix")
    internal_prefix = _safe_prefix(request.internal_prefix, "internal prefix")
    if not request.callback_url.startswith("http://") or not request.callback_token:
        raise SyncError("sync callback is unavailable")
    if request.parallelism < 1 or request.parallelism > 64:
        raise SyncError("invalid transfer parallelism")
    config = request.tosutil_config.resolve(strict=True)
    if not config.is_file() or config.is_symlink():
        raise SyncError("tosutil configuration is unavailable")
    source = _source_directory(request.source_root, relative_path)
    work_dir = request.work_dir.resolve()
    work_dir.mkdir(parents=True, exist_ok=True)

    previous = _load_previous_inventory(bucket, request.previous_inventory_key, config, work_dir)
    _mirror_source(source, bucket, mirror_prefix, config, work_dir, request.parallelism)
    entries: list[InventoryEntry] = []
    source_by_entry: list[tuple[Path, InventoryEntry]] = []
    new_count = 0
    changed_count = 0
    reused_count = 0
    for relative, candidate in _walk_files(source):
        observed = candidate.stat(follow_symlinks=False)
        prior = previous.get(relative)
        if prior is not None and prior.size_bytes == observed.st_size and prior.modified_at_ns == observed.st_mtime_ns:
            entry = prior
            reused_count += 1
        else:
            digest, size, modified_at_ns = _sha256_file(candidate)
            object_key = f"{internal_prefix}/idc-raw/sha256/{digest[:2]}/{digest}"
            entry = InventoryEntry(relative, size, modified_at_ns, digest, object_key)
            source_by_entry.append((candidate, entry))
            if prior is None:
                new_count += 1
            else:
                changed_count += 1
        entries.append(entry)
    if not entries:
        raise SyncError("source inventory is empty")
    entries.sort(key=lambda entry: entry.relative_path)
    with ThreadPoolExecutor(max_workers=request.parallelism) as pool:
        futures = [pool.submit(_copy_immutable, source, entry, bucket, config, work_dir) for source, entry in source_by_entry]
        for future in futures:
            future.result()

    _report_entries(request.callback_url, request.callback_token, run_id, entries)

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
        "newObjectCount": new_count,
        "changedObjectCount": changed_count,
        "reusedObjectCount": reused_count,
        "tombstonedObjectCount": len(set(previous) - {entry.relative_path for entry in entries}),
        "entries": [_entry_payload(entry) for entry in entries],
    }
    _post_callback(request.callback_url + "/complete", request.callback_token, {
        key: value for key, value in receipt.items() if key != "entries"
    })
    _write_receipt({key: value for key, value in receipt.items() if key != "entries"})
    return receipt


def _failure_callback_from_argv(arguments: list[str]) -> tuple[str, str, str] | None:
    values: dict[str, str] = {}
    for index, value in enumerate(arguments[:-1]):
        if value in {"--callback-url", "--callback-token", "--run-id"}:
            values[value] = arguments[index + 1]
    callback_url = values.get("--callback-url", "").rstrip("/")
    token = values.get("--callback-token", os.environ.get("IDC_SYNC_CALLBACK_TOKEN", ""))
    run_id = values.get("--run-id", "")
    if not callback_url.startswith("http://") or not token or not _IDENTIFIER.fullmatch(run_id):
        return None
    return callback_url + "/failed", token, run_id


def main() -> int:
    try:
        receipt = run()
        print(json.dumps({key: value for key, value in receipt.items() if key != "entries"}, sort_keys=True), flush=True)
        return 0
    except (SyncError, OSError, ValueError):
        failure = _failure_callback_from_argv(sys.argv[1:])
        if failure is not None:
            try:
                _post_callback(failure[0], failure[1], {"runId": failure[2], "failureReason": "IDC sync worker failed"})
            except SyncError:
                pass
        _write_receipt({"error": "IDC sync failed"})
        print("IDC sync failed", file=sys.stderr, flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
