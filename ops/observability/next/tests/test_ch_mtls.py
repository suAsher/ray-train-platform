"""CH/Keeper release gates. Run on the builder; no Kubernetes mutations."""

import copy
import importlib.util
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

import yaml


ROOT = Path(__file__).resolve().parents[1]
CH = ROOT / "clickhouse"
TRUST = "spiffe://observability.raytrain.local/"
ENVOY = ("harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:"
         "dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865")


def documents(path):
    return list(yaml.safe_load_all(path.read_text()))


def installation(path):
    return documents(path)[0]


def proxy(name):
    return yaml.safe_load((CH / ("proxy-" + name + ".yaml")).read_text())


def public_listeners(config):
    return {item["address"]["socket_address"]["port_value"]: item
            for item in config["static_resources"]["listeners"]
            if item["address"]["socket_address"]["address"] == "__POD_IP__"}


def identities(listener):
    """A trusted CA alone is deliberately insufficient for this release gate."""
    chains = listener["filter_chains"]
    if len(chains) != 1:
        raise AssertionError("one explicit authenticated filter chain required")
    tls = chains[0]["transport_socket"]["typed_config"]
    if tls.get("require_client_certificate") is not True:
        raise AssertionError("client certificate must be required")
    context = tls["common_tls_context"]["validation_context"]
    if context.get("trust_chain_verification", "VERIFY_TRUST_CHAIN") != "VERIFY_TRUST_CHAIN":
        raise AssertionError("certificate chain verification is required")
    if not context.get("trusted_ca", {}).get("filename"):
        raise AssertionError("explicit trust bundle required")
    result = set()
    for match in context.get("match_typed_subject_alt_names", []):
        if match.get("san_type") != "URI" or set(match["matcher"]) != {"exact"}:
            raise AssertionError("only exact URI identities are allowed")
        result.add(match["matcher"]["exact"])
    if not result:
        raise AssertionError("CA membership is not role authorization")
    return result


def role(namespace, name):
    component_names = {"analytics": "clickhouse", "metrics": "victoriametrics",
                       "otel": "opentelemetry", "alerting": "nightingale"}
    return TRUST + component_names[namespace] + "/" + name


def pod_template(cr):
    return cr["spec"]["templates"]["podTemplates"][0]["spec"]


class ClickHouseMTLSTests(unittest.TestCase):
    def test_altinity_owns_the_single_disruption_budget_per_cluster(self):
        # The pinned operator generates maxUnavailable:1 for each CH/CHK
        # cluster. Handwritten overlapping PDBs cause eviction ambiguity.
        for name in ("clickhouse", "keeper"):
            with self.subTest(component=name):
                self.assertFalse(any(doc and doc.get("kind") == "PodDisruptionBudget"
                                     for doc in documents(CH / (name + ".yaml"))))

    def test_operator_namespace_watch_is_one_literal_not_a_regexp(self):
        values = yaml.safe_load((CH / "operator-values.yaml").read_text())
        # Altinity 0.27.4 GetInformerNamespace/GetCacheNamespaces fall back
        # to cluster-wide LISTs for regexp patterns, even anchored ones.
        self.assertEqual(["clickhouse"], values["watchNamespaces"])
        self.assertTrue(values["rbac"]["namespaceScoped"])
        for key in ("prometheus.io/scrape", "clickhouse-operator-metrics/scrape"):
            self.assertEqual("false", values["podAnnotations"][key])

    def test_health_only_clusters_probe_even_without_routed_traffic(self):
        for name in ("clickhouse", "keeper", "operator"):
            for cluster in proxy(name)["static_resources"]["clusters"]:
                for health in cluster.get("health_checks", []):
                    with self.subTest(component=name, cluster=cluster["name"]):
                        self.assertEqual("5s", health["interval"])
                        self.assertEqual("5s", health["no_traffic_interval"])

    def test_https_replication_advertises_each_existing_certificate_name(self):
        chi = installation(CH / "clickhouse.yaml")
        replicas = chi["spec"]["configuration"]["clusters"][0]["layout"]["replicas"]
        self.assertEqual(["0", "1"], [entry["name"] for entry in replicas])
        for index, entry in enumerate(replicas):
            self.assertEqual(
                {"interserver_https_host":
                 f"chi-clickhouse-telemetry-0-{index}.clickhouse.svc.cluster.local"},
                entry["settings"])

    def test_readonly_sql_probe_does_not_try_to_change_session_settings(self):
        spec = importlib.util.spec_from_file_location("ch_runtime_support", CH / "runtime_support.py")
        runtime = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runtime)
        self.assertEqual([], runtime.sql_timeout_args("query-admin"))
        self.assertEqual([], runtime.sql_timeout_args("otel-gateway"))
        self.assertIn("--distributed_ddl_task_timeout", runtime.sql_timeout_args("schema-admin"))

    def test_trial_thread_budget_leaves_capacity_for_queries(self):
        settings = installation(CH / "clickhouse.yaml")["spec"]["configuration"]["settings"]
        # These pools otherwise default to 16 each. Their persistent workers
        # exhausted the global 128 slots before the first canary SELECT.
        for name in ("background_buffer_flush_schedule_pool_size",
                     "background_message_broker_schedule_pool_size",
                     "background_distributed_schedule_pool_size",
                     "background_streaming_schedule_pool_size",
                     "background_common_pool_size", "async_insert_threads"):
            with self.subTest(pool=name):
                self.assertGreaterEqual(settings[name], 1)
                self.assertLessEqual(settings[name], 2)
        self.assertEqual(128, settings["max_thread_pool_size"])
        self.assertEqual(settings["max_thread_pool_size"], settings["thread_pool_queue_size"])
        self.assertLess(settings["max_thread_pool_free_size"], settings["max_thread_pool_size"])

    def test_native_listeners_are_replaced_with_loopback_not_just_hidden_services(self):
        for name in ("clickhouse", "keeper"):
            with self.subTest(component=name):
                cr = installation(CH / (name + ".yaml"))
                files = cr["spec"]["configuration"]["files"]
                xml = ET.fromstring(files["config.d/zz-mtls.xml"] if name == "clickhouse"
                                    else files["zz-mtls.xml"])
                for field in ("listen_host", "interserver_listen_host"):
                    values = xml.findall(field)
                    self.assertEqual(1, len(values), field)
                    self.assertEqual("127.0.0.1", values[0].text)
                    self.assertIn(values[0].get("replace"), ("replace", "1"))

    def test_keeper_raft_and_client_have_distinct_identity_allowlists(self):
        public = public_listeners(proxy("keeper"))
        self.assertEqual({2281, 9234, 7000, 18080}, set(public))
        self.assertEqual({role("analytics", "keeper")}, identities(public[9234]))
        self.assertEqual({role("analytics", "clickhouse"), role("analytics", "ch-operator")},
                         identities(public[2281]))
        self.assertEqual({role("metrics", "vmagent")}, identities(public[7000]))
        self.assertNotIn(role("analytics", "clickhouse"), identities(public[9234]))
        # NuRaft's strict native TLS context has no initialized session-ID
        # context. Use a fresh authenticated TLS handshake on each local hop.
        for cluster in proxy("keeper")["static_resources"]["clusters"]:
            if cluster["name"] in ("native-client", "native-raft"):
                tls = cluster["transport_socket"]["typed_config"]
                self.assertEqual(0, tls["max_session_keys"])
                self.assertTrue(tls["common_tls_context"]["tls_certificates"])
                self.assertTrue(tls["common_tls_context"]["validation_context"]["trusted_ca"])

    def test_same_ca_wildcards_and_missing_client_cert_are_rejected(self):
        listener = public_listeners(proxy("keeper"))[9234]
        for change in ("no-cert", "no-role", "wildcard", "untrusted-chain"):
            bad = copy.deepcopy(listener)
            tls = bad["filter_chains"][0]["transport_socket"]["typed_config"]
            validation = tls["common_tls_context"]["validation_context"]
            if change == "no-cert":
                tls["require_client_certificate"] = False
            elif change == "no-role":
                validation["match_typed_subject_alt_names"] = []
            elif change == "wildcard":
                validation["match_typed_subject_alt_names"][0]["matcher"] = {"prefix": TRUST}
            else:
                validation["trust_chain_verification"] = "ACCEPT_UNTRUSTED"
            with self.subTest(change=change), self.assertRaises(AssertionError):
                identities(bad)

    def test_sql_admin_replication_and_metrics_are_separate_channels(self):
        public = public_listeners(proxy("clickhouse"))
        self.assertEqual({8443, 9440, 9010, 9363, 18080}, set(public))
        self.assertEqual({role("analytics", "ch-operator"), role("analytics", "schema-admin")},
                         identities(public[8443]))
        self.assertEqual({role("analytics", "clickhouse")}, identities(public[9010]))
        self.assertEqual({role("metrics", "vmagent")}, identities(public[9363]))
        self.assertEqual({role("otel", "otel-gateway"), role("analytics", "clickhouse"),
                          role("analytics", "schema-admin"), role("analytics", "query-admin"),
                          "spiffe://observability.raytrain.local/grafana/grafana-query"},
                         identities(public[9440]))

    def test_sql_application_permissions_remain_minimal(self):
        users = installation(CH / "clickhouse.yaml")["spec"]["configuration"]["users"]
        self.assertEqual({"GRANT INSERT, SHOW COLUMNS ON observability_canary.otel_logs",
                          "GRANT INSERT, SHOW COLUMNS ON observability_canary.otel_traces"},
                         set(users["otel_ingest/grants/query"]))
        self.assertEqual(["GRANT SELECT ON observability_canary.*"],
                         users["observability_query/grants/query"])
        self.assertIn("secretKeyRef", users["schema_admin/password_sha256_hex"]["valueFrom"])
        files = installation(CH / "clickhouse.yaml")["spec"]["configuration"]["files"]
        override = ET.fromstring(files["users.d/zz-mtls-operator.xml"])
        operator = override.find("users/clickhouse_operator")
        self.assertEqual(["networks"], [child.tag for child in operator])
        self.assertEqual("127.0.0.1", operator.find("networks/ip").text)
        self.assertEqual("replace", operator.find("networks").get("replace"))
        config = ET.fromstring(files["config.d/zz-mtls.xml"])
        # ON CLUSTER identifies the current host by a configured native port;
        # deleting :9440 while advertising secure replicas strands DDL tasks.
        self.assertEqual("9440", config.findtext("tcp_port_secure"))
        self.assertIsNone(config.find("tcp_port_secure").get("remove"))
        self.assertEqual("127.0.0.1", config.findtext("listen_host"))
        self.assertEqual("strict", config.findtext("openSSL/server/verificationMode"))

    def test_coordination_outbound_is_fixed_loopback_and_raft_dns_is_preserved(self):
        cr = installation(CH / "clickhouse.yaml")
        zk = cr["spec"]["configuration"]["zookeeper"]
        self.assertNotIn("keeper", zk)
        self.assertEqual([("127.0.0.1", port) for port in (12181, 12182, 12183)],
                         [(node["host"], node["port"]) for node in zk["nodes"]])
        for component in ("clickhouse", "operator"):
            config = proxy(component)
            listeners = config["static_resources"]["listeners"]
            egress = [item for item in listeners
                      if item["address"]["socket_address"]["port_value"] in (12181, 12182, 12183)]
            self.assertEqual(3, len(egress))
            self.assertTrue(all(item["address"]["socket_address"]["address"] == "127.0.0.1"
                                for item in egress))
        keeper = installation(CH / "keeper.yaml")
        cluster = keeper["spec"]["configuration"]["clusters"][0]
        self.assertEqual("yes", cluster["secure"])
        self.assertEqual("no", cluster["insecure"])
        self.assertNotIn("raft_configuration", keeper["spec"]["configuration"]["settings"])
        # CH 26.8 reads a cluster-wide flag. Altinity's generated per-server
        # <secure> is insufficient, so the final overlay must add this flag.
        xml = ET.fromstring(keeper["spec"]["configuration"]["files"]["zz-mtls.xml"])
        raft = xml.find("keeper_server/raft_configuration")
        self.assertEqual("true", raft.findtext("secure"))
        self.assertFalse(raft.findall("server"), "operator retains ownership of member DNS/IDs")

    def test_proxies_have_no_wildcard_bind_or_public_admin(self):
        for name in ("clickhouse", "keeper", "operator"):
            config = proxy(name)
            if "admin" in config:
                self.assertEqual("127.0.0.1", config["admin"]["address"]["socket_address"]["address"])
            for item in config["static_resources"]["listeners"]:
                address = item["address"]["socket_address"]["address"]
                self.assertIn(address, ("__POD_IP__", "127.0.0.1", "127.0.0.2", "127.0.0.3"))

    def test_ch_and_keeper_probes_do_not_require_unauthenticated_pod_ip_ports(self):
        for name in ("clickhouse", "keeper"):
            pod = pod_template(installation(CH / (name + ".yaml")))
            app = next(c for c in pod["containers"] if c["name"] in ("clickhouse", "clickhouse-keeper"))
            for field in ("startupProbe", "readinessProbe", "livenessProbe"):
                probe = app[field]
                self.assertIn("exec", probe, name + "/" + field)
                command = " ".join(probe["exec"]["command"])
                self.assertTrue("127.0.0.1" in command or "pgrep" in command)
            sidecar = next(c for c in pod["containers"] if c["name"] == "envoy")
            self.assertEqual(ENVOY, sidecar["image"])
            self.assertTrue(sidecar["securityContext"]["readOnlyRootFilesystem"])
            self.assertFalse(sidecar["securityContext"]["allowPrivilegeEscalation"])
            self.assertEqual(["ALL"], sidecar["securityContext"]["capabilities"]["drop"])

    def test_operator_routes_reject_expansion_without_alias_and_egress(self):
        path = CH / "validate_mtls.py"
        self.assertTrue(path.is_file(), "operator route validator must gate publication")
        spec = importlib.util.spec_from_file_location("validate_ch_mtls", path)
        contract = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(contract)
        chi = installation(CH / "clickhouse.yaml")
        routes = yaml.safe_load((CH / "operator-routes.yaml").read_text())
        self.assertEqual([], contract.validate_operator_routes(chi, routes, proxy("operator")))
        expanded = copy.deepcopy(chi)
        expanded["spec"]["configuration"]["clusters"][0]["layout"]["replicasCount"] = 3
        errors = contract.validate_operator_routes(expanded, routes, proxy("operator"))
        self.assertTrue(errors, "a third replica must not silently bypass the local proxy")
        self.assertTrue(any("route" in error.lower() for error in errors))
        # HTTPS replication uses a separate setting from the HTTP name that
        # Altinity generates. Renaming, omitting or swapping it must fail closed.
        for field, value in (("name", "unreviewed"),
                             ("settings", {}),
                             ("settings", {"interserver_https_host": "container-hostname"})):
            wrong = copy.deepcopy(chi)
            wrong["spec"]["configuration"]["clusters"][0]["layout"]["replicas"][0][field] = value
            with self.subTest(field=field, value=value):
                self.assertTrue(contract.validate_operator_routes(wrong, routes, proxy("operator")))


if __name__ == "__main__":
    unittest.main()
