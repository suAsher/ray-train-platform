# Interactive debug workspace image.
#
# The dev RayCluster head starts `ray start` and then execs JupyterLab, so the
# image must actually contain jupyterlab — the plain Ray image does not, and the
# head crash-loops with "exec: jupyter: not found".
# The interactive tools run on the Ray worker that requests a GPU. Use the GPU
# Ray image so CUDA libraries and nvidia-smi are present in exactly the shell,
# JupyterLab and VS Code environment the user enters.
ARG CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server:4.93.1
ARG WORKSPACE_RAY_BASE_IMAGE=harbor.wellspiking.ai/hub/rayproject/ray:2.35.0-py310-gpu
FROM ${CODE_SERVER_IMAGE} AS code-server

FROM ${WORKSPACE_RAY_BASE_IMAGE} AS workspace-legacy

ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
ARG APT_MIRROR=http://repo.wellspiking.ai/repository/ubuntu-jammy
# The debug workspace must contain the same deep-learning stack as the default
# training image. Without torch, `import torch` fails in JupyterLab and the
# GPU looks absent even though nvidia-smi sees it — which is exactly how the
# missing dependency was reported.
ARG TORCH_INDEX_URL=https://mirrors.aliyun.com/pytorch-wheels/cu121

USER root
RUN printf '%s\n' \
      "deb ${APT_MIRROR} jammy main restricted universe multiverse" \
      "deb ${APT_MIRROR} jammy-updates main restricted universe multiverse" \
      "deb ${APT_MIRROR} jammy-security main restricted universe multiverse" \
      "deb ${APT_MIRROR} jammy-backports main restricted universe multiverse" \
      > /etc/apt/sources.list \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      git curl less nano ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && pip install --index-url "${PIP_INDEX_URL}" \
      jupyterlab==4.2.5 \
      notebook==7.2.2 \
      ipywidgets==8.1.5 \
      mlflow-skinny==3.14.0 \
 && pip install --index-url "${TORCH_INDEX_URL}" \
      torch==2.4.1 torchvision==0.19.1 \
 || pip install --index-url "${PIP_INDEX_URL}" \
      torch==2.4.1 torchvision==0.19.1 \
 && printf '%s\n' \
      'default_channels:' \
      '- https://repo.wellspiking.ai/repository/conda-main' \
      '- https://repo.wellspiking.ai/repository/conda-r' \
      '- https://repo.wellspiking.ai/repository/conda-msys2' \
      'custom_multichannels:' \
      '  defaults:' \
      '  - https://repo.wellspiking.ai/repository/conda-main' \
      '  - https://repo.wellspiking.ai/repository/conda-r' \
      '  - https://repo.wellspiking.ai/repository/conda-msys2' \
      'channels:' \
      '- https://repo.wellspiking.ai/repository/conda-main' \
      '- https://repo.wellspiking.ai/repository/conda-r' \
      '- https://repo.wellspiking.ai/repository/conda-msys2' \
      '- https://repo.wellspiking.ai/repository/conda-forge' \
      'show_channel_urls: true' \
      > /home/ray/.condarc \
 && mkdir -p /workspace/.cache/pip /workspace/.cache/pycache \
 && chown -R ray:users /home/ray /workspace

# Copy a version-pinned code-server distribution from the internal Harbor
# mirror. This makes image builds independent of GitHub release downloads.
COPY --from=code-server /usr/lib/code-server /usr/lib/code-server
COPY --from=code-server /usr/bin/code-server /usr/bin/code-server
COPY raytrain-launch.py /usr/local/bin/raytrain-launch
RUN chmod 0755 /usr/local/bin/raytrain-launch

USER ray
WORKDIR /home/ray

# The platform proxies JupyterLab through the API, so it is served on a base
# path and must not open a browser or demand its own token.
ENV JUPYTER_ALLOW_INSECURE_WRITES=1
# The workspace data mount persists across debug sessions. Keep Python caches
# there so installing user-scoped dependencies in a virtual environment does
# not repeatedly fetch the same wheels after a debug pod is recreated.
ENV PIP_CACHE_DIR=/workspace/.cache/pip
ENV PYTHONPYCACHEPREFIX=/workspace/.cache/pycache
ENV PIP_DISABLE_PIP_VERSION_CHECK=1
# 8888 JupyterLab, 8443 code-server (VS Code in the browser)
EXPOSE 8888 8443

# Ray 2.56.1 interactive workspace. It inherits the same CUDA, PyTorch,
# JupyterLab, code-server, internal package mirrors, and persistent pip cache as
# the existing Ray 2.35 workspace.
FROM workspace-legacy AS workspace-ray256
ARG RAY_VERSION=2.56.1
ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
USER root
RUN python3 -m pip install --index-url "${PIP_INDEX_URL}" \
      "ray[train,tune]==${RAY_VERSION}" \
 && python3 -c "import ray, ray.train, ray.tune; assert ray.__version__ == '${RAY_VERSION}', ray.__version__" \
 && python3 -m pip check

# Task 9 supplies the real managed driver. The workspace can inspect and invoke
# both platform launch paths once that file exists; this task does not create a
# placeholder that could accidentally be promoted to production.
COPY raytrain-managed /usr/local/bin/raytrain-managed
RUN chmod 0755 /usr/local/bin/raytrain-managed \
 && chmod 0755 /usr/local/bin/raytrain-launch \
 && chown -R ray:users /home/ray /workspace

ENV PLATFORM_TRAINING_ENGINE=ray-ddp
USER ray
WORKDIR /home/ray

# Preserve the legacy default for direct builds that do not select a stage.
FROM workspace-legacy AS workspace-legacy-default
