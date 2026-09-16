# Add browser editors to the accepted Python 3.8 BEVFusion training runtime.
# Keep the training stack intact: no pip/apt installs or dependency upgrades.
ARG CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server:4.93.1
ARG BEVFUSION_WORKSPACE_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553
FROM ${CODE_SERVER_IMAGE} AS code-server
FROM ${BEVFUSION_WORKSPACE_BASE_IMAGE}

USER root
COPY --from=code-server /usr/lib/code-server /usr/lib/code-server
COPY --from=code-server /usr/bin/code-server /usr/bin/code-server
COPY runtime-smoke.py /usr/local/bin/bevfusion-workspace-smoke
RUN chmod 0755 /usr/local/bin/bevfusion-workspace-smoke \
 && install -d -o ray -g "$(id -gn ray)" \
      /workspace /workspace/.cache /workspace/.cache/pip /workspace/.cache/pycache \
      /home/ray/.config /home/ray/.config/code-server \
      /home/ray/.local /home/ray/.local/share /home/ray/.local/share/code-server

# Preserve Python, CUDA and the NVIDIA entrypoint. The legacy base advertises
# /opt/bevfusion, but its actual source and compiled ops live below /home/westwell.
# Make these imports work from any editor terminal or notebook working directory.
ENV HOME=/home/ray \
    PYTHONPATH=/home/westwell/bevfusion \
    JUPYTER_ALLOW_INSECURE_WRITES=1 \
    PIP_CACHE_DIR=/workspace/.cache/pip \
    PYTHONPYCACHEPREFIX=/workspace/.cache/pycache \
    PIP_DISABLE_PIP_VERSION_CHECK=1
USER ray
WORKDIR /home/ray
EXPOSE 8888 8443
