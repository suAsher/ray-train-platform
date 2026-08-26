#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly chart_dir="${root_dir}/helm/ray-train-platform"
readonly test_profile="${root_dir}/deploy/profiles/test.yaml"
readonly production_profile="${root_dir}/deploy/profiles/production.yaml.example"
readonly cpu_ha_profile="${root_dir}/deploy/profiles/vke-cpu-ha.yaml"

for command in helm grep mktemp; do
  command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

test_rendered="$(mktemp)"
production_rendered="$(mktemp)"
cpu_ha_rendered="$(mktemp)"
network_rendered="$(mktemp)"
trap 'rm -f "$test_rendered" "$production_rendered" "$cpu_ha_rendered" "$network_rendered"' EXIT

helm lint "$chart_dir" --values "$test_profile" >/dev/null
helm template ray-platform "$chart_dir" --namespace ray-train-platform --values "$test_profile" >"$test_rendered"
helm template ray-platform "$chart_dir" --namespace ray-train-platform --values "$production_profile" >"$production_rendered"
helm template ray-platform "$chart_dir" --namespace ray-train-platform --values "$cpu_ha_profile" >"$cpu_ha_rendered"
helm template ray-platform "$chart_dir" --namespace ray-train-platform --values "$cpu_ha_profile" --set albInstance.enabled=true >"$network_rendered"

require() {
  local file="$1"
  local expected="$2"
  grep -Fq -- "$expected" "$file" || { echo "render contract missing: ${expected}" >&2; exit 1; }
}

require "$test_rendered" 'kind: StatefulSet'
require "$test_rendered" 'name: postgres'
require "$test_rendered" 'name: PGDATA'
require "$test_rendered" 'value: "/var/lib/postgresql/data/pgdata"'
require "$production_rendered" 'kind: HorizontalPodAutoscaler'
require "$production_rendered" 'minReplicas: 2'
require "$production_rendered" 'type: NodePort'
require "$production_rendered" 'app.kubernetes.io/part-of: ray-train-platform'
require "$production_rendered" 'app.kubernetes.io/managed-by: Helm'
require "$cpu_ha_rendered" 'replicas: 2'
require "$cpu_ha_rendered" 'whenUnsatisfiable: DoNotSchedule'
require "$cpu_ha_rendered" 'requiredDuringSchedulingIgnoredDuringExecution:'
require "$cpu_ha_rendered" 'podAntiAffinity:'
# The 3-node CPU HA profile preserves one available replica while avoiding an
# unschedulable surge Pod on a fully packed control-plane pool.
require "$cpu_ha_rendered" 'maxUnavailable: 1'
require "$cpu_ha_rendered" 'maxSurge: 0'
require "$cpu_ha_rendered" 'kind: HorizontalPodAutoscaler'
require "$cpu_ha_rendered" 'name: postgres'
require "$cpu_ha_rendered" 'name: harbor-registry'
# The CPU HA profile must always use immutable artifacts, but their exact
# digest changes on every approved release. Assert repository and digest
# contract instead of coupling this reusable delivery check to one build.
require "$cpu_ha_rendered" 'harbor.wellspiking.ai/guofeng.su/ray-train-backend@sha256:'
require "$cpu_ha_rendered" 'harbor.wellspiking.ai/guofeng.su/ray-train-frontend@sha256:'
require "$cpu_ha_rendered" 'harbor.wellspiking.ai/guofeng.su/postgres@sha256:'
require "$cpu_ha_rendered" 'harbor.wellspiking.ai/guofeng.su/ray-workspace@sha256:'
require "$cpu_ha_rendered" 'harbor.wellspiking.ai/guofeng.su/ray-source-materializer@sha256:'
require "$cpu_ha_rendered" 'name: LOCAL_CACHE_STORAGE_CLASS_DATA1'
require "$cpu_ha_rendered" 'value: "ray-cache-local-data1"'
require "$cpu_ha_rendered" 'name: LOCAL_CACHE_STORAGE_CLASS_DATA2'
require "$cpu_ha_rendered" 'value: "ray-cache-local-data2"'
require "$cpu_ha_rendered" 'name: LOCAL_CACHE_MOUNT_PATH_DATA1'
require "$cpu_ha_rendered" 'value: "/mnt/cache"'
require "$cpu_ha_rendered" 'name: LOCAL_CACHE_MOUNT_PATH_DATA2'
require "$cpu_ha_rendered" 'value: "/mnt/cache2"'
require "$cpu_ha_rendered" 'value: "200Gi,500Gi,1Ti,2Ti,4Ti,5Ti"'
require "$cpu_ha_rendered" 'value: "5Ti"'
require "$cpu_ha_rendered" 'name: spk-rayjob-release'
require "$cpu_ha_rendered" 'ingressClassName: raytrain-prod-alb'
require "$network_rendered" 'kind: ALBInstance'
require "$network_rendered" 'name: raytrain-prod-alb'
require "$network_rendered" 'instanceID: "alb-1vzdtqufb4hz43766qah8okd5"'
require "$network_rendered" 'certificateSource: "cert_center"'
require "$network_rendered" 'certificateID: "cert-8fbe7f999bc3478483e8c8838c73b2b0"'
if ! awk '
  $0 == "kind: Service" { in_service = 1; is_release = 0 }
  in_service && $0 == "  name: spk-rayjob-release" { is_release = 1 }
  in_service && is_release && $0 == "  type: ClusterIP" { found = 1 }
  END { exit found ? 0 : 1 }
' "$cpu_ha_rendered"; then
  echo 'spk-rayjob release must render as ClusterIP behind the private ALB' >&2
  exit 1
fi
require "$cpu_ha_rendered" 'raytrain.wellspiking.ai'
require "$cpu_ha_rendered" 'path: /downloads/spk-rayjob/'
if awk '
  $0 == "  name: ray-platform-rayctl-https-ingress" { in_ingress = 1 }
  in_ingress && $0 == "---" { exit found ? 0 : 1 }
  in_ingress && $0 == "  tls:" { found = 1 }
  END { exit found ? 0 : 1 }
' "$cpu_ha_rendered"; then
  echo 'production ALBInstance certificate mode must not depend on the beta native Secret TLS flow' >&2
  exit 1
fi
if ! awk '
  $0 == "  name: ray-platform-rayctl-https-ingress" { in_ingress = 1 }
  in_ingress && $0 == "                name: spk-rayjob-release" { is_release_backend = 1 }
  in_ingress && is_release_backend && $0 == "                  number: 8080" { found = 1 }
  END { exit found ? 0 : 1 }
' "$cpu_ha_rendered"; then
  echo 'spk-rayjob HTTPS ingress must use the concrete ClusterIP service port' >&2
  exit 1
fi
if grep -A 8 'kind: StatefulSet' "$production_rendered" | grep -Fq 'name: postgres'; then
  echo 'production profile must not render the standalone PostgreSQL StatefulSet' >&2
  exit 1
fi

echo 'delivery render contract verified'
bash "${root_dir}/ops/platform/test/secret-restore-namespace-test.sh"
bash "${root_dir}/ops/platform/test/preflight-storage-image-test.sh"
bash "${root_dir}/ops/platform/test/fsx-prefix-verification-sequence-test.sh"
bash "${root_dir}/ops/platform/test/postgres-pull-secret-template-test.sh"
bash "${root_dir}/ops/platform/test/loki-cpu-fullname-test.sh"
bash "${root_dir}/ops/observability/loki/test/direct-client-paths-test.sh"
bash "${root_dir}/ops/platform/test/preflight-managed-ingress-class-test.sh"
bash "${root_dir}/ops/platform/test/preflight-local-cache-test.sh"
bash "${root_dir}/ops/platform/test/bootstrap-alb-contract-test.sh"
bash "${root_dir}/ops/platform/test/verify-rendered-ingress-test.sh"
bash "${root_dir}/ops/platform/test/ha-rollout-template-test.sh"
bash "${root_dir}/scripts/test-external-spk-rayjob-e2e-contract.sh"
bash "${root_dir}/scripts/test-go-builder-path-contract.sh"
bash "${root_dir}/scripts/test-nvme-cache-delivery.sh"
