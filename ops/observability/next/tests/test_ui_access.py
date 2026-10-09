"""Real Helm rendering contracts for the additive browser gateway (builder only)."""

import os
from pathlib import Path
import subprocess
import unittest

import yaml


CHART = Path(__file__).resolve().parents[1] / "ui-access"
SERVER_URI = "spiffe://observability.raytrain.local/nightingale/ui-browser-server"
NODE_IPS = ["172.28.2.65", "172.28.1.232"]


def render(*args, success=True):
    result = subprocess.run(
        [os.environ.get("HELM", "helm"), "template", "observability-ui", str(CHART),
         "--namespace", "nightingale", *args], capture_output=True, text=True, timeout=60,
    )
    if not success:
        return result
    if result.returncode:
        raise AssertionError(result.stderr)
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


def objects(docs, kind):
    return [doc for doc in docs if doc["kind"] == kind]


class UIAccessContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.docs = render()
        cls.pod = objects(cls.docs, "Deployment")[0]["spec"]["template"]["spec"]
        cls.config = objects(cls.docs, "ConfigMap")[0]["data"]
        cls.envoy = yaml.safe_load(cls.config["envoy.yaml"])

    def test_only_new_scoped_objects(self):
        expected = {"Deployment": 1, "Service": 3, "ServiceAccount": 1, "ConfigMap": 1,
                    "PodDisruptionBudget": 1, "Certificate": 1, "CertificateRequestPolicy": 1,
                    "ClusterRole": 1, "ClusterRoleBinding": 1}
        self.assertEqual(expected, {kind: len(objects(self.docs, kind)) for kind in expected})
        self.assertEqual(sum(expected.values()), len(self.docs))
        for doc in self.docs:
            namespace = doc["metadata"].get("namespace")
            if namespace is not None:
                self.assertEqual("monitoring" if doc["metadata"]["name"] == "grafana-ui-backend"
                                 else "nightingale", namespace)
        names = {doc["metadata"]["name"] for doc in self.docs}
        self.assertNotIn("nightingale", names)
        self.assertNotIn("prometheus-grafana", names)

    def test_exactly_two_https_nodeports_and_no_internal_admin(self):
        services = [doc for doc in objects(self.docs, "Service")
                    if doc["metadata"]["namespace"] == "nightingale"]
        self.assertEqual({31700, 31701}, {doc["spec"]["ports"][0]["nodePort"] for doc in services})
        self.assertEqual({8443, 8444}, {doc["spec"]["ports"][0]["targetPort"] for doc in services})
        for doc in services:
            self.assertEqual("NodePort", doc["spec"]["type"])
            self.assertEqual("Cluster", doc["spec"]["externalTrafficPolicy"])
            self.assertEqual("ClientIP", doc["spec"]["sessionAffinity"])
            self.assertEqual(1, len(doc["spec"]["ports"]))
        self.assertNotIn("admin", self.envoy)
        listeners = self.envoy["static_resources"]["listeners"]
        self.assertEqual({8443, 8444}, {item["address"]["socket_address"]["port_value"] for item in listeners})
        for listener in listeners:
            tls = listener["filter_chains"][0]["transport_socket"]["typed_config"]
            self.assertFalse(tls.get("require_client_certificate", False))
            context = tls["common_tls_context"]
            self.assertEqual("TLSv1_2", context["tls_params"]["tls_minimum_protocol_version"])
            self.assertEqual("browser-server", context["tls_certificate_sds_secret_configs"][0]["name"])

    def test_browser_certificate_cannot_be_used_as_mesh_client(self):
        cert = objects(self.docs, "Certificate")[0]["spec"]
        policy = objects(self.docs, "CertificateRequestPolicy")[0]["spec"]
        self.assertEqual("obs-ui-browser-server", cert["secretName"])
        self.assertEqual(NODE_IPS, cert["ipAddresses"])
        self.assertEqual([SERVER_URI], cert["uris"])
        self.assertEqual(["server auth"], cert["usages"])
        self.assertEqual("2160h", cert["duration"])
        self.assertEqual("720h", cert["renewBefore"])
        self.assertEqual("Always", cert["privateKey"]["rotationPolicy"])
        self.assertEqual({"name": "obs-mtls-mesh", "kind": "ClusterIssuer", "group": "cert-manager.io"},
                         cert["issuerRef"])
        self.assertEqual({"required": True, "values": NODE_IPS}, policy["allowed"]["ipAddresses"])
        self.assertEqual({"required": True, "values": [SERVER_URI]}, policy["allowed"]["uris"])
        self.assertEqual(["server auth"], policy["allowed"]["usages"])
        self.assertFalse(policy["allowed"]["isCA"])
        self.assertEqual({"matchNames": ["nightingale"]}, policy["selector"]["namespace"])
        self.assertEqual(cert["issuerRef"], policy["selector"]["issuerRef"])
        self.assertEqual("2160h", policy["constraints"]["maxDuration"])
        self.assertNotIn("commonName", cert)
        self.assertNotIn("dnsNames", cert)

    def test_policy_use_is_only_cert_manager_not_gateway(self):
        role = objects(self.docs, "ClusterRole")[0]
        policy = objects(self.docs, "CertificateRequestPolicy")[0]["metadata"]["name"]
        self.assertEqual([{"apiGroups": ["policy.cert-manager.io"],
                           "resources": ["certificaterequestpolicies"],
                           "resourceNames": [policy], "verbs": ["use"]}], role["rules"])
        binding = objects(self.docs, "ClusterRoleBinding")[0]
        self.assertEqual([{"kind": "ServiceAccount", "name": "cert-manager", "namespace": "cert-manager"}],
                         binding["subjects"])
        self.assertEqual(role["metadata"]["name"], binding["roleRef"]["name"])

    def test_sds_watches_projected_secret_directories_without_subpath(self):
        volumes = {volume["name"]: volume for volume in self.pod["volumes"]}
        self.assertEqual({"obs-ui-browser-server", "obs-mtls-n9e-ui-gateway"},
                         {volume["secret"]["secretName"] for volume in volumes.values() if "secret" in volume})
        for volume in volumes.values():
            self.assertNotIn("persistentVolumeClaim", volume)
            self.assertNotIn("hostPath", volume)
        for mount in self.pod["containers"][0]["volumeMounts"]:
            self.assertNotIn("subPath", mount)
            self.assertTrue(mount["readOnly"])
        for filename, name, directory in (("browser-sds.yaml", "browser-server", "/etc/ui-server"),
                                           ("client-sds.yaml", "n9e-client", "/etc/ui-client")):
            resource = yaml.safe_load(self.config[filename])["resources"][0]
            self.assertEqual(name, resource["name"])
            certificate = resource["tls_certificate"]
            self.assertEqual({"path": directory}, certificate["watched_directory"])
            self.assertEqual(directory + "/tls.crt", certificate["certificate_chain"]["filename"])
            self.assertEqual(directory + "/tls.key", certificate["private_key"]["filename"])
        validation = yaml.safe_load(self.config["validation-sds.yaml"])["resources"][0]["validation_context"]
        self.assertEqual({"path": "/etc/ui-client"}, validation["watched_directory"])
        self.assertEqual([{"san_type": "URI", "matcher": {
            "exact": "spiffe://observability.raytrain.local/nightingale/nightingale"}}],
            validation["match_typed_subject_alt_names"])

    def test_nightingale_upstream_requires_exact_mtls_identity(self):
        clusters = {cluster["name"]: cluster for cluster in self.envoy["static_resources"]["clusters"]}
        self.assertEqual({"nightingale", "grafana"}, set(clusters))
        upstream = clusters["nightingale"]
        address = upstream["load_assignment"]["endpoints"][0]["lb_endpoints"][0]["endpoint"]["address"]["socket_address"]
        self.assertEqual({"address": "nightingale.nightingale.svc", "port_value": 17443}, address)
        tls = upstream["transport_socket"]["typed_config"]["common_tls_context"]
        self.assertEqual({"tls_minimum_protocol_version": "TLSv1_3", "tls_maximum_protocol_version": "TLSv1_3"},
                         tls["tls_params"])
        self.assertEqual("n9e-client", tls["tls_certificate_sds_secret_configs"][0]["name"])
        self.assertEqual("n9e-validation", tls["validation_context_sds_secret_config"]["name"])

    def test_headers_authentication_websocket_and_metrics_boundary(self):
        for listener in self.envoy["static_resources"]["listeners"]:
            manager = listener["filter_chains"][0]["filters"][0]["typed_config"]
            self.assertEqual("REJECT_REQUEST", manager["path_with_escaped_slashes_action"])
            self.assertTrue(manager["normalize_path"])
            self.assertTrue(manager["merge_slashes"])
            self.assertNotIn("access_log", manager)
            self.assertEqual([{"upgrade_type": "websocket"}], manager["upgrade_configs"])
            route = manager["route_config"]
            for header in ("x-forwarded-user", "x-forwarded-email", "x-auth-request-user", "x-webauth-user"):
                self.assertIn(header, route["request_headers_to_remove"])
            self.assertNotIn("authorization", route["request_headers_to_remove"])
            self.assertNotIn("cookie", route["request_headers_to_remove"])
            self.assertEqual([{"header": {"key": "x-forwarded-proto", "value": "https"},
                               "append_action": "OVERWRITE_IF_EXISTS_OR_ADD"}], route["request_headers_to_add"])
            routes = route["virtual_hosts"][0]["routes"]
            self.assertEqual({"path_separated_prefix": "/metrics"}, routes[0]["match"])
            self.assertEqual({"status": 404}, routes[0]["direct_response"])
            self.assertEqual({"prefix": "/"}, routes[-1]["match"])
            self.assertEqual(2, len(routes))

    def test_grafana_stickiness_does_not_modify_existing_deployment(self):
        service = next(doc for doc in objects(self.docs, "Service")
                       if doc["metadata"]["namespace"] == "monitoring")
        self.assertEqual("grafana-ui-backend", service["metadata"]["name"])
        self.assertEqual("ClusterIP", service["spec"]["type"])
        self.assertEqual("ClientIP", service["spec"]["sessionAffinity"])
        self.assertEqual(10800, service["spec"]["sessionAffinityConfig"]["clientIP"]["timeoutSeconds"])
        self.assertEqual({"app.kubernetes.io/name": "grafana", "app.kubernetes.io/instance": "prometheus"},
                         service["spec"]["selector"])
        self.assertEqual("grafana", service["spec"]["ports"][0]["targetPort"])
        grafana = next(cluster for cluster in self.envoy["static_resources"]["clusters"] if cluster["name"] == "grafana")
        self.assertNotIn("transport_socket", grafana)
        address = grafana["load_assignment"]["endpoints"][0]["lb_endpoints"][0]["endpoint"]["address"]["socket_address"]
        self.assertEqual({"address": "grafana-ui-backend.monitoring.svc", "port_value": 80}, address)

    def test_runtime_has_no_api_permissions_no_gpu_and_bounded_resources(self):
        deployment = objects(self.docs, "Deployment")[0]
        self.assertEqual(2, deployment["spec"]["replicas"])
        self.assertFalse(self.pod["automountServiceAccountToken"])
        self.assertFalse(objects(self.docs, "ServiceAccount")[0]["automountServiceAccountToken"])
        self.assertEqual("raytrain-observability-additive", self.pod["priorityClassName"])
        self.assertTrue(self.pod["securityContext"]["runAsNonRoot"])
        self.assertEqual("RuntimeDefault", self.pod["securityContext"]["seccompProfile"]["type"])
        self.assertEqual(1, len(self.pod["containers"]))
        container = self.pod["containers"][0]
        self.assertRegex(container["image"], r"^harbor\.wellspiking\.ai/hub/envoyproxy/envoy:v1\.39\.3@sha256:[a-f0-9]{64}$")
        self.assertEqual({"requests": {"cpu": "50m", "memory": "64Mi"},
                          "limits": {"cpu": "300m", "memory": "128Mi"}}, container["resources"])
        self.assertFalse(container["securityContext"]["allowPrivilegeEscalation"])
        self.assertTrue(container["securityContext"]["readOnlyRootFilesystem"])
        self.assertEqual(["ALL"], container["securityContext"]["capabilities"]["drop"])
        self.assertEqual(1, objects(self.docs, "PodDisruptionBudget")[0]["spec"]["minAvailable"])
        affinity = self.pod["affinity"]
        self.assertTrue(affinity["podAntiAffinity"]["preferredDuringSchedulingIgnoredDuringExecution"])
        expression = affinity["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]["nodeSelectorTerms"][0]["matchExpressions"][0]
        self.assertEqual({"key": "kubernetes.io/hostname", "operator": "NotIn",
                          "values": ["172.28.1.222", "172.28.3.32"]}, expression)
        self.assertNotIn("tolerations", self.pod)

    def test_clusterip_transition_removes_nodeport_fields(self):
        for service in objects(render("--set", "service.type=ClusterIP"), "Service"):
            self.assertEqual("ClusterIP", service["spec"]["type"])
            self.assertNotIn("externalTrafficPolicy", service["spec"])
            for port in service["spec"]["ports"]:
                self.assertNotIn("nodePort", port)

    def test_rejects_unsafe_overrides(self):
        for args in (("--namespace", "default"), ("--set", "replicas=1"),
                     ("--set", "service.type=LoadBalancer"), ("--set", "service.grafanaNodePort=31700"),
                     ("--set", "service.nightingaleNodePort=443"),
                     ("--set", "certificate.ipAddresses[0]=0.0.0.0"),
                     ("--set", "image=envoyproxy/envoy:latest")):
            with self.subTest(args=args):
                self.assertNotEqual(0, render(*args, success=False).returncode)


if __name__ == "__main__":
    unittest.main()
