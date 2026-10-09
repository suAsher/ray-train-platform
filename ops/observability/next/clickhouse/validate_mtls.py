#!/usr/bin/env python3
"""Fail-closed route contract for Altinity 0.27.4's fixed CH topology.

This validates candidate files only. It never contacts Kubernetes. Runtime
validation must additionally compare operator-generated Service/Pod names.
"""

from pathlib import Path
import sys

import yaml


HERE = Path(__file__).resolve().parent
NAMESPACE = "clickhouse"


def hostnames(name):
    return [name, f"{name}.{NAMESPACE}", f"{name}.{NAMESPACE}.svc",
            f"{name}.{NAMESPACE}.svc.cluster.local"]


def _target(cluster):
    endpoints = cluster["load_assignment"]["endpoints"]
    if len(endpoints) != 1 or len(endpoints[0]["lb_endpoints"]) != 1:
        raise ValueError("route must have exactly one fixed target")
    return endpoints[0]["lb_endpoints"][0]["endpoint"]["address"]["socket_address"]


def validate_operator_routes(chi, routes, proxy_config):
    """Return route errors; unknown/expanded layouts cannot be silently passed."""
    errors = []
    try:
        clusters = chi["spec"]["configuration"]["clusters"]
        layout = clusters[0]["layout"]
        expected = (chi["metadata"]["namespace"], chi["metadata"]["name"],
                    clusters[0]["name"], layout.get("shardsCount"), layout.get("replicasCount"))
        if len(clusters) != 1 or expected != (NAMESPACE, "clickhouse", "telemetry", 1, 2):
            errors.append("route contract supports only clickhouse/telemetry/1 shard/2 replicas")
        expected_replicas = [
            {"name": str(index), "settings": {"interserver_https_host":
             f"chi-clickhouse-telemetry-0-{index}.{NAMESPACE}.svc.cluster.local"}}
            for index in range(2)
        ]
        if (set(layout) != {"shardsCount", "replicasCount", "replicas"}
                or layout.get("replicas") != expected_replicas):
            errors.append("route contract rejects custom layout host names")
        if (routes.get("namespace"), routes.get("chi"), routes.get("cluster"), routes.get("shards")) != expected[:4]:
            errors.append("route metadata differs from CHI")
        replicas = routes["replicas"]
        if len(replicas) != expected[4]:
            errors.append("route count differs from replica count")
        resources = proxy_config["static_resources"]
        listeners = {item["name"]: item for item in resources["listeners"]}
        upstreams = {item["name"]: item for item in resources["clusters"]}
        seen_hosts = set()
        for index, route in enumerate(replicas):
            errors.extend(_validate_route(index, route, listeners, upstreams))
            if seen_hosts.intersection(route["hostnames"]):
                errors.append("route has colliding host aliases")
            seen_hosts.update(route["hostnames"])
        actual = {name for name in listeners if name.startswith("ch-egress-")}
        if actual != {f"ch-egress-{i}" for i in range(len(replicas))}:
            errors.append("route listener set has unexpected entries")
    except (KeyError, TypeError, ValueError, IndexError) as exc:
        errors.append(f"invalid route structure: {type(exc).__name__}")
    return errors


def _validate_route(index, route, listeners, upstreams):
    errors = []
    host = f"chi-clickhouse-telemetry-0-{index}"
    service = f"obs-ch-r{index}-mtls"
    expected = {"index": index, "host": host, "pod": f"{host}-0",
                "loopback": f"127.0.0.{index + 2}", "service": service,
                "hostnames": hostnames(host), "cluster": f"ch-replica-{index}"}
    if route != expected:
        errors.append(f"route {index}: aliases or destination differ from fixed topology")
    listener = listeners[f"ch-egress-{index}"]
    address = listener["address"]["socket_address"]
    if address != {"address": expected["loopback"], "port_value": 8123}:
        errors.append(f"route {index}: plaintext listener is not its dedicated loopback")
    filters = listener["filter_chains"][0]["filters"]
    if len(filters) != 1 or filters[0]["typed_config"].get("cluster") != expected["cluster"]:
        errors.append(f"route {index}: listener is not a fixed single upstream")
    upstream = upstreams[expected["cluster"]]
    target = _target(upstream)
    endpoint = f"{service}.{NAMESPACE}.svc.cluster.local"
    if target != {"address": endpoint, "port_value": 8443}:
        errors.append(f"route {index}: upstream alias could bypass or recurse")
    tls = upstream["transport_socket"]["typed_config"]
    context = tls["common_tls_context"]
    validation = context["validation_context"]
    identity = f"spiffe://observability.raytrain.local/{NAMESPACE}/clickhouse"
    expected_san = [{"san_type": "URI", "matcher": {"exact": identity}}]
    if (tls.get("sni") != endpoint or not context.get("tls_certificates")
            or validation.get("match_typed_subject_alt_names") != expected_san
            or validation.get("trusted_ca", {}).get("filename") != "/etc/obs-mtls/ca.crt"
            or validation.get("trust_chain_verification", "VERIFY_TRUST_CHAIN") != "VERIFY_TRUST_CHAIN"):
        errors.append(f"route {index}: TLS identity authentication is missing")
    return errors


def validate_services(routes, services):
    expected = {r["service"]: r["pod"] for r in routes["replicas"]}
    actual = {s["metadata"]["name"]: s for s in services if s}
    if set(expected) != set(actual):
        return ["route alias Service set differs from contract"]
    errors = []
    for name, pod in expected.items():
        obj = actual[name]
        spec = obj["spec"]
        if (obj.get("kind") != "Service" or obj["metadata"].get("namespace") != NAMESPACE
                or spec.get("type") != "ClusterIP" or spec.get("externalIPs")
                or spec.get("selector") != {"statefulset.kubernetes.io/pod-name": pod}
                or spec.get("ports") != [{"name": "https", "port": 8443, "targetPort": 8443}]):
            errors.append(f"route alias Service {name} has an unexpected target")
    return errors


def load_contract():
    chi = next(yaml.safe_load_all((HERE / "clickhouse.yaml").read_text()))
    routes = yaml.safe_load((HERE / "operator-routes.yaml").read_text())
    proxy = yaml.safe_load((HERE / "proxy-operator.yaml").read_text())
    services = list(yaml.safe_load_all((HERE / "operator-egress-services.yaml").read_text()))
    errors = validate_operator_routes(chi, routes, proxy) + validate_services(routes, services)
    if errors:
        raise ValueError("; ".join(errors))
    return routes


if __name__ == "__main__":
    try:
        load_contract()
    except (ValueError, KeyError, TypeError, OSError) as exc:
        sys.exit(str(exc))
    print("CH mTLS fixed routes valid; generated runtime resources still require verification.")
