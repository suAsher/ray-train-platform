ARG PYTHON_BASE_IMAGE=python:3.11-slim-bookworm
FROM ${PYTHON_BASE_IMAGE}

ARG TOSUTIL_URL=https://m645b3e1bb36e-mrap.mrap.accesspoint.tos-global.volces.com/linux/amd64/tosutil
ARG TOSUTIL_SHA256=de97013fbf179a88fcc8da5035617cbed07e1318871e9af6e076348e879d9386

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app \
    HOME=/work

WORKDIR /app

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 && curl --fail --location --retry 3 --output /usr/local/bin/tosutil "${TOSUTIL_URL}" \
 && echo "${TOSUTIL_SHA256}  /usr/local/bin/tosutil" | sha256sum --check --strict \
 && chmod 0755 /usr/local/bin/tosutil \
 && groupadd --gid 65532 raytrain \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /work --shell /usr/sbin/nologin raytrain \
 && mkdir -p /work /data/source \
 && chown 65532:65532 /work

COPY idc_sync /app/idc_sync

USER 65532:65532

ENTRYPOINT ["python3", "-m", "idc_sync.worker"]
