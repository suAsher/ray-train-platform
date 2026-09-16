#!/usr/bin/env python3
"""Verify the pinned training stack and editors; CUDA execution is opt-in."""

import argparse
import importlib
import importlib.metadata
import json
import os
import platform
import shutil
import subprocess


EXPECTED = {
    "torch": "1.10.1+cu113",
    "torchvision": "0.11.2+cu113",
    "ray": "2.10.0",
    "mmcv-full": "1.4.0",
    "mmdet": "2.20.0",
    "mlflow-skinny": "2.17.2",
    "numpy": "1.23.5",
    "protobuf": "3.20.1",
    "jupyterlab": "3.6.8",
    "notebook": "6.5.7",
    "ipykernel": "6.29.5",
}
EXTENSIONS = (
    "mmcv._ext",
    "mmdet3d.ops.ball_query.ball_query_ext",
    "mmdet3d.ops.furthest_point_sample.furthest_point_sample_ext",
    "mmdet3d.ops.gather_points.gather_points_ext",
    "mmdet3d.ops.group_points.group_points_ext",
    "mmdet3d.ops.interpolate.interpolate_ext",
    "mmdet3d.ops.knn.knn_ext",
    "mmdet3d.ops.voxel.voxel_layer",
)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def gpu_smoke(torch):
    from mmcv.ops import nms

    require(torch.cuda.is_available(), "CUDA is not available")
    tensor = torch.arange(16, dtype=torch.float32, device="cuda")
    require(tensor.sum().item() == 120.0, "CUDA tensor reduction failed")
    boxes = torch.tensor(
        [[0, 0, 10, 10], [0, 0, 10, 10], [20, 20, 30, 30]],
        dtype=torch.float32,
        device="cuda",
    )
    scores = torch.tensor([0.9, 0.8, 0.7], device="cuda")
    _, keep = nms(boxes, scores, 0.5)
    require(keep.cpu().tolist() == [0, 2], "MMCV CUDA NMS failed")
    torch.cuda.synchronize()
    return {"device": torch.cuda.get_device_name(0), "nms_keep": [0, 2]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gpu", action="store_true", help="also execute CUDA ops")
    args = parser.parse_args()

    # Check the added capability first: running this against the training base
    # must fail with this diagnostic, rather than depending on GPU availability.
    require(shutil.which("code-server"), "code-server is missing")
    editor = subprocess.check_output(
        ["code-server", "--version"], text=True, timeout=30
    ).strip()
    require(editor.split()[0] == "4.93.1", "unexpected code-server: " + editor)
    require(os.getuid() != 0, "workspace must run as a non-root user")
    require(platform.python_version() == "3.8.10", "unexpected Python version")
    versions = {name: importlib.metadata.version(name) for name in EXPECTED}
    for name, expected in EXPECTED.items():
        require(versions[name] == expected, "{}: {} != {}".format(
            name, versions[name], expected
        ))
    for name in ("torch", "torchvision", "ray", "mmcv.ops", "mmdet", "mmdet3d", "mlflow"):
        importlib.import_module(name)
    for name in EXTENSIONS:
        importlib.import_module(name)
    jupyter = subprocess.check_output(
        ["jupyter", "lab", "--version"], text=True, timeout=30
    ).strip()
    require(jupyter == EXPECTED["jupyterlab"], "unexpected JupyterLab: " + jupyter)

    import torch

    require(torch.version.cuda == "11.3", "unexpected torch CUDA runtime")
    result = {
        "status": "PASS",
        "python": platform.python_version(),
        "versions": versions,
        "code_server": editor,
        "extensions": list(EXTENSIONS),
        "cuda_runtime": torch.version.cuda,
        "gpu": gpu_smoke(torch) if args.gpu else "not requested",
    }
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
