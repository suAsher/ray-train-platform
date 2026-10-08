"""Identity boundaries and private-material handling; run on the builder only."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "mtls" / "credentials.py"
SPEC = importlib.util.spec_from_file_location("obs_mtls_credentials", SOURCE)
mtls = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mtls)


def entry(**overrides):
    return dict(namespace="observability-metrics", role="vmstorage",
                trust_domain="mesh", dns_names=["vmstorage.observability-metrics.svc"],
                usages=["serverAuth", "clientAuth"], **overrides)


class IdentityContract(unittest.TestCase):
    def test_accepts_explicit_identity_without_mutating_input(self):
        original = [entry()]
        before = copy.deepcopy(original)
        actual = mtls.validate_plan(original)
        self.assertEqual(original, before)
        self.assertIsNot(actual, original)
        self.assertEqual(actual[0]["role"], "vmstorage")

    def test_rejects_namespace_escape(self):
        item = entry(); item["namespace"] = "tenant-local"
        with self.assertRaises(mtls.CredentialError):
            mtls.validate_plan([item])

    def test_rejects_duplicate_secret_identity_even_with_another_ca(self):
        item = entry(); other = copy.deepcopy(item); other["trust_domain"] = "redis"
        with self.assertRaises(mtls.CredentialError):
            mtls.validate_plan([item, other])

    def test_rejects_wildcard_and_external_dns(self):
        for host in ("*.observability-metrics.svc", "example.com", "127.0.0.1", "a\nDNS:evil.svc"):
            with self.subTest(host=host):
                item = entry(); item["dns_names"] = [host]
                with self.assertRaises(mtls.CredentialError):
                    mtls.validate_plan([item])

    def test_server_identity_requires_dns(self):
        item = entry(); item["dns_names"] = []
        with self.assertRaises(mtls.CredentialError):
            mtls.validate_plan([item])

    def test_client_only_identity_allows_no_dns_and_fixed_database_cn(self):
        item = entry(); item.update(role="n9e-postgres-client", namespace="observability-alerting",
                                   trust_domain="postgres-client", common_name="n9e",
                                   dns_names=[], usages=["clientAuth"])
        self.assertEqual(mtls.validate_plan([item])[0]["common_name"], "n9e")

    def test_rejects_extension_injection_and_unrecognized_domain(self):
        for field, value in (("role", "../admin"), ("common_name", "a/CN=root"),
                             ("trust_domain", "production"), ("usages", ["anyExtendedKeyUsage"])):
            with self.subTest(field=field):
                item = entry(); item[field] = value
                with self.assertRaises(mtls.CredentialError):
                    mtls.validate_plan([item])

    def test_secret_has_only_fixed_new_name_and_leaf_material(self):
        data = {"tls.crt": "public-cert", "tls.key": "private-test-key", "ca.crt": "public-ca"}
        result = mtls.make_secret(entry(), data)
        self.assertEqual(result["metadata"]["name"], "obs-mtls-vmstorage")
        self.assertEqual(result["metadata"]["namespace"], "observability-metrics")
        self.assertEqual(set(result["stringData"]), {"tls.crt", "tls.key", "ca.crt"})
        self.assertEqual(result["metadata"]["labels"]["observability.raytrain.io/credential-owner"], "mtls")
        self.assertNotIn("ca.key", json.dumps(result))

    def test_preflight_existing_target_stops_before_generation(self):
        done = subprocess.CompletedProcess([], 0, "secret/obs-mtls-vmstorage\n", "")
        with patch.object(mtls.subprocess, "run", return_value=done):
            with self.assertRaises(mtls.CredentialError):
                mtls.preflight_absent([entry()])

    def test_kubectl_error_redacts_payload_and_diagnostics(self):
        done = subprocess.CompletedProcess([], 1, "", "SECRET-VALUE-test")
        with patch.object(mtls.subprocess, "run", return_value=done):
            with self.assertRaises(mtls.CredentialError) as caught:
                mtls.create_secrets([mtls.make_secret(entry(),
                    {"tls.crt": "x", "tls.key": "SECRET-VALUE-test", "ca.crt": "y"})])
        self.assertNotIn("SECRET-VALUE", str(caught.exception))

    def test_create_transfers_secret_by_stdin_not_argv(self):
        bundle = [mtls.make_secret(entry(), {"tls.crt": "x", "tls.key": "PRIVATE-test", "ca.crt": "y"})]
        done = subprocess.CompletedProcess([], 0, "secret/obs-mtls-vmstorage", "")
        with patch.object(mtls.subprocess, "run", return_value=done) as mocked:
            mtls.create_secrets(bundle)
        args, kwargs = mocked.call_args
        self.assertNotIn("PRIVATE-test", " ".join(args[0]))
        self.assertIn("PRIVATE-test", kwargs["input"])

    def test_creation_rejects_forged_resource_and_wrong_owner(self):
        good = mtls.make_secret(entry(), {"tls.crt": "x", "tls.key": "z", "ca.crt": "y"})
        for change in ("kind", "owner", "ca-key"):
            bad = copy.deepcopy(good)
            if change == "kind":
                bad["kind"] = "ConfigMap"
            elif change == "owner":
                bad["metadata"]["labels"] = {}
            else:
                bad["stringData"]["ca.key"] = "never-publish-a-ca-key"
            with self.subTest(change=change), patch.object(mtls.subprocess, "run") as mocked:
                with self.assertRaises(mtls.CredentialError):
                    mtls.create_secrets([bad])
                mocked.assert_not_called()

    def test_public_ca_projections_contain_no_private_material(self):
        for name, domain in (("obs-mtls-cnpg-metrics-ca", "mesh"),
                             ("obs-mtls-postgres-client-ca", "postgres-client")):
            secret = mtls.public_ca_secret(name, domain, "public-only-certificate")
            self.assertEqual("observability-alerting", secret["metadata"]["namespace"])
            self.assertEqual({"ca.crt": "public-only-certificate"}, secret["stringData"])
            self.assertEqual("Opaque", secret["type"])
        with self.assertRaises(mtls.CredentialError):
            mtls.public_ca_secret("obs-mtls-cnpg-metrics-ca", "redis", "wrong-domain")


class RealCertificateContract(unittest.TestCase):
    def test_exact_uri_private_permissions_and_reusable_ca(self):
        with tempfile.TemporaryDirectory(prefix="obs-mtls-test-") as directory:
            pki = Path(directory) / "pki"
            first = mtls.issue_material(entry(), pki)
            second = mtls.issue_material(entry(), pki)
            self.assertEqual(first["ca.crt"], second["ca.crt"])
            self.assertNotEqual(first["tls.key"], second["tls.key"])
            cert = Path(directory) / "leaf.crt"; cert.write_text(first["tls.crt"])
            output = subprocess.check_output(["openssl", "x509", "-in", str(cert), "-noout", "-text"], text=True)
            self.assertIn("URI:spiffe://observability.raytrain.local/observability-metrics/vmstorage", output)
            self.assertIn("DNS:vmstorage.observability-metrics.svc", output)
            keys = list(pki.rglob("*.key"))
            self.assertEqual(len(keys), 1)
            self.assertEqual(keys[0].stat().st_mode & 0o777, 0o600)
            self.assertEqual(pki.stat().st_mode & 0o777, 0o700)

    def test_ca_path_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory(prefix="obs-mtls-test-") as directory:
            target = Path(directory) / "target"; target.mkdir()
            alias = Path(directory) / "alias"; alias.symlink_to(target, target_is_directory=True)
            with self.assertRaises(mtls.CredentialError):
                mtls.issue_material(entry(), alias)


if __name__ == "__main__":
    unittest.main()
