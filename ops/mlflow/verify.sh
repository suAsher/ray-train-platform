#!/usr/bin/env bash
set -euo pipefail

readonly NAMESPACE="mlflow-system"

for command in kubectl helm; do
  command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

helm -n "$NAMESPACE" status mlflow >/dev/null
kubectl -n "$NAMESPACE" rollout status deployment/mlflow --timeout=5m
kubectl -n "$NAMESPACE" rollout status deployment/mlflow-ingest --timeout=5m
kubectl -n "$NAMESPACE" rollout status statefulset/mlflow-postgres --timeout=5m
kubectl -n "$NAMESPACE" rollout status daemonset/mlflow-fsx-dns-probe --timeout=5m
kubectl -n "$NAMESPACE" rollout status daemonset/mlflow-fsx-probe --timeout=5m

services="$(kubectl -n "$NAMESPACE" get services -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.type}{"\n"}{end}')"
while IFS=$'\t' read -r service_name service_type; do
  [[ -n "$service_name" ]] || continue
  [[ "$service_type" == "ClusterIP" ]] || {
    echo "service ${service_name} in ${NAMESPACE} is not ClusterIP: ${service_type}" >&2
    exit 1
  }
done <<<"$services"

[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.replicas}')" == "2" ]] || {
  echo "MLflow deployment is not configured for two replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.status.availableReplicas}')" == "2" ]] || {
  echo "MLflow does not have two available replicas" >&2
  exit 1
}
mlflow_node_selector="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.nodeSelector}')"
if [[ -n "$mlflow_node_selector" && "$mlflow_node_selector" != "{}" ]]; then
  echo "MLflow deployment has a hard nodeSelector: ${mlflow_node_selector}" >&2
  exit 1
fi
mlflow_deployment="$(kubectl -n "$NAMESPACE" get deployment mlflow -o yaml)"
for expected in \
  preferredDuringSchedulingIgnoredDuringExecution \
  requiredDuringSchedulingIgnoredDuringExecution \
  platform.wellspiking.ai/pool \
  control-plane \
  virtual-node \
  virtual-kubelet; do
  grep -Fq "$expected" <<<"$mlflow_deployment" || {
    echo "MLflow deployment scheduling contract is missing ${expected}" >&2
    exit 1
  }
done
mlflow_pods="$(kubectl -n "$NAMESPACE" get pods -l 'app.kubernetes.io/name=mlflow,app.kubernetes.io/instance=mlflow' -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.nodeName}{"\t"}{.metadata.deletionTimestamp}{"\n"}{end}')"
while IFS=$'\t' read -r pod_name node_name deletion_timestamp; do
  [[ -n "$pod_name" && -n "$node_name" ]] || continue
  [[ -n "$deletion_timestamp" ]] && continue
  node_instance_type="$(kubectl get node "$node_name" -o jsonpath='{.metadata.labels.node\.kubernetes\.io/instance-type}')"
  node_type="$(kubectl get node "$node_name" -o jsonpath='{.metadata.labels.type}')"
  if [[ "$node_instance_type" == "virtual-node" || "$node_type" == "virtual-kubelet" ]]; then
    echo "MLflow Pod ${pod_name} is running on excluded virtual node ${node_name}" >&2
    exit 1
  fi
done <<<"$mlflow_pods"
[[ "$(kubectl -n "$NAMESPACE" get deployment mlflow-ingest -o jsonpath='{.status.availableReplicas}')" == "2" ]] || {
  echo "MLflow ingest gateway does not have two available replicas" >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get pvc mlflow-artifacts-irsa -o jsonpath='{.status.phase}')" == "Bound" ]] || {
  echo "MLflow artifact PVC is not Bound" >&2
  exit 1
}
artifact_pv="$(kubectl get pv mlflow-artifacts-irsa-pv -o yaml)"
if grep -Eq 'secretName|secretNamespace|tos-fsx-credentials' <<<"$artifact_pv"; then
  echo 'MLflow artifact PV contains a static FSX credential reference' >&2
  exit 1
fi
mlflow_command="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].command}')"
mlflow_args="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].args}')"
grep -Fq -- '--static-prefix=/mlflow' <<<"${mlflow_command} ${mlflow_args}" || {
  echo 'MLflow deployment is missing --static-prefix=/mlflow' >&2
  exit 1
}
artifact_claim="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.volumes[?(@.name=="mlflow-artifacts")].persistentVolumeClaim.claimName}')"
[[ "$artifact_claim" == "mlflow-artifacts-irsa" ]] || {
  echo "MLflow deployment does not reference the artifact PVC: ${artifact_claim}" >&2
  exit 1
}
artifact_mount="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].volumeMounts[?(@.name=="mlflow-artifacts")].mountPath}')"
[[ "$artifact_mount" == "/mlflow-artifacts" ]] || {
  echo "MLflow artifact PVC is not mounted at /mlflow-artifacts: ${artifact_mount}" >&2
  exit 1
}
artifact_destination="$(kubectl -n "$NAMESPACE" get deployment mlflow -o jsonpath='{.spec.template.spec.containers[?(@.name=="mlflow")].env[?(@.name=="MLFLOW_ARTIFACTS_DESTINATION")].value}')"
[[ "$artifact_destination" == "file:///mlflow-artifacts" ]] || {
  echo "unexpected MLflow artifact root: ${artifact_destination}" >&2
  exit 1
}
if grep -Fq 'nvidia.com/gpu' <<<"$mlflow_deployment"; then
  echo 'MLflow Pod requested an nvidia.com/gpu device' >&2
  exit 1
fi
if grep -Eq 'AWS_|TOS_|MLFLOW_(S3|BOTO)|tos-credentials|mlflow-aws-config|/etc/mlflow/aws' <<<"$mlflow_deployment"; then
  echo 'MLflow Pod still contains AWS/TOS credentials or configuration' >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get secret tos-credentials >/dev/null 2>&1; then
  echo 'shared tos-credentials Secret still exists in mlflow-system' >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get configmap mlflow-aws-config >/dev/null 2>&1; then
  echo 'legacy MLflow AWS config still exists in mlflow-system' >&2
  exit 1
fi
if kubectl -n "$NAMESPACE" get networkpolicy mlflow-storage-migration >/dev/null 2>&1; then
  echo 'temporary MLflow storage migration egress is still enabled' >&2
  exit 1
fi

kubectl -n "$NAMESPACE" get networkpolicy mlflow >/dev/null
kubectl -n "$NAMESPACE" get networkpolicy mlflow-ingest >/dev/null
kubectl -n "$NAMESPACE" get poddisruptionbudget mlflow >/dev/null
kubectl -n "$NAMESPACE" get servicemonitor mlflow >/dev/null
kubectl -n "$NAMESPACE" get prometheusrule mlflow-fsx-probe >/dev/null
kubectl -n "$NAMESPACE" get networkpolicy mlflow-fsx-probe >/dev/null
kubectl -n "$NAMESPACE" get networkpolicy mlflow-fsx-dns-probe >/dev/null
fsx_dns_probe_desired="$(kubectl -n "$NAMESPACE" get daemonset mlflow-fsx-dns-probe -o jsonpath='{.status.desiredNumberScheduled}')"
if [[ ! "$fsx_dns_probe_desired" =~ ^[1-9][0-9]*$ ]]; then
  echo 'FSX DNS probe has no matching MLflow serving nodes' >&2
  exit 1
fi
[[ "$(kubectl -n "$NAMESPACE" get daemonset mlflow-fsx-dns-probe -o jsonpath='{.status.numberReady}')" == "$fsx_dns_probe_desired" ]] || {
  echo 'MLflow FSX DNS probe is not Ready on every MLflow serving node' >&2
  exit 1
}
fsx_probe_desired="$(kubectl -n "$NAMESPACE" get daemonset mlflow-fsx-probe -o jsonpath='{.status.desiredNumberScheduled}')"
if [[ ! "$fsx_probe_desired" =~ ^[1-9][0-9]*$ ]]; then
  echo 'FSX probe has no matching MLflow serving nodes' >&2
  exit 1
fi
[[ "$(kubectl -n "$NAMESPACE" get daemonset mlflow-fsx-probe -o jsonpath='{.status.numberReady}')" == "$fsx_probe_desired" ]] || {
  echo 'MLflow FSX probe is not Ready on every MLflow serving node' >&2
  exit 1
}
[[ "$(kubectl -n "$NAMESPACE" get pvc data-mlflow-postgres-0 -o jsonpath='{.status.phase}')" == "Bound" ]]

health="$(kubectl get --raw '/api/v1/namespaces/mlflow-system/services/http:mlflow:5000/proxy/mlflow/health')"
[[ "$health" == "OK" || "$health" == *'status'* ]] || { echo "unexpected MLflow health response: ${health}" >&2; exit 1; }
echo "MLflow production deployment verified"
