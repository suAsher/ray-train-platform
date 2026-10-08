"""Run only on the builder; Kubernetes calls are always mocked."""

from contextlib import redirect_stderr, redirect_stdout
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import subprocess
import unittest
from unittest.mock import patch


MODULE_PATH = Path(__file__).resolve().parents[1] / "bootstrap_credentials.py"
SPEC = importlib.util.spec_from_file_location("bootstrap_credentials", MODULE_PATH)
credentials = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(credentials)


class CredentialTests(unittest.TestCase):
    def fake_tls(self):
        return {
            "clickhouse": {"tls.crt": "CH CERT", "tls.key": "CH PRIVATE", "ca.crt": "CH CA"},
            "otel_server": {"tls.crt": "OTEL SERVER", "tls.key": "SERVER PRIVATE", "ca.crt": "OTEL CA"},
            "otel_client": {"tls.crt": "OTEL CLIENT", "tls.key": "CLIENT PRIVATE", "ca.crt": "OTEL CA"},
        }

    def bundle(self):
        with patch.object(credentials, "generate_tls", return_value=self.fake_tls()):
            return credentials.build_secrets()

    def test_default_plan_is_offline_and_never_generates_credentials(self):
        output = io.StringIO()
        with patch.object(credentials.subprocess, "run") as run, \
                patch.object(credentials, "build_secrets") as generate, redirect_stdout(output):
            self.assertEqual(0, credentials.main([]))
        run.assert_not_called()
        generate.assert_not_called()
        self.assertIn("observability-metrics/vm-self-read", output.getvalue())
        self.assertNotIn("PRIVATE", output.getvalue())

    def test_plan_contains_only_the_fifteen_new_secrets(self):
        plan = credentials.secret_plan()
        self.assertEqual(15, len(plan))
        self.assertEqual({"observability-metrics", "observability-analytics", "observability-otel", "observability-alerting"},
                         {entry["namespace"] for entry in plan})
        self.assertNotIn("n9e-postgres-ca", {entry["name"] for entry in plan})
        self.assertEqual(len(plan), len({(entry["namespace"], entry["name"]) for entry in plan}))

    def test_existing_secret_stops_before_generation_or_writes(self):
        calls = []
        def existing(argv, **kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 0, "secret/vm-self-read\n", "")
        with patch.object(credentials.subprocess, "run", side_effect=existing), \
                patch.object(credentials, "build_secrets") as build, redirect_stderr(io.StringIO()):
            self.assertEqual(1, credentials.main(["--create"]))
        build.assert_not_called()
        self.assertTrue(all("get" in argv for argv in calls))
        self.assertTrue(all("-o" in argv and "name" in argv for argv in calls))

    def test_preflight_errors_fail_closed_without_exposing_stderr(self):
        output = io.StringIO()
        failed = subprocess.CompletedProcess([], 1, "", "FORBIDDEN-PRIVATE-DATA")
        with patch.object(credentials.subprocess, "run", return_value=failed), \
                patch.object(credentials, "build_secrets") as build, redirect_stderr(output):
            self.assertEqual(1, credentials.main(["--create"]))
        build.assert_not_called()
        self.assertNotIn("FORBIDDEN-PRIVATE-DATA", output.getvalue())

    def test_all_preflight_checks_finish_before_stdin_only_create(self):
        secrets = self.bundle()
        calls = []
        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0, "", "")
        with patch.object(credentials.subprocess, "run", side_effect=run), \
                patch.object(credentials, "build_secrets", return_value=secrets), redirect_stdout(io.StringIO()):
            self.assertEqual(0, credentials.main(["--create"]))
        self.assertEqual(16, len(calls))
        self.assertTrue(all("get" in argv for argv, _ in calls[:-1]))
        argv, kwargs = calls[-1]
        self.assertIn("create", argv)
        self.assertNotIn("apply", argv)
        self.assertEqual("-", argv[argv.index("-f") + 1])
        self.assertNotIn("CH PRIVATE", " ".join(argv))
        payload = json.loads(kwargs["input"])
        self.assertEqual("List", payload["kind"])
        self.assertEqual(15, len(payload["items"]))

    def test_create_failure_never_retries_overwrites_or_prints_payload(self):
        secrets = self.bundle()
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv, 1 if "create" in argv else 0,
                                               "CH PRIVATE" if "create" in argv else "", "CLIENT PRIVATE")
        output = io.StringIO()
        with patch.object(credentials.subprocess, "run", side_effect=run), \
                patch.object(credentials, "build_secrets", return_value=secrets), redirect_stderr(output):
            self.assertEqual(1, credentials.main(["--create"]))
        self.assertEqual(1, sum("create" in argv for argv in calls))
        self.assertFalse(any("delete" in argv or "replace" in argv or "patch" in argv for argv in calls))
        self.assertNotIn("PRIVATE", output.getvalue())
        self.assertIn("partially", output.getvalue())

    def test_linked_values_match_and_root_password_is_compatible(self):
        bundle = {(secret["metadata"]["namespace"], secret["metadata"]["name"]): secret for secret in self.bundle()}
        def data(namespace, name):
            return bundle[("observability-" + namespace, name)]["stringData"]
        read = data("metrics", "vm-self-read")["token"]
        self.assertEqual(read, data("alerting", "n9e-vm-read")["token"])
        self.assertNotEqual(read, data("metrics", "vm-self-write")["token"])
        hashes = data("analytics", "clickhouse-auth")
        for role, namespace, name in (("admin", "analytics", "clickhouse-bootstrap-auth"),
                                      ("query", "analytics", "clickhouse-query-auth"),
                                      ("ingest", "otel", "otel-clickhouse-auth")):
            plaintext = data(namespace, name)["password"]
            self.assertGreaterEqual(len(plaintext), 32)
            self.assertEqual(hashlib.sha256(plaintext.encode()).hexdigest(), hashes[role + "-password-sha256"])
        root_password = data("alerting", "n9e-bootstrap-admin")["password"]
        self.assertRegex(root_password, r"^[A-Za-z0-9]{24}$")
        self.assertNotEqual(data("alerting", "n9e-redis-auth")["auth"],
                            data("alerting", "n9e-redis-auth")["sentinel-password"])
        self.assertEqual("n9e", data("alerting", "n9e-db-app")["username"])
        self.assertEqual("kubernetes.io/basic-auth", bundle[("observability-alerting", "n9e-db-app")]["type"])

    def test_ca_private_keys_never_enter_a_secret_and_trust_matches(self):
        bundle = self.bundle()
        for secret in bundle:
            self.assertEqual({"apiVersion", "kind", "metadata", "type", "stringData"}, set(secret))
            self.assertNotIn("ca.key", secret["stringData"])
            self.assertNotIn("ca-private-key", secret["stringData"])
        by_name = {secret["metadata"]["name"]: secret["stringData"] for secret in bundle}
        self.assertEqual(by_name["clickhouse-server-tls"]["ca.crt"], by_name["clickhouse-server-ca"]["ca.crt"])
        self.assertEqual(by_name["otel-canary-server-tls"]["ca.crt"], by_name["otel-canary-client-tls"]["ca.crt"])
        self.assertNotEqual(by_name["clickhouse-server-ca"]["ca.crt"], by_name["otel-canary-server-tls"]["ca.crt"])

    def test_sans_include_services_and_both_gateway_replicas(self):
        sans = credentials.OTEL_SERVER_SANS
        for name in ("otel-gateway", "otel-gateway-headless"):
            self.assertIn(name + ".observability-otel.svc", sans)
            self.assertIn(name + ".observability-otel.svc.cluster.local", sans)
        for replica in (0, 1):
            self.assertIn(f"otel-gateway-{replica}.otel-gateway-headless.observability-otel.svc.cluster.local", sans)
        self.assertIn("clickhouse-observability.observability-analytics.svc.cluster.local", credentials.CLICKHOUSE_SANS)

    def test_openssl_failure_is_redacted(self):
        with patch.object(credentials.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "PRIVATE", "PRIVATE")):
            with self.assertRaises(credentials.BootstrapError) as raised:
                credentials.run_openssl(["version"])
        self.assertNotIn("PRIVATE", str(raised.exception))

    def test_real_openssl_certificates_have_scoped_eku_sans_and_private_temporary_files(self):
        """Local OpenSSL only on builder; an unexpected kubectl call is forbidden."""
        original_run = credentials.subprocess.run
        original_key = credentials.generate_key
        directories = set()
        def guarded_run(argv, **kwargs):
            self.assertEqual("openssl", argv[0])
            return original_run(argv, **kwargs)
        def checked_key(path):
            directories.add(path.parent)
            self.assertEqual(0o700, stat.S_IMODE(path.parent.stat().st_mode))
            original_key(path)
            self.assertEqual(0o600, stat.S_IMODE(path.stat().st_mode))
        with patch.object(credentials.subprocess, "run", side_effect=guarded_run), \
                patch.object(credentials, "generate_key", side_effect=checked_key):
            tls = credentials.generate_tls()
        self.assertTrue(directories)
        self.assertFalse(any(path.exists() for path in directories))
        for name, purpose in (("clickhouse", "TLS Web Server Authentication"),
                              ("otel_server", "TLS Web Server Authentication"),
                              ("otel_client", "TLS Web Client Authentication")):
            result = original_run(["openssl", "x509", "-noout", "-text"],
                                  input=tls[name]["tls.crt"], capture_output=True, text=True, check=True)
            self.assertIn(purpose, result.stdout)
            self.assertIn("CA:FALSE", result.stdout)
            if name == "otel_server":
                self.assertIn("otel-gateway-1.otel-gateway-headless.observability-otel.svc.cluster.local", result.stdout)
            if name == "otel_client":
                self.assertNotIn("TLS Web Server Authentication", result.stdout)


if __name__ == "__main__":
    unittest.main()
