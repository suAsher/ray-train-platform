#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dockerfile="$root/images/bevfusion-runtime/Dockerfile"

grep -Fq 'RAYTRAIN_IMAGE_SOURCE_ROOT=/home/westwell/bevfusion/mmdet3d' "$dockerfile"
grep -Fq 'COPY images/workspace/raytrain-launch.py /usr/local/bin/raytrain-launch' "$dockerfile"
grep -Fq 'COPY examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh /usr/local/bin/raytrain-bevfusion-prepare' "$dockerfile"
grep -Fq 'mlflow-skinny==2.17.2' "$dockerfile"
grep -Fq 'protobuf==3.20.1' "$dockerfile"
grep -Fq 'import mlflow, onnx' "$dockerfile"
grep -Fq 'USER ray' "$dockerfile"

echo 'BEVFusion runtime Dockerfile contract: ok'
