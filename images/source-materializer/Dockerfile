ARG ALPINE_RUNTIME_IMAGE=alpine:3.20
FROM ${ALPINE_RUNTIME_IMAGE}

ARG APK_MIRROR=https://repo.huaweicloud.com/repository/alpine

RUN alpine_version="$(cut -d. -f1-2 /etc/alpine-release)" \
  && printf '%s/v%s/main\n%s/v%s/community\n' "$APK_MIRROR" "$alpine_version" "$APK_MIRROR" "$alpine_version" > /etc/apk/repositories \
  && apk add --no-cache \
    ca-certificates \
    git \
    python3 \
  && addgroup -S -g 65532 platform \
  && adduser -S -D -H -u 65532 -G platform platform \
  && mkdir -p /workspace /tmp/platform-source \
  && chown -R 65532:65532 /workspace /tmp/platform-source

USER 65532:65532
WORKDIR /workspace
COPY images/source-materializer/platform-safe-extract.py /usr/local/bin/platform-safe-extract.py
COPY examples/dataset-cache/stage_dataset.py /usr/local/bin/platform-stage-dataset.py
ENTRYPOINT ["/bin/sh"]
