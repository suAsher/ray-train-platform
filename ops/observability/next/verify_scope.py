#!/usr/bin/env python3
"""Check rendered additive manifests, not live admission or Operator output.

Usage: python3 verify_scope.py rendered.yaml ... \
    --allow-cluster-resource ClusterRole/new-operator

Allowlisting acknowledges review, not ownership. Before apply, independently
check that every cluster-scoped name is absent and review server-side dry-run.
Runtime Secrets must be created separately; never pass their payloads here.
"""

import argparse
from decimal import Decimal, InvalidOperation
from pathlib import Path
import re
import sys

import yaml


NAMESPACES = frozenset({"observability-metrics", "observability-analytics",
                        "observability-otel", "observability-alerting"})
PRIORITY_CLASS = "raytrain-observability-additive"
STORAGE_CLASS = "ebs-ssd-observability-retain"
DEDICATED_TAINT = "platform.wellspiking.ai/dedicated-tenant"
CRD_GROUPS = frozenset({"operator.victoriametrics.com", "clickhouse.altinity.com",
                        "clickhouse-keeper.altinity.com", "postgresql.cnpg.io"})
CLUSTER_KINDS = frozenset({"Namespace", "PriorityClass", "StorageClass",
                           "CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding",
                           "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration"})
STANDARD_KINDS = frozenset({"ConfigMap", "Secret", "Service", "ServiceAccount",
                            "Role", "RoleBinding", "ResourceQuota", "LimitRange",
                            "NetworkPolicy", "PodDisruptionBudget", "PersistentVolumeClaim"})
POD_KINDS = frozenset({"Pod", "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "ReplicaSet"})
VM_WORKLOADS = frozenset({"VMAgent", "VMAuth", "VMAlert", "VMAlertmanager", "VMSingle"})
FORBIDDEN_KINDS = frozenset({"Ingress", "IngressClass", "Gateway", "HTTPRoute", "Node",
                             "PersistentVolume", "APIService", "MutatingAdmissionPolicy",
                             "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"})
MIN_EBS = Decimal(20 * 1024 ** 3)


class Report:
    def __init__(self):
        self.errors = []
        self.warnings = []
        self.pending = []
        self.inventory = []

    def error(self, where, message):
        self.errors.append(f"{where}: {message}")


def quantity(value):
    """Parse only standard nonnegative resource quantities; never echo input."""
    match = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)([eE][+-]?[0-9]+|[KMGTPE]i|[numkKMGTPE])?", str(value))
    if not match:
        return None
    number, suffix = match.groups()
    suffix = suffix or ""
    try:
        if suffix.startswith(("e", "E")) and len(suffix) > 1:
            factor = Decimal(10) ** int(suffix[1:])
        elif suffix.endswith("i"):
            factor = Decimal(1024) ** ("KMGTPE".index(suffix[0]) + 1)
        else:
            powers = {"": 0, "n": -9, "u": -6, "m": -3, "k": 3, "K": 3,
                      "M": 6, "G": 9, "T": 12, "P": 15, "E": 18}
            factor = Decimal(10) ** powers[suffix]
        return Decimal(number) * factor
    except (InvalidOperation, ValueError, KeyError):
        return None


def check_resources(resources, where, report):
    resources = resources or {}
    for bucket in ("requests", "limits"):
        values = resources.get(bucket) or {}
        for key in values:
            if "gpu" in key.lower() or key.startswith("nvidia.com/mig-"):
                report.error(where, "GPU requests/limits are forbidden, including zero")
        for key in ("cpu", "memory"):
            amount = quantity(values.get(key))
            if amount is None or amount <= 0:
                report.error(where, f"positive {bucket}.{key} is required")
    for key in ("cpu", "memory"):
        request = quantity((resources.get("requests") or {}).get(key))
        limit = quantity((resources.get("limits") or {}).get(key))
        if request is not None and limit is not None and request > limit:
            report.error(where, f"requests.{key} exceeds limits.{key}")


def check_claim(spec, where, report):
    if spec.get("storageClassName") != STORAGE_CLASS:
        report.error(where, "storageClassName must use the new retained EBS class")
    size = quantity(spec.get("resources", {}).get("requests", {}).get("storage"))
    if size is None or size < MIN_EBS:
        report.error(where, "EBS storage request must be at least 20Gi")
    if spec.get("volumeName") or spec.get("dataSource") or spec.get("dataSourceRef"):
        report.error(where, "binding or cloning an existing volume needs a separate migration review")


def cpu_only(key, values):
    if any(str(value).lower() in {"cpu", "cpu-only", "cpu_only"} for value in values):
        return True
    return "node-role" in key and "cpu" in key


def check_scheduling(spec, where, report):
    if spec.get("priorityClassName") != PRIORITY_CLASS:
        report.error(where, f"priorityClassName must be {PRIORITY_CLASS}")
    if spec.get("preemptionPolicy") not in (None, "Never"):
        report.error(where, "preemptionPolicy may only be Never")
    if spec.get("nodeName"):
        report.error(where, "nodeName bypasses scheduling; use reviewed affinity")
    for key, value in (spec.get("nodeSelector") or {}).items():
        if cpu_only(key, [value]):
            report.error(where, "CPU-only nodeSelector is forbidden")
    required = (spec.get("affinity") or {}).get("nodeAffinity", {}).get("requiredDuringSchedulingIgnoredDuringExecution", {})
    for term in required.get("nodeSelectorTerms", []):
        for expression in term.get("matchExpressions", []):
            key, operator = expression.get("key", ""), expression.get("operator")
            values = expression.get("values", [])
            if (operator in {"In", "Exists"} and cpu_only(key, values)) or (
                    key == "nvidia.com/gpu.present" and
                    (operator == "DoesNotExist" or (operator == "In" and "false" in values))):
                report.error(where, "CPU-only required node affinity is forbidden")
    for toleration in spec.get("tolerations", []):
        key = toleration.get("key", "")
        operator = toleration.get("operator", "Equal")
        effect = toleration.get("effect", "")
        broad = operator == "Exists" and not key
        dedicated = key == DEDICATED_TAINT and (operator == "Exists" or toleration.get("value") == "algorithm")
        if effect in ("", "NoSchedule") and (broad or dedicated):
            report.error(where, "toleration permits the algorithm dedicated node")


def selector_matches(selector, labels):
    if not isinstance(selector, dict):
        return False
    for key, value in selector.get("matchLabels", {}).items():
        if labels.get(key) != value:
            return False
    for expression in selector.get("matchExpressions", []):
        key, op, values = expression.get("key"), expression.get("operator"), expression.get("values", [])
        if op == "In" and labels.get(key) not in values:
            return False
        if op == "NotIn" and labels.get(key) in values:
            return False
        if op == "Exists" and key not in labels:
            return False
        if op == "DoesNotExist" and key in labels:
            return False
        if op not in {"In", "NotIn", "Exists", "DoesNotExist"}:
            return False
    return True


def check_anti_affinity(spec, labels, replicas, persistent, where, report):
    if replicas <= 1:
        return
    anti = (spec.get("affinity") or {}).get("podAntiAffinity", {})
    def valid(term):
        return (term.get("topologyKey") == "kubernetes.io/hostname" and
                selector_matches(term.get("labelSelector"), labels) and
                not term.get("namespaces") and not term.get("namespaceSelector"))
    hard = any(valid(term) for term in anti.get("requiredDuringSchedulingIgnoredDuringExecution", []))
    soft = any(valid(term.get("podAffinityTerm", {})) and term.get("weight", 0) > 0
               for term in anti.get("preferredDuringSchedulingIgnoredDuringExecution", []))
    for spread in spec.get("topologySpreadConstraints", []):
        if spread.get("topologyKey") != "kubernetes.io/hostname" or not selector_matches(spread.get("labelSelector"), labels):
            continue
        if spread.get("whenUnsatisfiable") == "DoNotSchedule" and spread.get("maxSkew") == 1 and spread.get("minDomains", 0) >= replicas:
            hard = True
        elif spread.get("whenUnsatisfiable") == "ScheduleAnyway":
            soft = True
    if (persistent and not hard) or (not hard and not soft):
        report.error(where, "replicas need matching hostname anti-affinity; persistent replicas require hard isolation")
    elif not hard:
        report.warnings.append(f"{where}: preferred anti-affinity does not guarantee different nodes")


def check_container(container, where, report):
    check_resources(container.get("resources"), where, report)
    security = container.get("securityContext") or {}
    if security.get("privileged"):
        report.error(where, "privileged container is forbidden")
    if security.get("allowPrivilegeEscalation") is True:
        report.error(where, "allowPrivilegeEscalation is forbidden")
    if security.get("capabilities", {}).get("add"):
        report.error(where, "additional Linux capabilities need a separate review")
    if any(port.get("hostPort", 0) for port in container.get("ports", [])):
        report.error(where, "hostPort is forbidden")
    for env in container.get("env", []):
        if re.search(r"(?:PASSWORD|TOKEN|SECRET|PRIVATE_KEY|ACCESS_KEY)$", env.get("name", ""), re.I) and env.get("value"):
            report.error(where, "sensitive environment variables must reference a runtime Secret")


def check_pod(spec, labels, replicas, persistent, where, report):
    check_scheduling(spec, where, report)
    for flag in ("hostNetwork", "hostPID", "hostIPC"):
        if spec.get(flag):
            report.error(where, f"{flag} is forbidden")
    for volume in spec.get("volumes", []):
        if "hostPath" in volume:
            report.error(where, "hostPath is forbidden")
        if "persistentVolumeClaim" in volume:
            persistent = True
        if "ephemeral" in volume:
            check_claim(volume["ephemeral"].get("volumeClaimTemplate", {}).get("spec", {}), where, report)
    if not spec.get("containers"):
        report.error(where, "Pod template must contain a container")
    for field in ("containers", "initContainers", "ephemeralContainers"):
        for index, container in enumerate(spec.get(field, [])):
            check_container(container, f"{where}/{field}[{index}]", report)
    check_anti_affinity(spec, labels, replicas, persistent, where, report)


def check_workload(document, where, report):
    kind, spec = document["kind"], document.get("spec", {})
    if kind == "CronJob":
        spec = spec.get("jobTemplate", {}).get("spec", {})
    template = document if kind == "Pod" else spec.get("template", {})
    replicas = spec.get("replicas", spec.get("parallelism", 1))
    claims = spec.get("volumeClaimTemplates", [])
    for claim in claims:
        check_claim(claim.get("spec", {}), where, report)
    check_pod(template.get("spec", {}), template.get("metadata", {}).get("labels", {}),
              replicas or 1, bool(claims), where, report)


def check_service(spec, where, report):
    if spec.get("type", "ClusterIP") != "ClusterIP":
        report.error(where, "only ClusterIP Services are permitted")
    if spec.get("externalIPs") or spec.get("loadBalancerIP") or any(port.get("nodePort") for port in spec.get("ports", [])):
        report.error(where, "externally exposed Service addresses/ports are forbidden")


def check_rbac(document, where, report):
    kind = document["kind"]
    if kind.endswith("Binding"):
        reference = document.get("roleRef", {})
        if reference.get("name") == "cluster-admin":
            report.error(where, "cluster-admin binding is forbidden")
        for subject in document.get("subjects", []):
            if subject.get("kind") != "ServiceAccount" or subject.get("namespace") not in NAMESPACES:
                report.error(where, "RBAC subject must be a ServiceAccount in an additive namespace")
    else:
        for rule in document.get("rules", []):
            # Reviewable metadata only: rules never contain credentials.
            report.inventory.append(f"{where}: RBAC groups={rule.get('apiGroups', [])} resources={rule.get('resources', [])} verbs={rule.get('verbs', [])}")
    report.pending.append(f"{where}: review RBAC roleRef/rules and live ownership; namespace scope cannot constrain a ClusterRoleBinding")


def check_webhook(document, where, report):
    hooks = document.get("webhooks", [])
    if not hooks:
        report.error(where, "webhook list must not be empty")
    for hook in hooks:
        expected = {"matchLabels": {"kubernetes.io/metadata.name": "observability-alerting"}}
        if hook.get("namespaceSelector") != expected:
            report.error(where, "webhook namespaceSelector must exactly isolate observability-alerting")
        client = hook.get("clientConfig", {})
        if client.get("url") or client.get("service", {}).get("namespace") != "observability-alerting":
            report.error(where, "webhook service must be in observability-alerting")
        if not hook.get("rules"):
            report.error(where, "webhook requires explicit CNPG rules")
        for rule in hook.get("rules", []):
            if rule.get("apiGroups") != ["postgresql.cnpg.io"] or rule.get("scope") != "Namespaced":
                report.error(where, "only Namespaced postgresql.cnpg.io webhook rules are permitted")
    report.pending.append(f"{where}: confirm no existing webhook/CRD owner; verify generated webhook remains namespace isolated live")


def check_cluster_resource(document, where, allowlist, report):
    kind, name = document["kind"], document.get("metadata", {}).get("name")
    if kind == "Namespace":
        if name not in NAMESPACES:
            report.error(where, "namespace is outside additive allowlist")
    elif kind == "PriorityClass":
        if name != PRIORITY_CLASS or document.get("value") != -1000 or document.get("globalDefault", False):
            report.error(where, "PriorityClass must be the exact non-default additive class at -1000")
        if document.get("preemptionPolicy") != "Never":
            report.error(where, "PriorityClass preemptionPolicy must be Never")
    elif kind == "StorageClass":
        checks = {"provisioner": "ebs.csi.volcengine.com", "reclaimPolicy": "Retain",
                  "volumeBindingMode": "WaitForFirstConsumer", "allowVolumeExpansion": True}
        if name != STORAGE_CLASS or any(document.get(key) != value for key, value in checks.items()) or document.get("parameters", {}).get("type") != "ESSD_PL0":
            report.error(where, "StorageClass must be new retained expandable WFFC EBS ESSD_PL0")
    else:
        if f"{kind}/{name}" not in allowlist:
            report.error(where, "cluster-scoped object requires explicit --allow-cluster-resource allowlist")
        if kind == "CustomResourceDefinition":
            spec = document.get("spec", {})
            group, plural = spec.get("group"), spec.get("names", {}).get("plural")
            if group not in CRD_GROUPS or name != f"{plural}.{group}" or spec.get("scope") != "Namespaced":
                report.error(where, "CRD group/name/scope must match an approved namespaced Operator API")
            if spec.get("conversion", {}).get("strategy") == "Webhook":
                report.error(where, "CRD conversion webhooks require separate review")
        elif kind.endswith("WebhookConfiguration"):
            check_webhook(document, where, report)
    report.pending.append(f"{where}: live collision/ownership check required before creating cluster-scoped resource")


def check_vm_component(spec, where, report):
    check_scheduling(spec, where, report)
    check_resources(spec.get("resources"), where, report)
    for name in ("configReloaderResources", "initContainerResources"):
        if name in spec:
            check_resources(spec[name], f"{where}/{name}", report)
    persistent = False
    for storage_name in ("storage", "statefulStorage"):
        storage = spec.get(storage_name) or {}
        claim = storage.get("volumeClaimTemplate")
        if claim is not None:
            persistent = True
            check_claim(claim.get("spec", {}), where, report)
    labels = (spec.get("podMetadata") or {}).get("labels", {})
    check_anti_affinity(spec, labels, spec.get("replicaCount", 1), persistent, where, report)
    for field in ("containers", "initContainers"):
        for index, container in enumerate(spec.get(field, [])):
            check_container(container, f"{where}/{field}[{index}]", report)
    for volume in spec.get("volumes", []):
        if "hostPath" in volume:
            report.error(where, "hostPath is forbidden")
    for flag in ("hostNetwork", "hostPID", "hostIPC"):
        if spec.get(flag):
            report.error(where, f"{flag} is forbidden")


def check_cnpg(spec, where, report):
    check_resources(spec.get("resources"), where, report)
    affinity = spec.get("affinity") or {}
    scheduling = {"priorityClassName": spec.get("priorityClassName"),
                  "affinity": {"nodeAffinity": affinity.get("nodeAffinity", {})},
                  "nodeSelector": affinity.get("nodeSelector", {}),
                  "tolerations": affinity.get("tolerations", [])}
    check_scheduling(scheduling, where, report)
    for field in ("storage", "walStorage"):
        if field not in spec:
            continue
        storage = spec[field]
        claim_spec = dict(storage.get("pvcTemplate", {}))
        claim_spec.update({"storageClassName": storage.get("storageClass"),
                           "resources": {"requests": {"storage": storage.get("size")}}})
        check_claim(claim_spec, where, report)
    if spec.get("instances", 1) > 1 and not (affinity.get("enablePodAntiAffinity") is True and
            affinity.get("podAntiAffinityType") == "required" and affinity.get("topologyKey") == "kubernetes.io/hostname"):
        report.error(where, "persistent CNPG instances require hard hostname anti-affinity")


def check_clickhouse(spec, where, report):
    templates = spec.get("templates", {})
    for template in templates.get("volumeClaimTemplates", []):
        check_claim(template.get("spec", {}), where, report)
    for template in templates.get("serviceTemplates", []):
        check_service(template.get("spec", {}), where, report)
    replicas = max([cluster.get("layout", {}).get("replicasCount", 1)
                    for cluster in spec.get("configuration", {}).get("clusters", [])] or [1])
    for index, template in enumerate(templates.get("podTemplates", [])):
        check_pod(template.get("spec", {}), template.get("metadata", {}).get("labels", {}),
                  replicas, True, f"{where}/podTemplates[{index}]", report)


def check_custom_resource(document, where, report):
    kind, spec = document["kind"], document.get("spec", {})
    group = document["apiVersion"].split("/")[0]
    if group == "operator.victoriametrics.com" and kind == "VMCluster":
        for component in ("vminsert", "vmselect", "vmstorage"):
            check_vm_component(spec.get(component, {}), f"{where}/{component}", report)
    elif group == "operator.victoriametrics.com" and kind in VM_WORKLOADS:
        check_vm_component(spec, where, report)
    elif group in {"clickhouse.altinity.com", "clickhouse-keeper.altinity.com"} and kind in {"ClickHouseInstallation", "ClickHouseKeeperInstallation"}:
        check_clickhouse(spec, where, report)
    elif group == "postgresql.cnpg.io" and kind == "Cluster":
        check_cnpg(spec, where, report)
    report.pending.append(f"{where}: partial/custom-API check only; review live generated Pods, PVCs, Services, RBAC and admitted defaults before accepting")


def check_document(document, allowlist, report):
    kind = document.get("kind")
    metadata = document.get("metadata") or {}
    name, namespace = metadata.get("name"), metadata.get("namespace")
    where = f"{kind or 'unknown'}/{namespace + '/' if namespace else ''}{name or 'unnamed'}"
    if not kind or not name or not document.get("apiVersion"):
        report.error(where, "input must be a rendered Kubernetes object with apiVersion/kind/name")
        return
    report.inventory.append(where)
    if kind in FORBIDDEN_KINDS:
        report.error(where, f"{kind} is outside additive publication scope")
        return
    if kind in CLUSTER_KINDS:
        check_cluster_resource(document, where, allowlist, report)
    elif namespace not in NAMESPACES:
        report.error(where, "explicit namespace must belong to the additive allowlist")
    if kind in POD_KINDS:
        check_workload(document, where, report)
    elif kind in {"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"}:
        check_rbac(document, where, report)
    elif kind == "Secret" and (document.get("data") or document.get("stringData")):
        report.error(where, "Secret payload is forbidden in rendered candidates; use runtime Secret references")
    elif kind == "Service":
        check_service(document.get("spec", {}), where, report)
    elif kind == "PersistentVolumeClaim":
        check_claim(document.get("spec", {}), where, report)
    elif kind not in CLUSTER_KINDS | STANDARD_KINDS | POD_KINDS:
        check_custom_resource(document, where, report)


def validate_documents(documents, allowlist=()):
    report = Report()
    seen = set()
    for document in documents:
        if document is None:
            continue
        if not isinstance(document, dict):
            report.error("document", "expected a Kubernetes mapping")
            continue
        if document.get("kind") == "List":
            nested = validate_documents(document.get("items", []), allowlist)
            for field in ("errors", "warnings", "pending", "inventory"):
                getattr(report, field).extend(getattr(nested, field))
            continue
        metadata = document.get("metadata") or {}
        identity = (document.get("apiVersion"), document.get("kind"), metadata.get("namespace"), metadata.get("name"))
        if identity in seen:
            report.error("document", "duplicate resource identity across inputs")
        seen.add(identity)
        check_document(document, set(allowlist), report)
    if not report.inventory:
        report.error("input", "no Kubernetes resources found")
    return report


class UniqueKeyLoader(yaml.SafeLoader):
    pass


def unique_mapping(loader, node, deep=False):
    mapping = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        if key in mapping:
            raise yaml.YAMLError("duplicate YAML key")
        mapping[key] = loader.construct_object(value_node, deep=deep)
    return mapping


UniqueKeyLoader.add_constructor(yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, unique_mapping)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifests", nargs="+", type=Path)
    parser.add_argument("--allow-cluster-resource", action="append", default=[], metavar="Kind/name")
    args = parser.parse_args(argv)
    documents = []
    for path in args.manifests:
        try:
            with path.open(encoding="utf-8") as stream:
                documents.extend(yaml.load_all(stream, Loader=UniqueKeyLoader))
        except (OSError, yaml.YAMLError, TypeError):
            # Parser exception text can contain a Secret payload; never print it.
            print(f"ERROR: cannot safely parse YAML file: {path}", file=sys.stderr)
            return 2
    try:
        report = validate_documents(documents, args.allow_cluster_resource)
    except (AttributeError, TypeError, ValueError, KeyError):
        print("ERROR: malformed Kubernetes field structure; contents withheld", file=sys.stderr)
        return 2
    for label, entries in (("RESOURCE", report.inventory), ("ERROR", report.errors),
                           ("WARNING", report.warnings), ("LIVE REVIEW REQUIRED", report.pending)):
        for entry in entries:
            print(f"{label}: {entry}")
    print(f"Static contract: {len(report.errors)} error(s), {len(report.warnings)} warning(s), {len(report.pending)} pending live review(s).")
    print("Static success is not deployment approval or proof that generated Pods and existing resources are unchanged.")
    return 1 if report.errors else 0


if __name__ == "__main__":
    sys.exit(main())
