#!/usr/bin/env python3
"""Scope fixed CNPG webhooks and protect the fixed operator's metrics listener.

This renderer fails until a derived digest is built and reviewed. It scopes the
fixed operator's webhook RBAC, protects metrics, and appends its ConfigMap/Service.
"""

from pathlib import Path
import re
import sys

import yaml

NAMESPACE = "nightingale"
CONFIG = Path(__file__).resolve().parent / "config"
DERIVED_IMAGE = re.compile(
    r"^harbor\.wellspiking\.ai/guofeng\.su/cnpg-observability:"
    r"[a-zA-Z0-9_.-]+@sha256:[0-9a-f]{64}$"
)
WEBHOOK_NAMES = {
    "MutatingWebhookConfiguration": "cnpg-mutating-webhook-configuration",
    "ValidatingWebhookConfiguration": "cnpg-validating-webhook-configuration",
}
UPSTREAM_CLUSTER_RULES = [
    {"apiGroups": [""], "resources": ["nodes"], "verbs": ["get", "list", "watch"]},
    {"apiGroups": ["admissionregistration.k8s.io"],
     "resources": ["mutatingwebhookconfigurations", "validatingwebhookconfigurations"], "verbs": ["get", "patch"]},
    {"apiGroups": ["postgresql.cnpg.io"], "resources": ["clusterimagecatalogs"], "verbs": ["get", "list", "watch"]},
]
AUXILIARY_RESOURCES = ["backups", "clusters", "clusters/status", "databaseroles", "databases",
                       "failoverquorums", "poolers", "publications", "scheduledbackups",
                       "imagecatalogs", "clusterimagecatalogs", "subscriptions"]


def scope_webhook(document):
    if document.get("metadata", {}).get("name") != WEBHOOK_NAMES.get(document.get("kind")):
        raise ValueError("unexpected CNPG webhook configuration name")
    hooks = []
    for hook in document.get("webhooks", []):
        if hook.get("clientConfig", {}).get("service", {}).get("namespace") != NAMESPACE:
            raise ValueError("unexpected CNPG webhook namespace")
        hooks.append({
            **hook,
            "rules": [{**rule, "scope": "Namespaced"} for rule in hook.get("rules", [])],
            "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": NAMESPACE}},
        })
    return {**document, "webhooks": hooks}


def scope_operator_role(document):
    metadata = document.get("metadata", {})
    if (document.get("kind") != "ClusterRole" or metadata.get("name") != "cloudnative-pg-operator"
            or metadata.get("namespace") or document.get("aggregationRule")
            or document.get("rules") != UPSTREAM_CLUSTER_RULES):
        raise ValueError("unexpected fixed-chart CNPG operator ClusterRole shape")
    scoped = [{"apiGroups": ["admissionregistration.k8s.io"],
               "resources": [mode + "webhookconfigurations"], "verbs": ["get", "patch"],
               "resourceNames": ["cnpg-" + mode + "-webhook-configuration"]}
              for mode in ("mutating", "validating")]
    return {**document, "rules": [document["rules"][0], *scoped, document["rules"][2]]}


def validate_auxiliary_role(document):
    # Fixed chart emits these inert, unbound helpers even with aggregation off.
    # Reject unrelated ClusterRoles rather than silently forwarding new powers.
    metadata = document.get("metadata", {})
    name = metadata.get("name")
    verbs = {"cloudnative-pg-operator-view": ["get", "list", "watch"],
             "cloudnative-pg-operator-edit": ["create", "delete", "deletecollection", "patch", "update"]}
    expected = [{"apiGroups": ["postgresql.cnpg.io"], "resources": AUXILIARY_RESOURCES,
                 "verbs": verbs.get(name)}]
    if (name not in verbs or metadata.get("namespace") or document.get("aggregationRule")
            or any(label.startswith("rbac.authorization.k8s.io/aggregate-to-") for label in metadata.get("labels", {}))
            or document.get("rules") != expected):
        raise ValueError("unexpected fixed-chart CNPG auxiliary ClusterRole shape")
    return document


def secure_operator(document):
    if document["metadata"].get("namespace") != NAMESPACE:
        raise ValueError("unexpected CNPG operator namespace")
    spec = document["spec"]["template"]["spec"]
    if len(spec["containers"]) != 1 or spec["containers"][0]["name"] != "manager":
        raise ValueError("unexpected CNPG operator containers")
    manager = spec["containers"][0]
    if not DERIVED_IMAGE.fullmatch(manager["image"]):
        raise ValueError("build and pin the reviewed CNPG hardening image first")
    env = {item["name"]: item.get("value") for item in manager.get("env", [])}
    if env.get("OPERATOR_IMAGE_NAME") != manager["image"]:
        raise ValueError("CNPG instance-manager injection image differs from operator")
    if env.get("OBS_WEBHOOK_API_SERVER_IPS") != "172.28.2.64,172.28.5.50":
        raise ValueError("unexpected CNPG webhook source allowlist")
    metric_args = [arg for arg in manager.get("args", []) if arg.startswith("--metrics-bind-address")]
    if metric_args != ["--metrics-bind-address=127.0.0.1:8080"]:
        raise ValueError("CNPG original metrics listener must be loopback only")
    extra = yaml.safe_load((CONFIG / "cnpg-proxy-pod.yaml").read_text())
    existing_names = {item["name"] for item in spec.get("volumes", [])}
    if existing_names.intersection(item["name"] for item in extra["volumes"]):
        raise ValueError("CNPG mTLS volume name collision")
    pod = {**spec, "containers": [manager, *extra["containers"]],
           "volumes": [*spec.get("volumes", []), *extra["volumes"]]}
    template = {**document["spec"]["template"], "spec": pod}
    return {**document, "spec": {**document["spec"], "template": template}}


def transform(documents):
    output, webhook_names, role_count, deployment_count, selector = [], set(), 0, 0, None
    for document in documents:
        if document is None:
            continue
        kind, metadata = document.get("kind"), document.get("metadata", {})
        if kind in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"):
            document = scope_webhook(document)
            if metadata.get("name") in webhook_names:
                raise ValueError("duplicate CNPG webhook configuration")
            webhook_names.add(metadata.get("name"))
        elif kind == "ClusterRole":
            if metadata.get("name") == "cloudnative-pg-operator":
                document = scope_operator_role(document)
                role_count += 1
            else:
                document = validate_auxiliary_role(document)
        elif kind == "Deployment" and metadata.get("name") == "cloudnative-pg-operator":
            document = secure_operator(document)
            selector = document["spec"]["selector"]["matchLabels"]
            deployment_count += 1
        output.append(document)
    if webhook_names != set(WEBHOOK_NAMES.values()) or role_count != 1 or deployment_count != 1:
        raise ValueError("expected exact CNPG webhook pair, one operator ClusterRole and one Deployment")
    extra_names = {(doc.get("kind"), doc.get("metadata", {}).get("name")) for doc in output}
    if ("ConfigMap", "cloudnative-pg-operator-mtls") in extra_names or ("Service", "cloudnative-pg-operator-metrics") in extra_names:
        raise ValueError("CNPG mTLS resource name collision")
    output.extend([
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {
            "name": "cloudnative-pg-operator-mtls", "namespace": NAMESPACE},
         "data": {"envoy.yaml": (CONFIG / "cnpg-envoy.yaml").read_text()}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {
            "name": "cloudnative-pg-operator-metrics", "namespace": NAMESPACE},
         "spec": {"type": "ClusterIP", "selector": selector,
                  "ports": [{"name": "https-metrics", "port": 18443, "targetPort": "mtls-metrics"}]}},
    ])
    return output


if __name__ == "__main__":
    try:
        yaml.safe_dump_all(transform(yaml.safe_load_all(sys.stdin)), sys.stdout, sort_keys=False)
    except (ValueError, KeyError) as error:
        raise SystemExit(str(error)) from None
