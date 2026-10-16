# Additional opt-in debug image. CUDA126_BASE_IMAGE must be an immutable digest
# of the separately verified, editor-free Dockerfile.base foundation.
ARG CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server@sha256:80a8e4e472be2feb0f90724b564ddf81ff38fdc482aab4fb887ef53413891f95
ARG CUDA126_BASE_IMAGE
FROM ${CODE_SERVER_IMAGE} AS code-server
FROM scratch AS editor-wheels
ADD --checksum=sha256:e23e3d76b92ad172b529fb6faf91e3edd264ffdff65b543949eb85daf7f8b113 https://files.pythonhosted.org/packages/0f/f2/a2f50a928fed0b8529dd80c4eb7d3aebe92f9658bfb3b63dce77fb5f1b95/jupyterlab-4.5.11-py3-none-any.whl /wheels/jupyterlab-4.5.11-py3-none-any.whl
ADD --checksum=sha256:2ae2e5ce5e97268553e25aebe040197455673d925b5fc7995477153318160cf7 https://files.pythonhosted.org/packages/cb/8a/cb97c2b353cb46cced413be8f742fa8fcd3cb1659524f27314494aa94968/jupyter_server-2.21.0-py3-none-any.whl /wheels/jupyter_server-2.21.0-py3-none-any.whl
ADD --checksum=sha256:4b6b6a54fff2e69a3dde9d21cc6301220bee3c3cb792187d11403fd795031081 https://files.pythonhosted.org/packages/58/9d/40b6267367182187139a4000b82a3b287d84d745bccd808e75d916920e9d/bleach-6.4.0-py3-none-any.whl /wheels/bleach-6.4.0-py3-none-any.whl
ADD --checksum=sha256:99de1585e42dcbd826faa9e11a202727a5e202e4e4722a4c69ac1ff615793dd7 https://files.pythonhosted.org/packages/89/70/b1e4737b84163db5bb1dfde6f216dbfbf32783330a9989c965e121172830/mistune-3.3.3-py3-none-any.whl /wheels/mistune-3.3.3-py3-none-any.whl
ADD --checksum=sha256:aa85c087b435e7bf1ffd03319f658e285f2b89eccab33bc1ba7025495ab3e7c8 https://files.pythonhosted.org/packages/67/f8/bb0a9d5f46819c821dc1f004aa2cc29b1d91453297dbf5ff20470f00f193/nbconvert-7.17.1-py3-none-any.whl /wheels/nbconvert-7.17.1-py3-none-any.whl
ADD --checksum=sha256:a2b2c76d67df2382d245409fd71e321a571717e58463efa32ace87dcadac2c12 https://files.pythonhosted.org/packages/7b/d6/3185ab5ad1280319b31986898f3206dd7227cd75e293d4dba2a5e6bf27a0/soupsieve-2.9-py3-none-any.whl /wheels/soupsieve-2.9-py3-none-any.whl
FROM ${CUDA126_BASE_IMAGE}
ARG CUDA126_BASE_IMAGE
ARG PIP_INDEX_URL=https://mirrors.ivolces.com/pypi/simple/
USER root
# Capture profile/baseline precede editor installation. The profile is root-owned
# image metadata, never an environment variable from a user request.
COPY images/environment-workspace/environment_runtime.py /usr/local/lib/raytrain-environment/environment_runtime.py
COPY images/environment-workspace/raytrain-environment /usr/local/bin/raytrain-environment
COPY images/cuda126-workspace/editor-requirements-official.txt /opt/raytrain/editor-requirements-official.txt
RUN --mount=from=editor-wheels,source=/wheels,target=/wheels,readonly \
    install -d -m 0755 /etc/raytrain \
 && printf '{"schemaVersion":1,"baseImage":"%s","basePython":"/usr/bin/python3.10","versions":{"python":"3.10","pytorch":"2.13.0+cu126","cuda":"12.6","ray":"2.58.0"}}\n' "${CUDA126_BASE_IMAGE}" > /etc/raytrain/environment-profile.json \
 && chmod 0444 /etc/raytrain/environment-profile.json \
 && chmod 0755 /usr/local/bin/raytrain-environment \
 && raytrain-environment baseline \
 && python3.10 -m venv --copies --system-site-packages --without-pip /opt/raytrain/editor \
 && /opt/raytrain/editor/bin/python -m pip install --no-cache-dir \
      --no-index --find-links /wheels --no-deps --require-hashes \
      -r /opt/raytrain/editor-requirements-official.txt \
 && /opt/raytrain/editor/bin/python -m pip install --no-cache-dir \
      --index-url "${PIP_INDEX_URL}" --constraint /opt/raytrain/environment-constraints.txt jupyterlab==4.5.11 jupyter_server==2.21.0 ipykernel==6.29.5 \
 && /opt/raytrain/editor/bin/python -m pip check \
 && printf '#!/bin/sh\nexec /opt/raytrain/editor/bin/python -m jupyter "$@"\n' > /opt/raytrain/editor/bin/jupyter \
 && chmod 0755 /opt/raytrain/editor/bin/jupyter
COPY --from=code-server /usr/lib/code-server /usr/lib/code-server
COPY --from=code-server /usr/bin/code-server /usr/bin/code-server
COPY images/environment-workspace/jupyter /opt/raytrain/environment-wrappers/jupyter
COPY images/environment-workspace/jupyter_smoke.py /usr/local/lib/raytrain-cuda126/jupyter_smoke.py
COPY images/cuda126-workspace/editor_smoke.py /usr/local/lib/raytrain-cuda126/editor_smoke.py
ENV JUPYTER_ALLOW_INSECURE_WRITES=1 \
    JUPYTER_PATH=/opt/raytrain/jupyter:/usr/local/share/jupyter
RUN chmod 0755 /opt/raytrain/environment-wrappers/jupyter \
 && install -d -m 0755 -o ray -g users /home/ray/.config \
      /home/ray/.local /home/ray/.local/share /home/ray/.local/share/jupyter \
      /home/ray/.local/share/code-server /home/ray/.local/share/code-server/User \
 && install -d -m 0700 -o ray -g users /home/ray/.local/share/jupyter/runtime \
 && mkdir -p /opt/raytrain/jupyter/kernels/python3 /opt/raytrain/jupyter/kernels/raytrain-environment \
 && printf '%s\n' '{"argv":["/opt/raytrain/environment/bin/python","-m","ipykernel_launcher","-f","{connection_file}"],"display_name":"RayTrain CUDA 12.6","language":"python"}' > /opt/raytrain/jupyter/kernels/raytrain-environment/kernel.json \
 && cp /opt/raytrain/jupyter/kernels/raytrain-environment/kernel.json /opt/raytrain/jupyter/kernels/python3/kernel.json \
 && printf '%s\n' '{"python.defaultInterpreterPath":"/opt/raytrain/environment/bin/python"}' > /home/ray/.local/share/code-server/User/settings.json \
 && chown ray:users /home/ray/.local/share/code-server/User/settings.json
USER ray
WORKDIR /workspace
EXPOSE 8888 8443
# Offline CPU/editor gates only. GPU and save/publish have separate acceptance.
RUN raytrain-cuda126-selfcheck \
 && python /usr/local/lib/raytrain-cuda126/jupyter_smoke.py \
 && python /usr/local/lib/raytrain-cuda126/editor_smoke.py \
 && python -c "import json, subprocess; captured = json.loads(subprocess.check_output(['raytrain-environment', 'capture'], text=True)); assert not captured['packages']; print(json.dumps({'zeroDeltaCapture': 'passed'}))" \
 && rm -f /home/ray/.local/share/jupyter/runtime/jupyter_cookie_secret
