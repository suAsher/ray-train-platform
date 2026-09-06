# Supply an actual published base address (tag OR digest) from the platform catalog.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
USER root
COPY requirements.txt /tmp/raytrain-requirements.txt
RUN python3 -m pip install --no-cache-dir \
      --constraint /opt/raytrain/constraints.txt -r /tmp/raytrain-requirements.txt \
 && python3 -m pip check
USER ray
RUN raytrain-selfcheck
WORKDIR /workspace
