ARG ALPINE_RUNTIME_IMAGE=alpine:3.20
FROM ${ALPINE_RUNTIME_IMAGE}

ARG APK_MIRROR=https://repo.huaweicloud.com/repository/alpine

RUN alpine_version="$(cut -d. -f1-2 /etc/alpine-release)" \
  && printf '%s/v%s/main\n%s/v%s/community\n' "$APK_MIRROR" "$alpine_version" "$APK_MIRROR" "$alpine_version" > /etc/apk/repositories \
  && apk add --no-cache \
    aws-cli \
    ca-certificates \
    git \
    tar \
    unzip \
  && addgroup -S -g 65532 platform \
  && adduser -S -D -H -u 65532 -G platform platform \
  && mkdir -p /workspace /tmp/platform-source \
  && chown -R 65532:65532 /workspace /tmp/platform-source

USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/bin/sh"]
