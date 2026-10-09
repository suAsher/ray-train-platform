"""Deployment naming contracts; run on the isolated builder, never locally.

Historical Markdown is deliberately excluded: old deployment evidence must stay
accurate. Runtime inputs, identities and fixtures must describe the new stack.
"""
import json
from pathlib import Path
import runpy
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
NAMESPACES = {"victoriametrics", "clickhouse", "opentelemetry", "nightingale"}
OLD_NAMESPACES = (
    "observability-metrics", "observability-analytics",
    "observability-otel", "observability-alerting",
)


class ComponentNameContracts(unittest.TestCase):
    def test_bootstrap_namespaces_and_quotas_use_component_names(self):
        documents = list(yaml.safe_load_all((ROOT / "00-bootstrap.yaml").read_text()))
        namespaces = {item["metadata"]["name"] for item in documents if item["kind"] == "Namespace"}
        quotas = {item["metadata"]["namespace"] for item in documents if item["kind"] == "ResourceQuota"}
        self.assertEqual(NAMESPACES, namespaces)
        self.assertEqual(NAMESPACES, quotas)

    def test_runtime_references_never_route_to_retired_namespaces(self):
        # These prefixes occurring in a rendered input could silently continue
        # sending data or credentials to the old namespace during coexistence.
        for directory in ("clickhouse", "otel", "nightingale", "victoriametrics", "mtls"):
            for path in (ROOT / directory).rglob("*"):
                if not path.is_file() or path.suffix in (".md", ".pyc"):
                    continue
                if "__pycache__" in path.parts:
                    continue
                text = path.read_text()
                for old in OLD_NAMESPACES:
                    with self.subTest(path=str(path.relative_to(ROOT)), old=old):
                        self.assertNotIn(old, text)

    def test_certificate_dns_names_match_their_component_namespace(self):
        for directory, expected in (("victoriametrics", "victoriametrics"),
                                    ("clickhouse", "clickhouse"),
                                    ("otel", "opentelemetry"),
                                    ("nightingale", "nightingale")):
            identities = json.loads((ROOT / directory / "mtls-identities.json").read_text())
            for identity in identities:
                with self.subTest(component=directory, role=identity["role"]):
                    self.assertEqual(expected, identity["namespace"])
                    for name in identity["dns_names"]:
                        self.assertTrue(name.endswith((f".{expected}.svc", f".{expected}.svc.cluster.local")), name)

    def test_operator_and_cluster_names_are_explanatory(self):
        for relative, name in (("victoriametrics/operator-values.yaml", "victoriametrics-operator"),
                               ("clickhouse/operator-values.yaml", "clickhouse-operator"),
                               ("nightingale/10-cnpg-values.yaml", "cloudnative-pg-operator"),
                               ("nightingale/30-redis-values.yaml", "nightingale-redis")):
            with self.subTest(relative=relative):
                self.assertEqual(name, yaml.safe_load((ROOT / relative).read_text())["fullnameOverride"])
        values = yaml.safe_load((ROOT / "victoriametrics/resources/values.yaml").read_text())
        self.assertEqual("victoriametrics-cluster", values["name"])
        self.assertEqual("victoriametrics", values["expectedNamespace"])
        postgres = yaml.safe_load((ROOT / "nightingale/20-postgres.yaml").read_text())
        self.assertEqual("nightingale-postgres", postgres["metadata"]["name"])
        # Namespacing is not a database identity migration.
        self.assertEqual("n9e", postgres["spec"]["bootstrap"]["initdb"]["database"])
        self.assertEqual("n9e", postgres["spec"]["bootstrap"]["initdb"]["owner"])

    def test_cnpg_webhooks_reject_old_namespace_and_suffix_new_resources(self):
        renderer = runpy.run_path(str(ROOT / "nightingale/scope-cnpg-webhooks.py"))
        name = renderer["WEBHOOK_NAMES"]["ValidatingWebhookConfiguration"]
        document = {"kind": "ValidatingWebhookConfiguration", "metadata": {"name": name},
                    "webhooks": [{"name": "test.cnpg.io", "rules": [{"scope": "*"}],
                                  "clientConfig": {"service": {"namespace": "nightingale"}}}]}
        result = renderer["scope_webhook"](document)
        self.assertEqual({"matchLabels": {"kubernetes.io/metadata.name": "nightingale"}},
                         result["webhooks"][0]["namespaceSelector"])
        self.assertEqual("Namespaced", result["webhooks"][0]["rules"][0]["scope"])
        document["webhooks"][0]["clientConfig"]["service"]["namespace"] = "observability-alerting"
        with self.assertRaisesRegex(ValueError, "namespace"):
            renderer["scope_webhook"](document)
        # Keep strict upstream input names, then apply the official runtime's
        # namespace suffix so the new Helm release cannot take the old webhook.
        self.assertEqual(name + "-nightingale", result["metadata"]["name"])
        self.assertEqual({"cnpg-mutating-webhook-configuration", "cnpg-validating-webhook-configuration"},
                         set(renderer["WEBHOOK_NAMES"].values()))


if __name__ == "__main__":
    unittest.main()
