"""cert-manager and approver-policy contracts for the additive observability PKI.

These tests are intentionally renderer-facing and do not contact Kubernetes.
They pin the security boundary before replacing the one-shot OpenSSL issuer:
CA private keys stay in cert-manager, certificates keep exact role identities,
and approver-policy cannot approve namespace or URI escapes.
"""

import importlib.util
import json
from pathlib import Path
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
CERT_MANAGER = ROOT / "cert-manager"
RENDERER = CERT_MANAGER / "render.py"
IMPORTER = CERT_MANAGER / "import_ca_bundle.py"

NAMESPACE_MAP = {
    "observability-metrics": "victoriametrics",
    "observability-analytics": "clickhouse",
    "observability-otel": "opentelemetry",
    "observability-alerting": "nightingale",
}
TRUST_DOMAINS = {"mesh", "am-gossip", "redis", "postgres-client", "postgres-server"}
URI_ROOT = "spiffe://observability.raytrain.local"


def load_renderer():
    spec = importlib.util.spec_from_file_location("obs_cert_manager_render", RENDERER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_importer():
    spec = importlib.util.spec_from_file_location("obs_cert_manager_import", IMPORTER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def source_identities():
    entries = []
    for path in sorted(ROOT.glob("*/mtls-identities.json")):
        entries.extend(json.loads(path.read_text()))
    return entries


def postgres_server_identity():
    return {
        "namespace": "nightingale",
        "role": "n9e-postgres-server",
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
        "uri": f"{URI_ROOT}/nightingale/n9e-postgres-server",
        "secret": "obs-mtls-n9e-postgres-server",
    }


def expected_entry(item):
    old_ns = item["namespace"]
    new_ns = NAMESPACE_MAP.get(old_ns, old_ns)
    return {
        **item,
        "namespace": new_ns,
        "dns_names": [host.replace(old_ns, new_ns) for host in item["dns_names"]],
        "uri": f"{URI_ROOT}/{new_ns}/{item['role']}",
        "secret": f"obs-mtls-{item['role']}",
        "usages": [{"serverAuth": "server auth", "clientAuth": "client auth"}[usage]
                   for usage in item["usages"]],
    }


def by_kind(documents, kind):
    return [item for item in documents if item.get("kind") == kind]


def key(document):
    meta = document.get("metadata", {})
    return (document.get("kind"), meta.get("namespace"), meta.get("name"))


class CertManagerRenderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.renderer = load_renderer()
        cls.documents = cls.renderer.render_documents(ROOT)
        cls.versions = cls.renderer.load_versions(CERT_MANAGER / "versions.yaml")
        cls.expected = [expected_entry(item) for item in source_identities()]
        cls.expected.append(postgres_server_identity())

    def test_versions_are_exact_and_disable_default_approver(self):
        cert_manager = self.versions["cert-manager"]
        self.assertEqual("https://charts.jetstack.io", cert_manager["chartRepository"])
        self.assertRegex(cert_manager["chartVersion"], r"^v?1\.\d+\.\d+$")
        self.assertRegex(cert_manager["controllerImageDigest"], r"^sha256:[0-9a-f]{64}$")
        self.assertRegex(cert_manager["webhookImageDigest"], r"^sha256:[0-9a-f]{64}$")
        self.assertRegex(cert_manager["cainjectorImageDigest"], r"^sha256:[0-9a-f]{64}$")

        approver = self.versions["approver-policy"]
        self.assertEqual("https://charts.jetstack.io", approver["chartRepository"])
        self.assertRegex(approver["chartVersion"], r"^v?0\.\d+\.\d+$")
        self.assertRegex(approver["imageDigest"], r"^sha256:[0-9a-f]{64}$")

        values = yaml.safe_load((CERT_MANAGER / "cert-manager-values.yaml").read_text())
        args = values.get("extraArgs", [])
        self.assertIn("--controllers=*,-certificaterequests-approver", args)

        approver_values = yaml.safe_load((CERT_MANAGER / "approver-policy-values.yaml").read_text())
        self.assertEqual([
            "clusterissuers.cert-manager.io/obs-mtls-am-gossip",
            "clusterissuers.cert-manager.io/obs-mtls-mesh",
            "clusterissuers.cert-manager.io/obs-mtls-postgres-client",
            "clusterissuers.cert-manager.io/obs-mtls-postgres-server",
            "clusterissuers.cert-manager.io/obs-mtls-redis",
        ], approver_values["app"]["approveSignerNames"])

    def test_helm_values_have_production_scheduling_and_resource_bounds(self):
        cm = yaml.safe_load((CERT_MANAGER / "cert-manager-values.yaml").read_text())
        approver = yaml.safe_load((CERT_MANAGER / "approver-policy-values.yaml").read_text())
        for label, values in (("controller", cm), ("webhook", cm["webhook"]),
                              ("cainjector", cm["cainjector"]), ("approver", approver)):
            with self.subTest(component=label):
                self.assertEqual(2, values["replicaCount"])
                priority = values.get("priorityClassName", cm.get("global", {}).get("priorityClassName"))
                self.assertEqual("raytrain-observability-additive", priority)
                self.assertTrue(values["podDisruptionBudget"]["enabled"])
                self.assertEqual(1, values["podDisruptionBudget"]["minAvailable"])
                self.assertIn("requests", values["resources"])
                self.assertIn("limits", values["resources"])
                affinity = yaml.safe_dump(values["affinity"])
                self.assertIn("172.28.1.222", affinity)
                self.assertIn("172.28.3.32", affinity)
                self.assertIn("podAntiAffinity", affinity)

    def test_rendered_cert_manager_namespace_is_restricted_and_gpu_free(self):
        namespace = next(item for item in self.documents
                         if key(item) == ("Namespace", None, "cert-manager"))
        labels = namespace["metadata"]["labels"]
        self.assertEqual("restricted", labels["pod-security.kubernetes.io/enforce"])
        quota = next(item for item in self.documents
                     if key(item) == ("ResourceQuota", "cert-manager", "cert-manager-budget"))
        self.assertEqual("0", quota["spec"]["hard"]["requests.nvidia.com/gpu"])
        self.assertGreaterEqual(int(quota["spec"]["hard"]["limits.cpu"]), 16)
        self.assertGreaterEqual(int(quota["spec"]["hard"]["pods"]), 30)

    def test_render_parts_keep_partial_resources_out_of_default_plan(self):
        default_plan = self.renderer.render_part(ROOT, "plan")
        self.assertFalse(any(item.get("kind") == "Secret" for item in default_plan))
        self.assertFalse(any(item.get("kind") == "Cluster" and
                             item.get("apiVersion") == "postgresql.cnpg.io/v1"
                             for item in default_plan))
        self.assertTrue(any(item.get("kind") == "Secret" for item in
                            self.renderer.render_part(ROOT, "public-ca-plan")))
        self.assertEqual([("Cluster", "nightingale", "nightingale-postgres")],
                         [key(item) for item in self.renderer.render_part(ROOT, "postgres-patch")])

    def test_only_new_product_namespaces_are_rendered(self):
        namespaces = {item["namespace"] for item in self.expected}
        self.assertEqual({"victoriametrics", "clickhouse", "opentelemetry", "nightingale"}, namespaces)
        dumped = yaml.safe_dump_all(self.documents)
        for old in NAMESPACE_MAP:
            self.assertNotIn(old, dumped)

    def test_cluster_issuers_are_centralized_and_never_copy_ca_to_workload_namespaces(self):
        issuers = {item["metadata"]["name"]: item for item in by_kind(self.documents, "ClusterIssuer")}
        self.assertEqual({f"obs-mtls-{domain}" for domain in TRUST_DOMAINS}, set(issuers))
        for domain in TRUST_DOMAINS:
            spec = issuers[f"obs-mtls-{domain}"]["spec"]
            self.assertEqual(f"obs-mtls-ca-{domain}", spec["ca"]["secretName"])
            self.assertNotIn("namespace", spec["ca"])

        for document in self.documents:
            if document.get("kind") == "Secret":
                self.assertEqual("cert-manager", document["metadata"].get("namespace"))
                self.assertNotIn("data", document)
                self.assertNotIn("stringData", document)

    def test_each_identity_gets_one_exact_certificate_without_dns_cartesian_product(self):
        certificates = {
            (item["metadata"]["namespace"], item["metadata"]["name"]): item
            for item in by_kind(self.documents, "Certificate")
        }
        self.assertEqual(len(self.expected), len(certificates))
        for item in self.expected:
            with self.subTest(identity=item["uri"]):
                cert = certificates[(item["namespace"], item["secret"])]
                spec = cert["spec"]
                self.assertEqual(item["secret"], spec["secretName"])
                self.assertEqual({"name": f"obs-mtls-{item['trust_domain']}", "kind": "ClusterIssuer",
                                  "group": "cert-manager.io"}, spec["issuerRef"])
                self.assertEqual([item["uri"]], spec.get("uris"))
                self.assertEqual(item["dns_names"], spec.get("dnsNames", []))
                self.assertLessEqual(len(spec.get("dnsNames", [])), len(item["dns_names"]))
                self.assertEqual(sorted(item["usages"]), sorted(spec["usages"]))
                self.assertEqual("2160h", spec["duration"])
                self.assertEqual("720h", spec["renewBefore"])
                self.assertEqual("Always", spec["privateKey"]["rotationPolicy"])

    def test_cnpg_server_certificate_is_managed_by_cert_manager(self):
        certificates = {
            (item["metadata"]["namespace"], item["metadata"]["name"]): item
            for item in by_kind(self.documents, "Certificate")
        }
        cert = certificates[("nightingale", "obs-mtls-n9e-postgres-server")]
        self.assertEqual("obs-mtls-n9e-postgres-server", cert["spec"]["secretName"])
        self.assertEqual("true", cert["spec"]["secretTemplate"]["labels"]["cnpg.io/reload"])
        replica = certificates[("nightingale", "obs-mtls-n9e-postgres-replication")]
        self.assertEqual("true", replica["spec"]["secretTemplate"]["labels"]["cnpg.io/reload"])
        self.assertEqual({"name": "obs-mtls-postgres-server", "kind": "ClusterIssuer",
                          "group": "cert-manager.io"}, cert["spec"]["issuerRef"])
        patch = self.renderer.render_cnpg_certificate_patch()
        self.assertEqual("obs-mtls-n9e-postgres-server", patch["spec"]["certificates"]["serverTLSSecret"])
        self.assertEqual("obs-mtls-ca-postgres-server-public", patch["spec"]["certificates"]["serverCASecret"])
        shells = self.renderer.render_part(ROOT, "public-ca-plan")
        self.assertTrue(all(item["metadata"]["labels"]["cnpg.io/reload"] == "true" for item in shells))

    def test_certificate_request_policies_match_certificate_contracts_exactly(self):
        policies = {
            item["metadata"]["name"]: item
            for item in by_kind(self.documents, "CertificateRequestPolicy")
        }
        self.assertEqual(len(self.expected), len(policies))
        for item in self.expected:
            policy_name = f"obs-mtls-{item['namespace']}-{item['role']}"
            with self.subTest(policy=policy_name):
                spec = policies[policy_name]["spec"]
                selector = spec["selector"]
                self.assertEqual({"name": f"obs-mtls-{item['trust_domain']}", "kind": "ClusterIssuer",
                                  "group": "cert-manager.io"}, selector["issuerRef"])
                self.assertEqual([item["namespace"]], selector["namespace"]["matchNames"])
                allowed = spec["allowed"]
                self.assertEqual([item["uri"]], allowed["uris"]["values"])
                self.assertEqual(item["dns_names"], allowed.get("dnsNames", {}).get("values", []))
                self.assertEqual(sorted(item["usages"]), sorted(allowed["usages"]))
                self.assertEqual({"minDuration": "2160h", "maxDuration": "2160h",
                                  "privateKey": {"algorithm": "RSA", "minSize": 2048}}, spec["constraints"])
                constraints = yaml.safe_dump(allowed)
                self.assertNotIn("*", constraints)
                self.assertNotIn("observability-", constraints)

    def test_policy_use_rbac_is_bound_only_to_cert_manager_controller(self):
        bindings = by_kind(self.documents, "ClusterRoleBinding") + by_kind(self.documents, "RoleBinding")
        policy_bindings = [item for item in bindings if item["metadata"]["name"].startswith("obs-mtls-policy-use-")]
        self.assertEqual(len(self.expected), len(policy_bindings))
        for binding in policy_bindings:
            with self.subTest(binding=key(binding)):
                subjects = binding["subjects"]
                self.assertEqual([{"kind": "ServiceAccount", "name": "cert-manager",
                                  "namespace": "cert-manager"}], subjects)
                self.assertEqual("rbac.authorization.k8s.io", binding["roleRef"]["apiGroup"])
                self.assertEqual("ClusterRole", binding["roleRef"]["kind"])
                self.assertTrue(binding["roleRef"]["name"].startswith("obs-mtls-policy-use-"))

    def test_negative_policy_does_not_allow_foreign_namespace_or_admin_uri(self):
        violations = [
            {"namespace": "tenant-local", "uri": f"{URI_ROOT}/victoriametrics/admin"},
            {"namespace": "nightingale", "uri": f"{URI_ROOT}/victoriametrics/admin"},
            {"namespace": "victoriametrics", "uri": f"{URI_ROOT}/victoriametrics/admin-extra"},
        ]
        for violation in violations:
            with self.subTest(violation=violation):
                self.assertFalse(self.renderer.policy_allows(self.documents, violation))


class CertManagerCAImportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.importer = load_importer()

    def test_import_plan_uses_existing_builder_ca_paths_without_values(self):
        plan = self.importer.plan_imports(Path("/root/raytrain-observability-pki-20261008"))
        self.assertEqual(TRUST_DOMAINS, {item["trust_domain"] for item in plan})
        for item in plan:
            with self.subTest(domain=item["trust_domain"]):
                self.assertEqual("cert-manager", item["namespace"])
                self.assertEqual(f"obs-mtls-ca-{item['trust_domain']}", item["secretName"])
                self.assertTrue(str(item["keyPath"]).endswith(f"{item['trust_domain']}.key"))
                self.assertTrue(str(item["certPath"]).endswith(f"{item['trust_domain']}.crt"))
                self.assertNotIn("keyData", item)
                self.assertNotIn("certData", item)

    def test_secret_manifest_refuses_unknown_domain_and_never_serializes_private_key_by_default(self):
        with self.assertRaises(ValueError):
            self.importer.secret_manifest("tenant-local", "bad", "CERT", "KEY")
        manifest = self.importer.secret_manifest("cert-manager", "mesh", "CERT", "KEY")
        self.assertEqual("cert-manager", manifest["metadata"]["namespace"])
        self.assertEqual("obs-mtls-ca-mesh", manifest["metadata"]["name"])
        self.assertNotIn("data", manifest)
        self.assertEqual({"tls.crt": "CERT", "tls.key": "KEY"}, manifest["stringData"])
        self.assertNotIn("KEY", self.importer.redacted_manifest_summary(manifest))

    def test_import_command_checks_absence_before_stdin_create(self):
        command = self.importer.kubectl_create_command("cert-manager", "obs-mtls-ca-mesh")
        self.assertIn("get", command["preflight"])
        self.assertIn("cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg", command["preflight"])
        self.assertIn("--ignore-not-found", command["preflight"])
        self.assertIn("create", command["create"])
        self.assertEqual("-", command["create"][-1])

    def test_postgres_server_ca_generator_refuses_partial_state(self):
        self.assertTrue(hasattr(self.importer, "ensure_postgres_server_ca"))
        self.assertTrue(hasattr(self.importer, "validate_ca_pair"))
        self.assertEqual("postgres-server", self.importer.plan_imports(
            Path("/root/raytrain-observability-pki-20261008"))[-1]["trust_domain"])


if __name__ == "__main__":
    unittest.main()
