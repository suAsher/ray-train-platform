"""Run on the builder with upstream source mounted separately and no network."""
import os
from pathlib import Path
import subprocess
import sys

import numpy as np
from PIL import Image
import torch
import yaml


def main():
    source = Path(sys.argv[1]).resolve()
    output = Path(sys.argv[2]).resolve()
    device = sys.argv[3] if len(sys.argv) > 3 else "cpu"
    torch.set_num_threads(2)
    output.mkdir(parents=True, exist_ok=False)
    for split in ("train", "val"):
        images = output / "data" / "images" / split
        labels = output / "data" / "labels" / split
        images.mkdir(parents=True)
        labels.mkdir(parents=True)
        for index in range(4):
            pixels = np.random.default_rng(index).integers(0, 255, (64, 64, 3), dtype=np.uint8)
            Image.fromarray(pixels).save(images / f"{index}.jpg")
            (labels / f"{index}.txt").write_text("0 0.5 0.5 0.4 0.4\n")
    data = output / "data.yaml"
    data.write_text(yaml.safe_dump({"path": str(output / "data"), "train": "images/train", "val": "images/val", "names": {0: "synthetic"}}))
    env = {**os.environ, "OMP_NUM_THREADS": "2", "OPENBLAS_NUM_THREADS": "2", "WANDB_MODE": "disabled", "COMET_MODE": "DISABLED"}
    subprocess.run([sys.executable, "train.py", "--weights", "", "--cfg", "models/yolov5n.yaml", "--data", str(data), "--epochs", "1", "--batch-size", "2", "--imgsz", "64", "--workers", "0", "--device", device, "--project", str(output), "--name", "train", "--exist-ok", "--noautoanchor"], cwd=source, env=env, check=True, timeout=600)
    assert (output / "train/results.csv").is_file()
    assert (output / "train/weights/last.pt").stat().st_size > 0
    subprocess.run([sys.executable, "val.py", "--weights", str(output / "train/weights/last.pt"), "--data", str(data), "--imgsz", "64", "--batch-size", "2", "--workers", "0", "--device", device, "--project", str(output), "--name", "val"], cwd=source, env=env, check=True, timeout=300)
    print("PASS: offline YOLOv5 training, checkpoint and validation", flush=True)


if __name__ == "__main__":
    main()
