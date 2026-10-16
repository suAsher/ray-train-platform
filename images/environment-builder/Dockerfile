# Dependency-layer builder: same Python/ABI as immutable training base.
# Runs as ray in Kubernetes, without nested runtimes or registry credentials.
ARG ENVIRONMENT_BASE_IMAGE=harbor.wellspiking.ai/guofeng.su/raytrain-base@sha256:3ec73cf863847a42bcac035309259ad9d08fd74561384cc8476020c62264d6e2
FROM ${ENVIRONMENT_BASE_IMAGE}
ARG ENVIRONMENT_BASE_IMAGE
ARG ENVIRONMENT_BASE_PYTHON=/home/ray/anaconda3/bin/python3
USER root
COPY images/environment-workspace/environment_runtime.py images/environment-workspace/raytrain-environment images/environment-workspace/ray images/environment-workspace/torchrun images/environment-workspace/environment-shell.sh /usr/local/lib/raytrain-environment/
COPY images/environment-builder/prepare.py images/environment-builder/build_layer.py /usr/local/lib/raytrain-environment/
COPY images/environment-workspace/raytrain-environment /usr/local/bin/raytrain-environment
RUN (test -x /opt/raytrain/environment/bin/python || python3 -m venv --copies --system-site-packages /opt/raytrain/environment) \
 && printf '#!/bin/sh\nexec /opt/raytrain/environment/bin/python -I /usr/local/lib/raytrain-environment/prepare.py "$@"\n' > /usr/local/bin/raytrain-environment-prepare \
 && printf '#!/bin/sh\nexec /opt/raytrain/environment/bin/python -I /usr/local/lib/raytrain-environment/build_layer.py "$@"\n' > /usr/local/bin/raytrain-environment-build \
 && chmod 0755 /usr/local/bin/raytrain-environment /usr/local/bin/raytrain-environment-prepare /usr/local/bin/raytrain-environment-build \
 && chmod 0755 /usr/local/lib/raytrain-environment/raytrain-environment /usr/local/lib/raytrain-environment/ray /usr/local/lib/raytrain-environment/torchrun
# -I omits script directory; put trusted helper imports on system site path.
RUN "${ENVIRONMENT_BASE_PYTHON}" -c "import pathlib, site; pathlib.Path(site.getsitepackages()[0], 'raytrain_environment.pth').write_text('/usr/local/lib/raytrain-environment\\n')" \
 && if [ "${ENVIRONMENT_BASE_PYTHON}" = /usr/bin/python3.10 ]; then \
      install -d -m 0755 /etc/raytrain \
      && ENVIRONMENT_BASE_IMAGE="${ENVIRONMENT_BASE_IMAGE}" python3 -c "import json,os,pathlib; pathlib.Path('/etc/raytrain/environment-profile.json').write_text(json.dumps({'schemaVersion':1,'baseImage':os.environ['ENVIRONMENT_BASE_IMAGE'],'basePython':'/usr/bin/python3.10','versions':{'python':'3.10','pytorch':'2.6.0+cu126','cuda':'12.6','ray':'2.58.0'}},separators=(',',':'))+'\n')" \
      && chmod 0444 /etc/raytrain/environment-profile.json; \
    elif [ "${ENVIRONMENT_BASE_PYTHON}" != /home/ray/anaconda3/bin/python3 ]; then exit 1; fi \
 && raytrain-environment baseline \
 && chown -R ray:users /opt/raytrain/environment
ENV PATH=/opt/raytrain/environment/bin:$PATH \
    VIRTUAL_ENV=/opt/raytrain/environment \
    PYTHONNOUSERSITE=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1
USER ray
WORKDIR /tmp
ENTRYPOINT []
CMD ["raytrain-environment-build", "--help"]
