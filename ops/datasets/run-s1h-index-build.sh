#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: run-s1h-index-build.sh --image <digest reference> --version <id> [--run-id retry1] [--namespace tenant-local] [--source labeled] [--finalize-only] [--slice-from-version <id> --train-samples <n> --val-samples <n>] [--wait]
EOF
}

namespace="tenant-local"
source_relative="labeled"
image=""
version=""
run_id=""
wait_for_completion=false
finalize_only=false
slice_from_version=""
train_sample_limit=""
val_sample_limit=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --image) image="${2:-}"; shift 2 ;;
    --version) version="${2:-}"; shift 2 ;;
    --run-id) run_id="${2:-}"; shift 2 ;;
    --namespace) namespace="${2:-}"; shift 2 ;;
    --source) source_relative="${2:-}"; shift 2 ;;
    --finalize-only) finalize_only=true; shift ;;
    --slice-from-version) slice_from_version="${2:-}"; shift 2 ;;
    --train-samples) train_sample_limit="${2:-}"; shift 2 ;;
    --val-samples) val_sample_limit="${2:-}"; shift 2 ;;
    --wait) wait_for_completion=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

[[ "$image" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]] || {
  echo '--image must be an immutable sha256 digest reference' >&2
  exit 2
}
[[ "$version" =~ ^[a-z0-9]([a-z0-9.-]{0,38}[a-z0-9])?$ ]] || {
  echo '--version must be a lowercase release identifier with at most 40 characters' >&2
  exit 2
}
[[ -z "$run_id" || "$run_id" =~ ^[a-z0-9]([a-z0-9.-]{0,18}[a-z0-9])?$ ]] || {
  echo '--run-id must be a lowercase identifier with at most 20 characters' >&2
  exit 2
}
if [[ -n "$slice_from_version" ]]; then
  [[ "$slice_from_version" =~ ^[a-z0-9]([a-z0-9.-]{0,38}[a-z0-9])?$ ]] || {
    echo '--slice-from-version must be a lowercase release identifier with at most 40 characters' >&2
    exit 2
  }
  [[ "$train_sample_limit" =~ ^[1-9][0-9]{0,5}$ && "$val_sample_limit" =~ ^[1-9][0-9]{0,5}$ ]] || {
    echo '--train-samples and --val-samples must be positive integers below one million' >&2
    exit 2
  }
  "$finalize_only" && {
    echo '--finalize-only cannot be combined with --slice-from-version' >&2
    exit 2
  }
elif [[ -n "$train_sample_limit" || -n "$val_sample_limit" ]]; then
  echo 'sample limits require --slice-from-version' >&2
  exit 2
fi
[[ "$namespace" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]] || {
  echo '--namespace must be a DNS label' >&2
  exit 2
}
[[ "$source_relative" =~ ^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$ ]] || {
  echo '--source must be a clean relative path' >&2
  exit 2
}
IFS='/' read -r -a source_components <<<"$source_relative"
for component in "${source_components[@]}"; do
  [[ "$component" != '.' && "$component" != '..' ]] || {
    echo '--source must not contain dot path components' >&2
    exit 2
  }
done

public_claim=""
while read -r claim volume; do
  [[ "$claim" == data-public-* ]] || continue
  root_path="$(kubectl get pv "$volume" -o jsonpath='{.spec.csi.volumeAttributes.path}')"
  if [[ "$root_path" == '/ray-train/public' || "$root_path" == 'ray-train/public' ]]; then
    if [[ -n "$public_claim" ]]; then
      echo "multiple governed public claims found in $namespace" >&2
      exit 1
    fi
    public_claim="$claim"
  fi
done < <(kubectl -n "$namespace" get pvc -o json | jq -r '.items[] | [.metadata.name, .spec.volumeName] | @tsv')
[[ -n "$public_claim" ]] || {
  echo "governed /ray-train/public claim not found in $namespace" >&2
  exit 1
}

job_name="s1h-index-${version//./-}"
if [[ -n "$run_id" ]]; then
  job_name="$job_name-${run_id//./-}"
fi
job_name="${job_name:0:63}"
if kubectl -n "$namespace" get job "$job_name" >/dev/null 2>&1; then
  echo "job already exists: $namespace/$job_name" >&2
  exit 1
fi

source_root="/mnt/storage/public/$source_relative"
output_key="$version"
if [[ -n "$run_id" ]]; then
  output_key="$version-$run_id"
fi
output_root="$source_root/.raytrain/index-builds/$output_key"
final_index="$source_root/.raytrain/indexes/$version/trusted-index-v3.json"
index_command='python3 /opt/raytrain-indexer/build_s1h_trusted_index.py --source-root "$source_root" --train-pkl "$output_root/merged_nuscenes_infos_train.pkl" --val-pkl "$output_root/merged_nuscenes_infos_val.pkl" --output "$output_root/trusted-index-v3.json" --summary "$output_root/trusted-index-v3.summary.json" --workers 32 --format multimodal-v2'
if [[ -n "$slice_from_version" ]]; then
  slice_source="$source_root/.raytrain/indexes/$slice_from_version/trusted-index-v3.json"
  generation_command='mkdir -p "$output_root"; test -f '"'"$slice_source"'"' || { echo "slice source index is unavailable" >&2; exit 4; }'
  index_command='python3 /opt/raytrain-indexer/slice_s1h_trusted_index.py --source-index '"'"$slice_source"'"' --output "$output_root/trusted-index-v3.json" --train-samples '"$train_sample_limit"' --val-samples '"$val_sample_limit"
elif "$finalize_only"; then
  generation_command='test -f "$output_root/merged_nuscenes_infos_train.pkl" -a -f "$output_root/merged_nuscenes_infos_val.pkl" -a -f "$output_root/rejected-packages.json" || { echo "validated index build outputs are incomplete" >&2; exit 4; }'
else
  generation_command='python3 /opt/raytrain-indexer/generate_s1h_public_indexes.py --source-root "$source_root" --output-root "$output_root" --workers 32 --max-sweeps 10 --min-scene-samples 81 --multimodal'
fi
command="$(printf '%s\n' \
  'set -euo pipefail' \
  "source_root='$source_root'" \
  "output_root='$output_root'" \
  "final_index='$final_index'" \
  'test ! -e "$final_index" || { echo "trusted index already exists; publish a new dataset source root instead of overwriting it" >&2; exit 3; }' \
  "$generation_command" \
  "$index_command" \
  'export INDEX_SOURCE="$output_root/trusted-index-v3.json" INDEX_TARGET="$final_index" INDEX_PARTS_SOURCE="$output_root/trusted-index-v3.parts" INDEX_PARTS_TARGET="$source_root/.raytrain/indexes/'"$version"'/trusted-index-v3.parts"' \
  "export INDEX_TEMP='$source_root/.raytrain/.trusted-index-v3.$version.tmp'" \
  'test -d "$INDEX_PARTS_SOURCE" || { echo "trusted index parts are missing" >&2; exit 5; }' \
  'mkdir -p "$INDEX_PARTS_TARGET"' \
  'part_count=0; for part in "$INDEX_PARTS_SOURCE"/sha256-*.json; do test -f "$part" || { echo "trusted index parts are empty" >&2; exit 5; }; destination="$INDEX_PARTS_TARGET/${part##*/}"; if test -e "$destination"; then cmp -s "$part" "$destination" || { echo "trusted index part conflict" >&2; exit 5; }; else cp "$part" "$destination"; fi; part_count=$((part_count + 1)); done' \
  'python3 -c '\''import os, shutil; from pathlib import Path; source=Path(os.environ["INDEX_SOURCE"]); target=Path(os.environ["INDEX_TARGET"]); temporary=Path(os.environ["INDEX_TEMP"]); target.parent.mkdir(parents=True, exist_ok=True); target.exists() and (_ for _ in ()).throw(RuntimeError("trusted index already exists")); shutil.copyfile(source, temporary); temporary.replace(target); print(f"manifest root committed last: published {target} bytes={target.stat().st_size}")'\''' \
)"

jq -n \
  --arg namespace "$namespace" \
  --arg name "$job_name" \
  --arg image "$image" \
  --arg claim "$public_claim" \
  --arg command "$command" '
  {
    apiVersion: "batch/v1",
    kind: "Job",
    metadata: {
      name: $name,
      namespace: $namespace,
      labels: {
        "app.kubernetes.io/name": "s1h-dataset-indexer",
        "app.kubernetes.io/part-of": "ray-train-platform",
        "app.kubernetes.io/managed-by": "ray-train-platform"
      }
    },
    spec: {
      backoffLimit: 0,
      activeDeadlineSeconds: 86400,
      ttlSecondsAfterFinished: 604800,
      template: {
        metadata: {labels: {"app.kubernetes.io/name": "s1h-dataset-indexer"}},
        spec: {
          restartPolicy: "Never",
          automountServiceAccountToken: false,
          priorityClassName: "ray-platform-ray-train-platform-dataset-publisher-priority",
          nodeSelector: {"platform.wellspiking.ai/gpu-pool": "production"},
          imagePullSecrets: [{name: "harbor-registry"}],
          securityContext: {
            runAsNonRoot: true,
            runAsUser: 1000,
            runAsGroup: 1000,
            fsGroup: 1000,
            fsGroupChangePolicy: "OnRootMismatch",
            seccompProfile: {type: "RuntimeDefault"}
          },
          containers: [{
            name: "indexer",
            image: $image,
            imagePullPolicy: "IfNotPresent",
            command: ["/bin/bash", "-lc", $command],
            env: [
              {name: "HOME", value: "/tmp"},
              {name: "PYTHONDONTWRITEBYTECODE", value: "1"}
            ],
            resources: {
              requests: {cpu: "32", memory: "128Gi"},
              limits: {cpu: "64", memory: "256Gi"}
            },
            securityContext: {
              allowPrivilegeEscalation: false,
              readOnlyRootFilesystem: true,
              capabilities: {drop: ["ALL"]}
            },
            volumeMounts: [
              {name: "public", mountPath: "/mnt/storage/public"},
              {name: "tmp", mountPath: "/tmp"}
            ]
          }],
          volumes: [
            {name: "public", persistentVolumeClaim: {claimName: $claim}},
            {name: "tmp", emptyDir: {sizeLimit: "20Gi"}}
          ]
        }
      }
    }
  }' | kubectl apply -f -

echo "started $namespace/$job_name with no GPU resource request"
echo "logs: kubectl -n $namespace logs -f job/$job_name"
if "$wait_for_completion"; then
  if ! kubectl -n "$namespace" wait --for=condition=complete "job/$job_name" --timeout=24h; then
    kubectl -n "$namespace" logs "job/$job_name" --tail=300 >&2 || true
    exit 1
  fi
  kubectl -n "$namespace" logs "job/$job_name" --tail=300
fi
