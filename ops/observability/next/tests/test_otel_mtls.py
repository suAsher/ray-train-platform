"""OTel role/bypass/queue contracts; builder-only static release gates."""

from pathlib import Path
import unittest

import yaml

from test_ch_mtls import ENVOY, identities, public_listeners, role


ROOT = Path(__file__).resolve().parents[1] / "otel"


class OTelMTLSTests(unittest.TestCase):
    def test_health_only_cluster_does_not_use_envoy_sixty_second_idle_default(self):
        config = yaml.safe_load((ROOT / "proxy-otel.yaml").read_text())
        cluster = next(c for c in config["static_resources"]["clusters"]
                       if c["name"] == "collector_health")
        health = cluster["health_checks"][0]
        self.assertEqual("5s", health["interval"])
        self.assertEqual("5s", health["no_traffic_interval"])
        self.assertEqual("1s", health["timeout"])
        self.assertEqual(2, health["unhealthy_threshold"])

    def collector(self):
        return yaml.safe_load((ROOT / "collector.yaml").read_text())

    def workload(self):
        return next(doc for doc in yaml.safe_load_all((ROOT / "gateway.yaml").read_text())
                    if doc and doc["kind"] == "StatefulSet")

    def test_receiver_metrics_and_health_native_endpoints_are_loopback(self):
        config = self.collector()
        protocols = config["receivers"]["otlp/canary"]["protocols"]
        self.assertEqual("127.0.0.1:4317", protocols["grpc"]["endpoint"])
        self.assertEqual("127.0.0.1:4318", protocols["http"]["endpoint"])
        self.assertEqual("127.0.0.1:13133", config["extensions"]["health_check"]["endpoint"])
        metrics = config["service"]["telemetry"]["metrics"]["readers"][0]["pull"]["exporter"]["prometheus"]
        self.assertEqual("127.0.0.1", metrics["host"])

    def test_canary_cannot_scrape_metrics_and_vmagent_cannot_write_telemetry(self):
        config = yaml.safe_load((ROOT / "proxy-otel.yaml").read_text())
        public = public_listeners(config)
        self.assertEqual({4317, 4318, 8888, 14318, 18080}, set(public))
        for port in (4317, 4318):
            self.assertEqual({role("otel", "canary")}, identities(public[port]))
        self.assertEqual({role("metrics", "vmagent")}, identities(public[8888]))
        self.assertEqual({"spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry"},
                         identities(public[14318]))

    def test_clickhouse_exporter_proves_gateway_identity_and_keeps_sql_secret(self):
        exporter = self.collector()["exporters"]["clickhouse/canary"]
        self.assertEqual("otel_ingest", exporter["username"])
        self.assertEqual("${env:CLICKHOUSE_PASSWORD}", exporter["password"])
        self.assertFalse(exporter["create_schema"])
        tls = exporter["tls"]
        self.assertEqual("/etc/obs-mtls/tls.crt", tls["cert_file"])
        self.assertEqual("/etc/obs-mtls/tls.key", tls["key_file"])
        self.assertEqual("/etc/obs-mtls/ca.crt", tls["ca_file"])
        self.assertFalse(tls.get("insecure", False))
        self.assertFalse(tls.get("insecure_skip_verify", False))
        self.assertEqual("clickhouse-clickhouse.clickhouse.svc.cluster.local",
                         tls["server_name_override"])

    def test_persistent_ack_and_canary_data_scope_are_unchanged(self):
        config = self.collector()
        self.assertTrue(config["extensions"]["file_storage/queue"]["fsync"])
        exporter = config["exporters"]["clickhouse/canary"]
        self.assertEqual("file_storage/queue", exporter["sending_queue"]["storage"])
        self.assertEqual("1", exporter["connection_params"]["wait_for_async_insert"])
        for name in ("logs/canary", "traces/canary"):
            pipeline = config["service"]["pipelines"][name]
            self.assertEqual(["otlp/canary"], pipeline["receivers"])
            self.assertNotIn("batch", pipeline["processors"])
            self.assertIn("transform/canary", pipeline["processors"])
        attrs = config["processors"]["resource/canary"]["attributes"]
        self.assertIn({"key": "raytrain.telemetry.source", "value": "synthetic-canary", "action": "upsert"}, attrs)

    def test_workload_pins_nonprivileged_proxy_and_new_role_secret(self):
        workload = self.workload()
        self.assertEqual(2, workload["spec"]["replicas"])
        pod = workload["spec"]["template"]["spec"]
        self.assertFalse(pod.get("hostNetwork", False))
        self.assertFalse(pod.get("automountServiceAccountToken", True))
        sidecar = next(c for c in pod["containers"] if c["name"] == "envoy")
        self.assertEqual(ENVOY, sidecar["image"])
        security = sidecar["securityContext"]
        self.assertTrue(security["readOnlyRootFilesystem"])
        self.assertFalse(security["allowPrivilegeEscalation"])
        self.assertEqual(["ALL"], security["capabilities"]["drop"])
        names = {v["secret"]["secretName"] for v in pod["volumes"] if "secret" in v}
        self.assertIn("obs-mtls-otel-gateway", names)
        self.assertNotIn("obs-mtls-canary", names)
        self.assertNotIn("obs-mtls-schema-admin", names)
        collector = next(c for c in pod["containers"] if c["name"] == "collector")
        for field in ("startupProbe", "readinessProbe", "livenessProbe"):
            self.assertEqual(18080, collector[field]["httpGet"]["port"])
            self.assertIn(collector[field]["httpGet"]["path"], ("/readyz", "/livez"))


if __name__ == "__main__":
    unittest.main()
