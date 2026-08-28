# Default training image: Ray + PyTorch with CUDA.
#
# The previous default was the bare Ray image, which has no deep learning
# framework at all — a job importing torch fails with ModuleNotFoundError. This
# is the image users get preselected when submitting a training job.
ARG RAY_BASE_IMAGE=rayproject/ray:2.35.0-py310-gpu
FROM ${RAY_BASE_IMAGE} AS pytorch-legacy

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
      mlflow-skinny==3.14.0 \
 && chown -R ray:users /home/ray

# The RayJob head is CPU-only.  This launcher places the user command on the
# GPUs reserved by its Ray workers and implements torchrun for both one-node
# and multi-node legacy DDP code.
COPY workspace/raytrain-launch.py /usr/local/bin/raytrain-launch
RUN chmod 0755 /usr/local/bin/raytrain-launch

USER ray
WORKDIR /home/ray

# Ray 2.56.1 is installed on top of the known CUDA/PyTorch platform runtime so
# the upgrade changes Ray without drifting the CUDA 12.1 and torch 2.4.1 stack.
# The version is deliberately repeated in the Dockerfile and build matrix: the
# published image digest is the immutable runtime selected by the catalog.
FROM pytorch-legacy AS pytorch-ray256-base
ARG RAY_VERSION=2.56.1
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
USER root
RUN test "${RAY_VERSION}" = "2.56.1" \
 && python3 -m pip uninstall -y \
      azure-cli-core \
      conda-content-trust \
      opentelemetry-exporter-otlp \
      opentelemetry-exporter-otlp-proto-grpc \
 && python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      "ray[default,train,tune]==${RAY_VERSION}" \
      "google-api-core==2.34.0" \
      "googleapis-common-protos==1.75.2" \
      "opentelemetry-exporter-otlp==1.44.0" \
 && python3 -c "import opentelemetry.exporter.prometheus, ray, ray.train, ray.tune; assert ray.__version__ == '${RAY_VERSION}', ray.__version__" \
 && python3 -m pip check

FROM pytorch-ray256-base AS pytorch-ray-ddp
ENV PLATFORM_TRAINING_ENGINE=ray-ddp
USER ray
WORKDIR /home/ray

FROM pytorch-ray256-base AS pytorch-ray-train
COPY workspace/raytrain_runtime /usr/local/lib/raytrain-platform/raytrain_runtime
COPY workspace/raytrain-managed /usr/local/bin/raytrain-managed
RUN chmod 0755 /usr/local/bin/raytrain-managed \
 && chmod 0755 /usr/local/bin/raytrain-launch \
 && find /usr/local/lib/raytrain-platform/raytrain_runtime -type f -name '*.py' -exec chmod 0644 {} + \
 && chown -R ray:users /home/ray
ENV PLATFORM_TRAINING_ENGINE=ray-train
ENV RAY_TRAIN_V2_ENABLED=1
USER ray
WORKDIR /home/ray

# Preserve plain `docker build` behavior for existing operators. New runtime
# variants are selected explicitly with --target by build-image.sh.
FROM pytorch-legacy AS pytorch-legacy-default
