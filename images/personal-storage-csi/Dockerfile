ARG GO_BUILDER_IMAGE=swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine@sha256:a9316ea600fe38d4527999823d67764dbd5b5ce4b4a0895266faf0134aa28264
FROM ${GO_BUILDER_IMAGE} AS builder
ENV PATH=/usr/local/go/bin:$PATH
ARG GOPROXY=https://mirrors.tencent.com/go/
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN GOPROXY=${GOPROXY} go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /personal-storage-csi ./cmd/personal-storage-csi

# go-fuse uses direct kernel mounts: no SSH/sshfs subprocess, FUSE helper,
# shell, package installation or credential files are needed in the runtime.
FROM scratch
COPY --from=builder /personal-storage-csi /personal-storage-csi
USER 0:0
ENTRYPOINT ["/personal-storage-csi"]
