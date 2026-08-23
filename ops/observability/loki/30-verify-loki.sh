#!/usr/bin/env bash
set -euo pipefail

readonly namespace="${LOKI_NAMESPACE:-loki}"
readonly release="${LOKI_RELEASE:-loki-cpu}"
readonly service="${LOKI_GATEWAY_SERVICE:-${release}-gateway}"
readonly port="${LOKI_VERIFY_PORT:-13100}"
readonly expected_instance_type="${LOKI_EXPECTED_INSTANCE_TYPE:-}"
readonly expected_node_pool="${LOKI_EXPECTED_NODE_POOL:-}"
readonly marker="platform-loki-smoke-$(date +%s)"
readonly service_proxy="/api/v1/namespaces/${namespace}/services/http:${service}:80/proxy"
proxy_pid=''

cleanup() {
  if [[ -n "$proxy_pid" ]]; then
    kill "$proxy_pid" >/dev/null 2>&1 || true
    wait "$proxy_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

proxy_url() {
  printf 'http://127.0.0.1:%s%s%s' "$port" "$service_proxy" "$1"
}

kubectl -n "$namespace" get "statefulset/${release}" >/dev/null
kubectl -n "$namespace" rollout status "statefulset/${release}" --timeout=10m
kubectl -n "$namespace" rollout status "deployment/${release}-gateway" --timeout=10m

readonly loki_selector="app.kubernetes.io/component=single-binary,app.kubernetes.io/instance=${release}"
kubectl -n "$namespace" wait --for=condition=Ready pod -l "$loki_selector" --timeout=10m
ready_loki="$(kubectl -n "$namespace" get pods -l "$loki_selector" -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}' | wc -l | tr -d ' ')"
[[ "$ready_loki" == "3" ]] || { echo "expected 3 running Loki pods, got ${ready_loki}" >&2; exit 1; }
bound_wal="$(kubectl -n "$namespace" get pvc -l "$loki_selector" -o jsonpath='{range .items[?(@.status.phase=="Bound")]}{.metadata.name}{"\n"}{end}' | wc -l | tr -d ' ')"
[[ "$bound_wal" == "3" ]] || { echo "expected 3 bound Loki WAL PVCs, got ${bound_wal}" >&2; exit 1; }

for node in $(kubectl -n "$namespace" get pods -l "$loki_selector" -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}'); do
  node_type="$(kubectl get node "$node" -o go-template='{{ index .metadata.labels "node.kubernetes.io/instance-type" }}')"
  if [[ -n "$expected_instance_type" ]]; then
    [[ "$node_type" == "$expected_instance_type" ]] || { echo "Loki pod scheduled on unexpected node type: ${node}" >&2; exit 1; }
  fi
  if [[ -n "$expected_node_pool" ]]; then
    node_pool="$(kubectl get node "$node" -o go-template='{{ index .metadata.labels "platform.wellspiking.ai/pool" }}')"
    [[ "$node_pool" == "$expected_node_pool" ]] || { echo "Loki pod scheduled outside expected CPU pool: ${node}" >&2; exit 1; }
  fi
done

# The caller includes :<port> in Host; accept that exact loopback form.
# Character classes avoid regex escaping differences between kubectl builds.
kubectl proxy --port="$port" --address=127.0.0.1 --accept-hosts='^127[.]0[.]0[.]1(:[0-9]+)?$' >/tmp/loki-kubectl-proxy.log 2>&1 &
proxy_pid=$!
for _ in {1..30}; do
  if curl --fail --silent "$(proxy_url '/')" | grep -Fxq 'OK'; then
    break
  fi
  sleep 1
done
curl --fail --silent "$(proxy_url '/')" | grep -Fxq 'OK'

timestamp="$(date +%s%N)"
payload="{\"streams\":[{\"stream\":{\"app\":\"ray-platform-smoke\",\"job_id\":\"loki-smoke\"},\"values\":[[\"${timestamp}\",\"${marker}\"]]}]}"
curl --fail --silent --show-error \
  -X POST "$(proxy_url '/loki/api/v1/push')" \
  -H 'Content-Type: application/json' \
  --data "$payload" >/dev/null

for _ in {1..30}; do
  response="$(curl --fail --silent --show-error "$(proxy_url '/loki/api/v1/query_range?query=%7Bapp%3D%22ray-platform-smoke%22%2Cjob_id%3D%22loki-smoke%22%7D')")"
  if grep -Fq "$marker" <<<"$response"; then
    echo "Loki push/query verified: ${marker}"
    exit 0
  fi
  sleep 1
done

echo 'Loki accepted the smoke write but did not return it before timeout' >&2
exit 1
