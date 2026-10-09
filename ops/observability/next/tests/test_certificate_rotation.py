"""Safety regressions for serial native-consumer leaf certificate rotation."""
import base64
import copy
import importlib.util
import json
from pathlib import Path
import unittest

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
            "spec": {"containers": [{"name": "native"}], "hostNetwork": False},
            "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}],
                       "containerStatuses": [{"name": "native", "ready": True,
                                              "state": {"running": {}}}]}}


def group():
    return {"name": "keeper", "namespace": "clickhouse", "replicas": 3,
            "selector": {"component": "keeper"}, "pdb": "keeper-pdb",
            "secrets": ["obs-mtls-keeper"], "controllers": [
                {"kind": "StatefulSet", "name": "keeper-" + str(i), "replicas": 1}
                for i in range(3)]}


class FakeAPI:
    def __init__(self):
        self.controllers = {"keeper-" + str(i): controller("keeper-" + str(i)) for i in range(3)}
        self.pods = [pod("keeper-" + str(i) + "-0", "keeper-" + str(i)) for i in range(3)]
        self.secrets = {"obs-mtls-keeper": secret()}
        self.pdb = {"metadata": {"generation": 1}, "spec": {"selector": {"matchLabels": {"component": "keeper"}}},
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


if __name__ == "__main__":
    unittest.main()
