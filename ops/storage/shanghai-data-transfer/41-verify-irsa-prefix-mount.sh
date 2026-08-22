#!/usr/bin/env bash
# Verify that FSX mounts a pair of narrow TOS prefixes through the csi-fsx
# component IRSA role. No access key, secret key, or Secret reference is ever
# accepted by this script or its rendered manifest.
set -euo pipefail

for command in kubectl jq envsubst awk grep; do
  command -v "$command" >/dev/null 2>&1 || { printf 'missing required command: %s\n' "$command" >&2; exit 2; }
done

required=(FSX_TOS_SERVER FSX_TOS_REGION FSX_TOS_BUCKET TRAINING_NODE_SELECTOR SMOKE_IMAGE)
for key in "${required[@]}"; do
  if [[ -z "${!key:-}" ]]; then
    printf 'IRSA mount identity is not configured: %s is required\n' "$key" >&2
    exit 2
  fi
done

if ! kubectl -n kube-system get daemonset csi-fsx-node -o json | \
  jq -e '.spec.template.spec.containers[] | select(.name == "driver") | .env[]? | select(.name == "CREDENTIALS_TYPE") | (.value | ascii_downcase) == "irsa"' >/dev/null; then
  printf '%s\n' 'IRSA mount identity is not configured: enable IRSA on the csi-fsx component first' >&2
  exit 2
fi

selector_json='{}'
IFS=',' read -r -a selector_parts <<<"$TRAINING_NODE_SELECTOR"
for part in "${selector_parts[@]}"; do
  [[ "$part" =~ ^([A-Za-z0-9./_-]+)=([A-Za-z0-9._-]+)$ ]] || { printf 'invalid training node selector: %s\n' "$TRAINING_NODE_SELECTOR" >&2; exit 2; }
  key="${BASH_REMATCH[1]}"
  value="${BASH_REMATCH[2]}"
  selector_json="$(jq -cn --argjson current "$selector_json" --arg key "$key" --arg value "$value" '$current + {($key): $value}')"
done

SMOKE_NAME="ray-tos-irsa-$(date +%s)"
SMOKE_NAMESPACE="$SMOKE_NAME"
manifest_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rendered="$(mktemp)"
rendered_documents="$(mktemp -d)"
deferred_smoke_b_pod=""
smoke_wait_timeout="${FSX_SMOKE_WAIT_TIMEOUT:-3m}"
smoke_retry_delay_seconds="${FSX_SMOKE_RETRY_DELAY_SECONDS:-5}"
[[ "$smoke_retry_delay_seconds" =~ ^[0-9]+$ ]] || {
  printf 'FSX_SMOKE_RETRY_DELAY_SECONDS must be a non-negative integer\n' >&2
  exit 2
}

cleanup() {
  kubectl delete namespace "$SMOKE_NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  rm -f "$rendered"
  rm -rf "$rendered_documents"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

wait_for_smoke() {
  local pod_name="$1"
  local attempt

  # The TOS fusedaemon is started lazily on a newly joined node. Its first
  # activation can outlive one CSI wait window even though the daemon becomes
  # healthy immediately afterwards. Retry the same bounded verification once;
  # persistent mount, DNS, IRSA, or isolation failures still fail the preflight.
  for attempt in 1 2; do
    if kubectl -n "$SMOKE_NAMESPACE" wait \
      --for=jsonpath='{.status.phase}'=Succeeded "pod/${pod_name}" \
      --timeout="$smoke_wait_timeout"; then
      return 0
    fi
    if [[ "$attempt" == "1" ]]; then
      printf 'FSX smoke pod %s did not finish in %s; allowing one cold-start retry\n' \
        "$pod_name" "$smoke_wait_timeout" >&2
      sleep "$smoke_retry_delay_seconds"
    fi
  done

  printf 'FSX smoke pod %s failed after two bounded wait windows\n' "$pod_name" >&2
  return 1
}

export SMOKE_NAME SMOKE_NAMESPACE SMOKE_IMAGE SMOKE_NODE_SELECTOR_JSON="$selector_json"
kubectl create namespace "$SMOKE_NAMESPACE"
envsubst < "$manifest_dir/40-irsa-prefix-smoke.yaml" > "$rendered"

# A csi-fsx node agent serializes mount staging. On a single-GPU smoke cluster,
# creating both Pods at once races its mount lock and produces a false-negative
# verification. Apply PVs/PVCs and smoke-a first, then release it before
# scheduling smoke-b. On a larger fleet this is still a valid, deterministic
# prefix-isolation check.
awk -v output_dir="$rendered_documents" '
  /^---[[:space:]]*$/ { document += 1; next }
  { print > (output_dir "/document-" document ".yaml") }
' "$rendered"

for document in "$rendered_documents"/document-*.yaml; do
  [[ -s "$document" ]] || continue
  if grep -Fxq 'kind: Pod' "$document" && grep -Fxq '  name: smoke-b' "$document"; then
    deferred_smoke_b_pod="$document"
    continue
  fi
  kubectl apply -f "$document"
done
[[ -n "$deferred_smoke_b_pod" ]] || { printf '%s\n' 'smoke-b Pod document was not rendered' >&2; exit 2; }

# A CSI/IRSA failure is normally returned within seconds. The second bounded
# window covers the one-time TOS fusedaemon cold start observed on new nodes.
wait_for_smoke smoke-a
kubectl -n "$SMOKE_NAMESPACE" logs smoke-a | grep -qx 'mount-root-isolated'
kubectl -n "$SMOKE_NAMESPACE" delete pod/smoke-a --wait=true

kubectl apply -f "$deferred_smoke_b_pod"
wait_for_smoke smoke-b
kubectl -n "$SMOKE_NAMESPACE" logs smoke-b | grep -qx 'mount-root-isolated'
printf '%s\n' 'IRSA prefix mount contract verified'
