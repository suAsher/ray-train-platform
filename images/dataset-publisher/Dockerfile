ARG PYTHON_BASE_IMAGE=python:3.11-slim-bookworm
FROM ${PYTHON_BASE_IMAGE}

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

COPY requirements.txt /app/requirements.txt
RUN python3 -m pip install --no-cache-dir -r /app/requirements.txt \
 && groupadd --gid 65532 raytrain \
 && useradd --uid 65532 --gid 65532 --no-create-home \
      --home-dir /nonexistent --shell /usr/sbin/nologin raytrain \
 && mkdir -p /data/input /data/output \
 && chown 65532:65532 /data/output

COPY raytrain_publisher /app/raytrain_publisher

USER 65532:65532

ENTRYPOINT ["python3", "-m", "raytrain_publisher.pack"]
