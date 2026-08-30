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

# Ray 2.58 is an additive BEVFusion canary.  The unqualified Dockerfile output
# remains the legacy compatibility runtime until the canary has completed the
# real S1H acceptance matrix.
grep -Fq 'AS bevfusion-legacy' "$dockerfile"
grep -Fq 'AS bevfusion-ray258-canary' "$dockerfile"
grep -Fq 'AS bevfusion-legacy-default' "$dockerfile"
grep -Fq 'ARG RAY_CANARY_FOUNDATION_IMAGE=harbor.wellspiking.ai/guofeng.su/ray-train-pytorch-ray-train@sha256:5bfa41f517c911e45e9856690ca66886f0b9c793b32938baa1dc24e697dc5a1d' "$dockerfile"
grep -Fq 'ARG RAY_CANARY_VERSION=2.58.0' "$dockerfile"
grep -Fq 'test "${RAY_CANARY_VERSION}" = "2.58.0"' "$dockerfile"
grep -Fq "assert torch.__version__.startswith('2.4.1')" "$dockerfile"
grep -Fq 'ray[default,train,tune]==${RAY_CANARY_VERSION}' "$dockerfile"
grep -Fq 'pyarrow==25.0.1' "$dockerfile"
grep -Fq 'shapely==1.8.5' "$dockerfile"
if grep -Fq 'shapely==1.8.5.post1' "$dockerfile"; then
  echo 'post-release Shapely is outside nuscenes-devkit 1.1.10 constraint' >&2
  exit 1
fi
grep -Fq 'opencv-python==4.10.0.84' "$dockerfile"
grep -Fq 'MMCV_WITH_OPS=1 FORCE_CUDA=1 TORCH_CUDA_ARCH_LIST=8.9 MAX_JOBS=8' "$dockerfile"
grep -Fq 'CXXFLAGS="-std=c++17"' "$dockerfile"
grep -Fq 'COPY images/bevfusion-runtime/prepare-mmcv-source.py /usr/local/bin/prepare-mmcv-source' "$dockerfile"
grep -Fq '/usr/local/bin/prepare-mmcv-source /tmp/mmcv-src/mmcv-full-1.4.0' "$dockerfile"
grep -Fq 'python3 setup.py bdist_wheel' "$dockerfile"
grep -Fq 'COPY images/workspace/raytrain_runtime /usr/local/lib/raytrain-platform/raytrain_runtime' "$dockerfile"
grep -Fq 'COPY images/workspace/raytrain-managed /usr/local/bin/raytrain-managed' "$dockerfile"
grep -Fq 'COPY examples/bevfusion/patches/ray_data_s1h.py /usr/local/lib/raytrain-platform/bevfusion/ray_data_s1h.py' "$dockerfile"
grep -Fq 'ENV RAY_TRAIN_V2_ENABLED=1' "$dockerfile"
grep -Fq 'ENV PLATFORM_RAY_VERSION=2.58.0' "$dockerfile"
grep -Fq 'import mmcv, mmdet, mmdet3d, pyarrow, ray, torch' "$dockerfile"

python3 -m unittest "$root/images/bevfusion-runtime/prepare_canary_source_test.py"
python3 -m unittest "$root/images/bevfusion-runtime/prepare_mmcv_source_test.py"

echo 'BEVFusion runtime Dockerfile contract: ok'
