#!/usr/bin/env bash
set -euo pipefail

cd /opt/guofeng/welldrive-train/raytrain-perf-anchor-s1h
mkdir -p runs
RUN_ID="$(date +%Y%m%d-%H%M%S)"

spk-rayjob submit \
  --config /root/raytrain-benchmark-auth.json \
  --dir /opt/guofeng/welldrive-train/raytrain-perf-anchor-s1h \
  --name "perf-anchor-1x8-${RUN_ID}" \
  --image 'harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553' \
  --entrypoint 'python3 training_benchmark.py --config configs/westwell_fix_anchor/det/anchorhead/secfpn/lidar/voxelnet_0p075.yaml --train 0429_pkl/fz/merged_nuscenes_infos_train.pkl --val 0429_pkl/fz/merged_nuscenes_infos_val.pkl --train-limit 4096 --val-limit 512 --samples-per-gpu 8 --workers-per-gpu 8 --learning-rate 0.0001 --copy-workers 32 --timeout 14400' \
  --input-space public \
  --input-path '' \
  --output-path "benchmarks/perf-anchor-1x8-${RUN_ID}" \
  --workers 1 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --execution-mode torchrun \
  --cache-mode off \
  --watch 2>&1 | tee "runs/05-anchor-single-8gpu-${RUN_ID}.log"
