# Default training image: Ray + PyTorch with CUDA.
#
# The previous default was the bare Ray image, which has no deep learning
# framework at all — a job importing torch fails with ModuleNotFoundError. This
# is the image users get preselected when submitting a training job.
ARG RAY_BASE_IMAGE=rayproject/ray:2.35.0-py310-gpu
FROM ${RAY_BASE_IMAGE}

ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
# cu121 matches the driver line used on the RTX 4090 nodes.
ARG TORCH_INDEX_URL=https://mirrors.aliyun.com/pytorch-wheels/cu121

USER root
RUN pip install --no-cache-dir --index-url "${TORCH_INDEX_URL}" \
      torch==2.4.1 torchvision==0.19.1 \
 || pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      torch==2.4.1 torchvision==0.19.1

# Ray Train/Tune plus the reporting libraries a training script normally wants.
RUN pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      "ray[train,tune]==2.35.0" \
      transformers==4.44.2 \
      datasets==2.21.0 \
      accelerate==0.34.2 \
      tensorboard==2.17.1 \
 && chown -R ray:users /home/ray

USER ray
WORKDIR /home/ray
