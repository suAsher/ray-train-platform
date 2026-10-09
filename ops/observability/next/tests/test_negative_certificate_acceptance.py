import importlib.util
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace


def load_module():
    root = Path(__file__).resolve().parents[1]
    path = root / "cert-manager" / "negative_certificate_acceptance.py"
    spec = importlib.util.spec_from_file_location("negative_certificate_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class NegativeCertificateAcceptanceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module()

    def test_cases_are_scoped_to_new_namespaces_and_expected_outcomes(self):
        cases = {case.name: case for case in self.module.cases()}

        self.assertEqual(cases["wrong-uri"].namespace, "nightingale")
        self.assertEqual(cases["wrong-uri"].expected, "denied")
        self.assertEqual(cases["wrong-dns"].namespace, "nightingale")
        self.assertEqual(cases["wrong-dns"].expected, "denied")
        self.assertEqual(cases["wrong-namespace"].namespace, "victoriametrics")
        self.assertEqual(cases["wrong-namespace"].expected, "pending_no_matching_policy")

    def test_manifest_uses_obs_neg_names_and_postgres_server_issuer(self):
        case = next(item for item in self.module.cases() if item.name == "wrong-uri")
        doc = self.module.manifest(case, "abc123")

        self.assertEqual(doc["metadata"]["name"], "obs-neg-wrong-uri-abc123")
        self.assertEqual(doc["spec"]["secretName"], "obs-neg-wrong-uri-abc123-secret")
        self.assertEqual(
            doc["spec"]["issuerRef"],
            {"name": "obs-mtls-postgres-server", "kind": "ClusterIssuer", "group": "cert-manager.io"},
        )

    def test_matching_policy_requires_postgres_server_issuer_and_namespace(self):
        policies = [
            {
                "metadata": {"name": "nightingale-policy"},
                "spec": {
                    "selector": {
                        "issuerRef": {
                            "name": "obs-mtls-postgres-server",
                            "kind": "ClusterIssuer",
                            "group": "cert-manager.io",
                        },
                        "namespace": {"matchNames": ["nightingale"]},
                    }
                },
            },
            {
                "metadata": {"name": "mesh-policy"},
                "spec": {
                    "selector": {
                        "issuerRef": {
                            "name": "obs-mtls-mesh",
                            "kind": "ClusterIssuer",
                            "group": "cert-manager.io",
                        },
                        "namespace": {"matchNames": ["victoriametrics"]},
                    }
                },
            },
        ]

        self.assertEqual(self.module.matching_policy_names(policies, "victoriametrics"), [])
        self.assertEqual(self.module.matching_policy_names(policies, "nightingale"), ["nightingale-policy"])

    def test_owner_check_uses_uid(self):
        item = {
            "metadata": {
                "ownerReferences": [
                    {"kind": "Certificate", "name": "same-name", "uid": "uid-a"},
                ]
            }
        }

        self.assertTrue(self.module.owned_by(item, "uid-a"))
        self.assertFalse(self.module.owned_by(item, "uid-b"))

    def test_delete_options_and_api_paths(self):
        self.assertEqual(
            self.module.delete_options("uid-1", "rv-1"),
            {
                "apiVersion": "v1",
                "kind": "DeleteOptions",
                "preconditions": {"uid": "uid-1", "resourceVersion": "rv-1"},
            },
        )
        self.assertEqual(
            self.module.api_path("cert-manager.io/v1", "nightingale", "certificates", "cert-1"),
            "/apis/cert-manager.io/v1/namespaces/nightingale/certificates/cert-1",
        )
        self.assertEqual(
            self.module.api_path("v1", "nightingale", "secrets", "secret-1"),
            "/api/v1/namespaces/nightingale/secrets/secret-1",
        )

    def test_certificate_identity_recovery_requires_run_id_case_and_spec(self):
        case = next(item for item in self.module.cases() if item.name == "wrong-uri")
        doc = self.module.manifest(case, "abc123")
        cert = {
            "metadata": {
                "name": doc["metadata"]["name"],
                "namespace": case.namespace,
                "labels": {"raytrain.wellspiking.ai/run-id": "abc123"},
                "annotations": {"raytrain.wellspiking.ai/negative-case": "wrong-uri"},
            },
            "spec": {
                "secretName": doc["spec"]["secretName"],
                "issuerRef": doc["spec"]["issuerRef"],
                "uris": doc["spec"]["uris"],
                "dnsNames": doc["spec"]["dnsNames"],
            },
        }

        self.assertTrue(self.module.certificate_identity_matches(cert, doc, case, "abc123"))
        cert["metadata"]["labels"]["raytrain.wellspiking.ai/run-id"] = "other"
        self.assertFalse(self.module.certificate_identity_matches(cert, doc, case, "abc123"))

    def test_secret_owner_metadata_uses_certificate_uid(self):
        item = {
            "name": "temp-key",
            "uid": "secret-uid",
            "resourceVersion": "12",
            "owners": "Certificate:cert-uid,",
        }

        self.assertTrue(self.module.owner_uid_in_secret_meta(item, "cert-uid"))
        self.assertFalse(self.module.owner_uid_in_secret_meta(item, "other"))

    def test_create_case_uses_create_output_uid(self):
        module = self.module
        case = next(item for item in module.cases() if item.name == "wrong-uri")
        doc = module.manifest(case, "abc123")
        calls = []
        original_kubectl = module.kubectl
        original_get_obj = module.get_obj
        original_secret_meta = module.secret_meta_by_name

        def fake_kubectl(_bin, _context, args, **kwargs):
            calls.append(args)
            cert = {
                "metadata": {
                    "name": doc["metadata"]["name"],
                    "namespace": case.namespace,
                    "uid": "cert-uid",
                    "labels": {"raytrain.wellspiking.ai/run-id": "abc123"},
                    "annotations": {"raytrain.wellspiking.ai/negative-case": "wrong-uri"},
                },
                "spec": doc["spec"],
            }
            return SimpleNamespace(stdout=module.json.dumps(cert))

        try:
            module.kubectl = fake_kubectl
            module.get_obj = lambda *_args, **_kwargs: None
            module.secret_meta_by_name = lambda *_args, **_kwargs: None
            created = module.create_case("kubectl", "ctx", case, "abc123", 30)
        finally:
            module.kubectl = original_kubectl
            module.get_obj = original_get_obj
            module.secret_meta_by_name = original_secret_meta

        self.assertEqual(created.uid, "cert-uid")
        self.assertIn(["create", "-o", "json", "-f", "-"], calls)


if __name__ == "__main__":
    unittest.main()
