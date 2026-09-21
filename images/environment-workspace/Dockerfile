# Additional opt-in debug environment. Never replaces the original training base.
ARG CODE_SERVER_IMAGE=harbor.wellspiking.ai/hub/codercom/code-server:4.93.1
FROM ${CODE_SERVER_IMAGE} AS code-server
FROM harbor.wellspiking.ai/guofeng.su/raytrain-base@sha256:3ec73cf863847a42bcac035309259ad9d08fd74561384cc8476020c62264d6e2
ARG PIP_INDEX_URL
USER root
# Editor server dependencies never enter the training interpreter.
RUN test -n "${PIP_INDEX_URL}" \
 && python3 -m venv --copies --system-site-packages /opt/raytrain/environment \
 && python3 -m venv --copies /opt/raytrain/editor \
 && /opt/raytrain/editor/bin/python -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" jupyterlab==4.2.5 ipykernel==6.29.5
COPY --from=code-server /usr/lib/code-server /usr/lib/code-server
COPY --from=code-server /usr/bin/code-server /usr/bin/code-server
COPY images/environment-workspace/environment_runtime.py /usr/local/lib/raytrain-environment/environment_runtime.py
COPY images/environment-workspace/raytrain-environment /usr/local/bin/raytrain-environment
COPY images/environment-workspace/runtime_smoke.py /usr/local/bin/raytrain-environment-selfcheck
COPY images/environment-workspace/ray /opt/raytrain/environment-wrappers/ray
COPY images/environment-workspace/torchrun /opt/raytrain/environment-wrappers/torchrun
COPY images/environment-workspace/jupyter /opt/raytrain/environment-wrappers/jupyter
COPY images/environment-workspace/environment-shell.sh /opt/raytrain/environment-shell.sh
COPY images/environment-workspace/environment-shell.sh /etc/profile.d/raytrain-environment.sh
ENV PATH=/opt/raytrain/environment-wrappers:/opt/raytrain/environment/bin:$PATH \
    VIRTUAL_ENV=/opt/raytrain/environment \
    PYTHONNOUSERSITE=1 \
    PIP_REQUIRE_VIRTUALENV=1 \
    PIP_INDEX_URL=${PIP_INDEX_URL} \
    PIP_CONSTRAINT=/opt/raytrain/environment-constraints.txt \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    JUPYTER_ALLOW_INSECURE_WRITES=1 \
    JUPYTER_PATH=/usr/local/share/jupyter \
    BASH_ENV=/opt/raytrain/environment-shell.sh
RUN chmod 0755 /usr/local/bin/raytrain-environment /usr/local/bin/raytrain-environment-selfcheck /opt/raytrain/environment-wrappers/ray /opt/raytrain/environment-wrappers/torchrun /opt/raytrain/environment-wrappers/jupyter \
 && raytrain-environment baseline \
 && python -m pip install --no-cache-dir --index-url "${PIP_INDEX_URL}" --constraint /opt/raytrain/environment-constraints.txt ipykernel==6.29.5 \
 && python -m pip check \
 && chown -R ray:users /opt/raytrain/environment \
 && install -d -o ray -g users /workspace /home/ray/.config /home/ray/.local/share/code-server/User \
 && mkdir -p /usr/local/share/jupyter/kernels/python3 /usr/local/share/jupyter/kernels/raytrain-environment \
 && printf '%s\n' '{"argv":["/opt/raytrain/environment/bin/python","-m","ipykernel_launcher","-f","{connection_file}"],"display_name":"RayTrain Environment","language":"python"}' > /usr/local/share/jupyter/kernels/raytrain-environment/kernel.json \
 && cp /usr/local/share/jupyter/kernels/raytrain-environment/kernel.json /usr/local/share/jupyter/kernels/python3/kernel.json \
 && printf '\n. /opt/raytrain/environment-shell.sh\n' >> /home/ray/.bashrc \
 && printf '\n. /opt/raytrain/environment-shell.sh\n' >> /home/ray/.profile \
 && printf '%s\n' '{"python.defaultInterpreterPath":"/opt/raytrain/environment/bin/python"}' > /home/ray/.local/share/code-server/User/settings.json \
 && chown ray:users /home/ray/.local/share/code-server/User/settings.json
USER ray
WORKDIR /workspace
EXPOSE 8888 8443
RUN raytrain-environment-selfcheck
