#!/usr/bin/env bash
set -euo pipefail

cd /opt/guofeng/welldrive-train/raytrain-perf-anchor-s1h
mkdir -p runs
RUN_ID="$(date +%Y%m%d-%H%M%S)"

spk-rayjob submit \
  --config /root/raytrain-user-perf-auth.json \
  --dir /opt/guofeng/welldrive-train/raytrain-perf-anchor-s1h \
  --name "perf-user-anchor-2x8-${RUN_ID}" \
  --image 'harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553' \
  --entrypoint 'python3 training_benchmark.py --config configs/westwell_fix_anchor/det/anchorhead/secfpn/lidar/voxelnet_0p075.yaml --train pkl/0825_pkl/mxg128/merged_nuscenes_infos_train.pkl --val pkl/0825_pkl/mxg128/merged_nuscenes_infos_val.pkl --train-limit 4096 --val-limit 512 --samples-per-gpu 8 --workers-per-gpu 8 --learning-rate 0.0001 --copy-workers 32 --timeout 14400' \
  --input-space my-storage \
  --input-path '' \
  --output-path "benchmarks/perf-user-anchor-2x8-${RUN_ID}" \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --execution-mode ray_train \
  --cache-mode off \
  --watch 2>&1 | tee "runs/08-user-data-anchor-multi-16gpu-${RUN_ID}.log"
