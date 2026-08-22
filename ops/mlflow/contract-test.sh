#!/usr/bin/env bash
set -euo pipefail

readonly ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly VALUES="${ROOT_DIR}/ops/mlflow/values-vke.yaml"
readonly NAMESPACE="${ROOT_DIR}/ops/mlflow/00-namespace.yaml"
readonly DATABASE="${ROOT_DIR}/ops/mlflow/10-database.yaml"
readonly STORAGE="${ROOT_DIR}/ops/mlflow/15-artifact-storage.yaml"
readonly BOOTSTRAP="${ROOT_DIR}/ops/mlflow/20-bootstrap.yaml"
readonly DB_UPGRADE="${ROOT_DIR}/ops/mlflow/22-db-upgrade.yaml"
readonly ACCEPTANCE="${ROOT_DIR}/ops/mlflow/25-artifact-acceptance.yaml"
readonly TRANSITION_POLICY="${ROOT_DIR}/ops/mlflow/29-storage-migration-policy.yaml"
readonly POLICY="${ROOT_DIR}/ops/mlflow/30-policy.yaml"
readonly FSX_PROBE="${ROOT_DIR}/ops/mlflow/35-fsx-health-probe.yaml"
readonly SMOKE="${ROOT_DIR}/ops/mlflow/40-smoke.yaml"
readonly DEPLOY="${ROOT_DIR}/ops/mlflow/deploy.sh"
readonly CONCURRENCY_TEST="${ROOT_DIR}/ops/mlflow/deploy-concurrency-contract-test.sh"
readonly FSX_IRSA_TEST="${ROOT_DIR}/ops/mlflow/fsx-irsa-contract-test.sh"
readonly VERIFY="${ROOT_DIR}/ops/mlflow/verify.sh"
readonly README="${ROOT_DIR}/ops/mlflow/README.md"
readonly VENDORED_CHART="${ROOT_DIR}/helm/vendor/mlflow-0.1.0.tgz"
readonly VENDORED_DEPLOYMENT="mlflow/templates/deployment.yaml"

vendored_deployment="$(tar -xOf "$VENDORED_CHART" "$VENDORED_DEPLOYMENT")"
grep -Fq '          command:' <<<"$vendored_deployment" || {
  echo 'vendored MLflow chart must render server flags in container.command' >&2
  exit 1
}
grep -Fq -- '- --static-prefix={{ .Values.server.staticPrefix }}' <<<"$vendored_deployment" || {
  echo 'vendored MLflow chart must render server.staticPrefix in container.command' >&2
  exit 1
}

grep -Fq 'replicaCount: 2' "$VALUES"
grep -A1 '^nodeSelector:$' "$VALUES" | grep -Fq 'accelerator: nvidia-rtx-4090' || {
  echo 'MLflow server must use CPU and memory from the GPU worker pool' >&2
  exit 1
}
grep -A2 '^nodeSelector:$' "$VALUES" | grep -Fq 'platform.wellspiking.ai/gpu-pool: production' || {
  echo 'MLflow server must be restricted to the production GPU worker pool' >&2
  exit 1
}
if grep -Fq 'nvidia.com/gpu' "$VALUES"; then
  echo 'MLflow server must not reserve a GPU device' >&2
  exit 1
fi
grep -Fq '  staticPrefix: /mlflow' "$VALUES" || {
  echo 'MLflow must be served under server.staticPrefix /mlflow' >&2
  exit 1
}
grep -Fq 'type: ClusterIP' "$VALUES"
grep -A1 '^ingress:$' "$VALUES" | grep -Fq '  enabled: false'
grep -Fq '@sha256:' "$VALUES"
grep -Fq 'name: mlflow-database' "$VALUES"
grep -Fq 'automountServiceAccountToken: false' "$VALUES"
grep -Fq 'name: mlflow-system' "$NAMESPACE"
grep -Fq 'kind: StatefulSet' "$DATABASE"
grep -Fq 'name: mlflow-postgres' "$DATABASE"
grep -Fq 'storageClassName: ebs-ssd' "$DATABASE"
grep -Fq 'storage: 20Gi' "$DATABASE"
if grep -Eq 'tos-credentials|AWS_(ACCESS_KEY_ID|SECRET_ACCESS_KEY|DEFAULT_REGION|CONFIG_FILE)|TOS_|MLFLOW_(S3|BOTO)' "$VALUES"; then
  echo 'MLflow Pod must not receive shared TOS credentials or AWS/TOS environment variables' >&2
  exit 1
fi
if grep -Eq 'mlflow-aws-config|aws-config|/etc/mlflow/aws' "$VALUES"; then
  echo 'MLflow Pod must not mount an AWS configuration' >&2
  exit 1
fi
grep -Fq 'artifactsDestination: file:///mlflow-artifacts' "$VALUES"
grep -Fq 'name: mlflow-artifacts' "$VALUES"
grep -Fq 'claimName: mlflow-artifacts-irsa' "$VALUES"
grep -Fq 'mountPath: /mlflow-artifacts' "$VALUES"
grep -Fq 'kind: PersistentVolume' "$STORAGE"
grep -Fq 'name: mlflow-artifacts-irsa-pv' "$STORAGE"
grep -Fq 'persistentVolumeReclaimPolicy: Retain' "$STORAGE"
grep -Fq 'volumeHandle: mlflow-artifacts-irsa-pv' "$STORAGE"
grep -Fq 'driver: fsx.csi.volcengine.com' "$STORAGE"
grep -Fq 'bucket: vke-cluster' "$STORAGE"
grep -Fq 'path: /ray-train/platform/mlflow-artifacts' "$STORAGE"
if grep -Eq 'secret(Name|Namespace):|tos-fsx-credentials|AWS_|TOS_' "$STORAGE"; then
  echo 'MLflow artifact PV must use the cluster FSX CSI IRSA identity without static credentials' >&2
  exit 1
fi
grep -Fq 'tos_allow_delete=true' "$STORAGE"
grep -Fq 'uid=1000' "$STORAGE"
grep -Fq 'gid=1000' "$STORAGE"
grep -Fq 'dir_mode=770' "$STORAGE"
grep -Fq 'file_mode=660' "$STORAGE"
grep -Fq 'name: mlflow-artifacts-irsa' "$STORAGE"
grep -Fq 'namespace: mlflow-system' "$STORAGE"
grep -Fq 'ReadWriteMany' "$STORAGE"
grep -Fq 'storageClassName: ""' "$STORAGE"
grep -Fq 'volumeName: mlflow-artifacts-irsa-pv' "$STORAGE"
grep -Fq 'claimName: mlflow-artifacts-irsa' "$BOOTSTRAP"
grep -Fq 'mountPath: /mlflow-artifacts' "$BOOTSTRAP"
grep -Fq 'rm -f "$probe"' "$BOOTSTRAP"
grep -Fq 'test ! -e "$probe"' "$BOOTSTRAP"
if grep -Eq 'tos-credentials|AWS_|TOS_' "$BOOTSTRAP"; then
  echo 'MLflow storage bootstrap must probe the PVC without object-store credentials' >&2
  exit 1
fi
grep -Fq 'verify_fsx_irsa' "$DEPLOY"
grep -Fq 'get csidriver fsx.csi.volcengine.com' "$DEPLOY"
grep -Fq 'get daemonset csi-fsx-node' "$DEPLOY"
grep -Fq 'CREDENTIALS_TYPE' "$DEPLOY"
grep -Fq 'ROLE_NAME_FOR_IRSA' "$DEPLOY"
if grep -Fq 'get secret tos-fsx-credentials' "$DEPLOY"; then
  echo 'MLflow deploy must not require a static FSX credential Secret' >&2
  exit 1
fi
grep -Fq '15-artifact-storage.yaml' "$DEPLOY"
grep -Fq 'pvc/mlflow-artifacts-irsa' "$DEPLOY"
helm_line="$(grep -nF 'helm upgrade --install' "$DEPLOY" | cut -d: -f1)"
legacy_delete_line="$(grep -nF 'delete secret tos-credentials' "$DEPLOY" | cut -d: -f1)"
strict_policy_line="$(grep -nF 'kubectl apply -f "${ROOT_DIR}/ops/mlflow/30-policy.yaml"' "$DEPLOY" | head -n1 | cut -d: -f1)"
if (( legacy_delete_line < helm_line || strict_policy_line < helm_line )); then
  echo 'MLflow deploy must preserve legacy S3 dependencies and egress until the new release passes acceptance' >&2
  exit 1
fi

grep -Fq '29-storage-migration-policy.yaml' "$DEPLOY"
bash "$CONCURRENCY_TEST"
bash "$FSX_IRSA_TEST"
grep -Fq 'run_job "$(deployment_job_name mlflow-artifact-storage-probe)"' "$DEPLOY"
grep -Fq 'run_job "$(deployment_job_name mlflow-db-upgrade)"' "$DEPLOY"
grep -Fq 'run_job "$(deployment_job_name mlflow-artifact-acceptance)"' "$DEPLOY"
grep -Fq 'created_uid' "$DEPLOY" || {
  echo 'MLflow jobs must capture the UID returned by create' >&2
  exit 1
}
grep -Fq 'cleanup_job_instance' "$DEPLOY" || {
  echo 'failed MLflow jobs must be terminated with a UID precondition' >&2
  exit 1
}
grep -Fq 'previous_revision' "$DEPLOY"
grep -Fq 'get deployment mlflow --ignore-not-found -o yaml' "$DEPLOY"
grep -Fq 'deployed_revision' "$DEPLOY"
grep -Fq 'previous_revision=$((deployed_revision - 1))' "$DEPLOY"
grep -Fq 'helm rollback' "$DEPLOY"
grep -Fq 'restore_legacy_dependencies' "$DEPLOY"
grep -Fq 'cleanup_legacy_dependencies' "$DEPLOY"
[[ "$(grep -Fc 'copy_secret tos-credentials' "$DEPLOY")" == "1" ]] || {
  echo 'shared TOS credentials may only be recopied by the automatic recovery path' >&2
  exit 1
}

storage_apply_line="$(grep -nF 'kubectl apply -f "$ARTIFACT_STORAGE"' "$DEPLOY" | cut -d: -f1)"
storage_bound_line="$(grep -nF 'pvc/mlflow-artifacts-irsa' "$DEPLOY" | cut -d: -f1)"
fsx_probe_apply_line="$(grep -nF 'kubectl apply -f "$FSX_HEALTH_PROBE"' "$DEPLOY" | cut -d: -f1)"
fsx_probe_ready_line="$(grep -nF 'rollout status daemonset/mlflow-fsx-probe' "$DEPLOY" | cut -d: -f1)"
transition_apply_line="$(grep -nF 'kubectl apply -f "$TRANSITION_POLICY"' "$DEPLOY" | cut -d: -f1)"
probe_line="$(grep -nF 'run_job "$(deployment_job_name mlflow-artifact-storage-probe)"' "$DEPLOY" | cut -d: -f1)"
migration_line="$(grep -nF 'run_job "$(deployment_job_name mlflow-db-upgrade)"' "$DEPLOY" | cut -d: -f1)"
acceptance_line="$(grep -nF 'if ! run_job "$(deployment_job_name mlflow-artifact-acceptance)"' "$DEPLOY" | cut -d: -f1)"
cleanup_line="$(grep -nF 'if ! cleanup_legacy_dependencies' "$DEPLOY" | cut -d: -f1)"
verify_line="$(grep -nF 'ops/mlflow/verify.sh' "$DEPLOY" | tail -n1 | cut -d: -f1)"
if ! (( transition_apply_line < storage_apply_line &&
        storage_apply_line < storage_bound_line &&
        storage_bound_line < fsx_probe_apply_line &&
        fsx_probe_apply_line < fsx_probe_ready_line &&
        fsx_probe_ready_line < probe_line &&
        probe_line < migration_line &&
        migration_line < helm_line &&
        helm_line < acceptance_line &&
        acceptance_line < cleanup_line &&
        cleanup_line < verify_line )); then
  echo 'MLflow deploy phases must be storage probe, migration, atomic Helm, artifact acceptance, then cleanup' >&2
  exit 1
fi

cleanup_body="$(sed -n '/^cleanup_legacy_dependencies()/,/^}/p' "$DEPLOY")"
cleanup_policy_line="$(grep -nF '30-policy.yaml' <<<"$cleanup_body" | cut -d: -f1)"
cleanup_secret_line="$(grep -nF 'delete secret tos-credentials' <<<"$cleanup_body" | cut -d: -f1)"
cleanup_config_line="$(grep -nF 'delete configmap mlflow-aws-config' <<<"$cleanup_body" | cut -d: -f1)"
cleanup_transition_line="$(grep -nF 'delete networkpolicy mlflow-storage-migration' <<<"$cleanup_body" | cut -d: -f1)"
if ! (( cleanup_policy_line < cleanup_secret_line &&
        cleanup_secret_line < cleanup_config_line &&
        cleanup_config_line < cleanup_transition_line )); then
  echo 'MLflow cleanup must stage the strict policy before deleting legacy dependencies and transition egress' >&2
  exit 1
fi
[[ "$(grep -Ec 'recover_cleanup_failure; return 1' <<<"$cleanup_body")" == "4" ]] || {
  echo 'every MLflow cleanup failure must stop deletion immediately after restoring legacy dependencies' >&2
  exit 1
}

grep -Fq 'name: mlflow-storage-migration' "$TRANSITION_POLICY"
grep -Fq 'app.kubernetes.io/component: artifact-acceptance' "$TRANSITION_POLICY"
grep -Fq 'cidr: 100.64.0.0/10' "$TRANSITION_POLICY"
grep -Fq 'port: 443' "$TRANSITION_POLICY"
grep -Fq 'name: mlflow-postgres' "$TRANSITION_POLICY"
if grep -Fq 'namespaceSelector: {}' "$TRANSITION_POLICY"; then
  echo 'MLflow transition policy must not allow every namespace' >&2
  exit 1
fi

grep -Fq 'name: mlflow-artifact-acceptance' "$ACCEPTANCE"
grep -Fq 'http://mlflow:5000/mlflow' "$ACCEPTANCE"
grep -Fq 'create_experiment' "$ACCEPTANCE"
grep -Fq 'create_run' "$ACCEPTANCE"
grep -Fq 'get_artifact_repository' "$ACCEPTANCE"
grep -Fq 'log_artifact' "$ACCEPTANCE"
grep -Fq 'download_artifacts' "$ACCEPTANCE"
grep -Fq 'delete_artifacts' "$ACCEPTANCE"
grep -Fq 'list_artifacts' "$ACCEPTANCE"
if grep -Eq 'tos-credentials|AWS_|TOS_' "$ACCEPTANCE"; then
  echo 'MLflow artifact acceptance must use the tracking service without object-store credentials' >&2
  exit 1
fi

grep -Fq 'mlflow.mlflow-system.svc.cluster.local:5000' "$VALUES"
grep -Fq '172.28.*' "$VALUES"
if grep -Eq 'mlflow-aws-config|addressing_style = virtual|cidr: 100\.64\.0\.0/10' "$POLICY"; then
  echo 'MLflow policy must not configure S3 or permit direct TOS egress' >&2
  exit 1
fi
grep -Fq 'name: mlflow-ingest' "$POLICY"
grep -Fq 'return 403' "$POLICY"
grep -Fq 'location = /api/2.0/mlflow/runs/log-batch' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/experiments/get-by-name;' "$POLICY" || {
  echo 'MLflow ingest must translate the client API path to the server /mlflow prefix' >&2
  exit 1
}
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/experiments/create;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/create;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/update;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/get;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/log-batch;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/log-metric;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/log-parameter;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/set-tag;' "$POLICY"
grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000/mlflow/api/2.0/mlflow/runs/delete-tag;' "$POLICY"
if grep -Fq 'proxy_pass http://mlflow.mlflow-system.svc.cluster.local:5000;' "$POLICY"; then
  echo 'MLflow ingest must not forward an unprefixed API path to the prefixed server' >&2
  exit 1
fi
grep -Fq 'app.kubernetes.io/managed-by: ray-train-platform' "$POLICY"
grep -Fq 'key: ray.io/tenant-id' "$POLICY"
grep -Fq 'operator: Exists' "$POLICY"
grep -Fq 'kubernetes.io/metadata.name: ray-train-platform' "$POLICY"
grep -Fq 'app: ray-train-backend' "$POLICY"
grep -Fq 'name: mlflow-postgres' "$POLICY"
grep -Fq 'path: /metrics' "$POLICY"
grep -Fq 'kind: DaemonSet' "$FSX_PROBE"
grep -Fq 'name: mlflow-fsx-probe' "$FSX_PROBE"
grep -Fq 'name: mlflow-fsx-dns-probe' "$FSX_PROBE"
grep -Fq 'claimName: mlflow-artifacts-irsa' "$FSX_PROBE"
grep -Fq 'accelerator: nvidia-rtx-4090' "$FSX_PROBE"
grep -Fq 'accelerator: nvidia-rtx-4090' "$BOOTSTRAP"
grep -Fq 'accelerator: nvidia-rtx-4090' "$ACCEPTANCE"
grep -Fq 'platform.wellspiking.ai/gpu-pool: production' "$FSX_PROBE"
grep -Fq 'platform.wellspiking.ai/gpu-pool: production' "$BOOTSTRAP"
grep -Fq 'platform.wellspiking.ai/gpu-pool: production' "$ACCEPTANCE"
grep -Fq 'platform.wellspiking.ai/pool: control-plane' "$DATABASE"
grep -Fq 'platform.wellspiking.ai/pool: control-plane' "$POLICY"
grep -Fq 'platform.wellspiking.ai/pool: control-plane' "$DB_UPGRADE"
grep -Fq 'platform.wellspiking.ai/pool: control-plane' "$SMOKE"
if grep -Fq 'nvidia.com/gpu' "$FSX_PROBE" "$BOOTSTRAP" "$ACCEPTANCE"; then
  echo 'MLflow probes and acceptance jobs must not reserve a GPU device' >&2
  exit 1
fi
grep -Fq 'dnsPolicy: Default' "$FSX_PROBE"
grep -Fq 'nslookup "$endpoint" "$resolver"' "$FSX_PROBE"
grep -Fq '192.168.110.61/32' "$FSX_PROBE"
grep -Fq '192.168.111.63/32' "$FSX_PROBE"
grep -Fq '100.96.0.2/32' "$FSX_PROBE"
grep -Fq '100.96.0.3/32' "$FSX_PROBE"
grep -Fq 'getent ahostsv4 "$endpoint"' "$FSX_PROBE"
grep -Fq 'timeout 10' "$FSX_PROBE"
grep -Fq 'memory: 8Mi' "$FSX_PROBE"
grep -Fq 'stat "$directory"' "$FSX_PROBE"
grep -Fq 'printf' "$FSX_PROBE"
grep -Fq 'rm -f' "$FSX_PROBE"
grep -Fq 'readinessProbe:' "$FSX_PROBE"
grep -Fq 'kind: PrometheusRule' "$FSX_PROBE"
grep -Fq 'release: prometheus' "$FSX_PROBE"
grep -Fq 'MLflowFSXMountUnavailable' "$FSX_PROBE"
grep -Fq 'MLflowFSXProbeUnschedulable' "$FSX_PROBE"
grep -Fq 'MLflowFSXDNSUnavailable' "$FSX_PROBE"
grep -Fq 'MLflowFSXDNSProbeUnschedulable' "$FSX_PROBE"
grep -Fq 'kube_pod_status_unschedulable' "$FSX_PROBE"
grep -Fq 'kube_pod_status_scheduled' "$FSX_PROBE"
grep -Fq '== bool 0' "$FSX_PROBE"
grep -Fq 'MLflowFSXProbeHasNoTargets' "$FSX_PROBE"
grep -Fq '== 0' "$FSX_PROBE"
grep -Fq 'policyTypes: [Ingress, Egress]' "$FSX_PROBE"
grep -Fq 'automountServiceAccountToken: false' "$FSX_PROBE"
grep -Fq 'readonly FSX_HEALTH_PROBE=' "$DEPLOY"
grep -Fq 'kubectl apply -f "$FSX_HEALTH_PROBE"' "$DEPLOY"
grep -Fq 'rollout status daemonset/mlflow-fsx-probe' "$DEPLOY"
grep -Fq 'rollout status daemonset/mlflow-fsx-dns-probe' "$DEPLOY"
grep -Fq 'FSX probe has no matching MLflow serving nodes' "$DEPLOY"
grep -Fq 'FSX DNS probe has no matching MLflow serving nodes' "$DEPLOY"
grep -Fq 'daemonset mlflow-fsx-probe' "$VERIFY"
grep -Fq 'daemonset mlflow-fsx-dns-probe' "$VERIFY"
grep -Fq 'FSX probe has no matching MLflow serving nodes' "$VERIFY"
grep -Fq 'FSX DNS probe has no matching MLflow serving nodes' "$VERIFY"
grep -Fq 'MLflow deployment is not restricted to the production GPU worker pool' "$VERIFY"
grep -Fq 'MLflow Pod requested an nvidia.com/gpu device' "$VERIFY"
grep -Fq '.metadata.deletionTimestamp' "$VERIFY"
grep -Fq '[[ -n "$deletion_timestamp" ]] && continue' "$VERIFY"
grep -Fq 'mlflow-ingest.mlflow-system.svc.cluster.local:8080' "$SMOKE"
grep -Fq 'MLFLOW_ARTIFACT_DOWNLOAD_BLOCKED' "$SMOKE"
grep -Fq 'namespace: mlflow-system' "$SMOKE"
grep -Fq 'app.kubernetes.io/name: mlflow-client-smoke' "$POLICY"
grep -Fq 'readonly CLIENT_SMOKE=' "$DEPLOY"
grep -Fq 'run_job "$(deployment_job_name mlflow-client-smoke)" "$CLIENT_SMOKE"' "$DEPLOY"
strict_policy_apply_line="$(grep -nF 'kubectl apply -f "${ROOT_DIR}/ops/mlflow/30-policy.yaml"' "$DEPLOY" | head -n1 | cut -d: -f1)"
client_smoke_line="$(grep -nF 'run_job "$(deployment_job_name mlflow-client-smoke)" "$CLIENT_SMOKE"' "$DEPLOY" | cut -d: -f1)"
if ! (( strict_policy_apply_line < client_smoke_line && client_smoke_line < cleanup_line )); then
  echo 'MLflow deploy must apply the strict gateway, run the real client smoke, then clean legacy dependencies' >&2
  exit 1
fi
if grep -Fq 'mlflow.log_dict' "$SMOKE"; then
  echo 'training clients must not use MLflow as an artifact download surface' >&2
  exit 1
fi
if grep -Fq 'namespaceSelector: {}' "$POLICY"; then
  echo 'MLflow policy must not allow every namespace' >&2
  exit 1
fi
if grep -Eq '^[[:space:]]*type:[[:space:]]*(NodePort|LoadBalancer)[[:space:]]*$' "$VALUES"; then
  echo 'MLflow service must remain internal-only' >&2
  exit 1
fi
if grep -Fq 'static-prefix' "$VALUES"; then
  echo 'MLflow static prefix must use server.staticPrefix, not a flag_options duplicate' >&2
  exit 1
fi
grep -Fq -- '--static-prefix=/mlflow' "$VERIFY"
grep -Fq 'containers[?(@.name=="mlflow")].command' "$VERIFY" || {
  echo 'MLflow live verification must inspect the mlflow container command' >&2
  exit 1
}
grep -Fq 'containers[?(@.name=="mlflow")].args' "$VERIFY" || {
  echo 'MLflow live verification must inspect the mlflow container args' >&2
  exit 1
}
grep -Fq 'pvc mlflow-artifacts-irsa' "$VERIFY"
grep -Fq 'claimName' "$VERIFY"
grep -Fq 'mountPath' "$VERIFY"
grep -Fq 'MLFLOW_ARTIFACTS_DESTINATION' "$VERIFY"
grep -Fq 'file:///mlflow-artifacts' "$VERIFY"
grep -Fq 'AWS_|TOS_|MLFLOW_(S3|BOTO)' "$VERIFY"
grep -Fq 'get pv mlflow-artifacts-irsa-pv' "$VERIFY"
grep -Fq 'secretName|secretNamespace|tos-fsx-credentials' "$VERIFY"
grep -Fq 'networkpolicy mlflow-storage-migration' "$VERIFY"
grep -Fq '/proxy/mlflow/health' "$VERIFY"
grep -Fq ".spec.replicas" "$VERIFY"
if grep -Fq 'get ingress' "$VERIFY"; then
  echo 'MLflow live verification must not depend on direct Ingress ownership' >&2
  exit 1
fi
grep -Fq 'FSX CSI' "$README"
grep -Fq 'CREDENTIALS_TYPE=IRSA' "$README"
grep -Fq 'ROLE_NAME_FOR_IRSA' "$README"
grep -Fq 'DaemonSet' "$README"
grep -Fq 'mlflow-artifacts-irsa' "$README"
grep -Fq '回滚通道' "$README"
grep -Fq 'Artifact CRUD' "$README"
grep -Fq '自动回滚' "$README"
echo 'MLflow delivery contract verified'
