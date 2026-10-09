"""Safety regressions for serial native-consumer leaf certificate rotation."""
import base64
import copy
import importlib.util
import hashlib
import json
from pathlib import Path
import unittest
from unittest import mock
import urllib.error

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("certificate_rotation", ROOT / "mtls/rotation.py")
rotation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(rotation)


def secret(name="obs-mtls-keeper", leaf=b"leaf-v1", ca=b"root-v1"):
    return {"metadata": {"name": name, "namespace": "clickhouse", "uid": name,
                         "annotations": {"cert-manager.io/certificate-name": name}},
            "data": {"tls.crt": base64.b64encode(leaf).decode(),
                     "ca.crt": base64.b64encode(ca).decode(), "tls.key": "DO-NOT-READ"}}


def controller(name, replicas=1):
    labels = {"component": "keeper", "instance": name}
    return {"kind": "StatefulSet", "metadata": {"namespace": "clickhouse", "name": name,
            "uid": name + "-uid", "generation": 1}, "spec": {"replicas": replicas,
            "selector": {"matchLabels": labels}, "template": {"metadata": {"labels": labels}}},
            "status": {"observedGeneration": 1, "readyReplicas": replicas,
                       "updatedReplicas": replicas, "currentRevision": "a", "updateRevision": "a"}}


def pod(name, owner, annotation=None):
    annotations = {} if annotation is None else {rotation.ANNOTATION: annotation}
    return {"metadata": {"namespace": "clickhouse", "name": name, "uid": name + "-uid",
            "resourceVersion": "1", "creationTimestamp": "2026-10-09T00:00:00Z",
            "labels": {"component": "keeper", "instance": owner}, "annotations": annotations,
            "ownerReferences": [{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": owner,
                                  "uid": owner + "-uid", "controller": True}]},
            "spec": {"containers": [{"name": "native"}], "hostNetwork": False,
                     "volumes": [{"name": "leaf", "secret": {"secretName": "obs-mtls-keeper"}}]},
            "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}],
                       "containerStatuses": [{"name": "native", "ready": True,
                                              "state": {"running": {}}}]}}


def group():
    return {"name": "keeper", "namespace": "clickhouse", "replicas": 3,
            "selector": {"component": "keeper"}, "pdb": "keeper-pdb",
            "secrets": ["obs-mtls-keeper"], "expected_ca_sha256": {"obs-mtls-keeper": hashlib.sha256(b"root-v1").hexdigest()},
            "generated_assets": {}, "controllers": [
                {"kind": "StatefulSet", "name": "keeper-" + str(i), "replicas": 1}
                for i in range(3)]}


class FakeAPI:
    def __init__(self):
        self.controllers = {"keeper-" + str(i): controller("keeper-" + str(i)) for i in range(3)}
        self.pods = [pod("keeper-" + str(i) + "-0", "keeper-" + str(i)) for i in range(3)]
        self.secrets = {"obs-mtls-keeper": secret()}
        self.pdb = {"metadata": {"generation": 1, "namespace": "clickhouse", "name": "keeper-pdb"}, "spec": {"selector": {"matchLabels": {"component": "keeper"}}},
                    "status": {"observedGeneration": 1, "expectedPods": 3,
                               "currentHealthy": 3, "disruptionsAllowed": 1}}
        self.events = []
        self.reject_eviction = False
        self.replace_ready = True
        self.change_secret_during_eviction = False
        self.unexpected_replacement = False
        self.counter = 0

    def get(self, path):
        name = path.rsplit("/", 1)[-1]
        if "/secrets/" in path:
            return copy.deepcopy(self.secrets[name])
        if "/statefulsets/" in path:
            return copy.deepcopy(self.controllers[name])
        if "/poddisruptionbudgets/" in path:
            return copy.deepcopy(self.pdb)
        raise AssertionError(path)

    def list_pods(self, namespace, selector=None):
        return copy.deepcopy([p for p in self.pods if not selector or rotation.matches(p["metadata"]["labels"], {"matchLabels": selector})])

    def request(self, method, path, body=None, content_type=None):
        self.events.append((method, path, copy.deepcopy(body)))
        if method == "POST" and path.endswith("/eviction"):
            if self.reject_eviction:
                raise rotation.APIError(429)
            name = path.split("/")[-2]
            item = next(p for p in self.pods if p["metadata"]["name"] == name)
            assert body["deleteOptions"]["preconditions"] == {
                "uid": item["metadata"]["uid"], "resourceVersion": item["metadata"]["resourceVersion"]}
            self.counter += 1
            item["metadata"]["uid"] += "-new"
            item["metadata"]["creationTimestamp"] = "2026-10-09T01:00:00Z"
            item["metadata"]["annotations"] = {}
            if not self.replace_ready:
                item["status"]["conditions"][0]["status"] = "False"
            if self.change_secret_during_eviction:
                self.secrets["obs-mtls-keeper"] = secret(leaf=b"leaf-v2")
            if self.unexpected_replacement:
                self.pods[-1]["metadata"]["uid"] += "-unexpected"
            return {}
        if method == "PATCH" and "/pods/" in path:
            name = path.rsplit("/", 1)[-1]
            item = next(p for p in self.pods if p["metadata"]["name"] == name)
            assert body[0] == {"op": "test", "path": "/metadata/uid", "value": item["metadata"]["uid"]}
            self.assert_hash_only(body)
            item["metadata"]["annotations"][rotation.ANNOTATION] = body[-1]["value"]
            return copy.deepcopy(item)
        raise AssertionError((method, path))

    @staticmethod
    def assert_hash_only(body):
        assert "tls.key" not in json.dumps(body)
        assert "DO-NOT-READ" not in json.dumps(body)


class FakeLease:
    def __init__(self):
        self.calls = 0
        self.fail_after = None

    def renew(self):
        self.calls += 1
        if self.fail_after is not None and self.calls >= self.fail_after:
            raise rotation.RotationError("Lease lost")


class RotationSafetyTests(unittest.TestCase):
    def setUp(self):
        self.api = FakeAPI()
        self.lease = FakeLease()
        self.now = 0.0
        def sleep(seconds):
            self.now += seconds
        self.runner = rotation.Rotator(self.api, self.lease, timeout=15, settle=0,
            clock=lambda: self.now, sleep=sleep, utc=lambda: "2026-10-09T00:59:59Z")

    def test_bootstrap_rolls_each_unknown_pod_once_then_second_run_is_noop(self):
        self.runner.rotate_group(group())
        self.assertEqual(3, self.api.counter)
        mutations = [e[0] for e in self.api.events]
        self.assertEqual(["POST", "PATCH"] * 3, mutations)
        self.runner.rotate_group(group())
        self.assertEqual(3, self.api.counter)

    def test_leaf_renewal_rolls_only_outdated_pods(self):
        state = rotation.certificate_state(self.api, group())
        for item in self.api.pods:
            item["metadata"]["annotations"][rotation.ANNOTATION] = state["annotation"]
        self.api.pods[0]["metadata"]["annotations"] = {}
        self.runner.rotate_group(group())
        self.assertEqual(1, self.api.counter)

    def test_key_material_is_never_hashed_or_returned(self):
        state = rotation.certificate_state(self.api, group())
        self.api.secrets["obs-mtls-keeper"]["data"]["tls.key"] = "ALSO-DO-NOT-READ"
        self.assertEqual(state, rotation.certificate_state(self.api, group()))
        self.assertNotIn("DO-NOT-READ", json.dumps(state))

    def test_ca_rotation_requires_separate_migration(self):
        state = rotation.certificate_state(self.api, group())
        self.api.pods[0]["metadata"]["annotations"][rotation.ANNOTATION] = state["annotation"]
        self.api.secrets["obs-mtls-keeper"] = secret(ca=b"new-root")
        with self.assertRaisesRegex(rotation.RotationError, "CA"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_one_unready_member_prevents_any_eviction(self):
        self.api.pods[1]["status"]["conditions"][0]["status"] = "False"
        with self.assertRaises(rotation.RotationError):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_wrong_owner_prevents_any_eviction(self):
        self.api.pods[0]["metadata"]["ownerReferences"][0]["uid"] = "unrelated"
        with self.assertRaisesRegex(rotation.RotationError, "owner"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_controller_rollout_prevents_any_eviction(self):
        self.api.controllers["keeper-0"]["status"]["updateRevision"] = "b"
        with self.assertRaises(rotation.RotationError):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_pdb_zero_budget_prevents_any_eviction(self):
        self.api.pdb["status"]["disruptionsAllowed"] = 0
        with self.assertRaisesRegex(rotation.RotationError, "PDB"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_stale_pdb_prevents_any_eviction(self):
        self.api.pdb["status"]["observedGeneration"] = 0
        with self.assertRaisesRegex(rotation.RotationError, "PDB"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_eviction_429_does_not_fall_back_to_delete(self):
        self.api.reject_eviction = True
        with self.assertRaises(rotation.RotationError):
            self.runner.rotate_group(group())
        self.assertEqual(["POST"], [e[0] for e in self.api.events])

    def test_replacement_timeout_stops_after_one_pod(self):
        self.api.replace_ready = False
        with self.assertRaisesRegex(rotation.RotationError, "timeout"):
            self.runner.rotate_group(group())
        self.assertEqual(1, self.api.counter)
        self.assertEqual(["POST"], [e[0] for e in self.api.events])

    def test_secret_change_mid_roll_never_acknowledged(self):
        self.api.change_secret_during_eviction = True
        with self.assertRaisesRegex(rotation.RotationError, "certificate"):
            self.runner.rotate_group(group())
        self.assertEqual(["POST"], [e[0] for e in self.api.events])

    def test_unexpected_parallel_replacement_never_acknowledged(self):
        self.api.unexpected_replacement = True
        with self.assertRaises(rotation.RotationError):
            self.runner.rotate_group(group())
        self.assertEqual(1, self.api.counter)

    def test_lost_lease_prevents_mutation(self):
        self.lease.fail_after = 1
        with self.assertRaisesRegex(rotation.RotationError, "Lease"):
            self.runner.rotate_group(group())
        self.assertEqual([], self.api.events)

    def test_public_annotation_cannot_silently_accept_unparseable_state(self):
        self.api.pods[0]["metadata"]["annotations"][rotation.ANNOTATION] = "invalid"
        with self.assertRaises(rotation.RotationError):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_policy_disallows_training_namespaces_or_pg_controllers(self):
        policy = json.loads((ROOT / "mtls/rotation-policy.json").read_text())
        unfilled = copy.deepcopy(policy)
        unfilled["groups"][0]["expected_ca_sha256"] = {unfilled["groups"][0]["secrets"][0]: "__UNFILLED__"}
        with self.assertRaisesRegex(rotation.RotationError, "CA"):
            rotation.validate_policy(unfilled)
        for configured in policy["groups"]:
            configured["expected_ca_sha256"] = {name: hashlib.sha256(b"root-v1").hexdigest() for name in configured["secrets"]}
        rotation.validate_policy(policy)
        for change in (lambda p: p["groups"][0].update(namespace="tenant-local"),
                       lambda p: p["groups"][0]["controllers"][0].update(name="nightingale-postgres-1")):
            changed = copy.deepcopy(policy)
            change(changed)
            with self.assertRaises(rotation.RotationError):
                rotation.validate_policy(changed)

    def test_manifest_defaults_suspended_and_has_no_secret_list_or_pod_delete(self):
        import yaml
        docs = list(yaml.safe_load_all((ROOT / "mtls/rotation-cronjob.yaml").read_text()))
        cron = next(d for d in docs if d.get("kind") == "CronJob")
        self.assertTrue(cron["spec"]["suspend"])
        self.assertEqual("Forbid", cron["spec"]["concurrencyPolicy"])
        self.assertEqual("*/15 * * * *", cron["spec"]["schedule"])
        for role in [d for d in docs if d.get("kind") == "Role"]:
            self.assertIn(role["metadata"]["namespace"], {"cert-manager", "clickhouse", "nightingale", "victoriametrics"})
            for rule in role["rules"]:
                self.assertNotIn("*", rule["verbs"])
                if "secrets" in rule["resources"]:
                    self.assertEqual(["get"], rule["verbs"])
                    self.assertTrue(rule["resourceNames"])
                if "pods" in rule["resources"]:
                    self.assertNotIn("delete", rule["verbs"])


class LeaseAPI:
    def __init__(self):
        self.lease = {"metadata": {"namespace": "cert-manager", "name": "native-certificate-rotation",
                      "uid": "fixed-lock", "resourceVersion": "1"}, "spec": {"leaseDurationSeconds": 120}}
        self.writes = 0
        self.conflict = False

    def get(self, path):
        return copy.deepcopy(self.lease)

    def request(self, method, path, body):
        if self.conflict:
            raise rotation.APIError(409)
        assert method == "PUT"
        assert body["metadata"]["resourceVersion"] == self.lease["metadata"]["resourceVersion"]
        self.writes += 1
        self.lease = copy.deepcopy(body)
        self.lease["metadata"]["resourceVersion"] = str(self.writes + 1)
        return copy.deepcopy(self.lease)


class LeaseSafetyTests(unittest.TestCase):
    def setUp(self):
        self.api = LeaseAPI()
        self.now = "2026-10-09T01:00:00Z"
        self.lease = rotation.Lease(self.api, "job-one", utc=lambda: self.now)

    def test_acquire_and_renew_use_resource_version_compare_and_swap(self):
        self.lease.acquire()
        self.lease.renew()
        self.assertEqual(2, self.api.writes)
        self.assertEqual("job-one", self.api.lease["spec"]["holderIdentity"])

    def test_active_other_holder_is_not_stolen(self):
        self.api.lease["spec"].update(holderIdentity="other", renewTime=self.now)
        with self.assertRaisesRegex(rotation.RotationError, "active holder"):
            self.lease.acquire()
        self.assertEqual(0, self.api.writes)

    def test_expired_other_holder_can_be_replaced_with_cas(self):
        self.api.lease["spec"].update(holderIdentity="other", renewTime="2026-10-09T00:00:00Z")
        self.lease.acquire()
        self.assertEqual("job-one", self.api.lease["spec"]["holderIdentity"])

    def test_expired_own_holder_must_not_resume_mutation(self):
        self.lease.acquire()
        self.now = "2026-10-09T01:02:01Z"
        with self.assertRaisesRegex(rotation.RotationError, "expired"):
            self.lease.renew()
        self.assertEqual(1, self.api.writes)

    def test_recreated_or_stolen_lease_stops_holder(self):
        self.lease.acquire()
        for field, value in (("uid", "replacement"),):
            self.api.lease["metadata"][field] = value
            with self.assertRaisesRegex(rotation.RotationError, "lost"):
                self.lease.renew()
        self.api.lease["metadata"]["uid"] = "fixed-lock"
        self.api.lease["spec"]["holderIdentity"] = "other"
        with self.assertRaisesRegex(rotation.RotationError, "lost"):
            self.lease.renew()

    def test_cas_conflict_stops_without_retrying_a_write(self):
        self.api.conflict = True
        with self.assertRaises(rotation.APIError):
            self.lease.acquire()
        self.assertEqual(0, self.api.writes)


class KubernetesTransportTests(unittest.TestCase):
    def client(self):
        with mock.patch.dict(rotation.os.environ, {"KUBERNETES_SERVICE_HOST": "10.0.0.1"}, clear=True), \
                mock.patch.object(rotation.ssl, "create_default_context") as context, \
                mock.patch.object(rotation.urllib.request, "build_opener") as builder:
            api = rotation.Kubernetes()
            context.assert_called_once_with(cafile="/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
            self.assertEqual("https://10.0.0.1:443", api.base)
            self.assertIsInstance(builder.call_args.args[0], rotation.NoRedirect)
        api.token = mock.Mock()
        api.token.read_text.return_value = "PRIVATE-TOKEN"
        return api

    def test_validated_tls_and_rotating_token_are_used(self):
        api = self.client()
        api.opener.open.return_value.__enter__.return_value.read.return_value = b'{"items":[]}'
        self.assertEqual([], api.list_pods("clickhouse", {"app": "keeper"}))
        request = api.opener.open.call_args.args[0]
        self.assertEqual("Bearer PRIVATE-TOKEN", request.get_header("Authorization"))
        self.assertIn("labelSelector=app%3Dkeeper", request.full_url)
        self.assertEqual(15, api.opener.open.call_args.kwargs["timeout"])

    def test_api_error_body_is_not_exposed(self):
        api = self.client()
        api.opener.open.side_effect = urllib.error.HTTPError("url", 403, "PRIVATE-SECRET", {}, None)
        with self.assertRaises(rotation.APIError) as caught:
            api.get("/api/v1/namespaces/clickhouse/secrets/leaf")
        self.assertNotIn("PRIVATE", str(caught.exception))

    def test_transport_error_is_redacted(self):
        api = self.client()
        api.opener.open.side_effect = urllib.error.URLError("PRIVATE-TOKEN")
        with self.assertRaisesRegex(rotation.RotationError, "transport") as caught:
            api.get("/api/v1/namespaces/clickhouse/pods")
        self.assertNotIn("PRIVATE", str(caught.exception))

    def test_delete_and_redirect_are_forbidden(self):
        api = self.client()
        with self.assertRaises(rotation.RotationError):
            api.request("DELETE", "/api/v1/namespaces/clickhouse/pods/keeper")
        self.assertIsNone(rotation.NoRedirect().redirect_request(None, None, 302, "", {}, "http://untrusted"))
        api.opener.open.assert_not_called()

    def test_response_size_and_pagination_fail_closed(self):
        api = self.client()
        api.opener.open.return_value.__enter__.return_value.read.return_value = b"x" * (4 * 1024 * 1024 + 1)
        with self.assertRaises(rotation.RotationError):
            api.get("/api/v1/namespaces/clickhouse/pods")
        api.opener.open.return_value.__enter__.return_value.read.return_value = b'{"metadata":{"continue":"next"},"items":[]}'
        with self.assertRaises(rotation.RotationError):
            api.list_pods("clickhouse")


class AdditionalRotationSafetyTests(unittest.TestCase):
    setUp = RotationSafetyTests.setUp
    def test_real_leaf_change_rotates_all_three_and_records_only_public_hash(self):
        self.runner.rotate_group(group())
        self.api.secrets["obs-mtls-keeper"] = secret(leaf=b"renewed-leaf")
        self.runner.rotate_group(group())
        self.assertEqual(6, self.api.counter)
        self.assertNotIn("DO-NOT-READ", json.dumps(self.api.events))

    def test_missing_secret_mount_is_refused(self):
        self.api.pods[0]["spec"]["volumes"] = []
        with self.assertRaisesRegex(rotation.RotationError, "mount"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_host_network_is_refused(self):
        self.api.pods[0]["spec"]["hostNetwork"] = True
        with self.assertRaises(rotation.RotationError):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_overall_deadline_stops_before_mutation(self):
        self.runner.deadline = 0
        with self.assertRaisesRegex(rotation.RotationError, "Overall"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)

    def test_stable_secret_wait_renews_lease(self):
        self.runner.settle = 11
        self.runner.rotate_group(group())
        self.assertGreaterEqual(self.now, 11)
        self.assertGreater(self.lease.calls, 10)

    def test_controller_generation_change_is_refused(self):
        _, baseline = self.runner.snapshot(group())
        self.api.controllers["keeper-0"]["metadata"]["generation"] += 1
        with self.assertRaisesRegex(rotation.RotationError, "Controller changed"):
            self.runner.snapshot(group(), baseline)

    def test_pdb_does_not_cover_unrelated_pods(self):
        unrelated = pod("other", "other")
        unrelated["metadata"]["labels"] = {"component": "keeper"}
        self.api.pods.append(unrelated)
        with self.assertRaises(rotation.RotationError):
            self.runner.check_pdb(group(), self.api.pods[:3])

    def test_secret_without_cert_manager_identity_is_refused(self):
        self.api.secrets["obs-mtls-keeper"]["metadata"]["annotations"] = {}
        with self.assertRaisesRegex(rotation.RotationError, "Secret owner"):
            self.runner.rotate_group(group())
        self.assertEqual(0, self.api.counter)


class RenewalTrustRegressionTests(unittest.TestCase):
    def test_unknown_bootstrap_ca_and_missing_pins_block_all_eviction(self):
        for expected in ({}, {"obs-mtls-keeper": hashlib.sha256(b"known-root").hexdigest()}):
            with self.subTest(pins=bool(expected)):
                api, lease = FakeAPI(), FakeLease()
                configured = {**group(), "expected_ca_sha256": expected}
                runner = rotation.Rotator(api, lease, settle=0)
                with self.assertRaisesRegex(rotation.RotationError, "CA"):
                    runner.rotate_group(configured)
                self.assertEqual([], api.events)

    def test_stale_generated_alertmanager_public_assets_block_rotation_state(self):
        api, configured = alertmanager_fixture()
        mapping = rotation.AM_ASSETS[rotation.AM_SOURCE]
        api.secrets[mapping["secret"]]["data"][mapping["certificate_key"]] = base64.b64encode(b"old-leaf").decode()
        with self.assertRaisesRegex(rotation.RotationError, "generated.*not synchronized"):
            rotation.certificate_state(api, configured)
        self.assertEqual([], api.events)


def alertmanager_fixture():
    source_name = rotation.AM_SOURCE
    mapping = rotation.AM_ASSETS[source_name]
    source = secret(name=source_name, leaf=b"leaf-v1\n", ca=b"root-v1\n")
    source["metadata"]["namespace"] = "victoriametrics"
    api = FakeAPI()
    path = mapping["mount_path"] + "/"
    gossip = "\n".join([
        "tls_server_config:", "  cert_file: " + path + mapping["certificate_key"],
        "  key_file: " + path + mapping["key_key"], "  client_ca_file: " + path + mapping["ca_key"],
        "  client_auth_type: RequireAndVerifyClientCert", "  min_version: TLS13",
        "tls_client_config:", "  cert_file: " + path + mapping["certificate_key"],
        "  key_file: " + path + mapping["key_key"], "  ca_file: " + path + mapping["ca_key"],
        "  server_name: alertmanager.victoriametrics.svc", "",
    ])
    generated = {"metadata": {"namespace": "victoriametrics", "name": mapping["secret"], "uid": "generated"},
        "data": {mapping["certificate_key"]: base64.b64encode(b"leaf-v1").decode(),
                 mapping["ca_key"]: base64.b64encode(b"root-v1").decode(),
                 mapping["key_key"]: "PRIVATE-DO-NOT-READ",
                 mapping["config_key"]: base64.b64encode(gossip.encode()).decode()}}
    api.secrets = {source_name: source, mapping["secret"]: generated}
    configured = {"name": "alertmanager", "namespace": "victoriametrics", "secrets": [source_name],
        "expected_ca_sha256": {source_name: hashlib.sha256(b"root-v1").hexdigest()},
        "generated_assets": copy.deepcopy(rotation.AM_ASSETS)}
    return api, configured


class GeneratedAssetConsumptionTests(unittest.TestCase):
    def native_pod(self):
        mapping = rotation.AM_ASSETS[rotation.AM_SOURCE]
        return {"spec": {"containers": [{"name": "alertmanager", "args": [
            "--cluster.tls-config=" + mapping["mount_path"] + "/" + mapping["config_key"]],
            "volumeMounts": [{"name": "tls-assets", "mountPath": mapping["mount_path"]}]}],
            "volumes": [{"name": "tls-assets", "secret": {"secretName": mapping["secret"]}}]}}

    def test_matching_public_copies_accept_operator_tail_trim_and_omit_private_key(self):
        api, configured = alertmanager_fixture()
        state = rotation.certificate_state(api, configured)
        self.assertEqual(2, len(state["uids"]))
        self.assertNotIn("PRIVATE", json.dumps(state))
        rotation.generated_mounts(self.native_pod(), configured)

    def test_copied_ca_mismatch_is_rejected(self):
        api, configured = alertmanager_fixture()
        mapping = rotation.AM_ASSETS[rotation.AM_SOURCE]
        api.secrets[mapping["secret"]]["data"][mapping["ca_key"]] = base64.b64encode(b"other-ca").decode()
        with self.assertRaisesRegex(rotation.RotationError, "generated"):
            rotation.certificate_state(api, configured)

    def test_unused_equal_assets_do_not_mask_wrong_gossip_paths(self):
        api, configured = alertmanager_fixture()
        mapping = rotation.AM_ASSETS[rotation.AM_SOURCE]
        data = api.secrets[mapping["secret"]]["data"]
        gossip = base64.b64decode(data[mapping["config_key"]]).replace(mapping["certificate_key"].encode(), b"wrong-old-cert")
        data[mapping["config_key"]] = base64.b64encode(gossip).decode()
        with self.assertRaisesRegex(rotation.RotationError, "consume"):
            rotation.certificate_state(api, configured)

    def test_insecure_generated_gossip_is_rejected(self):
        api, configured = alertmanager_fixture()
        mapping = rotation.AM_ASSETS[rotation.AM_SOURCE]
        data = api.secrets[mapping["secret"]]["data"]
        gossip = base64.b64decode(data[mapping["config_key"]]) + b"  insecure_skip_verify: true\n"
        data[mapping["config_key"]] = base64.b64encode(gossip).decode()
        with self.assertRaisesRegex(rotation.RotationError, "consume"):
            rotation.certificate_state(api, configured)

    def test_virtual_source_mount_does_not_bypass_native_consumer_binding(self):
        _, configured = alertmanager_fixture()
        pod = self.native_pod()
        pod["spec"]["volumes"][0]["secret"]["secretName"] = rotation.AM_SOURCE
        with self.assertRaisesRegex(rotation.RotationError, "mounted"):
            rotation.generated_mounts(pod, configured)

    def test_generated_mount_without_actual_gossip_argument_is_rejected(self):
        _, configured = alertmanager_fixture()
        pod = self.native_pod()
        pod["spec"]["containers"][0]["args"] = []
        with self.assertRaisesRegex(rotation.RotationError, "does not use"):
            rotation.generated_mounts(pod, configured)

    def test_subpath_or_incomplete_projection_is_rejected(self):
        _, configured = alertmanager_fixture()
        for change in (
            lambda p: p["spec"]["containers"][0]["volumeMounts"][0].update(subPath="tls"),
            lambda p: p["spec"]["volumes"][0]["secret"].update(items=[{"key": "wrong", "path": "wrong"}]),
        ):
            pod = self.native_pod()
            change(pod)
            with self.assertRaises(rotation.RotationError):
                rotation.generated_mounts(pod, configured)


if __name__ == "__main__":
    unittest.main()
