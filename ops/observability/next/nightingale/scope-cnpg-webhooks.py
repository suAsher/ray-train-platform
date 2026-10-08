#!/usr/bin/env python3
"""Scope fixed CNPG webhooks and protect the fixed operator's metrics listener.

This renderer fails until a derived digest is built and reviewed. It only changes
the expected new operator Deployment and appends its own ConfigMap/Service.
"""

from pathlib import Path
import re
import sys

import yaml

NAMESPACE = "observability-alerting"
CONFIG = Path(__file__).resolve().parent / "config"
DERIVED_IMAGE = re.compile(
    r"^harbor\.wellspiking\.ai/guofeng\.su/cnpg-observability:"
    r"[a-zA-Z0-9_.-]+@sha256:[0-9a-f]{64}$"
)


def scope_webhook(document):
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
    output, webhook_count, deployment_count, selector = [], 0, 0, None
    for document in documents:
        if document is None:
            continue
        kind, metadata = document.get("kind"), document.get("metadata", {})
        if kind in ("MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"):
            document = scope_webhook(document)
            webhook_count += 1
        elif kind == "Deployment" and metadata.get("name") == "n9e-cnpg":
            document = secure_operator(document)
            selector = document["spec"]["selector"]["matchLabels"]
            deployment_count += 1
        output.append(document)
    if webhook_count != 2 or deployment_count != 1:
        raise ValueError("expected two CNPG webhook configurations and one operator")
    extra_names = {(doc.get("kind"), doc.get("metadata", {}).get("name")) for doc in output}
    if ("ConfigMap", "n9e-cnpg-mtls") in extra_names or ("Service", "n9e-cnpg-metrics") in extra_names:
        raise ValueError("CNPG mTLS resource name collision")
    output.extend([
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {
            "name": "n9e-cnpg-mtls", "namespace": NAMESPACE},
         "data": {"envoy.yaml": (CONFIG / "cnpg-envoy.yaml").read_text()}},
        {"apiVersion": "v1", "kind": "Service", "metadata": {
            "name": "n9e-cnpg-metrics", "namespace": NAMESPACE},
         "spec": {"type": "ClusterIP", "selector": selector,
                  "ports": [{"name": "https-metrics", "port": 18443, "targetPort": "mtls-metrics"}]}},
    ])
    return output


if __name__ == "__main__":
    try:
        yaml.safe_dump_all(transform(yaml.safe_load_all(sys.stdin)), sys.stdout, sort_keys=False)
    except (ValueError, KeyError) as error:
        raise SystemExit(str(error)) from None
