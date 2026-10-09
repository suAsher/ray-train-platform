"""Deployment-facing SDS ConfigMap and Pod-volume contracts (builder only)."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import sys
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "mtls"))
SPEC = importlib.util.spec_from_file_location("obs_sds_post_render", ROOT / "mtls/sds_post_render.py")
renderer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(renderer)

IMAGE = "harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865"


def config(namespace="nightingale", name="n9e-config-v911", key="envoy.yaml"):
    bootstrap = {"static_resources": {"listeners": [{"filter_chains": [{"transport_socket": {
        "name": "envoy.transport_sockets.tls", "typed_config": {
            "require_client_certificate": True, "common_tls_context": {
                "tls_params": {"tls_minimum_protocol_version": "TLSv1_3"},
                "tls_certificates": [{"certificate_chain": {"filename": "/etc/obs-mtls/tls.crt"}, "private_key": {"filename": "/etc/obs-mtls/tls.key"}}],
                "validation_context": {"trusted_ca": {"filename": "/etc/obs-mtls/ca.crt"}},
            }}}}]}]}}
    return {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"namespace": namespace, "name": name},
            "data": {key: yaml.safe_dump(bootstrap), "unrelated.txt": "retain this exactly"}}


def pod(namespace="nightingale", name="n9e-config-v911", directory="/etc/envoy"):
    return {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"namespace": namespace, "name": "fixture"},
            "spec": {"template": {"spec": {
                "containers": [{"name": "envoy", "image": IMAGE, "args": ["-c", directory + "/envoy.yaml"], "volumeMounts": [
                    {"name": "config", "mountPath": directory, "readOnly": True},
                    {"name": "tls", "mountPath": "/etc/obs-mtls", "readOnly": True}]}],
                "volumes": [{"name": "config", "configMap": {"name": name}},
                            {"name": "tls", "secret": {"secretName": "obs-mtls-nightgale"}}],
            }}}}


def spec(document):
    return document["spec"]["template"]["spec"]


def sds_keys(configmap):
    return {key for key in configmap["data"] if key.startswith("leaf-")}


class SDSPostRenderTests(unittest.TestCase):
    def test_all_known_product_configmaps_have_correct_stable_mount(self):
        expected = {("victoriametrics", "victoriametrics-cluster-" + role + "-mtls"): ("envoy.yaml", "/etc/envoy-source")
                    for role in ("vmstorage", "vminsert", "vmselect", "vmauth", "vmagent", "vmalert", "alertmanager", "operator")}
        expected.update({("clickhouse", "clickhouse-proxy-" + role): ("proxy-" + role + ".yaml", "/etc/envoy-source")
                         for role in ("clickhouse", "keeper", "operator")})
        expected.update({("opentelemetry", "otel-gateway-proxy"): ("proxy-otel.yaml", "/etc/envoy-template"),
                         ("nightingale", "n9e-config-v911"): ("envoy.yaml", "/etc/envoy"),
                         ("nightingale", "cloudnative-pg-operator-mtls"): ("envoy.yaml", "/etc/envoy")})
        self.assertEqual(expected, renderer.MAPPINGS)
        for (namespace, name), (key, directory) in expected.items():
            with self.subTest(name=name):
                result = renderer.transform_documents([config(namespace, name, key)])
                self.assertEqual(1, len(sds_keys(result[0])))
                self.assertIn(directory + "/leaf-", result[0]["data"][key])

    def test_immutable_idempotent_and_preserves_other_data_and_pod_settings(self):
        source = [config(), pod()]
        before = deepcopy(source)
        result = renderer.transform_documents(source)
        self.assertEqual(before, source)
        self.assertEqual(result, renderer.transform_documents(result))
        self.assertEqual(source[1], result[1])
        self.assertEqual("retain this exactly", result[0]["data"]["unrelated.txt"])

    def test_configmap_items_whitelist_receives_generated_keys(self):
        source = [config(), pod()]
        spec(source[1])["volumes"][0]["configMap"]["items"] = [{"key": "envoy.yaml", "path": "envoy.yaml", "mode": 0o440}]
        result = renderer.transform_documents(source)
        items = spec(result[1])["volumes"][0]["configMap"]["items"]
        self.assertEqual({"envoy.yaml", *sds_keys(result[0])}, {item["key"] for item in items})
        self.assertEqual({"key": "envoy.yaml", "path": "envoy.yaml", "mode": 0o440}, items[0])

    def test_operator_cr_pod_templates_are_traversed(self):
        for kind, body in (("VMCluster", {"vmstorage": spec(pod())}),
                           ("ClickHouseInstallation", {"templates": {"podTemplates": [{"spec": spec(pod())}]}})):
            document = {"kind": kind, "metadata": {"namespace": "nightingale", "name": "fixture"}, "spec": body}
            for node in renderer.pod_specs(document):
                node["volumes"][0]["configMap"]["items"] = [{"key": "envoy.yaml", "path": "envoy.yaml"}]
            result = renderer.transform_documents([config(), document])
            self.assertEqual(1, len(list(renderer.pod_specs(result[1]))))
            self.assertEqual(2, len(next(renderer.pod_specs(result[1]))["volumes"][0]["configMap"]["items"]))

    def test_configmap_used_by_non_envoy_init_container_can_mount_elsewhere(self):
        document = pod()
        spec(document)["initContainers"] = [{"name": "render-app-config", "image": "app:fixture", "volumeMounts": [
            {"name": "config", "mountPath": "/config", "readOnly": True}]}]
        self.assertEqual(document, renderer.transform_documents([config(), document])[1])

    def test_explicit_helm_namespace_context_does_not_rewrite_documents(self):
        source = config()
        source["metadata"].pop("namespace")
        result = renderer.transform_documents([source], namespace="nightingale")
        self.assertNotIn("namespace", result[0]["metadata"])
        self.assertTrue(sds_keys(result[0]))
        with self.assertRaises(renderer.RenderError):
            renderer.transform_documents([source])

    def test_foreign_namespace_documents_are_not_modified(self):
        source = [config("monitoring", "legacy-envoy"), pod("monitoring", "legacy-envoy")]
        self.assertEqual(source, renderer.transform_documents(source))

    def test_unknown_tls_configmap_in_product_namespace_fails(self):
        with self.assertRaisesRegex(renderer.RenderError, "unrecognized"):
            renderer.transform_documents([config(name="unexpected-envoy")])

    def test_unrecognized_certificate_path_fails(self):
        source = config()
        source["data"]["envoy.yaml"] = source["data"]["envoy.yaml"].replace("/etc/obs-mtls/tls.crt", "/tmp/tls.crt")
        with self.assertRaises(renderer.RenderError):
            renderer.transform_documents([source])

    def test_conflicting_generated_key_is_rejected(self):
        transformed = renderer.transform_documents([config()])[0]
        key = next(iter(sds_keys(transformed)))
        source = config()
        source["data"][key] = "conflicting owned data"
        with self.assertRaisesRegex(renderer.RenderError, "collision"):
            renderer.transform_documents([source])

    def test_config_and_secret_subpaths_are_rejected(self):
        for index in (0, 1):
            source = pod()
            spec(source)["containers"][0]["volumeMounts"][index]["subPath"] = "single-file"
            with self.subTest(index=index), self.assertRaisesRegex(renderer.RenderError, "subPath"):
                renderer.transform_documents([config(), source])

    def test_wrong_mount_directory_and_non_secret_identity_fail(self):
        source = pod(directory="/temporary-envoy")
        with self.assertRaisesRegex(renderer.RenderError, "mount"):
            renderer.transform_documents([config(), source])
        source = pod()
        spec(source)["volumes"][1] = {"name": "tls", "hostPath": {"path": "/etc/obs-mtls"}}
        with self.assertRaisesRegex(renderer.RenderError, "Secret"):
            renderer.transform_documents([config(), source])

    def test_configmap_can_be_supplied_by_another_release_only_without_items(self):
        self.assertEqual([pod()], renderer.transform_documents([pod()]))
        source = pod()
        spec(source)["volumes"][0]["configMap"]["items"] = [{"key": "envoy.yaml", "path": "envoy.yaml"}]
        with self.assertRaisesRegex(renderer.RenderError, "same input"):
            renderer.transform_documents([source])

    def test_remapped_items_or_unknown_sds_reference_fail(self):
        source = pod()
        spec(source)["volumes"][0]["configMap"]["items"] = [{"key": "envoy.yaml", "path": "renamed.yaml"}]
        with self.assertRaises(renderer.RenderError):
            renderer.transform_documents([config(), source])
        transformed = renderer.transform_documents([config()])[0]
        transformed["data"]["envoy.yaml"] = transformed["data"]["envoy.yaml"].replace("/etc/envoy/leaf-", "/tmp/leaf-")
        with self.assertRaises(renderer.RenderError):
            renderer.transform_documents([transformed])


if __name__ == "__main__":
    unittest.main()
