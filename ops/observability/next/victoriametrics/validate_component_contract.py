#!/usr/bin/env python3
"""Validate explicit VM rollout boundaries against rendered YAML, on the builder.

Usage: python3 validate_component_contract.py operator-rendered.yaml metrics-rendered.yaml
       python3 validate_component_contract.py --business-wave1 operator.yaml metrics.yaml business.yaml
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
BUSINESS_NAMESPACES = {"kube-system", "monitoring", "loki"}
BUSINESS_TARGETS = {
    "alloy": ("monitoring", "alloy", "http-metrics", 12345, "alloy", "alloy"),
    "loki": ("loki", "loki-cpu", "http-metrics", 3100, "loki-cpu", "loki-cpu"),
    "dcgm": ("kube-system", "dcgm-exporter", "metrics", 9400, "dcgm-exporter", "nvidia-device-plugin"),
    "kube-state-metrics": ("monitoring", "prometheus-kube-state-metrics", "http", 8080,
                           "kube-state-metrics", "prometheus"),
    "node-exporter": ("monitoring", "prometheus-prometheus-node-exporter", "http-metrics", 9100,
                      "prometheus-node-exporter", "prometheus"),
}
DISCOVERY_RULES = [
    {"apiGroups": [""], "resources": ["pods", "services", "endpoints"], "verbs": ["get", "list", "watch"]},
    {"apiGroups": ["discovery.k8s.io"], "resources": ["endpointslices"], "verbs": ["get", "list", "watch"]},
]


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


def validate_scraper_rbac(objects, business_wave1=False):
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
    bindings = reloader_bindings(objects, service_account)
    require(len(bindings) == (4 if business_wave1 else 1), "scraper RBAC has missing or additional bindings")
    local = [item for item in bindings if item["metadata"].get("namespace") == NAMESPACE]
    require(len(local) == 1, "scraper RBAC needs exactly one local binding")
    binding = local[0]
    require(binding["kind"] == "RoleBinding" and binding["metadata"].get("namespace") == NAMESPACE
            and binding["roleRef"] == {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": service_account},
            "scraper RBAC binding must reference only the narrow namespaced Role")
    if business_wave1:
        validate_business_discovery(objects)


def validate_agent(objects, business_wave1=False):
    validate_scraper_rbac(objects, business_wave1=business_wave1)
    spec = one(objects, "VMAgent")["spec"]
    require(spec.get("selectAllByDefault") is False, "agent selects objects by default")
    require(spec.get("ignoreNamespaceSelectors") is (not business_wave1), "scrape namespace mode changed")
    selector = {"matchExpressions": [{"key": "observability.raytrain.io/scrape", "operator": "In",
                                      "values": ["metrics-self", "business-wave1"]}]} if business_wave1 else {
        "matchLabels": {"observability.raytrain.io/scrape": "metrics-self"}}
    require(spec.get("serviceScrapeSelector") == selector, "agent explicit scrape selector changed")
    for field in ["serviceScrapeNamespaceSelector", "podScrapeSelector", "podScrapeNamespaceSelector",
                  "nodeScrapeSelector", "nodeScrapeNamespaceSelector", "probeSelector",
                  "probeNamespaceSelector", "staticScrapeSelector", "staticScrapeNamespaceSelector",
                  "scrapeConfigSelector", "scrapeConfigNamespaceSelector"]:
        require(field not in spec, f"unapproved agent selector: {field}")
    require(spec.get("replicaCount") == 2 and spec.get("statefulMode") is True,
            "expected two persistent HA agents")
    require(spec.get("externalLabelName") == "", "agent identity label changes deduplication")
    if business_wave1:
        require(spec.get("scrapeInterval") == "30s" and "shardCount" not in spec,
                "business-wave1 requires 30-second HA double scraping without sharding")
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


def validate_auth(objects, grafana_read=False):
    spec = one(objects, "VMAuth")["spec"]
    require(spec.get("selectAllByDefault") is False, "auth selects users by default")
    require(spec.get("userSelector") == {
        "matchLabels": {"observability.raytrain.io/auth": "self-only"}
    }, "auth selector expanded")
    for key in ["unauthorizedUserAccessSpec", "unauthorizedAccessConfig", "externalConfig",
                "configSecret", "userNamespaceSelector", "ingress", "httpRoute"]:
        require(key not in spec, f"unapproved auth configuration: {key}")
    users = {item["metadata"]["name"]: item["spec"] for item in objects if item.get("kind") == "VMUser"}
    expected_users = {"self-read", "self-write"} | ({"grafana-read"} if grafana_read else set())
    require(set(users) == expected_users, "unapproved VMUser")
    read_paths = {"/api/v1/query", "/api/v1/query_range", "/api/v1/labels",
                  "/api/v1/label/[^/]+/values", "/api/v1/series", "/api/v1/metadata"}
    for name, user in users.items():
        require(user.get("tokenRef") == {"name": f"vm-{name}", "key": "token"},
                f"{name} must reference approved Secret")
        require(not any(key in user for key in ["password", "bearerToken", "username", "jwt"]),
                "inline or alternate VMUser authentication")
        refs = user.get("targetRefs", [])
        require(len(refs) == 1, "unapproved extra route")
        read = name in {"self-read", "grafana-read"}
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


def validate_business_discovery(objects):
    name = "business-wave1-vm-discovery"
    for kind in ("Role", "RoleBinding"):
        selected = [item for item in objects if item.get("kind") == kind
                    and item.get("metadata", {}).get("name") == name]
        require(len(selected) == 3 and {item["metadata"].get("namespace") for item in selected}
                == BUSINESS_NAMESPACES, "business discovery must use exactly three reviewed namespaces")
        for item in selected:
            if kind == "Role":
                require(item.get("rules") == DISCOVERY_RULES, "business discovery Role grants unapproved access")
            else:
                require(item.get("roleRef") == {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name}
                        and item.get("subjects") == [{"kind": "ServiceAccount", "name": "vm-self-scraper",
                                                       "namespace": NAMESPACE}],
                        "business discovery binding must name only the exact scraper SA and Role")


def business_service_labels(component):
    target = BUSINESS_TARGETS[component]
    labels = {"app.kubernetes.io/name": target[4], "app.kubernetes.io/instance": target[5]}
    if component == "loki":
        labels["app.kubernetes.io/component"] = "single-binary"
    return labels


def business_target_relabels(component):
    rules = [{"action": "keep", "sourceLabels": ["__meta_kubernetes_service_name"],
              "regex": BUSINESS_TARGETS[component][1]}]
    rules.extend({"sourceLabels": [source], "targetLabel": target} for source, target in (
        ("__meta_kubernetes_namespace", "exporter_namespace"),
        ("__meta_kubernetes_pod_name", "exporter_pod"),
        ("__meta_kubernetes_pod_node_name", "exporter_node")))
    if component in ("dcgm", "kube-state-metrics"):
        rules.append({"action": "labeldrop", "regex": "namespace|pod|container|node|service|endpoint"})
    return rules + [{"targetLabel": "component", "replacement": component},
                    {"targetLabel": "telemetry_scope", "replacement": "business-wave1"}]


def validate_business_scrape(item, policy):
    component = item["metadata"].get("labels", {}).get("observability.raytrain.io/target")
    require(component in BUSINESS_TARGETS, "business scrape has an unapproved component")
    namespace, service, port, _, _, _ = BUSINESS_TARGETS[component]
    is_vm = item["kind"] == "VMServiceScrape"
    require(is_vm or component in ("alloy", "loki"), "Prometheus must only gain Alloy/Loki monitors")
    require(item["metadata"]["name"] == "business-wave1-" + component
            and item["metadata"]["namespace"] == (NAMESPACE if is_vm else "monitoring"),
            "business scrape object identity changed")
    if is_vm:
        require(item["metadata"]["labels"].get("observability.raytrain.io/scrape") == "business-wave1",
                "business scrape selection label changed")
    spec = item["spec"]
    require(set(spec) == {"namespaceSelector", "selector", "endpoints"}
            and spec["namespaceSelector"] == {"matchNames": [namespace]}
            and spec["selector"] == {"matchLabels": business_service_labels(component)},
            f"business scrape discovery scope changed for {service}")
    endpoints = spec["endpoints"]
    require(len(endpoints) == 1, "business scrape must expose exactly one reviewed endpoint")
    endpoint = endpoints[0]
    relabel_key, metric_key = ("relabelConfigs", "metricRelabelConfigs") if is_vm else ("relabelings", "metricRelabelings")
    expected = {"port": port, "path": "/metrics", "scheme": "http", "interval": "30s",
                "scrapeTimeout": "10s", "honorLabels": False, relabel_key: business_target_relabels(component),
                metric_key: [{"action": "keep", "sourceLabels": ["__name__"], "regex": policy[component]["metrics"]},
                             {"action": "labelkeep", "regex": policy[component]["labels"]}]}
    require(endpoint == expected, "business scrape endpoint/relabel/allowlist differs from reviewed policy")


def validate_business_egress(item):
    expected = []
    for component, target in BUSINESS_TARGETS.items():
        peer = {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": target[0]}},
                "podSelector": {"matchLabels": business_service_labels(component)}}
        expected.append({"to": [peer], "ports": [{"protocol": "TCP", "port": target[3]}]})
    spec = item["spec"]
    require(set(spec) == {"podSelector", "policyTypes", "egress"}
            and spec["podSelector"] == {"matchLabels": {"observability.raytrain.io/component": "vmagent"}}
            and spec["policyTypes"] == ["Egress"] and spec["egress"] == expected,
            "business egress must remain VMagent to the five reviewed exporter peers/ports")


def validate_business_wave1(objects):
    required = {(kind, namespace, name) for kind in ("Role", "RoleBinding")
                for namespace in BUSINESS_NAMESPACES for name in ("business-wave1-vm-discovery",)}
    required.update(("VMServiceScrape", NAMESPACE, "business-wave1-" + component) for component in BUSINESS_TARGETS)
    required.update(("ServiceMonitor", "monitoring", "business-wave1-" + component) for component in ("alloy", "loki"))
    required.add(("NetworkPolicy", NAMESPACE, "business-wave1-vmagent-exporters"))
    dashboard = ("ConfigMap", "monitoring", "raytrain-observability-wave1-dashboard")
    identities = [(item.get("kind"), item.get("metadata", {}).get("namespace"),
                   item.get("metadata", {}).get("name")) for item in objects]
    require(len(identities) == len(set(identities)) and set(identities) in (required, required | {dashboard}),
            "business manifest contains missing, duplicate or unapproved objects")
    # White lists are a reviewed chart policy; the rendered deployment must not
    # be broadened by hand. Fixed target/RBAC boundaries above are independent.
    values = yaml.safe_load((Path(__file__).resolve().parents[1] / "business-metrics/values.yaml").read_text())
    policy = {item["component"]: item for item in values["targets"]}
    require(set(policy) == set(BUSINESS_TARGETS), "business source policy target set changed")
    validate_business_discovery(objects)
    for item in objects:
        labels = item.get("metadata", {}).get("labels", {})
        require(labels.get("app.kubernetes.io/part-of") == "raytrain-observability-next"
                and labels.get("observability.raytrain.io/wave") == "business-wave1",
                "business object missing ownership/wave labels")
        if item["kind"] in ("VMServiceScrape", "ServiceMonitor"):
            validate_business_scrape(item, policy)
        elif item["kind"] == "NetworkPolicy":
            validate_business_egress(item)
        elif item["kind"] == "ConfigMap":
            require(set(item.get("data", {})) == {"raytrain-observability-wave1.json"},
                    "business dashboard may only provision its reviewed dashboard")


def validate_business_base_scrapes(objects):
    components = {"vmstorage", "vmselect", "vminsert", "vmagent", "vmauth", "vmalert",
                  "vmalertmanager", "vm-operator"}
    scrapes = [item for item in objects if item.get("kind") == "VMServiceScrape"]
    require(len(scrapes) == 8 and {item["metadata"]["name"] for item in scrapes}
            == {"metrics-self-" + component for component in components},
            "business mode base render must contain exactly the original eight self scrapes")
    for item in scrapes:
        component = item["metadata"]["name"].removeprefix("metrics-self-")
        require(item["metadata"].get("namespace") == NAMESPACE
                and item["metadata"].get("labels", {}).get("observability.raytrain.io/scrape") == "metrics-self"
                and item["spec"].get("namespaceSelector") == {"matchNames": [NAMESPACE]}
                and item["spec"].get("selector") == {"matchLabels": {
                    "observability.raytrain.io/self-metrics": "true",
                    "observability.raytrain.io/component": component}},
                "business mode cannot broaden or relabel an existing self scrape")


def main(paths):
    flags = {item for item in paths if item.startswith("--")}
    require(flags <= {"--business-wave1", "--grafana-read"}, "unapproved validation mode")
    business_wave1 = "--business-wave1" in flags
    grafana_read = "--grafana-read" in flags
    filenames = [item for item in paths if not item.startswith("--")]
    require(len(filenames) == (3 if business_wave1 else 2),
            "pass operator and metrics rendered YAML; --business-wave1 also requires its separate business YAML")
    objects = []
    for filename in filenames[:2]:
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
    if business_wave1:
        validate_business_base_scrapes(objects)
        business = [item for item in yaml.safe_load_all(Path(filenames[2]).read_text()) if item]
        validate_business_wave1(business)
        objects = [*objects, *business]
    validate_operator(objects)
    validate_agent(objects, business_wave1=business_wave1)
    validate_reloader_rbac(objects)
    validate_auth(objects, grafana_read=grafana_read)
    validate_rules(objects)
    mode = "business-wave1" if business_wave1 else "first-stage"
    print(f"VM {mode} component boundaries: PASS (rendered YAML only; live verification still required)")


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (ValueError, KeyError, TypeError, OSError, yaml.YAMLError) as error:
        print(f"VM component contract: FAIL: {error}", file=sys.stderr)
        raise SystemExit(1)
