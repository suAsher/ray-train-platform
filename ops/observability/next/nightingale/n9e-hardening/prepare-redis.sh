#!/usr/bin/env bash
set -euo pipefail

if [[ "${OBS_N9E_ISOLATED_BUILDER:-}" != 1 ]]; then
  echo 'Run only in the approved builder isolated container.' >&2
  exit 2
fi
if [[ $# != 2 || ( "$1" != baseline && "$1" != patched ) || -e "$2" ]]; then
  echo 'Usage: prepare-redis.sh baseline|patched NEW_MODULE_DIRECTORY' >&2
  exit 2
fi
mode="$1"
destination="$2"
hardening_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
export GOTOOLCHAIN=local GOWORK=off
export GOPROXY="${GOPROXY:-https://mirrors.tencent.com/go/}"
export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= GONOPROXY=
metadata="$(mktemp)"
trap 'rm -f -- "$metadata"' EXIT
go mod download -json github.com/redis/go-redis/v9@v9.0.2 > "$metadata"
grep -Fq '"Sum": "h1:BA426Zqe/7r56kCcvxYLWe1mkaz71LKF77GwgFzSxfE="' "$metadata"
grep -Fq '"GoModSum": "h1:/xDTe9EF1LM61hek62Poq2nzQSGj0xSrEtEHbBQevps="' "$metadata"
module_cache="$(go env GOMODCACHE)/github.com/redis/go-redis/v9@v9.0.2"
test -f "$module_cache/sentinel.go"
mkdir -p -- "$(dirname -- "$destination")"
cp -R -- "$module_cache" "$destination"
chmod -R u+w -- "$destination"
if [[ "$mode" == patched ]]; then
  git -C "$destination" apply --check "$hardening_dir/patches/0001-sentinel-masteraddr-caller-context.patch"
  git -C "$destination" apply "$hardening_dir/patches/0001-sentinel-masteraddr-caller-context.patch"
fi
