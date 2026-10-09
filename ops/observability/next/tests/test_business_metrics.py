"""Render first-wave scrape boundaries on the builder; never contacts a cluster."""

import os
from pathlib import Path
import re
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "business-metrics"
VM_CHART = ROOT / "victoriametrics/resources"
WAVE = "business-wave1"
TARGETS = {
    "alloy": ("monitoring", "alloy", "http-metrics", 12345, "alloy", "alloy"),
    "loki": ("loki", "loki-cpu", "http-metrics", 3100, "loki-cpu", "loki-cpu"),
    "dcgm": ("kube-system", "dcgm-exporter", "metrics", 9400, "dcgm-exporter", "nvidia-device-plugin"),
    "kube-state-metrics": ("monitoring", "prometheus-kube-state-metrics", "http", 8080,
                           "kube-state-metrics", "prometheus"),
    "node-exporter": ("monitoring", "prometheus-prometheus-node-exporter", "http-metrics", 9100,
                      "prometheus-node-exporter", "prometheus"),
}


def render(chart=CHART, namespace="victoriametrics", *extra, success=True):
    result = subprocess.run(
        [os.environ.get("HELM", "helm"), "template", "business-metrics-wave1", str(chart),
         "--namespace", namespace, *extra], text=True, capture_output=True, timeout=60,
    )
    if not success:
        return result
    if result.returncode:
        raise AssertionError(f"Helm rendering failed: {result.stderr}")
    return [item for item in yaml.safe_load_all(result.stdout) if item]


def only(objects, kind):
    return [item for item in objects if item["kind"] == kind]


class BusinessMetricsRenderContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.objects = render()

    def targets(self, kind="VMServiceScrape"):
        return {item["metadata"]["labels"]["observability.raytrain.io/target"]: item
                for item in only(self.objects, kind)}

    def test_only_additive_monitoring_objects_no_workloads_or_credentials(self):
        allowed = {"VMServiceScrape", "ServiceMonitor", "Role", "RoleBinding", "NetworkPolicy", "ConfigMap"}
        self.assertLessEqual({item["kind"] for item in self.objects}, allowed)
        for item in self.objects:
            with self.subTest(kind=item["kind"], name=item["metadata"]["name"]):
                labels = item["metadata"]["labels"]
                self.assertEqual("raytrain-observability-next", labels["app.kubernetes.io/part-of"])
                self.assertEqual(WAVE, labels["observability.raytrain.io/wave"])

    def test_vm_targets_are_exact_existing_services_in_three_namespaces(self):
        targets = self.targets()
        self.assertEqual(set(TARGETS), set(targets))
        for component, monitor in targets.items():
            namespace, service, port, _, app, instance = TARGETS[component]
            with self.subTest(component=component):
                self.assertEqual("victoriametrics", monitor["metadata"]["namespace"])
                self.assertEqual(WAVE, monitor["metadata"]["labels"]["observability.raytrain.io/scrape"])
                spec = monitor["spec"]
                self.assertEqual({"matchNames": [namespace]}, spec["namespaceSelector"])
                selector = spec["selector"]["matchLabels"]
                self.assertEqual(app, selector["app.kubernetes.io/name"])
                self.assertEqual(instance, selector["app.kubernetes.io/instance"])
                self.assertEqual(1, len(spec["endpoints"]))
                endpoint = spec["endpoints"][0]
                self.assertEqual(port, endpoint["port"])
                self.assertEqual("/metrics", endpoint["path"])
                self.assertEqual("30s", endpoint["interval"])
                self.assertEqual("10s", endpoint["scrapeTimeout"])
                self.assertFalse(endpoint["honorLabels"])
                keeps = [rule for rule in endpoint["relabelConfigs"] if rule.get("action") == "keep"]
                self.assertEqual([{"action": "keep", "sourceLabels": ["__meta_kubernetes_service_name"],
                                   "regex": service}], keeps)
                values = {rule.get("targetLabel"): rule.get("replacement")
                          for rule in endpoint["relabelConfigs"]}
                self.assertEqual(component, values["component"])
                self.assertEqual(WAVE, values["telemetry_scope"])

    def test_old_prometheus_only_gains_alloy_and_loki_self_monitoring(self):
        targets = self.targets("ServiceMonitor")
        self.assertEqual({"alloy", "loki"}, set(targets))
        for component, monitor in targets.items():
            self.assertEqual("monitoring", monitor["metadata"]["namespace"])
            endpoint = monitor["spec"]["endpoints"][0]
            self.assertIn("relabelings", endpoint)
            self.assertIn("metricRelabelings", endpoint)
            self.assertNotIn("relabelConfigs", endpoint)
            self.assertNotIn("metricRelabelConfigs", endpoint)
            vm = self.targets()[component]
            self.assertEqual(vm["spec"]["namespaceSelector"], monitor["spec"]["namespaceSelector"])
            self.assertEqual(vm["spec"]["selector"], monitor["spec"]["selector"])
            self.assertEqual(vm["spec"]["endpoints"][0]["relabelConfigs"], endpoint["relabelings"])

    def test_metrics_and_labels_are_allowlisted_without_losing_identity(self):
        examples = {
            "alloy": ["loki_write_sent_entries_total", "loki_write_dropped_entries_total",
                      "loki_write_batch_retries_total", "process_resident_memory_bytes",
                      "cluster_ready_for_traffic", "cluster_node_peers",
                      "loki_write_entry_propagation_latency_seconds_bucket"],
            "loki": ["loki_distributor_lines_received_total", "loki_discarded_samples_total",
                     "loki_ingester_flush_queue_length", "loki_request_duration_seconds_bucket"],
            "dcgm": ["DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_FREE"],
            "kube-state-metrics": ["kube_pod_status_phase", "kube_pod_container_resource_requests",
                                   "kube_node_status_allocatable"],
            "node-exporter": ["node_cpu_seconds_total", "node_memory_MemAvailable_bytes",
                              "node_filesystem_avail_bytes"],
        }
        for component, monitor in self.targets().items():
            rules = monitor["spec"]["endpoints"][0]["metricRelabelConfigs"]
            keep = [rule for rule in rules if rule.get("action") == "keep"]
            self.assertEqual(1, len(keep))
            self.assertEqual(["__name__"], keep[0]["sourceLabels"])
            for metric in examples[component]:
                self.assertIsNotNone(re.fullmatch(keep[0]["regex"], metric), f"{component}: {metric}")
            self.assertIsNone(re.fullmatch(keep[0]["regex"], "unapproved_private_business_metric"))
            labels = [rule for rule in rules if rule.get("action") == "labelkeep"]
            self.assertEqual(1, len(labels))
            for label in ("__name__", "job", "instance", "component", "telemetry_scope"):
                self.assertIsNotNone(re.fullmatch(labels[0]["regex"], label), f"{component}: {label}")
            for label in ("token", "email", "username", "trace_id", "request_id", "authorization"):
                self.assertIsNone(re.fullmatch(labels[0]["regex"], label), f"{component}: {label}")

    def test_business_source_labels_do_not_collide_with_discovery_labels(self):
        for component in ("dcgm", "kube-state-metrics"):
            endpoint = self.targets()[component]["spec"]["endpoints"][0]
            rules = endpoint["relabelConfigs"]
            # Prometheus target namespace/pod are exporter coordinates, while
            # KSM/DCGM sample labels describe the observed training workload.
            labels = {"namespace": "monitoring", "pod": "exporter-0", "container": "exporter",
                      "node": "target-node", "service": "exporter", "endpoint": "metrics",
                      "__meta_kubernetes_namespace": "monitoring",
                      "__meta_kubernetes_pod_name": "exporter-0",
                      "__meta_kubernetes_pod_node_name": "target-node"}
            for rule in rules:
                if rule.get("action", "replace") == "replace" and rule.get("sourceLabels"):
                    value = ";".join(labels.get(key, "") for key in rule["sourceLabels"])
                    labels[rule["targetLabel"]] = value
                if rule.get("action") == "labeldrop":
                    labels = {key: value for key, value in labels.items()
                              if not re.fullmatch(rule["regex"], key)}
            for key in ("namespace", "pod", "container", "node"):
                self.assertNotIn(key, labels, f"{component}: target {key} would overwrite source")
            self.assertEqual("monitoring", labels["exporter_namespace"])
            self.assertEqual("exporter-0", labels["exporter_pod"])
            self.assertEqual("target-node", labels["exporter_node"])
            allowed = next(rule["regex"] for rule in endpoint["metricRelabelConfigs"]
                           if rule["action"] == "labelkeep")
            for key in ("namespace", "pod", "container", "node", "exporter_namespace", "exporter_pod"):
                self.assertIsNotNone(re.fullmatch(allowed, key), f"{component}: required {key} lost")
        expected = {
            "dcgm": ["Hostname", "UUID", "modelName", "gpu", "exported_namespace", "exported_pod", "err_code"],
            "node-exporter": ["cpu", "mode", "device", "device_error", "mountpoint", "fstype"],
            "kube-state-metrics": ["resource", "unit", "phase", "condition", "status", "reason",
                                   "pod_cidr", "priority_class"],
            "alloy": ["pod", "le", "state", "component_id", "host", "tenant", "component_path", "cluster_name"],
            "loki": ["pod", "le", "route", "status_code", "reason", "tenant", "format", "policy",
                     "retention_hours", "is_internal_stream", "method", "ws", "type"],
        }
        for component, required_labels in expected.items():
            rules = self.targets()[component]["spec"]["endpoints"][0]["metricRelabelConfigs"]
            allowed = next(rule["regex"] for rule in rules if rule["action"] == "labelkeep")
            for key in required_labels:
                self.assertIsNotNone(re.fullmatch(allowed, key), f"{component}: {key}")

    def test_dcgm_distinct_error_codes_survive_without_unbounded_error_messages(self):
        rules = self.targets()["dcgm"]["spec"]["endpoints"][0]["metricRelabelConfigs"]
        allowed = next(rule["regex"] for rule in rules if rule["action"] == "labelkeep")
        source = [
            {"__name__": "DCGM_FI_DEV_XID_ERRORS", "UUID": "GPU-fixture", "err_code": "31", "err_msg": "text-a"},
            {"__name__": "DCGM_FI_DEV_XID_ERRORS", "UUID": "GPU-fixture", "err_code": "43", "err_msg": "text-b"},
        ]
        series = [tuple(sorted((key, value) for key, value in sample.items()
                               if re.fullmatch(allowed, key))) for sample in source]
        self.assertEqual(2, len(set(series)))
        self.assertIsNone(re.fullmatch(allowed, "err_msg"))
        # Actual source versions also need the pre-release raw endpoint collision
        # gate: a fixture cannot prove err_code -> err_msg remains 1:1 in future.

    def test_cross_namespace_discovery_has_no_secret_or_write_permissions(self):
        roles = only(self.objects, "Role")
        bindings = only(self.objects, "RoleBinding")
        expected = {"kube-system", "monitoring", "loki"}
        self.assertEqual(expected, {item["metadata"]["namespace"] for item in roles})
        self.assertEqual(3, len(roles))
        self.assertEqual(expected, {item["metadata"]["namespace"] for item in bindings})
        self.assertEqual(3, len(bindings))
        for role in roles:
            actual = {(tuple(rule["apiGroups"]), tuple(rule["resources"]), tuple(rule["verbs"]))
                      for rule in role["rules"]}
            self.assertEqual({(("",), ("pods", "services", "endpoints"), ("get", "list", "watch")),
                              (("discovery.k8s.io",), ("endpointslices",), ("get", "list", "watch"))}, actual)
        for binding in bindings:
            self.assertEqual("Role", binding["roleRef"]["kind"])
            self.assertEqual([{"kind": "ServiceAccount", "name": "vm-self-scraper",
                               "namespace": "victoriametrics"}], binding["subjects"])

    def test_egress_is_from_vmagent_to_exact_exporter_namespace_and_port(self):
        policies = only(self.objects, "NetworkPolicy")
        self.assertEqual(1, len(policies))
        policy = policies[0]
        self.assertEqual("victoriametrics", policy["metadata"]["namespace"])
        self.assertEqual({"matchLabels": {"observability.raytrain.io/component": "vmagent"}},
                         policy["spec"]["podSelector"])
        self.assertEqual(["Egress"], policy["spec"]["policyTypes"])
        rules = policy["spec"]["egress"]
        self.assertEqual(5, len(rules))
        actual = set()
        for rule in rules:
            self.assertEqual(1, len(rule["to"]))
            peer = rule["to"][0]
            namespace = peer["namespaceSelector"]["matchLabels"]["kubernetes.io/metadata.name"]
            self.assertTrue(peer["podSelector"]["matchLabels"])
            self.assertEqual(1, len(rule["ports"]))
            self.assertEqual("TCP", rule["ports"][0]["protocol"])
            actual.add((namespace, rule["ports"][0]["port"]))
        self.assertEqual({(target[0], target[3]) for target in TARGETS.values()}, actual)

    def test_chart_rejects_wrong_namespace_and_non_30_second_interval(self):
        for namespace, args in (("default", []), ("victoriametrics", ["--set", "scrapeInterval=15s"]),
                                ("victoriametrics", ["--set", "prometheusNamespace=default"])):
            with self.subTest(namespace=namespace, args=args):
                self.assertNotEqual(0, render(CHART, namespace, *args, success=False).returncode)


class VMAgentBusinessScrapeContracts(unittest.TestCase):
    def spec(self, *extra):
        return only(render(VM_CHART, "victoriametrics", *extra), "VMAgent")[0]["spec"]

    def test_default_preserves_self_only_namespace_boundary(self):
        values = yaml.safe_load((VM_CHART / "values.yaml").read_text())
        self.assertFalse(values["businessScrape"]["enabled"])
        spec = self.spec()
        self.assertTrue(spec["ignoreNamespaceSelectors"])
        self.assertEqual({"matchLabels": {"observability.raytrain.io/scrape": "metrics-self"}},
                         spec["serviceScrapeSelector"])

    def test_opt_in_selects_only_two_labels_without_other_cr_types_or_all_namespaces(self):
        spec = self.spec("--set", "businessScrape.enabled=true")
        self.assertFalse(spec["ignoreNamespaceSelectors"])
        self.assertFalse(spec["selectAllByDefault"])
        self.assertEqual({"matchExpressions": [{"key": "observability.raytrain.io/scrape", "operator": "In",
                                                "values": ["metrics-self", WAVE]}]}, spec["serviceScrapeSelector"])
        for key in ("serviceScrapeNamespaceSelector", "podScrapeSelector", "podScrapeNamespaceSelector",
                    "nodeScrapeSelector", "staticScrapeSelector", "probeSelector", "scrapeConfigSelector"):
            self.assertNotIn(key, spec)
        self.assertEqual("", spec["externalLabelName"])
        self.assertEqual(2, spec["replicaCount"])
        self.assertEqual("30s", spec["scrapeInterval"])
        self.assertNotIn("shardCount", spec)


if __name__ == "__main__":
    unittest.main()
