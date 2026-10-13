"""Render contracts for opt-in platform telemetry. Execute on builder only."""
import json
import importlib.util
import os
from pathlib import Path
import subprocess
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[4]
NEXT = ROOT / "ops/observability/next"
URI = "spiffe://observability.raytrain.local/ray-train-platform/backend-telemetry"
GRAFANA_URI = "spiffe://observability.raytrain.local/grafana/grafana-query"


def render(chart, namespace, *args):
    result = subprocess.run(
        [os.environ.get("HELM", "helm"), "template", "test", str(chart),
         "--namespace", namespace, *args], capture_output=True, text=True, timeout=60)
    if result.returncode:
        raise AssertionError(result.stderr)
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


def one(docs, kind, name=None):
    selected = [d for d in docs if d["kind"] == kind
                and (name is None or d["metadata"]["name"] == name)]
    assert len(selected) == 1, (kind, name, len(selected))
    return selected[0]


class PlatformMetricsContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.docs = render(NEXT / "platform-telemetry", "victoriametrics")

    def test_only_required_scoped_objects(self):
        self.assertEqual(8, len(self.docs))
        for doc in self.docs:
            self.assertIn(doc["kind"], {"Certificate", "CertificateRequestPolicy", "ClusterRole",
                          "ClusterRoleBinding", "Service", "Role", "RoleBinding", "VMServiceScrape"})
            if "namespace" in doc["metadata"]:
                self.assertIn(doc["metadata"]["namespace"], ["ray-train-platform", "victoriametrics"])

    def test_certificate_and_approval_are_exact(self):
        cert = one(self.docs, "Certificate")["spec"]
        policy = one(self.docs, "CertificateRequestPolicy")["spec"]
        self.assertEqual([URI], cert["uris"])
        self.assertEqual(["backend-metrics.ray-train-platform.svc",
                          "backend-metrics.ray-train-platform.svc.cluster.local"], cert["dnsNames"])
        self.assertEqual({"server auth", "client auth"}, set(cert["usages"]))
        self.assertEqual("Always", cert["privateKey"]["rotationPolicy"])
        self.assertEqual(cert["uris"], policy["allowed"]["uris"]["values"])
        self.assertEqual(cert["dnsNames"], policy["allowed"]["dnsNames"]["values"])
        self.assertEqual({"matchNames": ["ray-train-platform"]}, policy["selector"]["namespace"])
        self.assertFalse(policy["allowed"]["isCA"])
        self.assertNotIn("Secret", [d["kind"] for d in self.docs])

    def test_metrics_are_separate_tls_service(self):
        svc = one(self.docs, "Service")["spec"]
        self.assertEqual("ClusterIP", svc["type"])
        self.assertEqual({"app": "ray-train-backend"}, svc["selector"])
        self.assertEqual([{"name": "metrics", "port": 9091, "targetPort": "metrics"}], svc["ports"])
        scrape = one(self.docs, "VMServiceScrape")["spec"]
        self.assertEqual({"matchNames": ["ray-train-platform"]}, scrape["namespaceSelector"])
        ep = scrape["endpoints"][0]
        self.assertEqual("https", ep["scheme"])
        self.assertEqual("/metrics", ep["path"])
        tls = ep["tlsConfig"]
        self.assertEqual("backend-metrics.ray-train-platform.svc", tls["serverName"])
        self.assertFalse(tls.get("insecureSkipVerify", False))
        self.assertEqual("obs-mtls-vmagent", tls["cert"]["secret"]["name"])
        keeps = [r for r in ep["metricRelabelConfigs"] if r.get("action") == "labelkeep"]
        self.assertEqual(1, len(keeps))
        for forbidden in ["user_id", "username", "request_id", "trace_id", "job_id", "run_id"]:
            self.assertNotIn(forbidden, keeps[0]["regex"].split("|"))

    def test_discovery_cannot_read_credentials_or_training(self):
        role = one(self.docs, "Role")
        self.assertEqual("ray-train-platform", role["metadata"]["namespace"])
        for rule in role["rules"]:
            self.assertTrue(set(rule["resources"]) <= {"pods", "services", "endpoints", "endpointslices"})
            self.assertEqual({"get", "list", "watch"}, set(rule["verbs"]))
        binding = one(self.docs, "RoleBinding")
        self.assertEqual([{"kind": "ServiceAccount", "name": "vm-self-scraper",
                           "namespace": "victoriametrics"}], binding["subjects"])


class GrafanaVMReadContract(unittest.TestCase):
    def test_default_does_not_add_identity_or_user(self):
        docs = render(NEXT / "victoriametrics/resources", "victoriametrics")
        self.assertNotIn("grafana-read", [d["metadata"]["name"] for d in docs if d["kind"] == "VMUser"])
        self.assertNotIn(GRAFANA_URI, json.dumps(docs))

    def test_opt_in_user_cannot_write_or_delete(self):
        docs = render(NEXT / "victoriametrics/resources", "victoriametrics", "--set", "grafanaRead.enabled=true")
        user = one(docs, "VMUser", "grafana-read")["spec"]
        self.assertEqual({"name": "vm-grafana-read", "key": "token"}, user["tokenRef"])
        self.assertTrue(user["disable_secret_creation"])
        paths = user["targetRefs"][0]["paths"]
        self.assertIn("/api/v1/query", paths)
        for denied in ["write", "delete", "admin", "export", "import"]:
            self.assertNotIn(denied, " ".join(paths))
        self.assertIn(GRAFANA_URI, json.dumps(docs))

    def test_existing_validator_requires_explicit_grafana_mode(self):
        spec = importlib.util.spec_from_file_location(
            "vm_component_contract", NEXT / "victoriametrics/validate_component_contract.py")
        contract = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(contract)
        docs = render(NEXT / "victoriametrics/resources", "victoriametrics", "--set", "grafanaRead.enabled=true")
        with self.assertRaises(ValueError):
            contract.validate_auth(docs)
        contract.validate_auth(docs, grafana_read=True)
        one(docs, "VMUser", "grafana-read")["spec"]["targetRefs"][0]["paths"].append("/api/v1/write")
        with self.assertRaises(ValueError):
            contract.validate_auth(docs, grafana_read=True)


if __name__ == "__main__":
    unittest.main()
