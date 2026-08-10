# Multi-stage Dockerfile for Go backend
ARG GO_BUILDER_IMAGE=golang:1.24-alpine3.20
ARG ALPINE_RUNTIME_IMAGE=alpine:3.20
FROM ${GO_BUILDER_IMAGE} AS builder

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
