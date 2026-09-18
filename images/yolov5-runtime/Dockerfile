# Code-free YOLOv5 training runtime for RayTrain.
#
# The user's YOLOv5 source is still submitted with each job. This image only
# supplies the platform launcher and Python/CUDA dependencies required by the
# official Ultralytics YOLOv5 repository on workers without internet access.
ARG YOLOV5_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/ray-train-pytorch-ray-train@sha256:5bfa41f517c911e45e9856690ca66886f0b9c793b32938baa1dc24e697dc5a1d
FROM ${YOLOV5_BASE_IMAGE}

ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

USER root
COPY requirements.txt /opt/raytrain-yolov5/requirements.txt
COPY selfcheck.py /usr/local/bin/raytrain-yolov5-selfcheck
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      fonts-dejavu-core git libgl1 libglib2.0-0 libsm6 libxext6 libxrender1 \
 && rm -rf /var/lib/apt/lists/* \
 && python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      -r /opt/raytrain-yolov5/requirements.txt \
 && install -d -o ray -g users /workspace /home/ray/.config/Ultralytics \
 && cp /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf /home/ray/.config/Ultralytics/Arial.ttf \
 && chmod 0755 /usr/local/bin/raytrain-yolov5-selfcheck \
 && raytrain-yolov5-selfcheck \
 && chown -R ray:users /home/ray /workspace

ENV YOLOv5_AUTOINSTALL=false \
    YOLOV5_CONFIG_DIR=/home/ray/.config/Ultralytics \
    MPLCONFIGDIR=/tmp/raytrain-matplotlib \
    RAY_USAGE_STATS_ENABLED=0

USER ray
WORKDIR /workspace
