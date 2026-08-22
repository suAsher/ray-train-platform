#!/usr/bin/env bash
set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

usage() {
  cat <<'EOF'
Usage: ops/platform/reset.sh --profile <profile.yaml> [--include-legacy-platform-resources]
       ops/platform/reset.sh --profile <profile.yaml> --execute --confirm-reset-ray-platform [--include-legacy-platform-resources]

The default is a read-only preview. --execute is ignored unless the exact
confirmation phrase is also supplied. This script never accesses object
storage and never deletes shared KubeRay, Kueue, CSI, Loki, Alloy, ingress or
identity components.
EOF
}

profile=""
execute=false
confirmation=""
include_legacy=false
while (($#)); do
  case "$1" in
    --profile) profile="${2:-}"; shift 2 ;;
    --execute) execute=true; shift ;;
    --confirm-reset-ray-platform) confirmation="reset-ray-platform"; shift ;;
    --include-legacy-platform-resources) include_legacy=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

require_profile "$profile"
require_command "$KUBECTL_BIN"
require_command "$HELM_BIN"
namespace="$(profile_namespace "$profile")"
queue_name="$(profile_section_value "$profile" kueue clusterQueueName)"
flavor_name="$(profile_section_value "$profile" kueue resourceFlavorName)"
manage_kueue="$(profile_section_value "$profile" kueue manageResources)"

if "$execute" && [[ "$confirmation" != "reset-ray-platform" ]]; then
  die "destructive reset requires --execute --confirm-reset-ray-platform"
fi

contains_line() {
  local lines="$1"
  local expected="$2"
  grep -Fqx "$expected" <<<"$lines"
}

part_of_namespaces="$(kube get namespaces -l "$PLATFORM_PART_OF_LABEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
legacy_namespaces="$(kube get namespaces -l "$PLATFORM_LEGACY_LABEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
legacy_test_namespaces=""
if "$include_legacy"; then
  legacy_test_namespaces="$(kube get namespaces -l ray-train-platform/test=true -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
fi
namespaces="$(printf '%s\n%s\n%s\n' "$part_of_namespaces" "$legacy_namespaces" "$legacy_test_namespaces" | awk 'NF && !seen[$0]++')"
if kube get namespace "$namespace" >/dev/null 2>&1; then
  if ! contains_line "$namespaces" "$namespace"; then
    namespaces="${namespaces:+${namespaces}$'\n'}${namespace}"
  fi
fi

managed_pvs="$(kube get pv -l "$PLATFORM_PART_OF_LABEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
namespace_claims=""
while read -r candidate_namespace; do
  [[ -n "$candidate_namespace" ]] || continue
  claims="$(kube -n "$candidate_namespace" get pvc -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.volumeName}{"\n"}{end}' 2>/dev/null || true)"
  while read -r claim_name claim_volume; do
    [[ -n "$claim_name" ]] || continue
    namespace_claims="${namespace_claims:+${namespace_claims}$'\n'}${candidate_namespace}/${claim_name} ${claim_volume}"
  done <<<"$claims"
done <<<"$namespaces"
namespace_claim_pvs="$(awk 'NF == 2 && $2 != "" { print $2 }' <<<"$namespace_claims")"
managed_pvs="$(printf '%s\n%s\n' "$managed_pvs" "$namespace_claim_pvs" | awk 'NF && !seen[$0]++')"
if "$include_legacy"; then
  for legacy_pv in ray-train-local-datasets ray-train-local-checkpoints ray-train-local-outputs; do
    if kube get pv "$legacy_pv" >/dev/null 2>&1; then
      if ! contains_line "$managed_pvs" "$legacy_pv"; then
        managed_pvs="${managed_pvs:+${managed_pvs}$'\n'}${legacy_pv}"
      fi
    fi
  done
fi

manage_queue=false
if [[ "$manage_kueue" == "true" && -n "$queue_name" ]]; then
  queue_exists="$(kube get clusterqueue "$queue_name" -o name 2>/dev/null || true)"
  owned_queues="$(kube get clusterqueues -l "$PLATFORM_PART_OF_LABEL" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  legacy_queues="$(kube get clusterqueues -l ray-train-platform/test=true -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  owned_queue=false
  legacy_queue=false
  contains_line "$owned_queues" "$queue_name" && owned_queue=true
  contains_line "$legacy_queues" "$queue_name" && legacy_queue=true
  if "$owned_queue" || ( "$include_legacy" && "$legacy_queue" ); then
    manage_queue=true
  elif [[ -z "$queue_exists" ]]; then
    # A freshly reset cluster has no queue yet; deployment will create it. It
    # is not a reset deletion candidate.
    manage_queue=false
  else
    die "refusing to manage ClusterQueue ${queue_name}; it is not labelled as this platform's resource"
  fi
fi

local_queues="$(kube get localqueues -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.spec.clusterQueue}{"\n"}{end}' 2>/dev/null || true)"
managed_local_queues=""
while read -r queue_namespace queue_local_name queue_cluster_name; do
  if [[ -n "$queue_namespace" && "$queue_cluster_name" == "$queue_name" ]] && contains_line "$namespaces" "$queue_namespace"; then
    managed_local_queues="${managed_local_queues:+${managed_local_queues}$'\n'}${queue_namespace}/${queue_local_name}"
  fi
done <<<"$local_queues"

printf 'PREVIEW ONLY — platform reset candidates\n'
printf '  Helm release: %s in %s\n' "$PLATFORM_RELEASE" "$namespace"
printf '  Namespaces: %s\n' "$(tr '\n' ' ' <<<"${namespaces:-}")"
printf '  Namespace PVCs: %s\n' "$(tr '\n' ' ' <<<"${namespace_claims:-}")"
printf '  LocalQueues: %s\n' "$(tr '\n' ' ' <<<"${managed_local_queues:-}")"
printf '  PersistentVolumes: %s\n' "$(tr '\n' ' ' <<<"${managed_pvs:-}")"
printf '  ClusterQueue: %s\n' "$([ "$manage_queue" = true ] && printf '%s' "$queue_name" || printf '%s' '(external or absent)')"
printf '  ResourceFlavor: %s\n' "$([ "$manage_queue" = true ] && printf '%s' "$flavor_name" || printf '%s' '(external or absent)')"
printf '  Excluded forever: TOS objects, KubeRay, Kueue controller, CSI, Loki, Alloy, ingress, Keycloak\n'

if ! "$execute"; then
  log "No resources were changed. Re-run with --execute --confirm-reset-ray-platform after reviewing this list."
  exit 0
fi

log "Deleting only the reviewed platform resources. TOS objects are not accessed."
helm_cmd uninstall "$PLATFORM_RELEASE" --namespace "$namespace" --ignore-not-found --wait || true

while read -r item; do
  [[ -n "$item" ]] || continue
  queue_namespace="${item%%/*}"
  queue_local_name="${item#*/}"
  kube -n "$queue_namespace" delete localqueue "$queue_local_name" --ignore-not-found
done <<<"$managed_local_queues"

while read -r item; do
  [[ -n "$item" ]] || continue
  kube delete namespace "$item" --ignore-not-found --wait=true
done <<<"$namespaces"

while read -r item; do
  [[ -n "$item" ]] || continue
  kube delete pv "$item" --ignore-not-found
done <<<"$managed_pvs"

if "$manage_queue"; then
  # Refuse to destroy a ClusterQueue if non-platform LocalQueues still refer to
  # it. This protects queues later shared with another workload.
  remaining=""
  while read -r queue_namespace queue_cluster_name; do
    if [[ "$queue_cluster_name" == "$queue_name" ]] && ! contains_line "$namespaces" "$queue_namespace"; then
      remaining="${remaining:+${remaining} }${queue_namespace}"
    fi
  done <<<"$(kube get localqueues -A -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.spec.clusterQueue}{"\n"}{end}' 2>/dev/null || true)"
  [[ -z "$remaining" ]] || die "refusing to delete ClusterQueue ${queue_name}; still referenced by namespace(s): ${remaining}"
  kube delete clusterqueue "$queue_name" --ignore-not-found --wait=true
  if [[ -n "$flavor_name" ]]; then
    # A ResourceFlavor can be shared by multiple ClusterQueues. It is deleted
    # only when no remaining queue references it after the platform queue goes.
    flavor_references="$(kube get clusterqueues -o jsonpath='{range .items[*]}{.metadata.name}{" "}{range .spec.resourceGroups[*].flavors[*]}{.name}{" "}{end}{"\n"}{end}' 2>/dev/null | awk -v flavor="$flavor_name" '$0 ~ ("(^| )" flavor "( |$)") { print $1 }')"
    [[ -z "$flavor_references" ]] || die "refusing to delete ResourceFlavor ${flavor_name}; still referenced by ClusterQueue(s): ${flavor_references}"
    kube delete resourceflavor "$flavor_name" --ignore-not-found
  fi
fi

log "Platform reset complete. Shared components and all TOS data were preserved."
