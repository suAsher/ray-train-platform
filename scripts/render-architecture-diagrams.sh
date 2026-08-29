#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly SVG_DIR="${ROOT_DIR}/docs/architecture"

find_browser() {
  local candidate
  for candidate in chromium chromium-browser google-chrome google-chrome-stable; do
    if command -v "$candidate" >/dev/null 2>&1; then
      command -v "$candidate"
      return 0
    fi
  done
  if [[ -x "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]]; then
    printf '%s\n' "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    return 0
  fi
  return 1
}

python3 "${ROOT_DIR}/scripts/generate_architecture_diagrams.py"
browser="$(find_browser)" || {
  echo "Chromium-compatible browser is required" >&2
  exit 1
}
profile_dir="$(mktemp -d)"
trap 'rm -rf "$profile_dir"' EXIT

diagrams=(
  ray-training-platform-production-architecture-v4
  ray-training-platform-control-plane-tenancy-v1
  ray-training-platform-job-lifecycle-v1
  ray-training-platform-storage-observability-v1
)

for name in "${diagrams[@]}"; do
  output="${SVG_DIR}/${name}.png"
  rm -f "$output"
  "$browser" --headless=new --no-first-run --disable-gpu --hide-scrollbars \
    --disable-background-networking --disable-component-update --disable-sync \
    --user-data-dir="${profile_dir}/${name}" \
    --force-device-scale-factor=2 --window-size=1920,1080 \
    --screenshot="$output" "file://${SVG_DIR}/${name}.svg" &
  browser_pid=$!

  for _ in {1..80}; do
    [[ -s "$output" ]] && break
    sleep 0.25
  done
  if [[ ! -s "$output" ]]; then
    kill "$browser_pid" 2>/dev/null || true
    wait "$browser_pid" 2>/dev/null || true
    echo "failed to render ${name}" >&2
    exit 1
  fi

  sleep 0.5
  kill "$browser_pid" 2>/dev/null || true
  wait "$browser_pid" 2>/dev/null || true
done
