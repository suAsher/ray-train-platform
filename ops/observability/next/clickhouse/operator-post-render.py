#!/usr/bin/env python3
"""Helm post-renderer scoped to the new, locked Altinity operator Deployment.

Requires PyYAML on the builder. Unknown topology or a changed chart fails closed.
No Secret data is read, emitted, or created.
"""

import copy
import sys

import yaml

from validate_mtls import load_contract


NAME = "observability-clickhouse-operator"
NAMESPACE = "observability-analytics"
ENVOY = ("harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:"
         "dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865")


def sidecar():
    return {
        "name": "envoy", "image": ENVOY, "command": ["/bin/sh", "-ec"],
        "args": ['sed "s/__POD_IP__/$POD_IP/g" /etc/envoy-source/proxy-operator.yaml '
                 '> /var/run/envoy/config.yaml; exec envoy -c /var/run/envoy/config.yaml '
                 '--concurrency 1 --disable-hot-restart'],
        "env": [{"name": "POD_IP", "valueFrom": {"fieldRef": {"fieldPath": "status.podIP"}}}],
        "resources": {"requests": {"cpu": "100m", "memory": "128Mi"},
                      "limits": {"cpu": "250m", "memory": "256Mi"}},
        "securityContext": {"runAsNonRoot": True, "readOnlyRootFilesystem": True,
                            "allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
        "readinessProbe": {"httpGet": {"path": "/readyz", "port": 18080}, "periodSeconds": 5},
        "volumeMounts": [
            {"name": "mesh-tls", "mountPath": "/etc/obs-mtls", "readOnly": True},
            {"name": "proxy-config", "mountPath": "/etc/envoy-source", "readOnly": True},
            {"name": "proxy-runtime", "mountPath": "/var/run/envoy"}],
    }


def render(documents, routes):
    targets = [d for d in documents if d and d.get("kind") == "Deployment"
               and d.get("metadata", {}).get("name") == NAME]
    if len(targets) != 1 or targets[0]["metadata"].get("namespace") != NAMESPACE:
        raise ValueError("expected exactly one scoped new operator Deployment")
    output = copy.deepcopy(documents)
    deployment = next(d for d in output if d and d.get("kind") == "Deployment"
                      and d.get("metadata", {}).get("name") == NAME)
    pod = deployment["spec"]["template"]["spec"]
    if (pod.get("hostNetwork") or pod.get("hostAliases") or pod.get("initContainers")
            or len(pod["containers"]) != 1):
        raise ValueError("unexpected operator Pod layout; review changed chart before rendering")
    app = pod["containers"][0]
    if app["name"] != "altinity-clickhouse-operator" or app.get("args") or app.get("command"):
        raise ValueError("unexpected operator entrypoint")
    new_volumes = [
        {"name": "mesh-tls", "secret": {"secretName": "obs-mtls-ch-operator", "defaultMode": 0o440}},
        {"name": "proxy-config", "configMap": {"name": "observability-proxy-operator"}},
        {"name": "proxy-runtime", "emptyDir": {"sizeLimit": "16Mi"}}]
    if {v["name"] for v in new_volumes}.intersection(v["name"] for v in pod.get("volumes", [])):
        raise ValueError("operator proxy volume names conflict")
    app["args"] = ["--metrics-endpoint=127.0.0.1:9999"]
    # Pure Go resolution normalizes the final dot before /etc/hosts lookup;
    # Kubernetes HostAliases themselves must not contain a trailing dot.
    app.setdefault("env", []).append({"name": "GODEBUG", "value": "netdns=go"})
    for probe in ("startupProbe", "readinessProbe", "livenessProbe"):
        app[probe] = {"httpGet": {"path": "/readyz", "port": 18080},
                      "periodSeconds": 10, "failureThreshold": 6}
    pod["hostAliases"] = [{"ip": r["loopback"], "hostnames": r["hostnames"]}
                          for r in routes["replicas"]]
    pod["containers"].append(sidecar())
    pod.setdefault("volumes", []).extend(new_volumes)
    return output


if __name__ == "__main__":
    try:
        result = render(list(yaml.safe_load_all(sys.stdin)), load_contract())
    except (ValueError, KeyError, TypeError, OSError) as exc:
        sys.exit(str(exc))
    yaml.safe_dump_all(result, sys.stdout, sort_keys=False)
