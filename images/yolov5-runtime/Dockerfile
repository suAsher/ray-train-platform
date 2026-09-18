# Dependencies only. YOLOv5 source, datasets and weights arrive with each job.
FROM harbor.wellspiking.ai/guofeng.su/raytrain-base@sha256:3ec73cf863847a42bcac035309259ad9d08fd74561384cc8476020c62264d6e2
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
USER root
RUN sed -i 's@http://archive.ubuntu.com/ubuntu@https://mirrors.huaweicloud.com/ubuntu@g; s@http://security.ubuntu.com/ubuntu@https://mirrors.huaweicloud.com/ubuntu@g' /etc/apt/sources.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends libgl1 libglib2.0-0 fonts-dejavu-core fonts-noto-cjk \
 && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /opt/raytrain/yolov5-requirements.txt
RUN python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      --constraint /opt/raytrain/constraints.txt --requirement /opt/raytrain/yolov5-requirements.txt \
 && python3 -m pip check
# Upstream asks for these filenames. Use packaged open fonts instead of fetching
# proprietary Arial fonts when a training node has no internet connection.
RUN install -d -o ray -g users /home/ray/.config/Ultralytics \
 && ln -s /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf /home/ray/.config/Ultralytics/Arial.ttf \
 && ln -s /usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc /home/ray/.config/Ultralytics/Arial.Unicode.ttf
ENV YOLO_CONFIG_DIR=/home/ray/.config/Ultralytics \
    YOLOV5_CONFIG_DIR=/home/ray/.config/Ultralytics \
    YOLO_AUTOINSTALL=false \
    YOLOv5_AUTOINSTALL=false \
    YOLO_OFFLINE=true \
    MPLBACKEND=Agg
USER ray
WORKDIR /workspace
RUN raytrain-selfcheck \
 && python3 -c "import cv2, matplotlib, seaborn, ultralytics, torch, torchvision; from PIL import ImageFont; ImageFont.truetype('/home/ray/.config/Ultralytics/Arial.Unicode.ttf', 12); assert torch.__version__.split('+')[0] == '2.4.1'; assert torchvision.__version__.split('+')[0] == '0.19.1'; assert torch.version.cuda == '12.1'"
