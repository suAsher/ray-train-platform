#!/usr/bin/env python3
"""Submit a PyTorch GPU job to validate a training image.

Unlike submit_smoke_job.py, which uses only the Ray runtime, this one imports
torch and runs a real CUDA matmul — it is what proves a registered training
image can actually train, rather than merely schedule.

    API_URL=http://<portal> PLATFORM_PASSWORD=... \
    IMAGE=<registry>/<repo>@sha256:<digest> python3 scripts/submit_torch_job.py
"""
import json
import os
import time
import urllib.error
import urllib.request

API = os.environ.get("API_URL", "http://172.28.0.167:31113").rstrip("/")
USER = os.environ.get("PLATFORM_USER", "admin")
PASSWORD = os.environ["PLATFORM_PASSWORD"]
IMAGE = os.environ["IMAGE"]
GIT_URL = os.environ.get("GIT_URL", "https://github.com/octocat/Hello-World")
GIT_COMMIT = os.environ.get("GIT_COMMIT", "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d")

TRAIN = "\n".join([
    "import json, os, ray, torch",
    "ray.init()",
    'print("RAY RESOURCES", json.dumps(ray.cluster_resources()), flush=True)',
    'print("DRIVER TORCH", torch.__version__, flush=True)',
    "@ray.remote(num_gpus=1)",
    "def step(i):",
    "    import torch",
    '    device = "cuda" if torch.cuda.is_available() else "cpu"',
    "    tensor = torch.randn(1024, 1024, device=device)",
    "    loss = float((tensor @ tensor.T).mean().abs())",
    "    return {",
    '        "step": i, "device": device, "torch": torch.__version__,',
    '        "cuda": torch.version.cuda,',
    '        "gpu": torch.cuda.get_device_name(0) if device == "cuda" else "none",',
    '        "loss": round(loss, 4),',
    "    }",
    "results = ray.get([step.remote(i) for i in range(3)])",
    "for record in results:",
    '    print("TRAIN", json.dumps(record), flush=True)',
    'assert all(r["device"] == "cuda" for r in results), "expected CUDA execution"',
    'print("TORCH TRAINING COMPLETE", flush=True)',
])


def call(method, url, token=None, body=None, headers=None):
    payload = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(url, data=payload, method=method)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    if token:
        request.add_header("Authorization", f"Bearer {token}")
    for key, value in (headers or {}).items():
        request.add_header(key, value)
    try:
        with urllib.request.urlopen(request, timeout=90) as response:
            return json.loads(response.read())
    except urllib.error.HTTPError as error:
        raise SystemExit(f"{method} {url} -> HTTP {error.code}\n{error.read().decode()}") from error


def main():
    token = call("POST", f"{API}/api/v1/auth/login",
                 body={"username": USER, "password": PASSWORD})["data"]["token"]
    name = f"torch-{int(time.time())}"
    spec = {
        "name": name,
        "image": IMAGE,
        "source": {"type": "git", "url": GIT_URL, "commit": GIT_COMMIT},
        "entrypoint": {"command": ["python", "-c"], "args": [TRAIN]},
        "resources": {"workerReplicas": 1, "gpusPerWorker": 1, "cpuPerWorker": 4, "memoryPerWorker": "16Gi"},
        "queue": "",
        "timeoutSeconds": 3600,
        "cleanupPolicy": {"successTtlSeconds": 300, "failureTtlSeconds": 900},
    }
    job = call("POST", f"{API}/api/v1/jobs", token=token, body={"spec": spec},
               headers={"Idempotency-Key": name})["data"]
    print(job["id"])


if __name__ == "__main__":
    main()
