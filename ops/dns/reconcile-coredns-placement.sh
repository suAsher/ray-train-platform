#!/usr/bin/env bash
set -euo pipefail

readonly KUBECTL_BIN="${KUBECTL_BIN:-kubectl}"
readonly NAMESPACE="${COREDNS_NAMESPACE:-kube-system}"
readonly DEPLOYMENT="${COREDNS_DEPLOYMENT:-coredns}"
readonly MODE="${1:---check}"
readonly CPU_REQUEST="${COREDNS_CPU_REQUEST:-250m}"
readonly MEMORY_REQUEST="${COREDNS_MEMORY_REQUEST:-256Mi}"
readonly CPU_LIMIT="${COREDNS_CPU_LIMIT:-2}"
readonly MEMORY_LIMIT="${COREDNS_MEMORY_LIMIT:-1Gi}"
readonly REPLICAS="${COREDNS_REPLICAS:-2}"

usage() {
  cat <<'EOF'
Usage: reconcile-coredns-placement.sh [--check|--apply]

Allows CoreDNS to run on either reviewed real-node pool, preferring the CPU
control-plane pool and using the production GPU pool as a fallback. It removes
virtual-node selectors, keeps the replica count, and reconciles conservative
resource requests suitable for the cluster DNS workload.
EOF
}

case "$MODE" in
  --check|--apply) ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

command -v "$KUBECTL_BIN" >/dev/null || { echo "kubectl is required" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

deployment_json() {
  "$KUBECTL_BIN" -n "$NAMESPACE" get deployment "$DEPLOYMENT" -o json
}

check_placement() {
  local deployment="$1"

  jq -e \
    --arg cpu_request "$CPU_REQUEST" \
    --arg memory_request "$MEMORY_REQUEST" \
    --arg cpu_limit "$CPU_LIMIT" \
    --arg memory_limit "$MEMORY_LIMIT" \
    --argjson replicas "$REPLICAS" '
    (.spec.replicas == $replicas)
    and
    ((.spec.template.spec.nodeSelector // {})
      | has("platform.wellspiking.ai/pool") | not)
    and
    ((.spec.template.spec.nodeSelector // {})
      | has("platform.wellspiking.ai/gpu-pool") | not)
    and
    ([.spec.template.spec.affinity.nodeAffinity
       .requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[]?
       .matchExpressions[]?
      | select(.key == "platform.wellspiking.ai/pool"
               and .operator == "In"
               and (.values | index("control-plane")))] | length == 1)
    and
    ([.spec.template.spec.affinity.nodeAffinity
       .requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[]?
       .matchExpressions[]?
      | select(.key == "platform.wellspiking.ai/gpu-pool"
               and .operator == "In"
               and (.values | index("production")))] | length == 1)
    and
    ([.spec.template.spec.affinity.nodeAffinity
       .preferredDuringSchedulingIgnoredDuringExecution[]?
      | select(.weight == 100)
      | .preference.matchExpressions[]?
      | select(.key == "platform.wellspiking.ai/pool"
               and .operator == "In"
               and (.values | index("control-plane")))] | length == 1)
    and
    ([.spec.template.spec.containers[]?
      | select(.name == "coredns")
      | select(.resources.requests.cpu == $cpu_request
               and .resources.requests.memory == $memory_request
               and .resources.limits.cpu == $cpu_limit
               and .resources.limits.memory == $memory_limit)] | length == 1)
  ' <<<"$deployment" >/dev/null
}

if [[ "$MODE" == "--check" ]]; then
  current="$(deployment_json)"
  check_placement "$current" || {
    echo "CoreDNS is not eligible for both reviewed real-node pools" >&2
    exit 1
  }
  echo "CoreDNS shared real-node placement is configured"
  exit 0
fi

patch="$(jq -n \
  --arg cpu_request "$CPU_REQUEST" \
  --arg memory_request "$MEMORY_REQUEST" \
  --arg cpu_limit "$CPU_LIMIT" \
  --arg memory_limit "$MEMORY_LIMIT" \
  --argjson replicas "$REPLICAS" '{
  "spec": {
    "replicas": $replicas,
    "template": {
      "metadata": {
        "annotations": {
          "vke.volcengine.com/burst-to-vci": null
        }
      },
      "spec": {
        "nodeSelector": {
          "node.kubernetes.io/instance-type": null,
          "vci.vke.volcengine.com/node-type": null,
          "platform.wellspiking.ai/pool": null,
          "platform.wellspiking.ai/gpu-pool": null
        },
        "affinity": {
          "nodeAffinity": {
            "requiredDuringSchedulingIgnoredDuringExecution": {
              "nodeSelectorTerms": [
                {
                  "matchExpressions": [
                    {
                      "key": "platform.wellspiking.ai/pool",
                      "operator": "In",
                      "values": ["control-plane"]
                    }
                  ]
                },
                {
                  "matchExpressions": [
                    {
                      "key": "platform.wellspiking.ai/gpu-pool",
                      "operator": "In",
                      "values": ["production"]
                    }
                  ]
                }
              ]
            },
            "preferredDuringSchedulingIgnoredDuringExecution": [
              {
                "weight": 100,
                "preference": {
                  "matchExpressions": [
                    {
                      "key": "platform.wellspiking.ai/pool",
                      "operator": "In",
                      "values": ["control-plane"]
                    }
                  ]
                }
              }
            ]
          }
        },
        "containers": [
          {
            "name": "coredns",
            "resources": {
              "requests": {
                "cpu": $cpu_request,
                "memory": $memory_request
              },
              "limits": {
                "cpu": $cpu_limit,
                "memory": $memory_limit
              }
            }
          }
        ]
      }
    }
  }
}')"

"$KUBECTL_BIN" -n "$NAMESPACE" patch deployment "$DEPLOYMENT" \
  --type strategic --patch "$patch" >/dev/null
"$KUBECTL_BIN" -n "$NAMESPACE" rollout status "deployment/${DEPLOYMENT}" --timeout=5m

current="$(deployment_json)"
check_placement "$current" || {
  echo "CoreDNS placement did not converge to the reviewed real-node pools" >&2
  exit 1
}
echo "CoreDNS prefers CPU control-plane nodes and can fall back to production GPU nodes"
