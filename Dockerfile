# One versioned image serves the embedded Vue UI and the authenticated Go API.
FROM node:20-alpine AS ui
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.25-alpine AS backend
RUN apk add --no-cache build-base git
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=ui /src/frontend/dist ./webui/dist
RUN CGO_ENABLED=1 go build -trimpath -o /out/raytrain .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 65532 platform \
    && adduser -S -D -H -u 65532 -G platform platform
USER 65532:65532
WORKDIR /app
COPY --from=backend /out/raytrain /app/raytrain
EXPOSE 8080
ENTRYPOINT ["/app/raytrain"]
