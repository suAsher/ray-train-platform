#!/usr/bin/env bash
set -euo pipefail

readonly root_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly chart_dir="${root_dir}/helm/ray-train-platform"
readonly test_profile="${root_dir}/deploy/profiles/test.yaml"
readonly scratch="$(mktemp -d)"
trap 'rm -rf -- "$scratch"' EXIT

for command in helm python3; do
  command -v "$command" >/dev/null || { echo "missing command: ${command}" >&2; exit 1; }
done

cat >"$scratch/sync.yaml" <<'YAML'
storageSync:
  enabled: true
  image:
    digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  bucket: test-bucket
  region: cn-shanghai
  endpoint: https://tos-cn-shanghai.ivolces.com
  credentialSecret: preexisting-tos-config
  serviceAccountName: ray-storage-sync
  callbackBaseURL: ""
  nodeSelector:
    kubernetes.io/hostname: verified-cpu-node
  workClaimName: ray-storage-sync-work
  workVolume:
    create: true
    storageClassName: test-csi
global:
  imagePullSecrets:
    - name: preexisting-registry
YAML

render() {
  helm template ray-platform "$chart_dir" --namespace ray-train-platform \
    --values "$test_profile" --values "$scratch/sync.yaml" "$@"
}

render >"$scratch/legacy.yaml"
render --set storageSync.namespace=ray-train-sync --set storageSync.createNamespace=true \
  --set 'storageSync.gcNamespaces={ray-train-platform,ray-train-platform,ray-train-sync}' >"$scratch/isolated.yaml"
render --set storageSync.namespace=ray-train-sync --set storageSync.createNamespace=false \
  --set storageSync.callbackBaseURL=https://callback.example.test >"$scratch/preexisting.yaml"
render --set storageSync.enabled=false >"$scratch/disabled.yaml"

python3 - "$scratch" <<'PY'
import json
import pathlib
import sys
import yaml

root = pathlib.Path(sys.argv[1])

def documents(name):
    return [doc for doc in yaml.safe_load_all((root / f"{name}.yaml").read_text()) if doc]

def find(docs, kind, name, namespace=None):
    matches = [doc for doc in docs if doc["kind"] == kind and doc["metadata"]["name"] == name
               and (namespace is None or doc["metadata"].get("namespace") == namespace)]
    assert len(matches) == 1, (kind, name, namespace, len(matches))
    return matches[0]

def environment(docs):
    deployment = find(docs, "Deployment", "ray-train-backend", "ray-train-platform")
    return {entry["name"]: entry.get("value") for entry in deployment["spec"]["template"]["spec"]["containers"][0]["env"]}

for name, namespace in [("legacy", "ray-train-platform"), ("isolated", "ray-train-sync")]:
    docs = documents(name)
    env = environment(docs)
    assert env["STORAGE_SYNC_NAMESPACE"] == namespace
    assert env["STORAGE_SYNC_CALLBACK_BASE_URL"] == "http://ray-train-backend.ray-train-platform.svc.cluster.local:8080"
    assert env["STORAGE_SYNC_GC_SUCCEEDED_TTL_SECONDS"] == "3600"
    assert env["STORAGE_SYNC_GC_FAILED_TTL_SECONDS"] == "86400"
    expected_gc = [namespace] + (["ray-train-platform"] if name == "isolated" else [])
    assert json.loads(env["STORAGE_SYNC_GC_NAMESPACES_JSON"]) == expected_gc
    worker = find(docs, "ServiceAccount", "ray-storage-sync", namespace)
    assert worker["automountServiceAccountToken"] is False
    assert worker["imagePullSecrets"] == [{"name": "preexisting-registry"}]
    volume = find(docs, "PersistentVolumeClaim", "ray-storage-sync-work", namespace)
    assert volume["metadata"]["annotations"]["helm.sh/resource-policy"] == "keep"
    role = find(docs, "Role", "ray-platform-storage-sync-controller", namespace)
    permissions = {resource: set(rule["verbs"]) for rule in role["rules"] for resource in rule["resources"]}
    assert permissions["jobs"] == {"get", "list", "create", "patch"}
    assert permissions["secrets"] == {"get", "create", "patch"}
    assert "delete" not in set().union(*permissions.values())
    binding = find(docs, "RoleBinding", "ray-platform-storage-sync-controller", namespace)
    assert binding["subjects"] == [{"kind": "ServiceAccount", "name": "ray-train-platform-sa", "namespace": "ray-train-platform"}]
    assert not any(doc["kind"] == "Secret" and doc["metadata"]["name"] in {"preexisting-tos-config", "preexisting-registry"} for doc in docs)

isolated = documents("isolated")
namespace = find(isolated, "Namespace", "ray-train-sync")
assert namespace["metadata"]["annotations"]["helm.sh/resource-policy"] == "keep"
assert namespace["metadata"]["labels"]["app.kubernetes.io/component"] == "storage-sync"
gc = find(isolated, "Role", "ray-platform-storage-sync-gc", "ray-train-platform")
permissions = {resource: set(rule["verbs"]) for rule in gc["rules"] for resource in rule["resources"]}
assert permissions == {"jobs": {"get", "list", "patch"}, "pods": {"get", "list"}, "secrets": {"get", "patch"}}
binding = find(isolated, "RoleBinding", "ray-platform-storage-sync-gc", "ray-train-platform")
assert binding["subjects"][0]["namespace"] == "ray-train-platform"
assert not any(doc["kind"] == "ServiceAccount" and doc["metadata"]["name"] == "ray-storage-sync" and doc["metadata"].get("namespace") == "ray-train-platform" for doc in isolated)
preexisting = documents("preexisting")
assert not any(doc["kind"] == "Namespace" and doc["metadata"]["name"] == "ray-train-sync" for doc in preexisting)
assert environment(preexisting)["STORAGE_SYNC_CALLBACK_BASE_URL"] == "https://callback.example.test"
disabled = documents("disabled")
assert not any(doc["metadata"]["name"].startswith(("ray-platform-storage-sync", "ray-storage-sync")) for doc in disabled)
print("storage sync namespace, RBAC, retention and credential boundary render contracts passed")
PY

for invalid in \
  'storageSync.namespace=invalid.namespace' \
  'storageSync.gcNamespaces={*}' \
  'storageSync.gcSucceededTTLSeconds=0' \
  'storageSync.gcFailedTTLSeconds=604801'; do
  if render --set "$invalid" >"$scratch/rejected.yaml" 2>"$scratch/rejected.log"; then
    echo "unsafe storage sync Helm configuration accepted: $invalid" >&2
    exit 1
  fi
done
