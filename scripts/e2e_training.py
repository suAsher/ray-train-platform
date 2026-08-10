#!/usr/bin/env python3
"""End-to-end acceptance for the Ray training platform.

Exercises the full user path with no external tooling beyond the standard
library, so it can run from a laptop or from inside the cluster:

    local login -> source artifact upload to object storage -> job submit
    -> Kueue admission -> RayJob run -> SUCCEEDED -> logs read back

Usage:
    API_URL=http://ray-train-backend:8080 \
    PLATFORM_USER=admin PLATFORM_PASSWORD=... \
    IMAGE=registry/ray-test@sha256:<64 hex> \
    python3 scripts/e2e_training.py
"""
from __future__ import annotations

import hashlib
import io
import json
import os
import sys
import time
import urllib.error
import urllib.request
import zipfile

API_URL = os.environ.get("API_URL", "http://ray-train-backend:8080").rstrip("/")
USERNAME = os.environ.get("PLATFORM_USER", "admin")
PASSWORD = os.environ["PLATFORM_PASSWORD"]
IMAGE = os.environ["IMAGE"]
JOB_NAME = os.environ.get("JOB_NAME", f"e2e-train-{int(time.time())}")
TIMEOUT_SECONDS = int(os.environ.get("POLL_SECONDS", "900"))

TRAIN_SOURCE = '''
"""Smoke training job: confirms Ray scheduling, GPU visibility and output write."""
import json
import os

import ray


@ray.remote(num_gpus=1)
def train_step(step):
    import torch

    device = "cuda" if torch.cuda.is_available() else "cpu"
    tensor = torch.randn(512, 512, device=device)
    loss = float((tensor @ tensor.T).mean().abs())
    return {
        "step": step,
        "loss": loss,
        "device": device,
        "gpu": torch.cuda.get_device_name(0) if device == "cuda" else "none",
    }


def main():
    ray.init(address="auto")
    print("RAY RESOURCES", json.dumps(ray.cluster_resources()), flush=True)

    results = ray.get([train_step.remote(step) for step in range(3)])
    for record in results:
        print("TRAIN", json.dumps(record), flush=True)

    output_dir = os.environ.get("PLATFORM_OUTPUT_DIR", "/tmp/platform-output")
    os.makedirs(output_dir, exist_ok=True)
    path = os.path.join(output_dir, "metrics.json")
    with open(path, "w", encoding="utf-8") as handle:
        json.dump({"results": results}, handle)
    with open(path, encoding="utf-8") as handle:
        print("READBACK", handle.read(), flush=True)

    print("TRAINING COMPLETE", flush=True)


if __name__ == "__main__":
    main()
'''


def step(message: str) -> None:
    print(f"\n==> {message}", flush=True)


def request(method: str, url: str, *, token=None, body=None, headers=None, raw=None):
    payload = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(url, data=payload, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    for key, value in (headers or {}).items():
        req.add_header(key, value)
    try:
        with urllib.request.urlopen(req, timeout=120) as response:
            content = response.read()
            return json.loads(content) if content and response.headers.get_content_type() == "application/json" else content
    except urllib.error.HTTPError as error:
        detail = error.read().decode(errors="replace")
        raise SystemExit(f"{method} {url} failed: HTTP {error.code}\n{detail}") from error


def main() -> None:
    step(f"1/6 local login as {USERNAME}")
    login = request("POST", f"{API_URL}/api/v1/auth/login", body={"username": USERNAME, "password": PASSWORD})
    token = login["data"]["token"]
    print(f"    tenant={login['data']['tenantId']} roles={login['data']['roles']} token={token[:12]}...")

    step("2/6 build source archive")
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("train.py", TRAIN_SOURCE)
    blob = buffer.getvalue()
    digest = hashlib.sha256(blob).hexdigest()
    print(f"    sha256={digest} size={len(blob)}")

    step("3/6 register artifact and upload to object storage")
    created = request("POST", f"{API_URL}/api/v1/source-artifacts", token=token,
                      body={"sha256": digest, "sizeBytes": len(blob)})["data"]
    artifact_id = created["artifactId"]
    print(f"    artifact={artifact_id} state={created['state']}")
    if created.get("uploadRequired"):
        request("PUT", created["uploadUrl"], raw=blob, headers=created.get("requiredHeaders") or {})
        print("    uploaded")
        request("POST", f"{API_URL}/api/v1/source-artifacts/{artifact_id}/complete", token=token, body={})
        print("    marked complete")

    step(f"4/6 submit training job {JOB_NAME}")
    spec = {
        "name": JOB_NAME,
        "image": IMAGE,
        "source": {"type": "artifact", "artifactId": artifact_id},
        "entrypoint": {"command": ["python", "train.py"]},
        "resources": {"workerReplicas": 1, "gpusPerWorker": 1, "cpuPerWorker": 4, "memoryPerWorker": "16Gi"},
        "queue": "",
        "timeoutSeconds": 1800,
        "cleanupPolicy": {"successTtlSeconds": 120, "failureTtlSeconds": 600},
    }
    submitted = request("POST", f"{API_URL}/api/v1/jobs", token=token, body={"spec": spec},
                        headers={"Idempotency-Key": JOB_NAME})
    job_id = submitted["data"]["id"]
    print(f"    job={job_id}")

    step("5/6 wait for a terminal state")
    deadline = time.time() + TIMEOUT_SECONDS
    last_state = None
    while time.time() < deadline:
        detail = request("GET", f"{API_URL}/api/v1/jobs/{job_id}", token=token)["data"]
        state = detail.get("observedState")
        if state != last_state:
            print(f"    [{time.strftime('%H:%M:%S')}] {state} {detail.get('statusReason') or ''}")
            last_state = state
        if state == "SUCCEEDED":
            break
        if state in {"FAILED", "CANCELED", "TIMED_OUT"}:
            print(json.dumps(detail, indent=2, ensure_ascii=False))
            raise SystemExit(f"job ended in {state}")
        time.sleep(5)
    else:
        raise SystemExit(f"job did not finish within {TIMEOUT_SECONDS}s (last={last_state})")

    step("6/6 read logs back through the platform")
    logs = request("GET", f"{API_URL}/api/v1/jobs/{job_id}/logs?limit=100", token=token)["data"]
    items = logs.get("items") or []
    print(f"    {len(items)} log lines")
    for entry in items[-15:]:
        print("   ", entry.get("line") or entry.get("message") or json.dumps(entry, ensure_ascii=False))

    print(f"\nE2E PASSED: {JOB_NAME} ({job_id})")


if __name__ == "__main__":
    main()
