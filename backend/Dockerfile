# Multi-stage Dockerfile for Go backend
ARG GO_BUILDER_IMAGE=swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine@sha256:a9316ea600fe38d4527999823d67764dbd5b5ce4b4a0895266faf0134aa28264
ARG ALPINE_RUNTIME_IMAGE=alpine:3.20
FROM ${GO_BUILDER_IMAGE} AS builder

# Some internal Go mirrors retain /usr/local/go but omit its PATH metadata.
# Make the toolchain location explicit so builds and verification use the same
# binary regardless of mirror image config.
ENV PATH=/usr/local/go/bin:$PATH

ARG GOPROXY=https://goproxy.cn,direct
ARG APK_MIRROR=https://repo.huaweicloud.com/repository/alpine

WORKDIR /app

RUN alpine_version="$(cut -d. -f1-2 /etc/alpine-release)" \
  && printf '%s/v%s/main\n%s/v%s/community\n' "$APK_MIRROR" "$alpine_version" "$APK_MIRROR" "$alpine_version" > /etc/apk/repositories \
  && apk add --no-cache git build-base

COPY go.mod go.sum ./
RUN go env -w GOPROXY="${GOPROXY}" && go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o main .

FROM ${ALPINE_RUNTIME_IMAGE}

ARG APK_MIRROR=https://repo.huaweicloud.com/repository/alpine

RUN alpine_version="$(cut -d. -f1-2 /etc/alpine-release)" \
  && printf '%s/v%s/main\n%s/v%s/community\n' "$APK_MIRROR" "$alpine_version" "$APK_MIRROR" "$alpine_version" > /etc/apk/repositories \
  && apk --no-cache add ca-certificates tzdata
RUN addgroup -S -g 65532 platform \
  && adduser -S -D -H -u 65532 -G platform platform \
  && mkdir -p /app \
  && chown 65532:65532 /app

WORKDIR /app

COPY --from=builder /app/main .
RUN chown 65532:65532 /app/main

USER 65532:65532

EXPOSE 8080

CMD ["./main"]
