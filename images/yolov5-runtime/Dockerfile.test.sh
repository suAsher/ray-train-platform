#!/usr/bin/env bash
set -Eeuo pipefail

dockerfile="${1:-images/yolov5-runtime/Dockerfile}"
requirements="${2:-images/yolov5-runtime/requirements.txt}"

grep -Fq 'FROM ${YOLOV5_BASE_IMAGE}' "$dockerfile"
grep -Fq 'YOLOv5_AUTOINSTALL=false' "$dockerfile"
grep -Fq 'YOLOV5_CONFIG_DIR=/home/ray/.config/Ultralytics' "$dockerfile"
grep -Fq '/home/ray/.config/Ultralytics/Arial.ttf' "$dockerfile"

if grep -Eq 'git clone|ultralytics/yolov5|COPY[[:space:]].*(train.py|models/|utils/)' "$dockerfile"; then
  echo 'YOLOv5 runtime image must not bake user or upstream training source' >&2
  exit 1
fi

grep -Fq 'ultralytics==8.4.132' "$requirements"
grep -Fq 'ultralytics-thop==2.1.6' "$requirements"
grep -Fq 'opencv-python==4.10.0.84' "$requirements"
grep -Fq 'filelock==3.19.1' "$requirements"
