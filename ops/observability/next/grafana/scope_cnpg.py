#!/usr/bin/env python3
"""Adapt the reviewed CNPG renderer to Grafana without changing Nightingale.

Input is the complete cloudnative-pg 0.29.1 chart rendered in grafana with
cnpg-values.yaml. Existing CRDs stay separately owned. Only cluster-scoped
RBAC and webhooks gain a namespace suffix; official Service/DNS names remain
namespace-local. Emit nothing until every document passes validation.
"""

import importlib.util
import json
from pathlib import Path
import sys

import yaml

ROOT = Path(__file__).resolve().parents[1]
NAMESPACE = "grafana"
OPERATOR = "cloudnative-pg-operator"
PRIORITY = "raytrain-observability-additive"
IMAGE = ("harbor.wellspiking.ai/guofeng.su/cnpg-observability:"
         "1.30.1-mtls-names-3ca43d7@sha256:"
         "71e30bd615ad4811099d6141c787c172f3e4f16f9ab0bf673146bd1d1e020c6f")
VMAGENT_URI = "spiffe://observability.raytrain.local/victoriametrics/vmagent"
CLUSTER_ROLES = frozenset((OPERATOR, OPERATOR + "-view", OPERATOR + "-edit"))
NAMESPACED_KINDS = frozenset(("ServiceAccount", "Role", "RoleBinding", "Service",
                              "ConfigMap", "Deployment", "PodDisruptionBudget"))


def load_module(name, path):
    """Load an isolated module so namespace overrides cannot affect another caller."""
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


base = load_module("grafana_cnpg_base", ROOT / "nightingale" / "scope-cnpg-webhooks.py")
base.NAMESPACE = NAMESPACE
# Keep CONFIG at the reviewed Nightingale templates: all file references are
# namespace-local, and the sole upstream is 127.0.0.1:8080, with no old DNS name.
sds = load_module("grafana_cnpg_sds", ROOT / "mtls" / "sds.py")


def validate_binding(document, cluster=False):
    expected_ref = {"apiGroup": "rbac.authorization.k8s.io",
                    "kind": "ClusterRole" if cluster else "Role", "name": OPERATOR}
    expected_subjects = [{"kind": "ServiceAccount", "name": OPERATOR, "namespace": NAMESPACE}]
    if (document["metadata"].get("name") != OPERATOR
            or document.get("roleRef") != expected_ref
            or document.get("subjects") != expected_subjects):
        raise ValueError("unexpected Grafana CNPG binding or subject")


def validate_deployment(document):
    specification = document["spec"]
    pod = specification["template"]["spec"]
    manager = pod["containers"][0]
    env = {item["name"]: item for item in manager.get("env", [])}
    if (specification.get("replicas") != 2 or pod.get("priorityClassName") != PRIORITY
            or pod.get("serviceAccountName") != OPERATOR
            or any(pod.get(field) for field in ("hostNetwork", "hostPID", "hostIPC"))
            or manager.get("image") != IMAGE
            or env.get("WATCH_NAMESPACE") != {"name": "WATCH_NAMESPACE", "value": NAMESPACE}
            or "--config-map-name=" + OPERATOR + "-config" not in manager.get("args", [])):
        raise ValueError("Grafana CNPG must use reviewed image, namespace, HA and priority settings")


def validate_service(document):
    specification = document.get("spec", {})
    if (document["metadata"].get("name") != "cnpg-webhook-service"
            or specification.get("type") != "ClusterIP"
            or specification.get("externalIPs") or specification.get("externalName")
            or specification.get("ports") != [{"port": 443, "targetPort": "webhook-server",
                                               "name": "webhook-server"}]):
        raise ValueError("unexpected CNPG webhook Service or exposure")


def validate_document(document):
    kind, metadata = document.get("kind"), document.get("metadata", {})
    name = metadata.get("name")
    if not isinstance(name, str) or not name:
        raise ValueError("CNPG document requires a resource name")
    if kind in NAMESPACED_KINDS:
        if metadata.get("namespace") != NAMESPACE:
            raise ValueError("Grafana CNPG resources must stay in grafana")
    elif kind in ("ClusterRole", "ClusterRoleBinding", *base.WEBHOOK_NAMES):
        if metadata.get("namespace"):
            raise ValueError("cluster-scoped CNPG resource has a namespace")
    else:
        raise ValueError("unexpected CNPG kind; existing CRDs must stay separately owned")
    if kind in ("ServiceAccount", "Role", "RoleBinding", "Deployment", "PodDisruptionBudget") and name != OPERATOR:
        raise ValueError("unexpected namespaced CNPG resource name")
    if kind == "ClusterRole" and name not in CLUSTER_ROLES:
        raise ValueError("unexpected CNPG ClusterRole")
    if kind in ("ClusterRoleBinding", "RoleBinding"):
        validate_binding(document, kind == "ClusterRoleBinding")
    elif kind == "Deployment":
        validate_deployment(document)
    elif kind == "Service":
        validate_service(document)
    elif kind in base.WEBHOOK_NAMES:
        hooks = document.get("webhooks", [])
        if not hooks or any(hook.get("clientConfig", {}).get("url")
                            or hook.get("clientConfig", {}).get("service", {}).get("name") != "cnpg-webhook-service"
                            for hook in hooks):
            raise ValueError("CNPG admission must use its namespace-local official Service")
    elif kind == "ConfigMap" and name == OPERATOR + "-config":
        if document.get("data", {}).get("WATCH_NAMESPACE") != NAMESPACE:
            raise ValueError("CNPG config must watch only grafana")


def rename_cluster_rbac(document):
    kind = document["kind"]
    if kind not in ("ClusterRole", "ClusterRoleBinding"):
        return document
    metadata = {**document["metadata"], "name": document["metadata"]["name"] + "-" + NAMESPACE}
    output = {**document, "metadata": metadata}
    if kind == "ClusterRoleBinding":
        output = {**output, "roleRef": {**document["roleRef"], "name": OPERATOR + "-" + NAMESPACE}}
    return output


def validate_metrics_mounts(document):
    pod = document["spec"]["template"]["spec"]
    volumes = {item["name"]: item for item in pod.get("volumes", [])}
    proxies = [item for item in pod["containers"] if item["name"] == "envoy"]
    expected_mounts = [
        {"name": "obs-mtls-identity", "mountPath": "/etc/obs-mtls", "readOnly": True},
        {"name": "obs-mtls-config", "mountPath": "/etc/envoy", "readOnly": True},
    ]
    if (len(proxies) != 1 or proxies[0].get("volumeMounts") != expected_mounts
            or volumes.get("obs-mtls-identity") != {"name": "obs-mtls-identity", "secret": {
                "secretName": "obs-mtls-cnpg-operator", "defaultMode": 0o440}}
            or volumes.get("obs-mtls-config") != {"name": "obs-mtls-config", "configMap": {
                "name": OPERATOR + "-mtls"}}):
        raise ValueError("CNPG metrics requires directory-mounted Grafana identity and SDS config")


def dynamic_metrics(document):
    """Retain exact vmagent HTTP authorization and rotate the leaf through SDS."""
    bootstrap = yaml.safe_load(document["data"]["envoy.yaml"])
    listeners = bootstrap["static_resources"]["listeners"]
    clusters = bootstrap["static_resources"]["clusters"]
    if len(listeners) != 1 or len(clusters) != 1:
        raise ValueError("unexpected CNPG metrics proxy topology")
    chain = listeners[0]["filter_chains"]
    if len(chain) != 1:
        raise ValueError("unexpected CNPG metrics filter chains")
    tls = chain[0]["transport_socket"]["typed_config"]
    common = tls["common_tls_context"]
    http = chain[0]["filters"][0]["typed_config"]
    rbac = [item["typed_config"] for item in http["http_filters"] if item["name"] == "envoy.filters.http.rbac"]
    expected_principal = [{"authenticated": {"principal_name": {"exact": VMAGENT_URI}}}]
    if (tls.get("require_client_certificate") is not True
            or common.get("tls_params") != {"tls_minimum_protocol_version": "TLSv1_3"}
            or common.get("tls_certificates") != [{"certificate_chain": {"filename": "/etc/obs-mtls/tls.crt"},
                                                   "private_key": {"filename": "/etc/obs-mtls/tls.key"}}]
            or common.get("validation_context") != {"trusted_ca": {"filename": "/etc/obs-mtls/ca.crt"}}
            or len(rbac) != 1 or rbac[0].get("rules", {}).get("action") != "ALLOW"
            or set(rbac[0]["rules"].get("policies", {})) != {"monitoring"}
            or rbac[0]["rules"]["policies"]["monitoring"].get("principals") != expected_principal):
        raise ValueError("CNPG metrics must require the exact reviewed vmagent identity")
    endpoint = clusters[0]["load_assignment"]["endpoints"]
    expected_endpoint = [{"lb_endpoints": [{"endpoint": {"address": {"socket_address": {
        "address": "127.0.0.1", "port_value": 8080}}}}]}]
    if endpoint != expected_endpoint or clusters[0].get("type") != "STATIC":
        raise ValueError("CNPG metrics upstream must remain loopback")
    converted, files = sds.transform_bootstrap(bootstrap, "/etc/envoy",
                                              node_id="grafana.cnpg-operator", node_cluster=NAMESPACE)
    if not files:
        raise ValueError("CNPG metrics identity requires dynamic SDS")
    data = {"envoy.yaml": yaml.safe_dump(converted, sort_keys=False),
            **{name: json.dumps(value, sort_keys=True, separators=(",", ":")) for name, value in files.items()}}
    return {**document, "data": data}


def transform(documents):
    documents = [document for document in documents if document is not None]
    if any(not isinstance(document, dict) for document in documents):
        raise ValueError("expected a Kubernetes document stream")
    identities = [(document.get("kind"), document.get("metadata", {}).get("name")) for document in documents]
    if len(set(identities)) != len(identities):
        raise ValueError("duplicate CNPG resource")
    for document in documents:
        validate_document(document)
    required = {("ClusterRole", name) for name in CLUSTER_ROLES} | {
        ("ClusterRoleBinding", OPERATOR), ("Role", OPERATOR), ("RoleBinding", OPERATOR),
        ("ServiceAccount", OPERATOR), ("Service", "cnpg-webhook-service"),
        ("ConfigMap", OPERATOR + "-config"), ("Deployment", OPERATOR),
    }
    if not required.issubset(identities):
        raise ValueError("expected the complete namespace-scoped CNPG chart")
    output = [rename_cluster_rbac(document) for document in base.transform(documents)]
    for document in output:
        if document["kind"] == "Deployment":
            validate_metrics_mounts(document)
    return [dynamic_metrics(document) if (document["kind"], document["metadata"]["name"])
            == ("ConfigMap", OPERATOR + "-mtls") else document for document in output]


def main():
    try:
        output = transform(yaml.safe_load_all(sys.stdin))
    except (ValueError, KeyError, TypeError, IndexError, AttributeError, yaml.YAMLError):
        print("Grafana CNPG validation failed; no manifests emitted", file=sys.stderr)
        return 1
    sys.stdout.write(yaml.safe_dump_all(output, sort_keys=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
