#!/usr/bin/env bash
set -euo pipefail

cd /opt/guofeng/welldrive-train/raytrain-perf-bevfusion
mkdir -p runs
RUN_ID="$(date +%Y%m%d-%H%M%S)"

spk-rayjob submit \
  --config /root/raytrain-benchmark-auth.json \
  --dir /opt/guofeng/welldrive-train/raytrain-perf-bevfusion \
  --name "perf-bev-2x8-dual-${RUN_ID}" \
  --image 'harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553' \
  --entrypoint 'python3 training_benchmark.py --config configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml --train 0429_pkl/fz/merged_nuscenes_infos_train.pkl --val 0429_pkl/fz/merged_nuscenes_infos_val.pkl --train-limit 4096 --val-limit 512 --stage-cache --copy-workers 32 --timeout 14400' \
  --input-space public \
  --input-path '' \
  --output-path "benchmarks/perf-bev-2x8-dual-${RUN_ID}" \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 128 \
  --memory-per-worker 512Gi \
  --execution-mode ray_train \
  --cache-mode runtime \
  --cache-size 5Ti \
  --watch 2>&1 | tee "runs/04-multi-16gpu-dual-nvme-${RUN_ID}.log"
