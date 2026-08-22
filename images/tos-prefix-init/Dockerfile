ARG GO_BUILDER_IMAGE=golang:1.25-alpine
ARG RAY_BASE_IMAGE=rayproject/ray:2.35.0-py310

FROM ${GO_BUILDER_IMAGE} AS builder
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src

COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/tos-prefix-init ./cmd/tos-prefix-init

FROM ${RAY_BASE_IMAGE}
COPY --from=builder /out/tos-prefix-init /usr/local/bin/tos-prefix-init
ENTRYPOINT ["/usr/local/bin/tos-prefix-init"]
