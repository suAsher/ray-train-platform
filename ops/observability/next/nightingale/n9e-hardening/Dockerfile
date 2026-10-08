# Root supplies checksum-verified local contexts. Never fetch floating FE/assets.
FROM --platform=$BUILDPLATFORM harbor.wellspiking.ai/hub/library/golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS builder
ENV GOPROXY=https://mirrors.tencent.com/go/ GOTOOLCHAIN=local GOWORK=off GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= GONOPROXY= CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOMAXPROCS=2 OBS_N9E_ISOLATED_BUILDER=1
WORKDIR /build
COPY --from=n9e-upstream nightingale.tar.gz /build/nightingale.tar.gz
COPY --from=n9e-assets . /assets
COPY . /hardening
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    bash /hardening/prepare-source.sh /build/nightingale.tar.gz /assets /src
WORKDIR /src
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -mod=readonly -p 2 -trimpath -tags=sandbox_embed,qa_code_embed \
      -ldflags='-s -w -X github.com/ccfos/nightingale/v6/pkg/version.Version=v9.1.1-observability.1-4e056150142b2e1ed6966c9317a9e08442c399f7' \
      -o /out/n9e ./cmd/center \
    && go version -m /out/n9e > /out/build-info.txt

# Preserve the official runtime, Python, fonts, configuration and entrypoint.
# Only the center binary changes; its original embedded resources are retained.
FROM harbor.wellspiking.ai/hub/flashcatcloud/nightingale:9.1.1@sha256:81b9aa562549b5ee9d36283a11a14bf257e62fa71eb181277ae4841f7c80e2f9
LABEL org.opencontainers.image.source="https://github.com/ccfos/nightingale" \
      org.opencontainers.image.revision="4e056150142b2e1ed6966c9317a9e08442c399f7" \
      io.raytrain.observability.patch="n9e-sentinel-caller-context-v1"
COPY --from=builder --chown=0:0 --chmod=0755 /out/n9e /app/n9e
