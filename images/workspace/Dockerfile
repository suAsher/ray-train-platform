# Interactive debug workspace image.
#
# The dev RayCluster head starts `ray start` and then execs JupyterLab, so the
# image must actually contain jupyterlab — the plain Ray image does not, and the
# head crash-loops with "exec: jupyter: not found".
ARG RAY_BASE_IMAGE=rayproject/ray:2.35.0-py310
FROM ${RAY_BASE_IMAGE}

ARG PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
# code-server gives the workspace a browser-based VS Code alongside JupyterLab.
ARG CODE_SERVER_VERSION=4.93.1
# GitHub release downloads are unreliable from mainland China, so a proxy is
# used by default. Override CODE_SERVER_URL to pull from GitHub directly.
ARG CODE_SERVER_URL=https://gh-proxy.com/https://github.com/coder/code-server/releases/download

USER root
RUN pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" \
      jupyterlab==4.2.5 \
      notebook==7.2.2 \
      ipywidgets==8.1.5 \
 && chown -R ray:users /home/ray

# The Ray base image ships no curl, so install it before fetching code-server.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends curl ca-certificates; \
    rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    curl -fsSL --retry 3 -o /tmp/code-server.deb \
      "${CODE_SERVER_URL}/v${CODE_SERVER_VERSION}/code-server_${CODE_SERVER_VERSION}_${arch}.deb"; \
    dpkg -i /tmp/code-server.deb; \
    rm -f /tmp/code-server.deb

USER ray
WORKDIR /home/ray

# The platform proxies JupyterLab through the API, so it is served on a base
# path and must not open a browser or demand its own token.
ENV JUPYTER_ALLOW_INSECURE_WRITES=1
# 8888 JupyterLab, 8443 code-server (VS Code in the browser)
EXPOSE 8888 8443
