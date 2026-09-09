import hashlib
import json
from pathlib import Path

import pytest

from idc_sync import worker


def test_canonical_inventory_is_stable_and_content_addressed(tmp_path: Path):
    source = tmp_path / "source" / "QP_NuScene" / "labeled"
    source.mkdir(parents=True)
    (source / "frame.bin").write_bytes(b"frame")
    config = tmp_path / "config"
    config.write_text("configured")

    commands: list[list[str]] = []
    original = worker._run_tosutil
    worker._run_tosutil = commands.append
    try:
        receipt = worker.run([
            "--run-id", "run-1", "--source-root", str(tmp_path / "source"), "--source-relative-path", "QP_NuScene/labeled",
            "--mirror-prefix", "ray-train/platform/idc-mirror/labeled", "--internal-prefix", "ray-train/platform",
            "--bucket", "training-data", "--tosutil-config", str(config), "--work-dir", str(tmp_path / "work"),
        ])
    finally:
        worker._run_tosutil = original

    assert receipt["sourceObjectCount"] == 1
    entry = receipt["entries"][0]
    assert entry["sha256"] == hashlib.sha256(b"frame").hexdigest()
    assert entry["object_key"].endswith("/" + entry["sha256"])
    assert receipt["inventorySha256"] == hashlib.sha256((tmp_path / "work" / "inventory.json").read_bytes()).hexdigest()
    assert commands[0][1:5] == ["cp", str(source) + "/", "tos://training-data/ray-train/platform/idc-mirror/labeled/", "-r"]


@pytest.mark.parametrize("source_path", ["/etc", "../outside", "QP_NuScene/../labeled"])
def test_worker_rejects_unsafe_source_paths(tmp_path: Path, source_path: str):
    config = tmp_path / "config"
    config.write_text("configured")
    with pytest.raises(worker.SyncError):
        worker.run([
            "--run-id", "run-1", "--source-root", str(tmp_path), "--source-relative-path", source_path,
            "--mirror-prefix", "mirror", "--internal-prefix", "internal", "--bucket", "training-data",
            "--tosutil-config", str(config), "--work-dir", str(tmp_path / "work"),
        ])
