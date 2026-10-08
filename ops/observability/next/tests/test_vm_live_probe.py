"""Contracts for the in-cluster stdlib probe; run from a builder candidate."""

from collections import Counter
import importlib.util
import json
from pathlib import Path
import socket
import ssl
import tempfile
import unittest
from unittest.mock import patch


MODULE_PATH = Path(__file__).resolve().parents[1] / "victoriametrics/verify-live-probe.py"
spec = importlib.util.spec_from_file_location("vm_live_probe", MODULE_PATH)
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


def inventory():
    roles = [role for role, count in probe.FULL_COUNTS.items() for _ in range(count)]
    return {"pods": [{"name": f"pod-{i}", "role": role, "ip": f"172.28.20.{i + 1}"}
                     for i, role in enumerate(roles)],
            "services": {"vmauth": "vmauth-raytrain-next.observability-metrics.svc"}}


class InventoryContracts(unittest.TestCase):
    def test_full_requires_all_replicas_and_exact_service(self):
        value = inventory()
        self.assertEqual(Counter(probe.FULL_COUNTS), Counter(pod["role"] for pod in probe.validate_inventory(value, "full")["pods"]))
        for key in ("missing", "duplicate", "external-service"):
            value = inventory()
            if key == "missing":
                value["pods"].pop()
            elif key == "duplicate":
                value["pods"][1]["ip"] = value["pods"][0]["ip"]
            else:
                value["services"]["vmauth"] = "outside.example.com"
            with self.subTest(key=key), self.assertRaises(probe.ProbeError):
                probe.validate_inventory(value, "full")

    def test_operator_mode_is_limited_to_operator_pods(self):
        selected = probe.validate_inventory(inventory(), "operator")
        self.assertEqual(["operator"], [pod["role"] for pod in selected["pods"]])

    def test_non_pod_addresses_and_unknown_roles_are_rejected(self):
        for invalid in ("127.0.0.1", "0.0.0.0", "8.8.8.8", "169.254.169.254", "example.com"):
            value = inventory()
            value["pods"][0]["ip"] = invalid
            with self.subTest(address=invalid), self.assertRaises(probe.ProbeError):
                probe.validate_inventory(value, "full")
        value = inventory()
        value["pods"][0]["role"] = "unrelated-service"
        with self.assertRaises(probe.ProbeError):
            probe.validate_inventory(value, "full")


class DenialContracts(unittest.TestCase):
    def test_only_explicit_denials_or_tls_rejection_count(self):
        for status in (401, 403, 404, 405):
            self.assertEqual("denied", probe.expect_denied(lambda: (status, b"private body")))
        self.assertEqual("denied", probe.expect_denied(lambda: (_ for _ in ()).throw(ssl.SSLError("private error"))))
        for status in (200, 204, 500, 502, 503):
            with self.subTest(status=status), self.assertRaises(probe.ProbeError):
                probe.expect_denied(lambda: (status, b"private body"))

    def test_network_timeout_does_not_prove_authorization(self):
        with self.assertRaises(socket.timeout):
            probe.expect_denied(lambda: (_ for _ in ()).throw(socket.timeout("private address")))


class TokenContracts(unittest.TestCase):
    def test_client_server_names_match_the_signed_identity_manifest(self):
        manifest = json.loads((MODULE_PATH.parent / "mtls-identities.json").read_text())
        by_role = {item["role"]: item for item in manifest}
        for role in probe.FULL_COUNTS:
            with self.subTest(role=role), patch.object(probe.ssl, "create_default_context"):
                with patch.object(probe, "TLSConnection") as connection:
                    response = connection.return_value.getresponse.return_value
                    response.status, response.read.return_value = 200, b""
                    probe.request({"ip": "172.28.20.1", "role": role}, probe.METRICS[role], "/metrics", "vmagent", True)
                self.assertIn(connection.call_args.args[3], by_role[role]["dns_names"])

    def test_token_validation_never_echoes_material(self):
        with tempfile.TemporaryDirectory(prefix="vm-live-token-") as temporary:
            path = Path(temporary) / "token"
            path.write_text("test-fixture-with-newline\nnot-a-token")
            with self.assertRaises(probe.ProbeError) as caught:
                probe.read_token(path)
            self.assertNotIn("test-fixture", str(caught.exception))

    def test_health_only_mode_does_not_read_certificates_or_tokens(self):
        selected = probe.validate_inventory(inventory(), "operator")
        with patch.object(probe, "request", return_value=(200, b"")) as request:
            with patch.object(probe, "read_token", side_effect=AssertionError("credential read")):
                with self.assertRaises(probe.ProbeError):
                    # /metrics must be 404, even though every mocked path is 200.
                    probe.run_operator(selected, [])
        self.assertTrue(request.called)

    def test_full_mode_operator_health_checks_only_the_operator_pod(self):
        selected = probe.validate_inventory(inventory(), "full")
        operator = next(pod for pod in selected["pods"] if pod["role"] == "operator")
        with patch.object(probe, "request", side_effect=[(200, b""), (200, b""), (404, b"")]) as request:
            with patch.object(probe, "probe_pod"), patch.object(probe, "probe_gateway"), patch.object(probe, "probe_alertmanagers"):
                probe.run_full(selected, [])
        self.assertEqual(3, request.call_count)
        self.assertTrue(all(call.args[0] == operator for call in request.call_args_list))


if __name__ == "__main__":
    unittest.main()
