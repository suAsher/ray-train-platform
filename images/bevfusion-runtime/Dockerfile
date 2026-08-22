# Compatibility runtime for the existing BEVFusion stack. It layers only the
# platform DDP launcher onto the already dependency-pinned BEVFusion image.
ARG BEVFUSION_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/bevfusion@sha256:88e9c5045ced1b4b3dc49ddf1f2e22a8c9702574fd8103afcdff83577784a5ee
FROM ${BEVFUSION_BASE_IMAGE}

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
