ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# The legacy base image still points pip at repo.westwell-research.com.  That
# hostname was retired when the internal certificate moved to wellspiking.ai.
ARG PIP_INDEX_URL=https://repo.wellspiking.ai/repository/pip/simple
# The internal Python repository currently mirrors Ray through 2.10.0.  Keep
# this explicit: using an unavailable newer version makes an image appear to
# build until its runtime layer, then fails in an opaque way.
ARG RAY_VERSION=2.10.0

WORKDIR /opt/bevfusion
COPY . /opt/bevfusion

# BEVFusion keeps CUDA/C++ operators in its source tree.  Installing the
# package in the derived image makes their ABI match the pinned Torch/CUDA
# base image; merely copying the repository is not sufficient.
RUN find mmdet3d -type f -name '*.so' -delete \
    && rm -rf build \
    && python3 -m pip install --index-url "${PIP_INDEX_URL}" --no-cache-dir yapf==0.40.1 setuptools==59.5.0 \
    && python3 setup.py develop

# A KubeRay worker invokes `ray start` inside the user-selected image.  The
# original BEVFusion runtime is a standalone training environment and does
# not contain Ray, so it cannot be used as a RayJob image without this layer.
RUN python3 -m pip install --index-url "${PIP_INDEX_URL}" --no-cache-dir "ray[default]==${RAY_VERSION}"

COPY raytrain-bevfusion-prepare-runtime.sh /usr/local/bin/raytrain-bevfusion-prepare
RUN chmod 0755 /usr/local/bin/raytrain-bevfusion-prepare
ENV PYTHONPATH=/opt/bevfusion
