#!/usr/bin/env python3
"""Render cert-manager PKI objects for the additive observability stack.

The renderer is offline-only. It reads the checked-in identity contracts and
emits Kubernetes objects that must still be server-side dry-run validated before
installation. CA private keys are imported separately into cert-manager.
"""

import json
from pathlib import Path
import re
import sys
import argparse

import yaml


NAMESPACE_MAP = {
    "observability-metrics": "victoriametrics",
    "observability-analytics": "clickhouse",
    "observability-otel": "opentelemetry",
    "observability-alerting": "nightingale",
}
PRODUCT_NAMESPACES = frozenset(("victoriametrics", "clickhouse", "opentelemetry", "nightingale"))
TRUST_DOMAINS = frozenset(("mesh", "am-gossip", "redis", "postgres-client", "postgres-server"))
URI_ROOT = "spiffe://observability.raytrain.local"
ISSUER_GROUP = "cert-manager.io"
POLICY_GROUP = "policy.cert-manager.io"
PRIORITY_CLASS = "raytrain-observability-additive"
DURATION = "2160h"
RENEW_BEFORE = "720h"
USAGE_MAP = {"serverAuth": "server auth", "clientAuth": "client auth"}
CNPG_RELOAD_ROLES = frozenset(("n9e-postgres-server", "n9e-postgres-replication"))


class RenderError(ValueError):
    """Raised for unsafe or inconsistent local contracts."""


def load_versions(path):
    return yaml.safe_load(Path(path).read_text())


def canonical_namespace(namespace):
    return NAMESPACE_MAP.get(namespace, namespace)


def canonical_dns(host, old_namespace, new_namespace):
    return host.replace(old_namespace, new_namespace)


def identity_uri(namespace, role):
    return f"{URI_ROOT}/{namespace}/{role}"


def cert_name(role):
    return f"obs-mtls-{role}"


def issuer_name(domain):
    return f"obs-mtls-{domain}"


def policy_name(namespace, role):
    return f"obs-mtls-{namespace}-{role}"


def normalize_usage(values):
    try:
        return [USAGE_MAP[item] for item in values]
    except KeyError as error:
        raise RenderError(f"unsupported certificate usage: {error.args[0]}") from None


def source_identities(root):
    entries = []
    for path in sorted(Path(root).glob("*/mtls-identities.json")):
        entries.extend(json.loads(path.read_text()))
    return entries


def normalize_identity(item):
    namespace = canonical_namespace(item["namespace"])
    if namespace not in PRODUCT_NAMESPACES:
        raise RenderError(f"unexpected namespace: {namespace}")
    role = item["role"]
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,48}", role):
        raise RenderError(f"unsafe role: {role}")
    domain = item["trust_domain"]
    if domain not in TRUST_DOMAINS - {"postgres-server"}:
        raise RenderError(f"unexpected trust domain: {domain}")
    dns_names = [canonical_dns(host, item["namespace"], namespace) for host in item.get("dns_names", [])]
    for host in dns_names:
        if not (host.endswith(f".{namespace}.svc") or host.endswith(f".{namespace}.svc.cluster.local")):
            raise RenderError(f"DNS name escapes namespace: {host}")
    return {
        "namespace": namespace,
        "role": role,
        "trust_domain": domain,
        "dns_names": dns_names,
        "usages": normalize_usage(item["usages"]),
        "common_name": item.get("common_name", role),
        "uri": identity_uri(namespace, role),
        "secret": cert_name(role),
    }


def postgres_server_identity():
    namespace = "nightingale"
    role = "n9e-postgres-server"
    return {
        "namespace": namespace,
        "role": role,
        "trust_domain": "postgres-server",
        "dns_names": [
            "nightingale-postgres-rw.nightingale.svc",
            "nightingale-postgres-rw.nightingale.svc.cluster.local",
            "nightingale-postgres-ro.nightingale.svc",
            "nightingale-postgres-ro.nightingale.svc.cluster.local",
            "nightingale-postgres-r.nightingale.svc",
            "nightingale-postgres-r.nightingale.svc.cluster.local",
        ],
        "usages": ["server auth"],
        "common_name": role,
        "uri": identity_uri(namespace, role),
        "secret": cert_name(role),
    }


def identities(root):
    normalized = [normalize_identity(item) for item in source_identities(root)]
    normalized.append(postgres_server_identity())
    seen = set()
    for item in normalized:
        marker = (item["namespace"], item["role"])
        if marker in seen:
            raise RenderError(f"duplicate identity: {marker}")
        seen.add(marker)
    return normalized


def cluster_issuer(domain):
    return {
        "apiVersion": "cert-manager.io/v1",
        "kind": "ClusterIssuer",
        "metadata": {"name": issuer_name(domain)},
        "spec": {"ca": {"secretName": f"obs-mtls-ca-{domain}"}},
    }


def cert_manager_namespace():
    return {
        "apiVersion": "v1",
        "kind": "Namespace",
        "metadata": {
            "name": "cert-manager",
            "labels": {
                "app.kubernetes.io/part-of": "raytrain-observability-cert-manager",
                "pod-security.kubernetes.io/enforce": "restricted",
                "pod-security.kubernetes.io/audit": "restricted",
                "pod-security.kubernetes.io/warn": "restricted",
            },
        },
    }


def cert_manager_resource_quota():
    return {
        "apiVersion": "v1",
        "kind": "ResourceQuota",
        "metadata": {"name": "cert-manager-budget", "namespace": "cert-manager"},
        "spec": {"hard": {
            "requests.cpu": "6",
            "requests.memory": "8Gi",
            "limits.cpu": "16",
            "limits.memory": "16Gi",
            "pods": "30",
            "requests.nvidia.com/gpu": "0",
        }},
    }


def certificate(item):
    spec = {
        "secretName": item["secret"],
        "duration": DURATION,
        "renewBefore": RENEW_BEFORE,
        "issuerRef": {"name": issuer_name(item["trust_domain"]), "kind": "ClusterIssuer", "group": ISSUER_GROUP},
        "usages": item["usages"],
        "uris": [item["uri"]],
        "privateKey": {"algorithm": "RSA", "size": 2048, "rotationPolicy": "Always"},
    }
    if item["role"] in CNPG_RELOAD_ROLES:
        spec["secretTemplate"] = {"labels": {"cnpg.io/reload": "true"}}
    if item["dns_names"]:
        spec["dnsNames"] = item["dns_names"]
    if item.get("common_name"):
        spec["commonName"] = item["common_name"]
    return {
        "apiVersion": "cert-manager.io/v1",
        "kind": "Certificate",
        "metadata": {"namespace": item["namespace"], "name": item["secret"]},
        "spec": spec,
    }


def certificate_request_policy(item):
    allowed = {
        "commonName": {"value": item["common_name"]},
        "dnsNames": {"values": item["dns_names"]},
        "uris": {"required": True, "values": [item["uri"]]},
        "usages": item["usages"],
        "isCA": False,
    }
    return {
        "apiVersion": "policy.cert-manager.io/v1alpha1",
        "kind": "CertificateRequestPolicy",
        "metadata": {"name": policy_name(item["namespace"], item["role"])},
        "spec": {
            "allowed": allowed,
            "constraints": {
                "minDuration": DURATION,
                "maxDuration": DURATION,
                "privateKey": {"algorithm": "RSA", "minSize": 2048},
            },
            "selector": {
                "issuerRef": {"name": issuer_name(item["trust_domain"]), "kind": "ClusterIssuer", "group": ISSUER_GROUP},
                "namespace": {"matchNames": [item["namespace"]]},
            },
        },
    }


def policy_use_role(item):
    name = policy_name(item["namespace"], item["role"])
    return {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "ClusterRole",
        "metadata": {"name": f"obs-mtls-policy-use-{name}"},
        "rules": [{
            "apiGroups": [POLICY_GROUP],
            "resources": ["certificaterequestpolicies"],
            "resourceNames": [name],
            "verbs": ["use"],
        }],
    }


def policy_use_binding(item):
    name = policy_name(item["namespace"], item["role"])
    return {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "ClusterRoleBinding",
        "metadata": {"name": f"obs-mtls-policy-use-{name}"},
        "roleRef": {
            "apiGroup": "rbac.authorization.k8s.io",
            "kind": "ClusterRole",
            "name": f"obs-mtls-policy-use-{name}",
        },
        "subjects": [{"kind": "ServiceAccount", "name": "cert-manager", "namespace": "cert-manager"}],
    }


def public_ca_secret_shell(name, domain, target_namespace):
    return {
        "apiVersion": "v1",
        "kind": "Secret",
        "type": "Opaque",
        "metadata": {
            "namespace": target_namespace,
            "name": name,
            "labels": {"cnpg.io/reload": "true"},
            "annotations": {
                "raytrain.wellspiking.ai/source-ca-secret": f"cert-manager/obs-mtls-ca-{domain}",
                "raytrain.wellspiking.ai/import-mode": "public-ca-only",
            },
        },
    }


def render_cnpg_certificate_patch():
    return {
        "apiVersion": "postgresql.cnpg.io/v1",
        "kind": "Cluster",
        "metadata": {"name": "nightingale-postgres", "namespace": "nightingale"},
        "spec": {"certificates": {
            "serverTLSSecret": "obs-mtls-n9e-postgres-server",
            "serverCASecret": "obs-mtls-ca-postgres-server-public",
            "clientCASecret": "obs-mtls-postgres-client-ca",
            "replicationTLSSecret": "obs-mtls-n9e-postgres-replication",
        }},
    }


def render_documents(root):
    return render_part(root, "all")


def render_part(root, part):
    items = identities(root)
    bootstrap = [cert_manager_namespace(), cert_manager_resource_quota()]
    issuers = [cluster_issuer(domain) for domain in sorted(TRUST_DOMAINS)]
    identity_docs = [
        document
        for item in items
        for document in (certificate(item), certificate_request_policy(item), policy_use_role(item), policy_use_binding(item))
    ]
    public_ca = [
        public_ca_secret_shell("obs-mtls-cnpg-metrics-ca", "mesh", "nightingale"),
        public_ca_secret_shell("obs-mtls-postgres-client-ca", "postgres-client", "nightingale"),
        public_ca_secret_shell("obs-mtls-ca-postgres-server-public", "postgres-server", "nightingale"),
    ]
    postgres_patch = [render_cnpg_certificate_patch()]
    parts = {
        "plan": bootstrap + issuers + identity_docs,
        "bootstrap": bootstrap,
        "issuers": issuers,
        "identities": identity_docs,
        "public-ca-plan": public_ca,
        "postgres-patch": postgres_patch,
        "all": bootstrap + issuers + identity_docs + public_ca + postgres_patch,
    }
    if part not in parts:
        raise RenderError(f"unknown render part: {part}")
    return parts[part]


def policy_allows(documents, request):
    namespace = request.get("namespace")
    uri = request.get("uri")
    dns_names = request.get("dns_names", [])
    issuer = request.get("issuer")
    for policy in (doc for doc in documents if doc.get("kind") == "CertificateRequestPolicy"):
        spec = policy["spec"]
        selector = spec["selector"]
        if namespace not in selector["namespace"].get("matchNames", []):
            continue
        if issuer and issuer != selector["issuerRef"]["name"]:
            continue
        allowed = spec["allowed"]
        if uri not in allowed["uris"].get("values", []):
            continue
        if not set(dns_names).issubset(set(allowed.get("dnsNames", {}).get("values", []))):
            continue
        return True
    return False


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", nargs="?", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--part", choices=("plan", "bootstrap", "issuers", "identities",
                                           "public-ca-plan", "postgres-patch", "all"),
                        default="plan")
    args = parser.parse_args(argv)
    yaml.safe_dump_all(render_part(args.root, args.part), sys.stdout, sort_keys=False)


if __name__ == "__main__":
    main(sys.argv[1:])
