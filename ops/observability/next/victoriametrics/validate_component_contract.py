#!/usr/bin/env python3
"""Validate first-stage VM boundaries against Helm-rendered YAML, on the builder.

Usage: python3 validate_component_contract.py operator-rendered.yaml metrics-rendered.yaml
Requires the builder's PyYAML. This reads files only and never contacts Kubernetes.
"""

import sys
from pathlib import Path

import yaml


NAMESPACE = "victoriametrics"
DENIED_KINDS = {
    "Prometheus", "PrometheusRule", "ServiceMonitor", "PodMonitor", "Ingress",
    "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration",
}
CONVERTERS = {
    "VM_ENABLEDPROMETHEUSCONVERTER_PODMONITOR",
    "VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE",
    "VM_ENABLEDPROMETHEUSCONVERTER_PROMETHEUSRULE",
    "VM_ENABLEDPROMETHEUSCONVERTER_PROBE",
    "VM_ENABLEDPROMETHEUSCONVERTER_ALERTMANAGERCONFIG",
    "VM_ENABLEDPROMETHEUSCONVERTER_SCRAPECONFIG",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def one(objects, kind):
    matches = [item for item in objects if item.get("kind") == kind]
    require(len(matches) == 1, f"expected exactly one {kind}, got {len(matches)}")
    return matches[0]


def validate_operator(objects):
    deployments = [item for item in objects if item.get("kind") == "Deployment"]
    matches = []
    for deployment in deployments:
        for container in deployment["spec"]["template"]["spec"]["containers"]:
            env = {item["name"]: item.get("value") for item in container.get("env", [])}
            if "WATCH_NAMESPACE" in env:
                matches.append((env, container.get("args", [])))
    require(len(matches) == 1, "expected one namespaced operator deployment")
    env, args = matches[0]
    require(env["WATCH_NAMESPACE"] == NAMESPACE, "operator namespace watch expanded")
    require(all(env.get(key) == "false" for key in CONVERTERS), "converter enabled/missing")
    require(env.get("VM_ENABLEDPROMETHEUSCONVERTEROWNERREFERENCES") != "true",
            "converter ownership must remain disabled")
    disabled = {
        kind
        for argument in args
        if argument.startswith("--controller.disableReconcileFor=")
        for kind in argument.split("=", 1)[1].split(",")
    }
    require("VTAgent" in disabled, "VTAgent global watch must remain disabled")


def validate_scraper_rbac(objects):
    agent = one(objects, "VMAgent")
    spec, service_account = agent["spec"], "vm-self-scraper"
    require(spec.get("serviceAccountName") == service_account and not spec.get("useLegacyNaming"),
            "scraper RBAC requires explicit managed SA and default config naming")
    roles = [item for item in objects if item.get("kind") == "Role"
             and item["metadata"].get("namespace") == NAMESPACE
             and item["metadata"]["name"] == service_account]
    require(len(roles) == 1, "scraper RBAC needs exactly one namespaced Role")
    expected = [
        {"apiGroups": [""], "resources": ["pods", "services", "endpoints"], "verbs": ["get", "list", "watch"]},
        {"apiGroups": ["discovery.k8s.io"], "resources": ["endpointslices"], "verbs": ["get", "list", "watch"]},
        {"apiGroups": [""], "resources": ["secrets"], "verbs": ["get", "list", "watch"],
         "resourceNames": ["vmagent-" + agent["metadata"]["name"]]},
    ]
    require(roles[0].get("rules") == expected, "scraper RBAC expanded beyond discovery and own config Secret")
    bindings = []
    groups = {"system:serviceaccounts", "system:serviceaccounts:" + NAMESPACE, "system:authenticated"}
    for item in objects:
        if item.get("kind") not in ("RoleBinding", "ClusterRoleBinding"):
            continue
        for subject in item.get("subjects", []):
            direct = subject.get("kind") == "ServiceAccount" and subject.get("name") == service_account \
                and subject.get("namespace", item["metadata"].get("namespace")) == NAMESPACE
            group = subject.get("kind") == "Group" and subject.get("name") in groups
            if direct or group:
                bindings.append(item)
                break
    require(len(bindings) == 1, "scraper RBAC has missing or additional bindings")
    binding = bindings[0]
    require(binding["kind"] == "RoleBinding" and binding["metadata"].get("namespace") == NAMESPACE
            and binding["roleRef"] == {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": service_account},
            "scraper RBAC binding must reference only the narrow namespaced Role")


def validate_agent(objects):
    validate_scraper_rbac(objects)
    spec = one(objects, "VMAgent")["spec"]
    require(spec.get("selectAllByDefault") is False, "agent selects objects by default")
    require(spec.get("ignoreNamespaceSelectors") is True, "scrape namespace not enforced")
    require(spec.get("serviceScrapeSelector") == {
        "matchLabels": {"observability.raytrain.io/scrape": "metrics-self"}
    }, "agent self selector changed")
    for field in ["serviceScrapeNamespaceSelector", "podScrapeSelector", "podScrapeNamespaceSelector",
                  "nodeScrapeSelector", "nodeScrapeNamespaceSelector", "probeSelector",
                  "probeNamespaceSelector", "staticScrapeSelector", "staticScrapeNamespaceSelector",
                  "scrapeConfigSelector", "scrapeConfigNamespaceSelector"]:
        require(field not in spec, f"unapproved agent selector: {field}")
    require(spec.get("replicaCount") == 2 and spec.get("statefulMode") is True,
            "expected two persistent HA agents")
    require(spec.get("externalLabelName") == "", "agent identity label changes deduplication")
    require(spec.get("remoteWriteSettings", {}).get("tmpDataPath") ==
            "/vmagent_pq/vmagent-remotewrite-data", "queue path not on StatefulMode PVC")
    remote = spec.get("remoteWrite", [])
    require(len(remote) == 1, "unapproved extra remote write destination")
    require(remote[0].get("url") ==
            f"https://vmauth-victoriametrics-cluster.{NAMESPACE}.svc:8427/api/v1/write",
            "agent must write through authenticated gateway")
    require(remote[0].get("bearerTokenSecret") == {"name": "vm-self-write", "key": "token"},
            "agent credential must be approved write Secret reference")


def validate_reloader_rbac(objects):
    pairs = (("VMAuth", "vm-auth-reloader", "vmauth-config-", ""),
             ("VMAlertmanager", "vm-am-reloader", "vmalertmanager-", "-config"))
    for kind, account, prefix, suffix in pairs:
        cr = one(objects, kind)
        spec = cr["spec"]
        require(spec.get("serviceAccountName") == account and not spec.get("useLegacyNaming"),
                "reloader RBAC requires explicit SA and default naming")
        require(not any(spec.get(key) for key in ("secretRef", "localPath", "configSecret")),
                "reloader RBAC requires the generated config Secret")
        expected = [{"apiGroups": [""], "resources": ["secrets"], "verbs": ["get", "list", "watch"],
                     "resourceNames": [prefix + cr["metadata"]["name"] + suffix]}]
        roles = [item for item in objects if item.get("kind") == "Role"
                 and item["metadata"].get("namespace") == NAMESPACE and item["metadata"]["name"] == account]
        require(len(roles) == 1 and roles[0].get("rules") == expected,
                "reloader RBAC expanded beyond own config Secret")
        accounts = [item for item in objects if item.get("kind") == "ServiceAccount"
                    and item["metadata"].get("namespace") == NAMESPACE and item["metadata"]["name"] == account]
        require(len(accounts) == 1, "reloader RBAC requires exactly one explicit ServiceAccount")
        bindings = reloader_bindings(objects, account)
        require(len(bindings) == 1, "reloader RBAC has missing or additional bindings")
        binding = bindings[0]
        require(binding["kind"] == "RoleBinding" and binding["metadata"].get("namespace") == NAMESPACE
                and binding["roleRef"] == {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": account},
                "reloader RBAC must reference only its narrow namespaced Role")


def reloader_bindings(objects, account):
    bindings = []
    groups = {"system:serviceaccounts", "system:serviceaccounts:" + NAMESPACE, "system:authenticated"}
    for item in objects:
        if item.get("kind") not in ("RoleBinding", "ClusterRoleBinding"):
            continue
        for subject in item.get("subjects", []):
            direct = subject.get("kind") == "ServiceAccount" and subject.get("name") == account \
                and subject.get("namespace", item["metadata"].get("namespace")) == NAMESPACE
            user = subject.get("kind") == "User" and subject.get("name") == f"system:serviceaccount:{NAMESPACE}:{account}"
            group = subject.get("kind") == "Group" and subject.get("name") in groups
            if direct or user or group:
                bindings.append(item)
                break
    return bindings


def validate_auth(objects):
    spec = one(objects, "VMAuth")["spec"]
    require(spec.get("selectAllByDefault") is False, "auth selects users by default")
    require(spec.get("userSelector") == {
        "matchLabels": {"observability.raytrain.io/auth": "self-only"}
    }, "auth selector expanded")
    for key in ["unauthorizedUserAccessSpec", "unauthorizedAccessConfig", "externalConfig",
                "configSecret", "userNamespaceSelector", "ingress", "httpRoute"]:
        require(key not in spec, f"unapproved auth configuration: {key}")
    users = {item["metadata"]["name"]: item["spec"] for item in objects if item.get("kind") == "VMUser"}
    require(set(users) == {"self-read", "self-write"}, "unapproved VMUser")
    read_paths = {"/api/v1/query", "/api/v1/query_range", "/api/v1/labels",
                  "/api/v1/label/[^/]+/values", "/api/v1/series", "/api/v1/metadata"}
    for name, user in users.items():
        require(user.get("tokenRef") == {"name": f"vm-{name}", "key": "token"},
                f"{name} must reference approved Secret")
        require(not any(key in user for key in ["password", "bearerToken", "username", "jwt"]),
                "inline or alternate VMUser authentication")
        refs = user.get("targetRefs", [])
        require(len(refs) == 1, "unapproved extra route")
        read = name == "self-read"
        port, action = (18481, "select") if read else (18480, "insert")
        require(refs[0].get("static", {}).get("url") ==
                f"http://127.0.0.1:{port}/{action}/1001/prometheus",
                "auth tenant/backend route changed")
        require(set(refs[0].get("paths", [])) == (read_paths if read else {"/api/v1/write"}),
                "auth endpoint scope expanded")


def validate_rules(objects):
    alert = one(objects, "VMAlert")["spec"]
    require(alert.get("selectAllByDefault") is False and "ruleNamespaceSelector" not in alert,
            "alert rule namespace expanded")
    require(alert.get("ruleSelector") == {
        "matchLabels": {"observability.raytrain.io/rules": "canary-only"}
    }, "alert may select production rules")
    rule = one(objects, "VMRule")["spec"]
    rules = [item for group in rule["groups"] for item in group["rules"]]
    require(len(rules) == 2 and all(item.get("expr") == "vector(1)" for item in rules),
            "first stage must contain only canary rules")
    am = one(objects, "VMAlertmanager")["spec"]
    require(am.get("selectAllByDefault") is False, "alertmanager merges arbitrary config")
    require(not any(key in am for key in ["configSelector", "configNamespaceSelector", "configSecret"]),
            "unapproved alertmanager configuration source")
    config = yaml.safe_load(am["configRawYaml"])
    require(config.get("receivers") == [{"name": "blackhole-canary"}], "real notification receiver found")
    require(config.get("route", {}).get("receiver") == "blackhole-canary", "route not isolated")


def main(paths):
    require(len(paths) == 2, "pass operator-rendered.yaml and metrics-rendered.yaml")
    objects = []
    for filename in paths:
        objects.extend(item for item in yaml.safe_load_all(Path(filename).read_text()) if item)
    for item in objects:
        require(item.get("kind") not in DENIED_KINDS, f"unapproved kind {item.get('kind')}")
        namespace = item.get("metadata", {}).get("namespace")
        require(namespace in (None, NAMESPACE), f"resource targets another namespace: {namespace}")
        if item.get("kind") == "ClusterRole":
            labels = item.get("metadata", {}).get("labels", {})
            require(not any(key.startswith("rbac.authorization.k8s.io/aggregate-to-") for key in labels),
                    "operator aggregates privileges into existing user roles")
            if labels.get("observability.raytrain.io/rbac-scope") == "cluster-read":
                allowed = {"": {"nodes", "namespaces"}, "storage.k8s.io": {"storageclasses"},
                           "apiextensions.k8s.io": {"customresourcedefinitions"}}
                for rule in item.get("rules", []):
                    require(not rule.get("nonResourceURLs"), "unapproved cluster-wide metrics access")
                    require(set(rule.get("verbs", [])) <= {"get", "list", "watch"},
                            "cluster role must remain read-only")
                    require(all(set(rule.get("resources", [])) <= allowed.get(group, set())
                                for group in rule.get("apiGroups", [])),
                            "unapproved cluster-wide resource discovery")
    validate_operator(objects)
    validate_agent(objects)
    validate_reloader_rbac(objects)
    validate_auth(objects)
    validate_rules(objects)
    print("VM first-stage component boundaries: PASS (rendered YAML only; live verification still required)")


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (ValueError, KeyError, TypeError, OSError, yaml.YAMLError) as error:
        print(f"VM component contract: FAIL: {error}", file=sys.stderr)
        raise SystemExit(1)
