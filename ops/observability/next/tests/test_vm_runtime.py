"""Pure fixture/readiness contracts; execution belongs on the Linux builder."""

from copy import deepcopy
import importlib.util
import ipaddress
from pathlib import Path
import ssl
import sys
import tempfile
import unittest
from unittest.mock import patch


VM_ROOT = Path(__file__).resolve().parents[1] / "victoriametrics"
sys.path.insert(0, str(VM_ROOT))
from runtime_lab import DockerLab, LabError, auth_users, await_http_status, command  # noqa: E402


def fixture_users():
    return [{"kind": "VMUser", "metadata": {"name": name}, "spec": {
        "tokenRef": {"name": "vm-" + name, "key": "token"},
        "targetRefs": [{"paths": [path], "static": {"url": url}}]}}
        for name, path, url in (
            ("self-read", "/api/v1/query", "http://127.0.0.1:18481/select/1001/prometheus"),
            ("self-write", "/api/v1/write", "http://127.0.0.1:18480/insert/1001/prometheus"))]


class VMRuntimeFixtureContracts(unittest.TestCase):
    def test_two_distinct_users_keep_their_own_token_and_routes(self):
        objects = fixture_users()
        before = deepcopy(objects)
        users = auth_users(objects, {"self-read": "read-fixture", "self-write": "write-fixture"})
        self.assertEqual(2, len(users))
        self.assertEqual({"read-fixture", "write-fixture"}, {user["bearer_token"] for user in users})
        self.assertEqual("/api/v1/query", users[0]["url_map"][0]["src_paths"][0])
        self.assertEqual("/api/v1/write", users[1]["url_map"][0]["src_paths"][0])
        self.assertEqual(before, objects)

    def test_duplicate_or_missing_user_is_rejected_before_start(self):
        objects = fixture_users()
        for malformed in (objects + [deepcopy(objects[0])], objects[:1]):
            with self.subTest(users=len(malformed)), self.assertRaises(LabError):
                auth_users(malformed, {"self-read": "a", "self-write": "b"})

    def test_shared_token_or_unexpected_secret_mapping_is_rejected(self):
        with self.assertRaises(LabError):
            auth_users(fixture_users(), {"self-read": "same", "self-write": "same"})
        objects = fixture_users()
        objects[0]["spec"]["tokenRef"]["name"] = "vm-self-write"
        with self.assertRaises(LabError):
            auth_users(objects, {"self-read": "a", "self-write": "b"})


class VMRuntimeReadinessContracts(unittest.TestCase):
    def run_statuses(self, responses, timeout=30):
        operation = unittest.mock.Mock(side_effect=responses)
        with patch("runtime_lab.time.sleep"), patch("runtime_lab.time.monotonic", side_effect=range(100)):
            result = await_http_status(operation, 204, "synthetic-write", timeout)
        return result, operation

    def test_transient_backend_failures_are_bounded_and_reported(self):
        result, operation = self.run_statuses([(502, b""), (503, b""), (204, b"")])
        self.assertEqual(3, operation.call_count)
        self.assertEqual({"attempts": 3, "http_status": 204, "transient_statuses": [502, 503]}, result)

    def test_auth_or_permission_failures_do_not_retry(self):
        for status in (400, 401, 403, 404, 405):
            with self.subTest(status=status), self.assertRaisesRegex(LabError, f"HTTP {status}"):
                self.run_statuses([(status, b"untrusted response")])

    def test_persistent_backend_failure_is_not_success(self):
        with self.assertRaisesRegex(LabError, "deadline.*HTTP 502"):
            self.run_statuses([(502, b"")] * 10, timeout=4)

    def test_certificate_verification_failure_is_not_hidden_by_retry(self):
        with self.assertRaisesRegex(LabError, "TLS verification"):
            self.run_statuses([ssl.SSLCertVerificationError("untrusted fixture certificate")])


class VMRuntimeCertificateContracts(unittest.TestCase):
    def test_expired_fixture_preserves_identity_key_and_trusted_signature(self):
        spec = importlib.util.spec_from_file_location("vm_runtime_credentials", VM_ROOT.parent / "mtls/credentials.py")
        credentials = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(credentials)
        with tempfile.TemporaryDirectory(prefix="vm-expiry-test-") as temporary:
            lab = DockerLab(temporary, VM_ROOT, credentials)
            lab.issue("canary")
            expired = lab.issue_expired_canary()
            public_key = command(["openssl", "pkey", "-in", str(expired / "tls.key"), "-pubout"]).stdout
            certificate_key = command(["openssl", "x509", "-in", str(expired / "tls.crt"), "-pubkey", "-noout"]).stdout
            self.assertEqual(public_key, certificate_key)
            sans = command(["openssl", "x509", "-in", str(expired / "tls.crt"), "-ext", "subjectAltName", "-noout"]).stdout
            self.assertIn("URI:spiffe://observability.raytrain.local/observability-metrics/canary", sans)
            self.assertEqual(expired, lab.materials["expired-canary"])


class VMRuntimeAlertmanagerContracts(unittest.TestCase):
    def test_native_tls_gossip_binds_a_valid_ip_with_peer_discovery(self):
        spec = importlib.util.spec_from_file_location("vm_runtime_verifier", VM_ROOT / "verify-runtime.py")
        verifier = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(verifier)
        lab = unittest.mock.Mock()
        lab.directory = Path("/unused-private-lab")
        lab.materials = {"alertmanager-gossip": Path("/unused-private-identity")}
        lab.native.side_effect = LabError("stop before any container creation")
        with patch.object(verifier, "private_file"), patch.object(verifier, "writable_data"):
            with self.assertRaisesRegex(LabError, "stop before any container creation"):
                verifier.alertmanagers(lab)
        flags = lab.native.call_args.args[2]
        listen = next(flag.split("=", 1)[1] for flag in flags if flag.startswith("--cluster.listen-address="))
        address, port = listen.rsplit(":", 1)
        self.assertEqual("0.0.0.0", str(ipaddress.ip_address(address)))
        self.assertEqual("9094", port)
        self.assertIn("--cluster.tls-config=/config/gossip.yaml", flags)
        peers = [flag for flag in flags if flag.startswith("--cluster.peer=")]
        self.assertEqual(2, len(peers))
        self.assertTrue(all("vmalertmanager-raytrain-next-0." not in peer for peer in peers))


class VMRuntimeOperatorHealthContracts(unittest.TestCase):
    def verifier(self):
        spec = importlib.util.spec_from_file_location("vm_operator_health", VM_ROOT / "verify-operator-health.py")
        verifier = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(verifier)
        return verifier

    def test_cache_not_ready_does_not_replace_upstream_process_health(self):
        verifier = self.verifier()
        with patch.object(verifier, "get_status", side_effect=[200, 404, 200, 200, 404, 500]):
            report = verifier.inspect_health(28081, 28082)
        self.assertEqual("passed", report["status"])
        self.assertEqual(500, report["observed"]["native:/ready"])

    def test_real_native_or_proxy_health_failure_is_not_accepted(self):
        verifier = self.verifier()
        for native, proxy in ((500, 200), (200, 503)):
            with self.subTest(native=native, proxy=proxy):
                with patch.object(verifier, "get_status", side_effect=[native, 404, proxy, 200, 404, 200]):
                    report = verifier.inspect_health(28081, 28082)
                self.assertEqual("failed", report["status"])


class VMRuntimePausedHealthContracts(unittest.TestCase):
    def verifier(self):
        spec = importlib.util.spec_from_file_location("vm_pause_health", VM_ROOT / "verify-runtime.py")
        verifier = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(verifier)
        return verifier

    def fixture(self):
        lab = unittest.mock.Mock()
        lab.prefix = "rt-vm-mtls-fixture"
        app = lab.prefix + "-storage-0-app"
        lab.containers = [app]
        lab.results = []
        inspected = unittest.mock.Mock(stdout='[{"Config":{"Labels":{"raytrain.mtls-lab":"rt-vm-mtls-fixture"}}}]')
        return lab, {"app": app, "ip": "172.19.0.2"}, inspected

    def test_health_pause_requires_bounded_failure_then_recovery(self):
        verifier = self.verifier()
        lab, pod, inspected = self.fixture()
        observed = [200, 200, 503, 503, 200, 200]
        with patch.object(verifier, "command", return_value=inspected) as command_mock:
            with patch.object(verifier, "plain_request", side_effect=[(status, b"") for status in observed]):
                with patch.object(verifier, "eventually", side_effect=lambda operation, timeout: operation()) as wait:
                    verifier.paused_storage_health(lab, pod)
        self.assertEqual([20, 20, 20], [call.kwargs["timeout"] for call in wait.call_args_list])
        command_mock.assert_any_call(["docker", "pause", pod["app"]])
        command_mock.assert_any_call(["docker", "unpause", pod["app"]])
        self.assertEqual("storage-pause-health-fails-within-20s-and-recovers", lab.results[0]["check"])

    def test_failed_probe_always_unpauses_without_claiming_success(self):
        verifier = self.verifier()
        lab, pod, inspected = self.fixture()
        with patch.object(verifier, "command", return_value=inspected) as command_mock:
            with patch.object(verifier, "eventually", side_effect=[True, LabError("health deadline")]):
                with self.assertRaisesRegex(LabError, "health deadline"):
                    verifier.paused_storage_health(lab, pod)
        self.assertEqual(["docker", "unpause", pod["app"]], command_mock.call_args.args[0])
        self.assertEqual([], lab.results)

    def test_slow_observed_failure_does_not_pass_deadline(self):
        verifier = self.verifier()
        lab, pod, inspected = self.fixture()
        with patch.object(verifier, "command", return_value=inspected) as command_mock:
            with patch.object(verifier, "eventually", return_value=True):
                with patch.object(verifier.time, "monotonic", side_effect=[0, 21]):
                    with self.assertRaisesRegex(LabError, "within 20 seconds"):
                        verifier.paused_storage_health(lab, pod)
        self.assertEqual(["docker", "unpause", pod["app"]], command_mock.call_args.args[0])
        self.assertEqual([], lab.results)

    def test_pause_rejects_proxy_unknown_or_foreign_container(self):
        verifier = self.verifier()
        for target in ("rt-vm-mtls-fixture-storage-0-proxy", "foreign-storage-0-app"):
            lab, pod, inspected = self.fixture()
            with self.subTest(target=target), patch.object(verifier, "command") as command_mock:
                with self.assertRaises(LabError):
                    verifier.paused_storage_health(lab, {**pod, "app": target})
                command_mock.assert_not_called()
        lab, pod, inspected = self.fixture()
        inspected.stdout = '[{"Config":{"Labels":{"raytrain.mtls-lab":"foreign"}}}]'
        with patch.object(verifier, "command", return_value=inspected) as command_mock:
            with self.assertRaises(LabError):
                verifier.paused_storage_health(lab, pod)
        self.assertEqual(1, command_mock.call_count)
        self.assertEqual("inspect", command_mock.call_args.args[0][1])


if __name__ == "__main__":
    unittest.main()
