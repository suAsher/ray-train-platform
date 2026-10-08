#!/usr/bin/env bash
set -euo pipefail

if [[ "${OBS_N9E_ISOLATED_BUILDER:-}" != 1 ]]; then
  echo 'Run only in the approved builder isolated container.' >&2
  exit 2
fi
if [[ $# != 2 || ( "$1" != baseline && "$1" != patched ) || -e "$2" ]]; then
  echo 'Usage: test-redis.sh baseline|patched NEW_EVIDENCE_DIRECTORY' >&2
  exit 2
fi
mode="$1"
evidence="$2"
hardening_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p -- "$evidence"
evidence="$(cd -- "$evidence" && pwd)"
export GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=2
export GOPROXY="${GOPROXY:-https://mirrors.tencent.com/go/}"
export GOSUMDB=sum.golang.org GONOSUMDB= GOPRIVATE= GONOPROXY=
bash "$hardening_dir/prepare-redis.sh" "$mode" "$evidence/redis"
cp "$hardening_dir"/tests/*_test.go "$evidence/redis/"
cd -- "$evidence/redis"
go version > "$evidence/go-version.txt"
gofmt -l sentinel_tls_failover_test.go sentinel_tls_fixture_test.go > "$evidence/gofmt.txt"
test ! -s "$evidence/gofmt.txt"
set +e
go test -mod=readonly -p 2 -json -count=1 -timeout=60s -run '^TestN9E' . > "$evidence/protocol.json" 2> "$evidence/protocol.stderr"
result=$?
set -e
if [[ "$mode" == baseline ]]; then
  if [[ "$result" == 0 ]] || grep -Eq '(\[build failed\]|"Action":"build-fail")' "$evidence/protocol.json" "$evidence/protocol.stderr"; then
    echo 'Invalid RED: baseline passed or failed to compile.' >&2
    exit 1
  fi
  for path in uncached cached; do
    grep -Eq '"Action":"fail".*"Test":"TestN9ESentinelTLSHandshakeTimeoutContinues/'"$path"'"' "$evidence/protocol.json"
  done
  grep -Fq 'live caller did not reach healthy Sentinel after TLS handshake timeout' "$evidence/protocol.json"
  echo 'RED reproduced with real TLS handshakes; inspect protocol.json.'
  exit 0
fi
if [[ "$result" != 0 ]]; then
  echo 'Patched protocol regression failed; inspect protocol.json.' >&2
  exit "$result"
fi
go test -mod=readonly -p 2 -json -count=1 -timeout=60s -coverprofile="$evidence/coverage.out" -run '^TestN9E' . > "$evidence/coverage.json" 2> "$evidence/coverage.stderr"
go vet -mod=readonly . > "$evidence/vet.txt" 2>&1
go tool cover -func="$evidence/coverage.out" > "$evidence/coverage.txt"
echo 'GREEN protocol, certificate negatives and caller cancellation tests passed.'
