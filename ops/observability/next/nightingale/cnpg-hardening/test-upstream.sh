#!/usr/bin/env bash
set -euo pipefail

if [[ "${OBS_CNPG_ISOLATED_BUILDER:-}" != 1 ]]; then
  echo 'Run only in an isolated container on the approved builder.' >&2
  exit 2
fi
if [[ $# != 3 || ( "$1" != baseline && "$1" != patched ) ]]; then
  echo 'Usage: test-upstream.sh baseline|patched ARCHIVE NEW_EVIDENCE_DIRECTORY' >&2
  exit 2
fi
mode="$1"
archive="$2"
evidence="$3"
if [[ -e "$evidence" ]]; then
  echo 'Evidence destination must not exist.' >&2
  exit 2
fi
hardening_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p -- "$evidence"
evidence="$(cd -- "$evidence" && pwd)"
source_dir="$evidence/source"
bash "$hardening_dir/prepare-source.sh" "$mode" "$archive" "$source_dir"

# The real constructor calls InClusterConfig. Use only a clearly fake token,
# never a mounted real service-account or user kubeconfig.
token=/var/run/secrets/kubernetes.io/serviceaccount/token
if [[ -e "$token" ]]; then
  echo 'Refusing an existing service-account token; use a fresh container.' >&2
  exit 2
fi
mkdir -p -- "$(dirname -- "$token")"
printf '%s' observability-protocol-fixture-only > "$token"
trap 'rm -f -- "$token"' EXIT
unset KUBECONFIG
export KUBERNETES_SERVICE_HOST=127.0.0.1 KUBERNETES_SERVICE_PORT=1
export GOTOOLCHAIN=local GOPROXY="${GOPROXY:-https://mirrors.tencent.com/go/}"

mkdir -p "$source_dir/internal/observabilitytest" "$source_dir/pkg/observabilityauth"
cp "$hardening_dir/tests/sharedfixture/fixture.go" "$source_dir/internal/observabilitytest/fixture.go"
cp "$hardening_dir/tests/fixtures/webserver_access.go" "$source_dir/pkg/management/postgres/webserver/observability_fixture_access.go"
cp "$hardening_dir/tests/metrics_tls_test.go" "$source_dir/pkg/management/postgres/webserver/metricserver/observability_metrics_tls_test.go"
cp "$hardening_dir/tests/status_auth_test.go" "$source_dir/pkg/management/postgres/webserver/observability_status_auth_test.go"
cp "$hardening_dir/tests/status_retry_test.go" "$source_dir/pkg/management/postgres/webserver/client/remote/observability_status_retry_test.go"
cp "$hardening_dir/tests/webhook_source_test.go" "$source_dir/pkg/observabilityauth/observability_webhook_source_test.go"
if [[ "$mode" == baseline ]]; then
  cp "$hardening_dir/tests/baseline/webhook_identity_adapter.go" "$source_dir/pkg/observabilityauth/observability_baseline_adapter.go"
fi

cd -- "$source_dir"
go version > "$evidence/go-version.txt"
packages=(./pkg/observabilityauth ./pkg/management/postgres/webserver ./pkg/management/postgres/webserver/metricserver ./pkg/management/postgres/webserver/client/remote)
set +e
go test -json -count=1 -timeout=20m -run '^TestObservability' "${packages[@]}" > "$evidence/protocol.json" 2> "$evidence/protocol.stderr"
protocol_result=$?
set -e
if [[ "$mode" == baseline ]]; then
  if [[ "$protocol_result" == 0 ]]; then
    echo 'Baseline unexpectedly passed; the security regressions did not reproduce.' >&2
    exit 1
  fi
  if grep -Eq '(\[build failed\]|"Action":"build-fail")' "$evidence/protocol.json" "$evidence/protocol.stderr"; then
    echo 'A package did not compile; this run is not valid RED evidence.' >&2
    exit 1
  fi
  # Compilation/environment errors are not RED. Require actual failing test
  # assertions for each protected endpoint, not merely a failed go invocation.
  for test_name in TestObservabilityMetricsTLSProtocol TestObservabilityStatusAuthenticationProtocol TestObservabilityWebhookSourceProtocol; do
    if ! grep -E '"Action":"fail".*"Test":"'"$test_name"'"' "$evidence/protocol.json" >/dev/null; then
      echo "No assertion-level RED evidence for $test_name" >&2
      exit 1
    fi
  done
  echo 'RED reproduced for 8000, 9187 and 9443; inspect protocol.json.'
  exit 0
fi
if [[ "$protocol_result" != 0 ]]; then
  echo 'Protocol regression failed; inspect protocol.json and protocol.stderr.' >&2
  exit "$protocol_result"
fi
coverage_packages="$(IFS=,; echo "${packages[*]}")"
go test -json -count=1 -timeout=20m -coverpkg="$coverage_packages" -coverprofile="$evidence/coverage.out" "${packages[@]}" ./internal/cmd/manager/controller > "$evidence/regression.json" 2> "$evidence/regression.stderr"
go vet "${packages[@]}" ./internal/cmd/manager/controller > "$evidence/vet.txt" 2>&1
go tool cover -func="$evidence/coverage.out" > "$evidence/coverage.txt"
awk '$1 ~ /\/pkg\/observabilityauth\// { statements[$1]=$2; if ($3>0) hit[$1]=1 }
  END { for (block in statements) { total+=statements[block]; if (hit[block]) covered+=statements[block] }
    if (total==0) exit 1;
    coverage=100*covered/total; printf "New authentication helper statement coverage: %.2f%%\n", coverage;
    if (coverage<80) exit 1 }' "$evidence/coverage.out" > "$evidence/auth-coverage.txt"
echo 'GREEN protocol and upstream affected-package regressions passed; inspect evidence files.'
