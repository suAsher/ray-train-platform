# Compatibility runtime for the existing BEVFusion stack. It layers only the
# platform DDP launcher onto the already dependency-pinned BEVFusion image.
ARG BEVFUSION_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/bevfusion@sha256:88e9c5045ced1b4b3dc49ddf1f2e22a8c9702574fd8103afcdff83577784a5ee
ARG RAY_CANARY_FOUNDATION_IMAGE=harbor.wellspiking.ai/guofeng.su/ray-train-pytorch-ray-train@sha256:5bfa41f517c911e45e9856690ca66886f0b9c793b32938baa1dc24e697dc5a1d

# Source-only handle for the validated S1H tree. The canary copies source from
# here, never the Python 3.8 / CUDA 11.3 site-packages.
FROM ${BEVFUSION_BASE_IMAGE} AS bevfusion-source

FROM ${BEVFUSION_BASE_IMAGE} AS bevfusion-legacy

USER root
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

# The pinned BEVFusion base image uses Python 3.8. MLflow 2.17.2 is the last
# client line that still supports Python 3.8, while remaining compatible with
# the platform's MLflow 3.x tracking API. Keep this client in the immutable
# runtime layer so user code can report metrics without installing packages at
# job startup.
RUN python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      mlflow-skinny==2.17.2 \
      protobuf==3.20.1 \
 && python3 -c 'import mlflow, onnx; print("mlflow", mlflow.__version__, "onnx", onnx.__version__)'

COPY images/workspace/raytrain-launch.py /usr/local/bin/raytrain-launch
COPY examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh /usr/local/bin/raytrain-bevfusion-prepare
RUN chmod 0755 /usr/local/bin/raytrain-launch /usr/local/bin/raytrain-bevfusion-prepare

# The legacy image keeps compiled mmdet3d CUDA extensions next to its original
# source tree.  A spk-rayjob working directory deliberately overrides the
# Python source, so the compatibility helper copies only missing .so/.py files
# from this immutable image path before starting the user command.
ENV RAYTRAIN_IMAGE_SOURCE_ROOT=/home/westwell/bevfusion/mmdet3d

# Keep the source-materializer and original BEVFusion image ownership model.
USER ray
WORKDIR /workspace

# Additive Ray 2.58 / CUDA 12.1 canary. This target is selected explicitly and
# is not part of BUILD_TARGETS=all. The final stage remains the legacy runtime.
FROM ${RAY_CANARY_FOUNDATION_IMAGE} AS bevfusion-ray258-canary
ARG RAY_CANARY_VERSION=2.58.0
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple

USER root
RUN test "${RAY_CANARY_VERSION}" = "2.58.0" \
 && test "$(python3 -c 'import sys; print(f"{sys.version_info.major}.{sys.version_info.minor}")')" = "3.10" \
 && python3 -c "import pyarrow, ray, torch; assert ray.__version__ == '2.56.1'; assert torch.__version__.startswith('2.4.1'); assert torch.version.cuda == '12.1'; assert pyarrow.__version__ == '25.0.1'" \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential git libgl1 libglib2.0-0 ninja-build \
 && rm -rf /var/lib/apt/lists/* \
 && python3 -m pip uninstall -y \
      azure-cli-core conda-content-trust \
      opentelemetry-exporter-otlp opentelemetry-exporter-otlp-proto-grpc

# The foundation is the immutable production Ray 2.56 managed image. Upgrade
# Ray only; keep its already accepted torch 2.4.1/CUDA 12.1/PyArrow stack.
RUN python3 -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      "ray[default,train,tune]==${RAY_CANARY_VERSION}" \
      pyarrow==25.0.1 numpy==1.23.5 numba==0.57.1 scipy==1.10.1 \
      mmcv-full==1.4.0 mmdet==2.20.0 nuscenes-devkit==1.1.10 \
      onnx==1.16.2 opencv-python-headless==4.10.0.84 \
      pyquaternion==0.9.9 shapely==1.8.5 spconv-cu120==2.3.6 \
      tensorboard==2.17.1 timm==0.3.4 yapf==0.40.1 \
      mlflow-skinny==3.14.0 \
      "google-api-core==2.34.0" "googleapis-common-protos==1.75.2" \
      "opentelemetry-exporter-otlp==1.44.0"

COPY --from=bevfusion-source /home/westwell/bevfusion /opt/bevfusion
COPY images/bevfusion-runtime/prepare-canary-source.py /usr/local/bin/prepare-bevfusion-canary-source
RUN chmod 0755 /usr/local/bin/prepare-bevfusion-canary-source \
 && /usr/local/bin/prepare-bevfusion-canary-source /opt/bevfusion \
 && cd /opt/bevfusion \
 && FORCE_CUDA=1 MAX_JOBS=8 python3 setup.py build_ext --inplace

COPY images/workspace/raytrain-launch.py /usr/local/bin/raytrain-launch
COPY images/workspace/raytrain-managed /usr/local/bin/raytrain-managed
COPY images/workspace/raytrain_runtime /usr/local/lib/raytrain-platform/raytrain_runtime
COPY examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh /usr/local/bin/raytrain-bevfusion-prepare
COPY examples/bevfusion/patches/ray_data_s1h.py /usr/local/lib/raytrain-platform/bevfusion/ray_data_s1h.py
RUN chmod 0755 /usr/local/bin/raytrain-launch /usr/local/bin/raytrain-managed /usr/local/bin/raytrain-bevfusion-prepare \
 && python3 -c "import pathlib, site; pathlib.Path(site.getsitepackages()[0], 'raytrain_bevfusion_canary.pth').write_text('/opt/bevfusion\\n/usr/local/lib/raytrain-platform\\n/usr/local/lib/raytrain-platform/bevfusion\\n', encoding='utf-8')" \
 && python3 -c "import mmcv, mmdet, mmdet3d, pyarrow, ray, torch; assert ray.__version__ == '${RAY_CANARY_VERSION}'; assert torch.__version__.startswith('2.4.1')" \
 && python3 -c "from mmdet3d.ops.voxel import voxel_layer; import raytrain_runtime.managed_driver, raytrain_runtime.ray_data, ray_data_s1h" \
 && python3 -m pip check \
 && chown -R ray:users /home/ray /opt/bevfusion

ENV RAY_TRAIN_V2_ENABLED=1
ENV PLATFORM_TRAINING_ENGINE=ray-train
ENV PLATFORM_RAY_VERSION=2.58.0
ENV RAYTRAIN_IMAGE_SOURCE_ROOT=/opt/bevfusion/mmdet3d
ENV PYTHONPATH=/opt/bevfusion:/usr/local/lib/raytrain-platform:/usr/local/lib/raytrain-platform/bevfusion
USER ray
WORKDIR /workspace

FROM bevfusion-legacy AS bevfusion-legacy-default
