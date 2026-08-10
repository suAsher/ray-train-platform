#!/usr/bin/env bash
set -euo pipefail

image="${1:?usage: test-frontend-image.sh IMAGE}"
container_id="$(docker run --detach --add-host ray-train-backend:127.0.0.1 "$image")"

cleanup() {
  docker rm --force "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for _attempt in $(seq 1 12); do
  sleep 0.25
  status="$(docker inspect --format '{{.State.Status}}' "$container_id")"
  if [ "$status" = "exited" ] || [ "$status" = "dead" ]; then
    printf 'FAIL: frontend container stopped with status %s\n' "$status" >&2
    docker logs "$container_id" >&2
    exit 1
  fi
done

printf 'PASS: frontend container remained running: %s\n' "$image"
