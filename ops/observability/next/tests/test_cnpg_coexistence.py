"""CNPG coexistence contracts; only execute in the isolated builder candidate."""
import copy
from pathlib import Path
import runpy
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1] / "nightingale"


def renderer():
    return runpy.run_path(str(ROOT / "scope-cnpg-webhooks.py"))


def deployment():
    image = "harbor.wellspiking.ai/guofeng.su/cnpg-observability:test@sha256:" + "a" * 64
    return {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {
        "name": "cloudnative-pg-operator", "namespace": "nightingale"}, "spec": {
        "selector": {"matchLabels": {"app.kubernetes.io/instance": "cloudnative-pg-operator"}},
        "template": {"spec": {"containers": [{"name": "manager", "image": image,
            "args": ["controller", "--metrics-bind-address=127.0.0.1:8080"], "env": [
                {"name": "OPERATOR_IMAGE_NAME", "value": image},
                {"name": "OBS_WEBHOOK_API_SERVER_IPS", "value": "172.28.2.64,172.28.5.50"},
                {"name": "ENABLE_WEBHOOK_NAMESPACE_SUFFIX", "value": "true"},
                {"name": "OPERATOR_NAMESPACE", "valueFrom": {"fieldRef": {
                    "apiVersion": "v1", "fieldPath": "metadata.namespace"}}},
            ]}], "volumes": []}}}}


def webhook(kind, mode):
    return {"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind,
            "metadata": {"name": f"cnpg-{mode}-webhook-configuration"}, "webhooks": [{
                "name": "test.postgresql.cnpg.io", "rules": [{"scope": "*"}],
                "clientConfig": {"service": {"name": "cnpg-webhook-service", "namespace": "nightingale"}}}]}


class CnpgCoexistenceTests(unittest.TestCase):
    def test_values_opt_in_to_official_suffix_and_do_not_own_existing_crds(self):
        values = yaml.safe_load((ROOT / "10-cnpg-values.yaml").read_text())
        env = {item["name"]: item.get("value") for item in values["additionalEnv"]}
        self.assertEqual("true", env.get("ENABLE_WEBHOOK_NAMESPACE_SUFFIX"))
        self.assertIs(values.get("crds", {}).get("create"), False)
        self.assertEqual("nightingale", values["config"]["data"]["WATCH_NAMESPACE"])
        self.assertIs(values["config"]["clusterWide"], False)

    def test_output_webhook_names_and_exact_patch_rights_are_disjoint_from_old_operator(self):
        module = renderer()
        old_names = set(module["WEBHOOK_NAMES"].values())
        for kind, mode in (("MutatingWebhookConfiguration", "mutating"),
                           ("ValidatingWebhookConfiguration", "validating")):
            original = webhook(kind, mode)
            actual = module["scope_webhook"](original)
            self.assertEqual(f"cnpg-{mode}-webhook-configuration-nightingale", actual["metadata"]["name"])
            self.assertNotIn(actual["metadata"]["name"], old_names)
            self.assertEqual(webhook(kind, mode), original, "renderer mutated its input")
            self.assertEqual({"matchLabels": {"kubernetes.io/metadata.name": "nightingale"}},
                             actual["webhooks"][0]["namespaceSelector"])
            self.assertEqual("cnpg-webhook-service", actual["webhooks"][0]["clientConfig"]["service"]["name"])
        role = {"kind": "ClusterRole", "metadata": {"name": "cloudnative-pg-operator"},
                "rules": copy.deepcopy(module["UPSTREAM_CLUSTER_RULES"])}
        scoped = module["scope_operator_role"](role)
        admission = [rule for rule in scoped["rules"] if rule["apiGroups"] == ["admissionregistration.k8s.io"]]
        self.assertEqual(2, len(admission))
        for rule in admission:
            self.assertEqual(["get", "patch"], rule["verbs"])
            self.assertEqual(1, len(rule["resourceNames"]))
            self.assertTrue(rule["resourceNames"][0].endswith("-nightingale"))
            self.assertTrue(old_names.isdisjoint(rule["resourceNames"]))

    def test_suffix_requires_exact_true_and_downward_api_namespace_without_duplicate_env(self):
        module = renderer()
        module["secure_operator"](deployment())
        for change in ("suffix_missing", "suffix_false", "suffix_valuefrom", "duplicate_suffix",
                       "namespace_missing", "namespace_literal", "namespace_other_field", "duplicate_namespace"):
            document = deployment()
            env = document["spec"]["template"]["spec"]["containers"][0]["env"]
            if change == "suffix_missing":
                env.pop(2)
            elif change == "suffix_false":
                env[2]["value"] = "false"
            elif change == "suffix_valuefrom":
                env[2] = {"name": "ENABLE_WEBHOOK_NAMESPACE_SUFFIX", "valueFrom": {
                    "secretKeyRef": {"name": "external-config", "key": "enabled"}}}
            elif change == "duplicate_suffix":
                env.append({"name": "ENABLE_WEBHOOK_NAMESPACE_SUFFIX", "value": "false"})
            elif change == "namespace_missing":
                env.pop(3)
            elif change == "namespace_literal":
                env[3] = {"name": "OPERATOR_NAMESPACE", "value": "observability-alerting"}
            elif change == "namespace_other_field":
                env[3]["valueFrom"]["fieldRef"]["fieldPath"] = "metadata.name"
            else:
                env.append(copy.deepcopy(env[3]))
            with self.subTest(change=change), self.assertRaises(ValueError):
                module["secure_operator"](document)

    def test_postrenderer_refuses_crds_instead_of_forwarding_or_adopting_them(self):
        module = renderer()
        documents = [deployment(), {"kind": "ClusterRole", "metadata": {"name": "cloudnative-pg-operator"},
                     "rules": copy.deepcopy(module["UPSTREAM_CLUSTER_RULES"])},
                     webhook("MutatingWebhookConfiguration", "mutating"),
                     webhook("ValidatingWebhookConfiguration", "validating")]
        # The basic chart shape is valid; adding an existing CRD must alone fail.
        module["transform"](documents)
        documents.append({"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
                          "metadata": {"name": "clusters.postgresql.cnpg.io"}, "spec": {}})
        with self.assertRaisesRegex(ValueError, "CRD"):
            module["transform"](documents)


if __name__ == "__main__":
    unittest.main()
