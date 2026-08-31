#!/usr/bin/env python3
"""Generate fresh LiDAR-only S1H PKLs from public NuScenes packages."""

from __future__ import annotations

import argparse
from concurrent.futures import ProcessPoolExecutor
from contextlib import contextmanager, redirect_stderr, redirect_stdout
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import pickle
import sys
from typing import Any


REQUIRED_METADATA = (
    "sample.json",
    "scene.json",
    "sample_data.json",
    "sample_annotation.json",
)


@dataclass(frozen=True)
class PackageSpec:
    collection: str
    name: str
    path: Path
    sample_count: int
    fingerprint: str


def discover_packages(source_root: Path) -> tuple[PackageSpec, ...]:
    root = Path(source_root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError("source root must be a directory")
    packages = []
    for collection in _safe_directories(root):
        for package in _safe_directories(collection):
            metadata = package / "v1.0-mini"
            if metadata.is_symlink() or not metadata.is_dir():
                continue
            if any(not (metadata / name).is_file() for name in REQUIRED_METADATA):
                continue
            try:
                metadata_files = {
                    name: (metadata / name).read_bytes() for name in REQUIRED_METADATA
                }
                samples = json.loads(metadata_files["sample.json"].decode("utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
                raise ValueError(f"invalid sample metadata in {collection.name}/{package.name}") from error
            if not isinstance(samples, list):
                raise ValueError(f"sample metadata must be a list in {collection.name}/{package.name}")
            packages.append(
                PackageSpec(
                    collection=collection.name,
                    name=package.name,
                    path=package,
                    sample_count=len(samples),
                    fingerprint=_metadata_fingerprint(metadata_files),
                )
            )
    return tuple(
        sorted(
            packages,
            key=lambda item: (
                item.collection.encode("utf-8"),
                item.name.encode("utf-8"),
            ),
        )
    )


def _metadata_fingerprint(metadata_files: dict[str, bytes]) -> str:
    digest = hashlib.sha256(b"raytrain-s1h-package-metadata-v1\x00")
    for name in REQUIRED_METADATA:
        payload = metadata_files[name]
        encoded_name = name.encode("utf-8")
        digest.update(len(encoded_name).to_bytes(8, byteorder="big", signed=False))
        digest.update(encoded_name)
        digest.update(len(payload).to_bytes(8, byteorder="big", signed=False))
        digest.update(payload)
    return digest.hexdigest()


def merge_package_outputs(output_root: Path) -> dict[str, int]:
    root = Path(output_root)
    train_infos: list[dict[str, Any]] = []
    val_infos: list[dict[str, Any]] = []
    seen_tokens: set[str] = set()
    package_root = root / "packages"
    train_files = sorted(package_root.glob("*/*/nuscenes_infos_train.pkl"))
    for train_file in train_files:
        val_file = train_file.with_name("nuscenes_infos_val.pkl")
        if not val_file.is_file():
            raise ValueError(f"missing paired validation PKL for {train_file.parent.name}")
        for split, source, destination in (
            ("train", train_file, train_infos),
            ("val", val_file, val_infos),
        ):
            for info in _load_generated_infos(source):
                token = info.get("token")
                if not isinstance(token, str) or not token:
                    raise ValueError(f"{split} info must contain a token")
                if token in seen_tokens:
                    raise ValueError("generated PKLs contain a duplicate token")
                if not isinstance(info.get("scene_token"), str) or not info["scene_token"]:
                    raise ValueError("generated PKLs must contain scene_token provenance")
                seen_tokens.add(token)
                destination.append(dict(info))

    if not train_files:
        raise ValueError("no generated package PKLs were found")
    metadata = {"version": "v1.0-mini", "raytrain_index": "s1h-lidar-v1"}
    _dump_generated_infos(root / "merged_nuscenes_infos_train.pkl", train_infos, metadata)
    _dump_generated_infos(root / "merged_nuscenes_infos_val.pkl", val_infos, metadata)
    return {"train_samples": len(train_infos), "val_samples": len(val_infos)}


def _safe_directories(root: Path) -> list[Path]:
    return sorted(
        (
            item
            for item in root.iterdir()
            if not item.is_symlink() and item.is_dir()
        ),
        key=lambda item: item.name.encode("utf-8"),
    )


def _load_generated_infos(path: Path) -> list[dict[str, Any]]:
    with path.open("rb") as stream:
        payload = pickle.load(stream)
    infos = payload.get("infos") if isinstance(payload, dict) else None
    if not isinstance(infos, list) or any(not isinstance(info, dict) for info in infos):
        raise ValueError(f"generated converter output is invalid: {path.name}")
    return infos


def _dump_generated_infos(path: Path, infos: list[dict[str, Any]], metadata: dict[str, str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    with temporary.open("wb") as stream:
        pickle.dump({"infos": infos, "metadata": dict(metadata)}, stream, protocol=pickle.HIGHEST_PROTOCOL)
    temporary.replace(path)


@contextmanager
def _redirect_process_output(stream: Any):
    """Redirect Python and native progress output in one worker process."""
    sys.stdout.flush()
    sys.stderr.flush()
    stdout_fd = os.dup(1)
    stderr_fd = os.dup(2)
    try:
        os.dup2(stream.fileno(), 1)
        os.dup2(stream.fileno(), 2)
        with redirect_stdout(stream), redirect_stderr(stream):
            yield
    finally:
        sys.stdout.flush()
        sys.stderr.flush()
        os.dup2(stdout_fd, 1)
        os.dup2(stderr_fd, 2)
        os.close(stdout_fd)
        os.close(stderr_fd)


def _generate_package(arguments: tuple[PackageSpec, Path, int, int]) -> dict[str, Any]:
    package, output_root, max_sweeps, min_scene_samples = arguments
    output = output_root / "packages" / package.collection / package.name
    train = output / "nuscenes_infos_train.pkl"
    val = output / "nuscenes_infos_val.pkl"
    receipt = output / "raytrain-package.json"
    expected_receipt = {
        "collection": package.collection,
        "fingerprint": package.fingerprint,
        "package": package.name,
        "sample_count": package.sample_count,
    }
    if train.is_file() and val.is_file() and _receipt_matches(receipt, expected_receipt):
        return {
            "collection": package.collection,
            "package": package.name,
            "cached": True,
            "status": "accepted",
        }
    if train.exists() or val.exists() or receipt.exists():
        raise ValueError(
            f"stale package output requires a new output root: {package.collection}/{package.name}"
        )
    output.mkdir(parents=True, exist_ok=True)
    from tools.data_converter.nuscenes_converter import create_nuscenes_infos

    converter_log = output / "converter.log"
    try:
        with converter_log.open("a", encoding="utf-8") as log_stream:
            with _redirect_process_output(log_stream):
                create_nuscenes_infos(
                    str(package.path),
                    str(output),
                    "nuscenes",
                    version="v1.0-mini",
                    max_sweeps=max_sweeps,
                    site_name=None,
                    min_scene_samples=min_scene_samples,
                    lidar_only=True,
                )
    except Exception as error:
        train.unlink(missing_ok=True)
        val.unlink(missing_ok=True)
        receipt.unlink(missing_ok=True)
        return {
            "collection": package.collection,
            "error_type": type(error).__name__,
            "package": package.name,
            "reason": str(error),
            "status": "rejected",
        }
    temporary_receipt = receipt.with_suffix(".json.tmp")
    temporary_receipt.write_text(
        json.dumps(expected_receipt, ensure_ascii=False, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    temporary_receipt.replace(receipt)
    return {
        "collection": package.collection,
        "package": package.name,
        "cached": False,
        "status": "accepted",
    }


def _receipt_matches(path: Path, expected: dict[str, Any]) -> bool:
    try:
        return json.loads(path.read_text(encoding="utf-8")) == expected
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return False


def _write_json_atomic(path: Path, payload: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    temporary.replace(path)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", type=Path, required=True)
    parser.add_argument("--output-root", type=Path, required=True)
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--max-sweeps", type=int, default=0)
    parser.add_argument("--min-scene-samples", type=int, default=81)
    parser.add_argument("--dry-run", action="store_true")
    arguments = parser.parse_args()
    if arguments.workers < 1 or arguments.workers > 32:
        parser.error("--workers must be between 1 and 32")
    if arguments.max_sweeps < 0:
        parser.error("--max-sweeps must not be negative")
    if arguments.min_scene_samples < 0:
        parser.error("--min-scene-samples must not be negative")
    return arguments


def main() -> int:
    arguments = parse_args()
    packages = discover_packages(arguments.source_root)
    inventory = {
        "collections": len({package.collection for package in packages}),
        "packages": len(packages),
        "raw_samples": sum(package.sample_count for package in packages),
    }
    print(json.dumps({"inventory": inventory}, separators=(",", ":"), sort_keys=True), flush=True)
    if arguments.dry_run:
        return 0
    work = (
        (package, arguments.output_root, arguments.max_sweeps, arguments.min_scene_samples)
        for package in packages
    )
    rejected: list[dict[str, str]] = []
    with ProcessPoolExecutor(max_workers=arguments.workers) as executor:
        for result in executor.map(_generate_package, work):
            print(json.dumps({"package": result}, separators=(",", ":"), sort_keys=True), flush=True)
            if result.get("status") == "rejected":
                rejected.append(
                    {
                        "collection": str(result["collection"]),
                        "error_type": str(result["error_type"]),
                        "package": str(result["package"]),
                        "reason": str(result["reason"]),
                    }
                )
    _write_json_atomic(arguments.output_root / "rejected-packages.json", rejected)
    if discover_packages(arguments.source_root) != packages:
        raise ValueError("source metadata changed during index generation; retry with a new output root")
    summary = merge_package_outputs(arguments.output_root)
    summary["accepted_packages"] = len(packages) - len(rejected)
    summary["rejected_packages"] = len(rejected)
    print(json.dumps({"merged": summary}, separators=(",", ":"), sort_keys=True), flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
