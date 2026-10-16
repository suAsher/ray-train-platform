# Additional opt-in debug image. CUDA126_BASE_IMAGE must be an immutable digest
# of the separately verified, editor-free Dockerfile.base foundation.
ARG CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server@sha256:80a8e4e472be2feb0f90724b564ddf81ff38fdc482aab4fb887ef53413891f95
ARG CUDA126_BASE_IMAGE
FROM ${CODE_SERVER_IMAGE} AS code-server
FROM ${CUDA126_BASE_IMAGE}
ARG CUDA126_BASE_IMAGE
ARG PIP_INDEX_URL=https://mirrors.ivolces.com/pypi/simple/
USER root
# Capture profile/baseline precede editor installation. The profile is root-owned
# image metadata, never an environment variable from a user request.
COPY images/environment-workspace/environment_runtime.py /usr/local/lib/raytrain-environment/environment_runtime.py
COPY images/environment-workspace/raytrain-environment /usr/local/bin/raytrain-environment
RUN install -d -m 0755 /etc/raytrain \
 && printf '{"schemaVersion":1,"baseImage":"%s","basePython":"/usr/bin/python3.10","versions":{"python":"3.10","pytorch":"2.13.0+cu126","cuda":"12.6","ray":"2.58.0"}}\n' "${CUDA126_BASE_IMAGE}" > /etc/raytrain/environment-profile.json \
 && chmod 0444 /etc/raytrain/environment-profile.json \
 && chmod 0755 /usr/local/bin/raytrain-environment \
 && raytrain-environment baseline \
 && python3.10 -m venv --copies /opt/raytrain/editor \
 && /opt/raytrain/editor/bin/python -m pip install --no-cache-dir \
      --index-url "${PIP_INDEX_URL}" jupyterlab==4.5.11 jupyter_server==2.21.0 ipykernel==6.29.5 \
 && /opt/raytrain/editor/bin/python -m pip check
COPY --from=code-server /usr/lib/code-server /usr/lib/code-server
COPY --from=code-server /usr/bin/code-server /usr/bin/code-server
COPY images/environment-workspace/jupyter /opt/raytrain/environment-wrappers/jupyter
COPY images/environment-workspace/jupyter_smoke.py /usr/local/lib/raytrain-cuda126/jupyter_smoke.py
COPY images/cuda126-workspace/editor_smoke.py /usr/local/lib/raytrain-cuda126/editor_smoke.py
ENV JUPYTER_ALLOW_INSECURE_WRITES=1 \
    JUPYTER_PATH=/usr/local/share/jupyter
RUN chmod 0755 /opt/raytrain/environment-wrappers/jupyter \
 && install -d -m 0755 -o ray -g users /home/ray/.config \
      /home/ray/.local /home/ray/.local/share /home/ray/.local/share/jupyter \
      /home/ray/.local/share/code-server /home/ray/.local/share/code-server/User \
 && install -d -m 0700 -o ray -g users /home/ray/.local/share/jupyter/runtime \
 && mkdir -p /usr/local/share/jupyter/kernels/python3 /usr/local/share/jupyter/kernels/raytrain-environment \
 && printf '%s\n' '{"argv":["/opt/raytrain/environment/bin/python","-m","ipykernel_launcher","-f","{connection_file}"],"display_name":"RayTrain CUDA 12.6","language":"python"}' > /usr/local/share/jupyter/kernels/raytrain-environment/kernel.json \
 && cp /usr/local/share/jupyter/kernels/raytrain-environment/kernel.json /usr/local/share/jupyter/kernels/python3/kernel.json \
 && printf '%s\n' '{"python.defaultInterpreterPath":"/opt/raytrain/environment/bin/python"}' > /home/ray/.local/share/code-server/User/settings.json \
 && chown ray:users /home/ray/.local/share/code-server/User/settings.json
USER ray
WORKDIR /workspace
EXPOSE 8888 8443
# Offline CPU/editor gates only. GPU and save/publish have separate acceptance.
RUN raytrain-cuda126-selfcheck \
 && python /usr/local/lib/raytrain-cuda126/jupyter_smoke.py \
 && python /usr/local/lib/raytrain-cuda126/editor_smoke.py \
 && rm -f /home/ray/.local/share/jupyter/runtime/jupyter_cookie_secret
