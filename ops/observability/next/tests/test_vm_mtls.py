"""VM mTLS safety contracts against rendered resources; run on the builder only.

These checks do not establish protocol compatibility or replace the required
three-storage / three-Alertmanager live negative tests. HELM may select the
builder's installed Helm binary. No cluster or registry is contacted.
"""

from copy import deepcopy
import os
from pathlib import Path
import subprocess
import sys
import unittest

import yaml


VM_ROOT = Path(__file__).resolve().parents[1] / "victoriametrics"
sys.path.insert(0, str(VM_ROOT))
from validate_component_contract import validate_scraper_rbac  # noqa: E402

NS = "observability-metrics"
IDENTITY = "spiffe://observability.raytrain.local/"
ENVOY_DIGEST = "sha256:dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865"
ROLES = ("vmstorage", "vmselect", "vminsert", "vmauth", "vmagent", "vmalert", "alertmanager", "operator")
HTTP_PORTS = {"vmstorage": 8482, "vmselect": 8481, "vminsert": 8480,
              "vmauth": 8427, "vmagent": 8429, "vmalert": 8880, "alertmanager": 9093}


def walk(value):
    yield value
    if isinstance(value, dict):
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


def render(*extra):
    result = subprocess.run(
        [os.environ.get("HELM", "helm"), "template", "raytrain-next",
         str(VM_ROOT / "resources"), "--namespace", NS, *extra],
        text=True, capture_output=True, check=False, timeout=60,
    )
    if result.returncode:
        raise AssertionError(f"Helm rendering failed: {result.stderr}")
    return [item for item in yaml.safe_load_all(result.stdout) if item]


class VMMTLSContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.objects = render()
        cls.operator = yaml.safe_load((VM_ROOT / "operator-values.yaml").read_text())

    def one(self, kind, name=None):
        matches = [item for item in self.objects if item["kind"] == kind
                   and (name is None or item["metadata"]["name"] == name)]
        self.assertEqual(1, len(matches), f"expected {kind}/{name or '*'}")
        return matches[0]

    def spec(self, role):
        if role in ("vmstorage", "vmselect", "vminsert"):
            return self.one("VMCluster")["spec"][role]
        return self.one({"vmauth": "VMAuth", "vmagent": "VMAgent", "vmalert": "VMAlert",
                         "alertmanager": "VMAlertmanager"}[role])["spec"]

    def config(self, role):
        cm = self.one("ConfigMap", f"raytrain-next-{role}-mtls")
        return yaml.safe_load(cm["data"]["envoy.yaml"])

    def listeners(self, role):
        return self.config(role)["static_resources"]["listeners"]

    def listener(self, role, port):
        matches = [item for item in self.listeners(role)
                   if item["address"]["socket_address"]["port_value"] == port]
        self.assertEqual(1, len(matches), f"{role} listener {port}")
        return matches[0]

    def server_tls(self, role, port):
        listener = self.listener(role, port)
        chains = listener.get("filter_chains", [])
        self.assertTrue(chains, f"{role}:{port} has no TLS filter chain")
        contexts = [chain.get("transport_socket", {}).get("typed_config", {}) for chain in chains]
        self.assertTrue(all(item.get("require_client_certificate") is True for item in contexts),
                        f"{role}:{port} accepts anonymous clients")
        return contexts

    def exact_sans(self, tls_context):
        context = tls_context["common_tls_context"]["validation_context"]
        self.assertEqual("/etc/obs-mtls/ca.crt", context["trusted_ca"]["filename"])
        matchers = context["match_typed_subject_alt_names"]
        self.assertTrue(matchers, "CA trust without role authorization")
        self.assertTrue(all(item.get("san_type") == "URI" and set(item.get("matcher", {})) == {"exact"}
                            for item in matchers), "wildcard or non-URI Envoy role matcher")
        return {item["matcher"]["exact"] for item in matchers}

    def test_all_native_http_servers_are_loopback_only(self):
        for role in HTTP_PORTS:
            with self.subTest(role=role):
                args = self.spec(role).get("extraArgs", {})
                flag = "web.listen-address" if role == "alertmanager" else "httpListenAddr"
                address = args.get(flag, "")
                self.assertTrue(address.startswith("127.0.0.1:"), f"{role} native HTTP remains exposed")
                self.assertNotIn(",", address, "additional wildcard listener")
        self.assertTrue(self.spec("vmauth")["extraArgs"].get("httpInternalListenAddr", "").startswith("127.0.0.1:"))

    def test_storage_rpc_native_ports_cannot_bypass_proxy(self):
        args = self.spec("vmstorage").get("extraArgs", {})
        self.assertEqual("127.0.0.1:28400", args.get("vminsertAddr"))
        self.assertEqual("127.0.0.1:28401", args.get("vmselectAddr"))
        for port, allowed in ((8400, "vminsert"), (8401, "vmselect")):
            for tls in self.server_tls("vmstorage", port):
                self.assertEqual({f"{IDENTITY}{NS}/{allowed}"}, self.exact_sans(tls))

    def test_storage_shards_are_three_distinct_local_egress_routes(self):
        for role, base, remote in (("vminsert", 18400, 8400), ("vmselect", 19400, 8401)):
            with self.subTest(role=role):
                args = self.spec(role).get("extraArgs", {})
                self.assertEqual(",".join(f"127.0.0.1:{base + index}" for index in range(3)), args.get("storageNode"))
                clusters = self.config(role)["static_resources"]["clusters"]
                destinations = set()
                for index in range(3):
                    listener = self.listener(role, base + index)
                    self.assertEqual("127.0.0.1", listener["address"]["socket_address"]["address"])
                    proxy = listener["filter_chains"][0]["filters"][0]["typed_config"]
                    cluster = next(item for item in clusters if item["name"] == proxy["cluster"])
                    tls = cluster["transport_socket"]["typed_config"]
                    self.assertEqual({f"{IDENTITY}{NS}/vmstorage"}, self.exact_sans(tls))
                    endpoint = cluster["load_assignment"]["endpoints"][0]["lb_endpoints"][0]["endpoint"]["address"]["socket_address"]
                    destinations.add((endpoint["address"], endpoint["port_value"]))
                self.assertEqual({(f"vmstorage-raytrain-next-{i}.vmstorage-raytrain-next.{NS}.svc", remote)
                                  for i in range(3)}, destinations)

    def test_storage_replica_override_rebuilds_routes_without_plaintext_fallback(self):
        objects = render("--set", "storage.replicas=4")
        cluster = next(item for item in objects if item["kind"] == "VMCluster")["spec"]
        for role, base in (("vminsert", 18400), ("vmselect", 19400)):
            self.assertEqual(",".join(f"127.0.0.1:{base + i}" for i in range(4)),
                             cluster[role].get("extraArgs", {}).get("storageNode"))

    def test_optional_select_cache_peer_plaintext_route_is_explicitly_disabled(self):
        self.assertEqual("", self.spec("vmselect").get("extraArgs", {}).get("selectNode"))

    def test_every_externally_reachable_listener_has_client_identity_gate(self):
        for role in ROLES:
            for listener in self.listeners(role):
                socket = listener["address"]["socket_address"]
                if socket["address"] == "127.0.0.1" or socket["port_value"] == 18081:
                    continue
                with self.subTest(role=role, port=socket["port_value"]):
                    self.assertEqual("__POD_IP__", socket["address"])
                    for context in self.server_tls(role, socket["port_value"]):
                        self.exact_sans(context)

    def test_tls13_has_explicit_compatible_minimum_and_maximum(self):
        # Envoy's upstream maximum defaults to TLS1.2 even though downstream's
        # defaults to TLS1.3. Merely setting minimum=TLS1.3 validates statically
        # but fails at handshake with NO_SUPPORTED_VERSIONS_ENABLED.
        for role in ROLES:
            contexts = [node["common_tls_context"] for node in walk(self.config(role))
                        if isinstance(node, dict) and "common_tls_context" in node]
            self.assertTrue(contexts)
            for context in contexts:
                self.assertEqual({"tls_minimum_protocol_version": "TLSv1_3",
                                  "tls_maximum_protocol_version": "TLSv1_3"}, context["tls_params"])

    def test_http_role_gate_is_not_only_a_shared_ca_check(self):
        for role, port in HTTP_PORTS.items():
            listener = self.listener(role, port)
            filters = [node for node in walk(listener) if isinstance(node, dict)
                       and node.get("name") == "envoy.filters.http.rbac"]
            self.assertTrue(filters, f"{role} HTTP needs method/path role authorization")
            for item in filters:
                rules = item["typed_config"]["rules"]
                self.assertEqual("ALLOW", rules["action"])
                self.assertTrue(rules["policies"])
                self.assertFalse(any(node.get("any") is True for node in walk(rules) if isinstance(node, dict)),
                                 f"{role} uses wildcard permission/principal")

    def test_public_health_has_backend_gating_and_no_management_body(self):
        for role in ROLES:
            listener = self.listener(role, 18081)
            filters = [node for node in walk(listener) if isinstance(node, dict)
                       and node.get("name") == "envoy.filters.http.health_check"]
            self.assertGreaterEqual(len(filters), 2, f"{role} readiness and app health need backend checks")
            for item in filters:
                health = item["typed_config"]
                self.assertIs(health["pass_through_mode"], False)
                self.assertTrue(health.get("cluster_min_healthy_percentages"))
            for node in walk(listener):
                if isinstance(node, dict):
                    self.assertNotIn("route", node, "public health proxies a backend endpoint")
                    self.assertNotIn("body", node, "public health discloses a response body")

    def test_envoy_admin_is_not_pod_reachable(self):
        for role in ROLES:
            admin = self.config(role).get("admin", {})
            address = admin.get("address", {})
            self.assertTrue(not admin or "pipe" in address or
                            address.get("socket_address", {}).get("address") == "127.0.0.1")

    def test_sidecars_are_unprivileged_bounded_and_use_fixed_image(self):
        for role in HTTP_PORTS:
            spec = self.spec(role)
            sidecars = [item for item in spec.get("containers", []) if item["name"] == "mtls-proxy"]
            self.assertEqual(1, len(sidecars), f"{role} missing proxy")
            sidecar = sidecars[0]
            self.assertTrue(sidecar["image"].endswith("@" + ENVOY_DIGEST))
            security = sidecar["securityContext"]
            self.assertIs(security["allowPrivilegeEscalation"], False)
            self.assertIs(security["readOnlyRootFilesystem"], True)
            self.assertIs(security["runAsNonRoot"], True)
            self.assertEqual(["ALL"], security["capabilities"]["drop"])
            self.assertFalse(security["capabilities"].get("add"))
            for bound in ("requests", "limits"):
                self.assertTrue(all(key in sidecar["resources"][bound] for key in ("cpu", "memory")))
            self.assertIn("--concurrency 1", " ".join(sidecar["args"]))
            self.assertEqual("/livez", sidecar["livenessProbe"]["httpGet"]["path"])
            volumes = {item["name"]: item for item in spec["volumes"]}
            identity_volumes = [item for item in volumes.values() if item.get("secret", {}).get("secretName") == f"obs-mtls-{role}"]
            self.assertEqual(1, len(identity_volumes))
            self.assertEqual(0o440, identity_volumes[0]["secret"]["defaultMode"])
            self.assertEqual(65534, spec["securityContext"]["fsGroup"])
            self.assertFalse(any("hostPath" in item for item in volumes.values()))

    def test_reloader_http_is_loopback_and_reload_url_stays_inside_pod(self):
        for role in ("vmagent", "vmauth", "vmalert", "alertmanager"):
            spec = self.spec(role)
            args = spec.get("configReloaderExtraArgs", {})
            self.assertEqual("127.0.0.1:8435", args.get("http.listenAddr"))
            self.assertTrue(args.get("reload-url", "").startswith("http://127.0.0.1:"))
            reloader = next((item for item in spec.get("containers", []) if item["name"] == "config-reloader"), {})
            self.assertTrue(reloader.get("readinessProbe"), "default PodIP:8435 probe is invalid after loopback bind")

    def test_scraper_can_read_only_its_generated_config_secret(self):
        validate_scraper_rbac(self.objects)
        role = self.one("Role", "vm-self-scraper")
        rules = [item for item in role["rules"] if "secrets" in item["resources"]]
        self.assertEqual(1, len(rules))
        self.assertEqual(["vmagent-raytrain-next"], rules[0]["resourceNames"])
        self.assertFalse(any("configmaps" in item["resources"] for item in role["rules"]))
        self.assertEqual("vm-self-scraper", self.spec("vmagent")["serviceAccountName"])

    def test_scraper_rbac_rejects_unscoped_secrets_or_extra_bindings(self):
        broad = deepcopy(self.objects)
        role = next(item for item in broad if item.get("kind") == "Role" and item["metadata"]["name"] == "vm-self-scraper")
        secret_rule = next(item for item in role["rules"] if "secrets" in item["resources"])
        secret_rule.pop("resourceNames")
        with self.assertRaisesRegex(ValueError, "scraper RBAC"):
            validate_scraper_rbac(broad)
        for replacement in (["obs-mtls-admin"], ["vmagent-raytrain-next", "obs-mtls-vmauth"]):
            broadened = deepcopy(self.objects)
            role = next(item for item in broadened if item.get("kind") == "Role" and item["metadata"]["name"] == "vm-self-scraper")
            next(item for item in role["rules"] if "secrets" in item["resources"])["resourceNames"] = replacement
            with self.assertRaisesRegex(ValueError, "scraper RBAC"):
                validate_scraper_rbac(broadened)
        binding = deepcopy(self.one("RoleBinding", "vm-self-scraper"))
        binding["metadata"]["name"] = "unapproved-extra-grant"
        binding["roleRef"] = {"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"}
        with self.assertRaisesRegex(ValueError, "scraper RBAC"):
            validate_scraper_rbac([*self.objects, binding])

    def test_self_scrapes_use_per_role_https_identity_without_expanding_selectors(self):
        scrapes = [item["spec"] for item in self.objects if item["kind"] == "VMServiceScrape"]
        self.assertEqual(8, len(scrapes))
        expected = set(ROLES)
        actual = set()
        for spec in scrapes:
            self.assertEqual([NS], spec["namespaceSelector"]["matchNames"])
            for endpoint in spec["endpoints"]:
                self.assertEqual("https", endpoint.get("scheme"))
                self.assertEqual("/metrics", endpoint["path"])
                tls = endpoint["tlsConfig"]
                self.assertIs(tls.get("insecureSkipVerify", False), False)
                self.assertEqual("obs-mtls-vmagent", tls["ca"]["secret"]["name"])
                self.assertEqual("obs-mtls-vmagent", tls["cert"]["secret"]["name"])
                self.assertEqual("obs-mtls-vmagent", tls["keySecret"]["name"])
                actual.add(tls["serverName"].removesuffix(f".{NS}.svc"))
        self.assertEqual(expected, actual)

    def test_agent_and_alert_use_native_tls_and_preserve_tokens(self):
        agent = self.spec("vmagent")["remoteWrite"][0]
        alert = self.spec("vmalert")
        for role, target, token in (("vmagent", agent, "vm-self-write"),
                                    ("vmalert", alert["datasource"], "vm-self-read"),
                                    ("vmalert", alert["remoteRead"], "vm-self-read"),
                                    ("vmalert", alert["remoteWrite"], "vm-self-write")):
            self.assertTrue(target["url"].startswith("https://vmauth-"))
            self.assertEqual(token, target["bearerTokenSecret"]["name"])
            tls = target["tlsConfig"]
            self.assertEqual(f"vmauth.{NS}.svc", tls["serverName"])
            self.assertEqual(f"obs-mtls-{role}", tls["keySecret"]["name"])
            self.assertIs(tls.get("insecureSkipVerify", False), False)

    def test_vmauth_fixed_tenant_routes_use_only_local_egress(self):
        for name, port, action in (("self-write", 18480, "insert"), ("self-read", 18481, "select")):
            user = self.one("VMUser", name)["spec"]
            self.assertEqual({"name": f"vm-{name}", "key": "token"}, user["tokenRef"])
            self.assertEqual(f"http://127.0.0.1:{port}/{action}/1001/prometheus", user["targetRefs"][0]["static"]["url"])
        self.assertNotIn("unauthorizedUserAccessSpec", self.spec("vmauth"))

    def test_am_gossip_uses_separate_ca_and_client_certificate(self):
        am = self.spec("alertmanager")
        gossip = am.get("gossipConfig", {})
        server = gossip.get("tls_server_config", {})
        client = gossip.get("tls_client_config", {})
        self.assertEqual("RequireAndVerifyClientCert", server.get("client_auth_type"))
        self.assertEqual("obs-mtls-alertmanager-gossip", server.get("client_ca_secret_ref", {}).get("name"))
        self.assertEqual("obs-mtls-alertmanager-gossip", client.get("ca_secret_ref", {}).get("name"))
        self.assertEqual("obs-mtls-alertmanager-gossip", client.get("cert_secret_ref", {}).get("name"))
        self.assertEqual(f"alertmanager.{NS}.svc", client.get("server_name"))
        self.assertIs(client.get("insecure_skip_verify"), False)
        self.assertEqual(3, am["replicaCount"])

    def test_local_only_am_preserves_service_named_target_port(self):
        am = self.spec("alertmanager")
        self.assertIs(am.get("listenLocal"), True)
        ports = [port for container in am.get("containers", []) for port in container.get("ports", [])]
        self.assertIn({"name": "web", "containerPort": 9093, "protocol": "TCP"}, ports,
                      "Operator removes AM web containerPort in listenLocal mode")

    def test_notifier_is_authenticated_https(self):
        notifiers = self.spec("vmalert")["notifiers"]
        self.assertEqual(3, len(notifiers), "each AM replica needs an explicit authenticated notifier")
        for notifier in notifiers:
            self.assertTrue(notifier.get("url", "").startswith("https://vmalertmanager-"))
            self.assertNotIn("selector", notifier, "Operator selector discovery discards per-notifier TLS overrides")
            self.assertEqual(f"alertmanager.{NS}.svc", notifier["tlsConfig"]["serverName"])
            self.assertEqual("obs-mtls-vmalert", notifier["tlsConfig"]["keySecret"]["name"])

    def test_operator_metrics_and_health_do_not_open_a_side_channel(self):
        args = self.operator["extraArgs"]
        self.assertTrue(args.get("metrics-bind-address", "").startswith("127.0.0.1:"))
        self.assertTrue(args.get("health-probe-bind-address", "").startswith("127.0.0.1:"))
        self.assertEqual(1, len([item for item in self.operator.get("extraContainers", []) if item["name"] == "mtls-proxy"]))


if __name__ == "__main__":
    unittest.main()
