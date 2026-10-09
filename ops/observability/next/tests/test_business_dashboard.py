"""Protect the additive dashboard's data source, scope and metric semantics."""

import json
from pathlib import Path
import re
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "business-metrics/dashboard.yaml"


class BusinessDashboardContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.documents = list(yaml.safe_load_all(MANIFEST.read_text()))
        cls.config = cls.documents[0]
        cls.dashboard = json.loads(next(iter(cls.config["data"].values())))
        cls.metric_panels = [panel for panel in cls.dashboard["panels"]
                             if panel.get("targets")]

    def test_only_one_new_configmap_is_provisioned_without_runtime_changes(self):
        self.assertEqual(1, len(self.documents))
        self.assertEqual("v1", self.config["apiVersion"])
        self.assertEqual("ConfigMap", self.config["kind"])
        self.assertEqual("monitoring", self.config["metadata"]["namespace"])
        self.assertEqual("1", self.config["metadata"]["labels"]["grafana_dashboard"])
        self.assertEqual("raytrain-observability-wave1", self.dashboard["uid"])
        self.assertNotEqual("gpu-training-dashboard", self.config["metadata"]["name"])
        self.assertEqual(1, len(self.config["data"]))
        self.assertTrue(next(iter(self.config["data"])).endswith(".json"))

    def test_all_queries_use_existing_prometheus_without_credentials_or_alerts(self):
        expected = {"type": "prometheus", "uid": "prometheus"}
        self.assertGreater(len(self.metric_panels), 10)
        for panel in self.metric_panels:
            self.assertEqual(expected, panel["datasource"])
            for target in panel["targets"]:
                self.assertEqual(expected, target.get("datasource", expected))
            self.assertNotIn("alert", panel)
        text = MANIFEST.read_text().lower()
        for forbidden in ("bearer", "password", "securejsondata", "client_secret",
                          "webhook", "private key", "basic_auth", "token:"):
            self.assertNotIn(forbidden, text)
        self.assertIn("原 Prometheus", self.dashboard["description"])
        self.assertIn("夜莺", self.dashboard["description"])

    def test_ids_grid_and_rows_keep_the_dashboard_readable(self):
        panels = self.dashboard["panels"]
        ids = [panel["id"] for panel in panels]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertGreaterEqual(sum(panel["type"] == "row" for panel in panels), 3)
        occupied = set()
        for panel in panels:
            box = panel["gridPos"]
            self.assertGreater(box["w"], 0)
            self.assertLessEqual(box["x"] + box["w"], 24)
            self.assertGreater(box["h"], 0)
            cells = {(x, y) for x in range(box["x"], box["x"] + box["w"])
                     for y in range(box["y"], box["y"] + box["h"])}
            self.assertTrue(occupied.isdisjoint(cells), panel["title"])
            occupied.update(cells)

    def test_missing_series_stays_unknown_and_counters_show_recent_activity(self):
        for panel in self.metric_panels:
            self.assertEqual("N/A", panel["fieldConfig"]["defaults"]["noValue"])
            for target in panel["targets"]:
                query = target["expr"]
                self.assertNotRegex(query, r"\bor\s+(?:on\([^)]*\)\s+)?vector\(0\)")
                if re.search(r"\b\w+_(?:total|bucket|count|sum)\b", query):
                    self.assertRegex(query, r"\b(?:rate|increase)\(", query)

    def test_logging_series_are_scoped_and_rejection_counts_keep_pod_dimension(self):
        queries = [target["expr"] for panel in self.metric_panels
                   for target in panel["targets"]]
        for query in queries:
            if "loki_" in query or "process_" in query or "cluster_" in query:
                self.assertIn('telemetry_scope="business-wave1"', query)
                self.assertRegex(query, r'component="(?:alloy|loki)"')
        discarded = [panel for panel in self.metric_panels if any(
            "loki_discarded_samples_total" in target["expr"]
            for target in panel["targets"])]
        self.assertEqual(1, len(discarded))
        self.assertIn("唯一", discarded[0]["description"])
        query = discarded[0]["targets"][0]["expr"]
        self.assertRegex(query, r"by\s*\(pod,\s*reason\)")
        self.assertIn("increase(", query)

    def test_required_operational_signals_are_present(self):
        queries = "\n".join(target["expr"] for panel in self.metric_panels
                            for target in panel["targets"])
        for metric in ("up{", "loki_write_batch_retries_total",
                       "loki_write_dropped_entries_total", "process_cpu_seconds_total",
                       "process_resident_memory_bytes", "loki_distributor_bytes_received_total",
                       "loki_request_duration_seconds_count", "loki_request_duration_seconds_bucket",
                       "loki_discarded_samples_total", "loki_ingester_flush_queue_length",
                       "DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED",
                       "kube_pod_status_ready", "kube_pod_container_status_restarts_total",
                       "kube_node_status_condition"):
            self.assertIn(metric, queries)
        self.assertIn('status_code=~"429|5.."', queries)
        self.assertIn('namespace="ray-train-platform"', queries)
        self.assertIn('pod=~"ray-train-backend-.+"', queries)
        self.assertIn('condition="Ready"', queries)
        self.assertIn('route="loki_api_v1_push"', queries)
        self.assertIn("histogram_quantile(0.95", queries)


if __name__ == "__main__":
    unittest.main()
