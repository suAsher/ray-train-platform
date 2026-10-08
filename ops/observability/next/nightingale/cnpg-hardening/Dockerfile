# Build only on the existing builder. cnpg-upstream is an explicit local named
# context containing the checksum-verified source archive; it is not a registry.
FROM --platform=$BUILDPLATFORM harbor.wellspiking.ai/hub/library/golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS builder
ENV GOPROXY=https://mirrors.tencent.com/go/ GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY --from=cnpg-upstream cloudnative-pg.tar.gz /tmp/cloudnative-pg.tar.gz
COPY patches/0001-observability-auth.patch /tmp/observability-auth.patch
RUN printf '%s  %s\n' c4ebb2fc69572825fb67bff43fbff32cb00791f4e3bbd1ae02835cb25fbc2be0 /tmp/cloudnative-pg.tar.gz | sha256sum -c - \
    && tar -xzf /tmp/cloudnative-pg.tar.gz --strip-components=1 -C /src \
    && patch --batch --fuzz=0 -p1 < /tmp/observability-auth.patch
# Both bundled architectures must be patched: upstream bootstrap/update can
# select an architecture-specific manager from /operator.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for arch in amd64 arm64; do \
      GOARCH="$arch" go build -trimpath \
        -ldflags='-s -w -X github.com/cloudnative-pg/cloudnative-pg/pkg/versions.buildVersion=1.30.1-observability.1 -X github.com/cloudnative-pg/cloudnative-pg/pkg/versions.buildCommit=2a35abb4628f209d149825ef3c38011e0701ff2f+observability.1' \
        -o "/out/manager_$arch" ./cmd/manager || exit 1; \
    done

# Retain upstream nonroot runtime, licenses, /manager symlink and entrypoint.
# PostgreSQL is a separate, unchanged image selected by the Cluster resource.
FROM ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1@sha256:923c267ec29636db3bee20f993d0ec4973fa22998e1adad37da79e4d32b5bc07
COPY --from=builder --chown=65532:65532 --chmod=0755 /out/manager_amd64 /operator/manager_amd64
COPY --from=builder --chown=65532:65532 --chmod=0755 /out/manager_arm64 /operator/manager_arm64
USER 65532:65532
ENTRYPOINT ["/manager"]
