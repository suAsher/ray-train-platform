#!/usr/bin/env python3
"""Submit a GPU smoke training job through the platform API.

Proves the full path: local login -> job submit -> Kueue admission -> RayJob
-> GPU task execution -> artifact write/read-back. Uses only the Ray runtime,
so it works on any Ray image regardless of which ML framework is installed.

Usage:
    API_URL=http://172.28.0.167:31113 PLATFORM_PASSWORD=... \
    IMAGE=registry/ray-test@sha256:<64hex> python3 scripts/submit_smoke_job.py
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

API = os.environ.get("API_URL", "http://172.28.0.167:31113").rstrip("/")
USER = os.environ.get("PLATFORM_USER", "admin")
PASSWORD = os.environ["PLATFORM_PASSWORD"]
IMAGE = os.environ["IMAGE"]
WAIT_FOR_COMPLETION = os.environ.get("WAIT_FOR_COMPLETION", "false").lower() == "true"
WAIT_TIMEOUT_SECONDS = int(os.environ.get("WAIT_TIMEOUT_SECONDS", "1800"))
GIT_URL = os.environ.get("GIT_URL", "https://github.com/octocat/Hello-World")
GIT_COMMIT = os.environ.get("GIT_COMMIT", "7fd1a60b01f91b314f59955a4e4d4e80d8edf11d")

TRAIN = "\n".join([
    "import json, os, ray",
    "ray.init()",
    'print("RAY RESOURCES", json.dumps(ray.cluster_resources()), flush=True)',
    "@ray.remote(num_gpus=1)",
    "def step(i):",
    "    ids = ray.get_gpu_ids()",
    '    visible = os.environ.get("CUDA_VISIBLE_DEVICES", "")',
    "    total = 0.0",
    "    for n in range(200000):",
    "        total += n ** 0.5",
    '    return {"step": i, "gpu_ids": ids, "cuda_visible": visible, "checksum": round(total, 3)}',
    "results = ray.get([step.remote(i) for i in range(3)])",
    "for record in results:",
    '    print("TRAIN", json.dumps(record), flush=True)',
    'output = "/tmp/platform-output"',
    "os.makedirs(output, exist_ok=True)",
    'path = os.path.join(output, "metrics.json")',
    'open(path, "w").write(json.dumps({"results": results}))',
    'print("READBACK", open(path).read(), flush=True)',
    'print("TRAINING COMPLETE", flush=True)',
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
    token = call("POST", f"{API}/api/v1/auth/login", body={"username": USER, "password": PASSWORD})["data"]["token"]
    name = f"smoke-{int(time.time())}"
    spec = {
        "name": name,
        "image": IMAGE,
        "source": {"type": "git", "url": GIT_URL, "commit": GIT_COMMIT},
        "entrypoint": {"command": ["python", "-c"], "args": [TRAIN]},
        "resources": {"workerReplicas": 1, "gpusPerWorker": 1, "cpuPerWorker": 4, "memoryPerWorker": "16Gi"},
        "queue": "",
        "timeoutSeconds": 1800,
        "cleanupPolicy": {"successTtlSeconds": 300, "failureTtlSeconds": 900},
    }
    job = call("POST", f"{API}/api/v1/jobs", token=token, body={"spec": spec},
               headers={"Idempotency-Key": name})["data"]
    job_id = job["id"]
    print(job_id, flush=True)
    if not WAIT_FOR_COMPLETION:
        return

    deadline = time.monotonic() + WAIT_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        detail = call("GET", f"{API}/api/v1/jobs/{job_id}", token=token)["data"]
        state = detail.get("observedState", "UNKNOWN")
        print(f"{job_id} {state}", flush=True)
        if state == "SUCCEEDED":
            call("GET", f"{API}/api/v1/jobs/{job_id}/logs?limit=20", token=token)
            print("GPU SMOKE SUCCEEDED", flush=True)
            return
        if state in {"FAILED", "CANCELED", "TIMED_OUT"}:
            raise SystemExit(f"GPU smoke ended in {state}: {json.dumps(detail, ensure_ascii=False)}")
        time.sleep(5)
    raise SystemExit(f"GPU smoke timed out after {WAIT_TIMEOUT_SECONDS}s: {job_id}")


if __name__ == "__main__":
    main()
