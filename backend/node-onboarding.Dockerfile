ARG GO_BUILDER_IMAGE=swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25-alpine@sha256:a9316ea600fe38d4527999823d67764dbd5b5ce4b4a0895266faf0134aa28264
FROM ${GO_BUILDER_IMAGE} AS build
ENV PATH=/usr/local/go/bin:$PATH
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go env -w GOPROXY="${GOPROXY}" && go mod download
COPY nodeonboarding ./nodeonboarding
COPY cmd/node-onboarding ./cmd/node-onboarding
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /node-onboarding ./cmd/node-onboarding
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /node-onboarding /app/node-onboarding
USER 65532:65532
ENTRYPOINT ["/app/node-onboarding"]
