"""Nightingale security contracts; execute only on the isolated builder candidate.

These checks cover the deployment's actual input files. Runtime TLS, failover,
and the CNPG Go regression suite remain separate mandatory acceptance gates.
"""

from contextlib import redirect_stderr, redirect_stdout
import copy
import io
import json
import os
from pathlib import Path
import re
import runpy
import shutil
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import yaml


ROOT = Path(__file__).resolve().parents[1] / "nightingale"
ALERTING = "nightingale"
IDENTITY = "spiffe://observability.raytrain.local/"
ENVOY_IMAGE = (
    "harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:"
    "dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865"
)


def documents(name):
    return list(yaml.safe_load_all((ROOT / name).read_text()))


def named_document(name, kind):
    return next(item for item in documents(name) if item.get("kind") == kind)


def section(text, name):
    match = re.search(r"(?m)^\[" + re.escape(name) + r"\]\s*$", text)
    if match is None:
        return {}
    body = text[match.end():]
    next_section = re.search(r"(?m)^\[", body)
    if next_section:
        body = body[:next_section.start()]
    values = {}
    for line in body.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        try:
            values[key.strip()] = json.loads(value)
        except json.JSONDecodeError:
            values[key.strip()] = value
    return values


def nodes(value):
    yield value
    if isinstance(value, dict):
        for child in value.values():
            yield from nodes(child)
    elif isinstance(value, list):
        for child in value:
            yield from nodes(child)


class NightingaleMTLSContractTests(unittest.TestCase):
    def required(self, relative):
        path = ROOT / relative
        self.assertTrue(path.is_file(), f"required reviewed artifact missing: {relative}")
        return path

    def template(self):
        return (ROOT / "config/config.toml.template").read_text()

    def test_redis_and_sentinel_disable_plaintext_and_require_peer_tls(self):
        values = documents("30-redis-values.yaml")[0]
        for component, port in (("redis", 6379), ("sentinel", 26379)):
            with self.subTest(component=component):
                item = values[component]
                self.assertEqual(0, item.get("port"), "a TLS Service does not close the native plaintext listener")
                self.assertEqual(port, item.get("tlsPort"))
                self.assertIs(item.get("tlsReplication"), True)
                self.assertEqual("yes", str(item.get("authClients")).lower())

    def test_sentinel_announces_verified_hostnames_after_failover(self):
        sentinel = documents("30-redis-values.yaml")[0]["sentinel"]
        self.assertIs(sentinel.get("resolveHostnames"), True)
        self.assertIs(sentinel.get("announceHostnames"), True)
        self.assertEqual(2, sentinel["quorum"])

    def test_redis_uses_dedicated_certificate_and_preserves_password_auth(self):
        values = documents("30-redis-values.yaml")[0]
        tls = values.get("tls", {})
        self.assertEqual("obs-mtls-redis-ha-peer", tls.get("secretName"))
        self.assertEqual(("tls.crt", "tls.key", "ca.crt"),
                         tuple(tls.get(key) for key in ("certFile", "keyFile", "caCertFile")))
        self.assertIs(values["auth"], True)
        self.assertEqual("nightingale-redis-auth", values["existingSecret"])
        self.assertIs(values["sentinel"]["auth"], True)
        self.assertEqual("nightingale-redis-auth", values["sentinel"]["existingSecret"])
        self.assertFalse(values.get("redisPassword"))
        self.assertFalse(values["sentinel"].get("password"))

    def test_runtime_failure_injection_preserves_dns_and_checks_owner(self):
        runtime_type = runpy.run_path(str(ROOT / "runtime_helpers.py"))["Runtime"]
        runtime = object.__new__(runtime_type)
        runtime.run, runtime.created, runtime.checks = "owned-run", ["owned-member"], {}
        owned = {"Config": {"Labels": {"obs.n9e.test": "owned-run"}}, "State": {"Paused": False}}
        paused = {**owned, "State": {"Paused": True}}
        runtime.docker = mock.Mock(side_effect=[SimpleNamespace(stdout=json.dumps([owned])),
                                                SimpleNamespace(stdout="owned-member"),
                                                SimpleNamespace(stdout=json.dumps([paused]))])
        runtime.pause_member("owned-member")
        self.assertEqual([mock.call("inspect", "owned-member"), mock.call("pause", "owned-member"),
                          mock.call("inspect", "owned-member")], runtime.docker.call_args_list)
        self.assertTrue(runtime.checks["faulted_master_pair_is_paused"])
        runtime.docker.reset_mock()
        with self.assertRaisesRegex(RuntimeError, "outside this run"):
            runtime.pause_member("other-member")
        runtime.docker.assert_not_called()
        foreign = {**owned, "Config": {"Labels": {"obs.n9e.test": "other-run"}}}
        runtime.docker.side_effect = [SimpleNamespace(stdout=json.dumps([foreign]))]
        with self.assertRaisesRegex(RuntimeError, "different owner"):
            runtime.pause_member("owned-member")
        self.assertEqual([mock.call("inspect", "owned-member")], runtime.docker.call_args_list)

    def test_nightingale_native_http_cannot_be_reached_by_pod_ip(self):
        http = section(self.template(), "HTTP")
        self.assertEqual("127.0.0.1", http.get("Host"))
        self.assertEqual(17000, http.get("Port"))
        self.assertIs(http.get("PProf"), False)

    def test_runtime_waits_for_complete_healthy_sentinel_replica_discovery(self):
        module = runpy.run_path(str(ROOT / "runtime_helpers.py"))
        runtime = object.__new__(module["Runtime"])
        hosts = module["REDIS_HOSTS"]
        peers = [{"ip": host, "flags": "slave", "master-link-status": "ok",
                  "role-reported": "slave", "master-host": hosts[0]} for host in hosts[1:]]
        runtime.master = mock.Mock(return_value=hosts[0])
        # Redis CLI RESP2 JSON is a list of alternating key/value arrays.
        runtime.redis = mock.Mock(return_value=json.dumps([
            [value for pair in peer.items() for value in pair] for peer in peers]))
        self.assertTrue(runtime.sentinel_topology_ready("client", hosts[2], hosts[0]))
        for change in ("missing", "duplicate", "down", "wrong_master", "replication_down"):
            invalid = copy.deepcopy(peers)
            if change == "missing":
                invalid.pop()
            elif change == "duplicate":
                invalid[1] = copy.deepcopy(invalid[0])
            elif change == "down":
                invalid[1]["flags"] = "slave,disconnected"
            elif change == "wrong_master":
                invalid[1]["master-host"] = hosts[1]
            else:
                invalid[1]["master-link-status"] = "err"
            runtime.redis.return_value = json.dumps(invalid)
            with self.subTest(change=change):
                self.assertFalse(runtime.sentinel_topology_ready("client", hosts[2], hosts[0]))

    def test_runtime_redacts_short_captcha_answers_without_destroying_ports_or_dates(self):
        runtime_type = runpy.run_path(str(ROOT / "runtime_helpers.py"))["Runtime"]
        runtime = object.__new__(runtime_type)
        runtime.secret_values = ["26", "test-only-long-credential"]
        original = "2026-10-08 port=26379 answer=26 credential=test-only-long-credential"
        self.assertEqual("2026-10-08 port=26379 answer=<redacted> credential=<redacted>", runtime.redact(original))

    def test_runtime_frontend_requires_original_homepage_and_referenced_javascript(self):
        module = runpy.run_path(str(ROOT / "frontend_probe.py"))
        lock = json.loads((ROOT / "n9e-hardening/upstream-lock.json").read_text())
        javascript = lock["frontend_javascript"]
        homepage = {"status": 200, "content_type": "text/html",
                    "sha256": lock["frontend_index_html_sha256"], "bytes": 200,
                    "body": '<html><script type="module" src="' + javascript["path"] + '"></script></html>'}
        script = {"status": 200, "content_type": "text/javascript",
                  "sha256": javascript["sha256"], "bytes": javascript["bytes"]}
        fetch = mock.Mock(side_effect=[homepage, script])
        result = module["verify_frontend"](fetch, lock)
        self.assertTrue(all(result["checks"].values()))
        self.assertEqual(["/", javascript["path"]], [call.args[0] for call in fetch.call_args_list])
        self.assertEqual(javascript["sha256"], result["javascript"]["sha256"])
        cases = [
            ({**homepage, "status": 302}, script),
            ({**homepage, "sha256": "0" * 64}, script),
            ({**homepage, "body": '<script src="https://external.invalid/file.js"></script>'}, script),
            (homepage, {**script, "status": 404}),
            (homepage, {**script, "content_type": "text/html"}),
            (homepage, {**script, "bytes": 0}),
            (homepage, {**script, "sha256": "0" * 64}),
        ]
        for page, asset in cases:
            with self.subTest(page=page["status"], script=asset["status"], mime=asset["content_type"]):
                with self.assertRaises(ValueError):
                    module["verify_frontend"](mock.Mock(side_effect=[page, asset]), lock)

    def test_runtime_frontend_refuses_foreign_or_ambiguous_asset_paths(self):
        module = runpy.run_path(str(ROOT / "frontend_probe.py"))
        for path in ("https://external.invalid/a.js", "//external.invalid/a.js",
                     "/../a.js", "/a.js?x=1", "/a.js#x", "/%2e%2e/a.js", "/a\\b.js"):
            with self.subTest(path=path):
                with self.assertRaises(ValueError):
                    module["local_script_path"](path)
        self.assertEqual("/assets/index.js", module["local_script_path"]("/assets/index.js"))

    def test_nightingale_redis_validates_server_and_presents_client_certificate(self):
        redis = section(self.template(), "Redis")
        self.assertEqual("sentinel", redis.get("RedisType"))
        self.assertIs(redis.get("UseTLS"), True)
        self.assertIs(redis.get("InsecureSkipVerify"), False)
        self.assertIn(redis.get("TLSMinVersion"), ("1.2", "1.3"))
        self.assertEqual("", redis.get("ServerName", ""), "discovered master DNS must be checked, not a fixed unrelated SNI")
        for key, basename in (("TLSCA", "ca.crt"), ("TLSCert", "tls.crt"), ("TLSKey", "tls.key")):
            self.assertEqual(f"/etc/n9e-redis-mtls/{basename}", redis.get(key))
        self.assertEqual(3, len(redis["Address"].split(",")))
        self.assertTrue(all(address.endswith(".nightingale.svc:26379")
                            for address in redis["Address"].split(",")))

    def test_authentication_is_not_disabled_to_make_the_proxy_work(self):
        text = self.template()
        self.assertIs(section(text, "HTTP.ShowCaptcha").get("Enable"), True)
        self.assertIs(section(text, "HTTP.TokenAuth").get("Enable"), True)
        for name in ("HTTP.ProxyAuth", "HTTP.APIForAgent", "HTTP.APIForService", "EmbeddedTSDB", "Ibex"):
            self.assertIs(section(text, name).get("Enable"), False)
        self.assertEqual({"PromQuerier": False, "AlertDetail": False}, section(text, "Center.AnonymousAccess"))

    def render_with_fake_credentials(self, base):
        """Exercise the real renderer using a virtualized filesystem, not a cluster."""
        # Keep the concrete class: patching pathlib.Path also changes the global
        # Path name consulted by its __new__ on Python 3.10.
        real_path, real_copy = type(base), shutil.copyfile
        destinations = {
            "/run/n9e": base / "runtime",
            "/app/etc": base / "image-etc",
            "/secrets/db": base / "db",
            "/secrets/redis": base / "redis",
            "/config": ROOT / "config",
        }

        def mapped(value):
            value = str(value)
            for prefix, target in destinations.items():
                if value == prefix or value.startswith(prefix + "/"):
                    return target / value[len(prefix):].lstrip("/")
            return real_path(value)

        for directory in ("runtime", "image-etc", "db", "redis"):
            (base / directory).mkdir()
        fake = "runtime-test-only-" + "a" * 32
        (base / "image-etc/metrics.yaml").write_text("{}\n")
        (base / "db/username").write_text("n9e")
        (base / "db/password").write_text(fake)
        (base / "redis/auth").write_text(fake)
        (base / "redis/sentinel-password").write_text(fake)
        output = io.StringIO()
        with mock.patch("pathlib.Path", side_effect=mapped), \
                mock.patch("shutil.copyfile", side_effect=lambda source, target: real_copy(mapped(source), mapped(target))), \
                mock.patch.dict(os.environ, {"POD_IP": "192.0.2.10"}), \
                redirect_stdout(output), redirect_stderr(output):
            runpy.run_path(str(ROOT / "config/render-config.py"), run_name="__main__")
        self.assertNotIn(fake, output.getvalue())
        self.assertEqual(0o600, (base / "runtime/etc/config.toml").stat().st_mode & 0o777)
        return (base / "runtime/etc/config.toml").read_text()

    def test_rendered_postgres_dsn_requires_tls_and_the_application_certificate(self):
        with tempfile.TemporaryDirectory() as directory:
            rendered = self.render_with_fake_credentials(Path(directory))
        dsn = section(rendered, "DB")["DSN"]
        self.assertIn("sslmode=verify-full", dsn)
        self.assertIn("sslcert=/etc/n9e-pg-mtls/tls.crt", dsn)
        self.assertIn("sslkey=/etc/n9e-pg-mtls/tls.key", dsn)
        self.assertIn("sslrootcert=/etc/n9e-pg-mtls/ca.crt", dsn)
        self.assertNotIn("sslmode=disable", dsn)

    def test_nightingale_workload_uses_a_bounded_nonprivileged_proxy(self):
        deployment = named_document("40-nightingale.yaml", "Deployment")
        pod = deployment["spec"]["template"]["spec"]
        proxies = [item for item in pod["containers"] if item.get("name") == "envoy"]
        self.assertEqual(1, len(proxies), "loopback application needs its reviewed mTLS proxy")
        proxy = proxies[0]
        self.assertEqual(ENVOY_IMAGE, proxy["image"])
        security = proxy.get("securityContext", {})
        self.assertIs(security.get("readOnlyRootFilesystem"), True)
        self.assertIs(security.get("allowPrivilegeEscalation"), False)
        self.assertEqual(["ALL"], security.get("capabilities", {}).get("drop"))
        self.assertFalse(security.get("capabilities", {}).get("add"))
        for bucket in ("requests", "limits"):
            for resource in ("cpu", "memory"):
                self.assertTrue(proxy.get("resources", {}).get(bucket, {}).get(resource))
        self.assertIn("--concurrency", proxy.get("args", []))
        service = named_document("40-nightingale.yaml", "Service")
        self.assertTrue(all(port["targetPort"] != 17000 and port["targetPort"] != "http"
                            for port in service["spec"]["ports"]))
        n9e = next(item for item in pod["containers"] if item["name"] == "nightingale")
        for probe in ("startupProbe", "readinessProbe", "livenessProbe"):
            self.assertIn("exec", n9e[probe], "kubelet PodIP probes cannot target a loopback-only application")

    def test_proxy_requires_mtls_and_separates_metrics_and_ui_identities(self):
        proxy = yaml.safe_load(self.required("config/envoy.yaml").read_text())
        listeners = proxy["static_resources"]["listeners"]
        inbound = next(item for item in listeners if item["name"] == "n9e-inbound")
        for chain in inbound["filter_chains"]:
            tls = chain["transport_socket"]["typed_config"]
            self.assertIs(tls.get("require_client_certificate"), True)
        managers = [item for item in nodes(inbound)
                    if isinstance(item, dict) and item.get("@type", "").endswith("HttpConnectionManager")]
        self.assertEqual(1, len(managers))
        manager = managers[0]
        self.assertIs(manager.get("normalize_path"), True)
        self.assertEqual("REJECT_REQUEST", manager.get("path_with_escaped_slashes_action"))
        routes = manager["route_config"]["virtual_hosts"][0]["routes"]
        metrics = next(item for item in routes if item.get("match", {}).get("path") == "/metrics")
        ui = next(item for item in routes if item.get("match", {}).get("prefix") == "/")
        for route, role in ((metrics, "victoriametrics/vmagent"),
                            (ui, "nightingale/n9e-ui-gateway")):
            policy = route["typed_per_filter_config"]["envoy.filters.http.rbac"]["rbac"]["rules"]
            self.assertEqual("ALLOW", policy["action"])
            principals = [item["principal_name"]["exact"] for item in nodes(policy)
                          if isinstance(item, dict) and "principal_name" in item]
            self.assertEqual([IDENTITY + role], principals)
        method_matchers = [item for item in nodes(metrics) if isinstance(item, dict) and item.get("name") == ":method"]
        self.assertTrue(any(item.get("string_match", {}).get("exact") == "GET" for item in method_matchers))
        if "admin" in proxy:
            address = proxy["admin"]["address"]
            self.assertTrue("pipe" in address or address.get("socket_address", {}).get("address") == "127.0.0.1")

    def test_postgres_requires_client_certificates_and_closes_password_fallback(self):
        spec = documents("20-postgres.yaml")[0]["spec"]
        self.assertNotIn("tolerations", spec["affinity"],
                         "CNPG 1.30.1 empty slice differs from serialized nil and triggers endless rollout")
        self.assertIs(spec["enableSuperuserAccess"], False)
        self.assertEqual(3, spec["instances"])
        certificates = spec.get("certificates", {})
        for key in ("clientCASecret", "replicationTLSSecret"):
            self.assertTrue(certificates.get(key), f"missing dedicated {key}")
        rules = spec["postgresql"].get("pg_hba", [])
        self.assertIn("hostnossl all all all reject", rules)
        self.assertIn("hostssl n9e n9e all scram-sha-256 clientcert=verify-full", rules)
        self.assertIn("hostssl postgres n9e all scram-sha-256 clientcert=verify-full", rules)
        self.assertFalse(any(rule.startswith("hostssl all n9e ") for rule in rules))
        self.assertIn("hostssl all all all reject", rules)

    def test_cnpg_instance_metrics_enforces_the_monitoring_identity(self):
        spec = documents("20-postgres.yaml")[0]["spec"]
        self.assertIs(spec["monitoring"].get("tls", {}).get("enabled"), True)
        env = {item["name"]: item.get("value") for item in spec.get("env", [])}
        self.assertEqual("/projected/obs-metrics/ca.crt", env.get("OBS_METRICS_CLIENT_CA_FILE"))
        self.assertEqual(IDENTITY + "victoriametrics/vmagent",
                         env.get("OBS_METRICS_ALLOWED_CLIENT_URIS"))
        projection = spec.get("projectedVolumeTemplate", {})
        secrets = [item["secret"] for item in projection.get("sources", []) if "secret" in item]
        self.assertTrue(any(secret.get("name") == "obs-mtls-cnpg-metrics-ca"
                            and {"key": "ca.crt", "path": "obs-metrics/ca.crt"} in secret.get("items", [])
                            for secret in secrets))

    def test_cnpg_operator_webhook_does_not_trust_forwarded_headers(self):
        values = documents("10-cnpg-values.yaml")[0]
        args = values.get("additionalArgs", [])
        self.assertIn("--metrics-bind-address=127.0.0.1:8080", args)
        env = {item["name"]: item.get("value") for item in values.get("additionalEnv", [])}
        self.assertEqual("172.28.2.64,172.28.5.50", env.get("OBS_WEBHOOK_API_SERVER_IPS"))
        lock = json.loads(self.required("cnpg-hardening/upstream-lock.json").read_text())
        self.assertEqual("v1.30.1", lock.get("tag"))
        self.assertRegex(lock.get("commit", ""), r"^[0-9a-f]{40}$")
        self.assertRegex(lock.get("source_sha256", ""), r"^[0-9a-f]{64}$")

    def test_cnpg_hardening_has_real_protocol_regressions_and_reproducible_build(self):
        dockerfile = self.required("cnpg-hardening/Dockerfile").read_text()
        self.assertIn("@sha256:", dockerfile)
        self.assertNotIn("curl |", dockerfile)
        self.required("cnpg-hardening/patches/0001-observability-auth.patch")
        self.required("cnpg-hardening/tests/metrics_tls_test.go")
        self.required("cnpg-hardening/tests/status_auth_test.go")
        self.required("cnpg-hardening/tests/webhook_source_test.go")

    def operator_fixture(self):
        image = "harbor.wellspiking.ai/guofeng.su/cnpg-observability:test@sha256:" + "a" * 64
        manager = {
            "name": "manager", "image": image,
            "args": ["controller", "--metrics-bind-address=127.0.0.1:8080"],
            "env": [{"name": "OPERATOR_IMAGE_NAME", "value": image},
                    {"name": "OBS_WEBHOOK_API_SERVER_IPS", "value": "172.28.2.64,172.28.5.50"}],
        }
        return {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {
            "name": "cloudnative-pg-operator", "namespace": ALERTING}, "spec": {
                "selector": {"matchLabels": {"app.kubernetes.io/instance": "cloudnative-pg-operator"}},
                "template": {"spec": {"containers": [manager], "volumes": []}}}}

    def test_operator_renderer_rejects_unhardened_or_mismatched_images(self):
        module = runpy.run_path(str(ROOT / "scope-cnpg-webhooks.py"))
        valid = self.operator_fixture()
        for change in ("upstream", "injection", "metrics", "namespace", "webhook"):
            item = copy.deepcopy(valid)
            manager = item["spec"]["template"]["spec"]["containers"][0]
            if change == "upstream":
                manager["image"] = "ghcr.io/cloudnative-pg/cloudnative-pg:1.30.1"
            elif change == "injection":
                manager["env"][0]["value"] = "different-image"
            elif change == "metrics":
                manager["args"].append("--metrics-bind-address=:8080")
            elif change == "namespace":
                item["metadata"]["namespace"] = "default"
            else:
                manager["env"][1]["value"] = "0.0.0.0/0"
            with self.subTest(change=change), self.assertRaises(ValueError):
                module["secure_operator"](item)
        secured = module["secure_operator"](valid)
        self.assertEqual(valid, self.operator_fixture(), "renderer mutated its input")
        containers = secured["spec"]["template"]["spec"]["containers"]
        self.assertEqual(["manager", "envoy"], [item["name"] for item in containers])
        self.assertEqual(ENVOY_IMAGE, containers[1]["image"])

    def operator_rbac_fixture(self):
        return {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
                "metadata": {"name": "cloudnative-pg-operator"}, "rules": [
                    {"apiGroups": [""], "resources": ["nodes"], "verbs": ["get", "list", "watch"]},
                    {"apiGroups": ["admissionregistration.k8s.io"], "resources": [
                        "mutatingwebhookconfigurations", "validatingwebhookconfigurations"], "verbs": ["get", "patch"]},
                    {"apiGroups": ["postgresql.cnpg.io"], "resources": ["clusterimagecatalogs"], "verbs": ["get", "list", "watch"]},
                ]}

    def operator_documents_fixture(self):
        hooks = [{"apiVersion": "admissionregistration.k8s.io/v1", "kind": kind,
                  "metadata": {"name": f"cnpg-{mode}-webhook-configuration"}, "webhooks": [{
                      "name": "test.postgresql.cnpg.io", "clientConfig": {"service": {"namespace": ALERTING}},
                      "rules": [{"apiGroups": ["postgresql.cnpg.io"], "resources": ["clusters"]}],
                  }]} for kind, mode in (("MutatingWebhookConfiguration", "mutating"),
                                        ("ValidatingWebhookConfiguration", "validating"))]
        resources = ["backups", "clusters", "clusters/status", "databaseroles", "databases", "failoverquorums",
                     "poolers", "publications", "scheduledbackups", "imagecatalogs", "clusterimagecatalogs", "subscriptions"]
        auxiliary = [{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
                      "metadata": {"name": "cloudnative-pg-operator-" + name}, "rules": [{
                          "apiGroups": ["postgresql.cnpg.io"], "resources": resources, "verbs": verbs,
                      }]} for name, verbs in (("view", ["get", "list", "watch"]),
                                              ("edit", ["create", "delete", "deletecollection", "patch", "update"]))]
        return [self.operator_fixture(), self.operator_rbac_fixture(), *hooks, *auxiliary]

    def test_transformed_operator_rbac_only_patches_its_exact_webhooks(self):
        module = runpy.run_path(str(ROOT / "scope-cnpg-webhooks.py"))
        inputs = self.operator_documents_fixture()
        result = module["transform"](inputs)
        role = next(item for item in result if item["kind"] == "ClusterRole")
        webhooks = [rule for rule in role["rules"] if rule["apiGroups"] == ["admissionregistration.k8s.io"]]
        expected = [{"apiGroups": ["admissionregistration.k8s.io"], "resources": [mode + "webhookconfigurations"],
                     "verbs": ["get", "patch"], "resourceNames": ["cnpg-" + mode + "-webhook-configuration"]}
                    for mode in ("mutating", "validating")]
        self.assertEqual(expected, webhooks)
        self.assertEqual(4, len(role["rules"]))
        self.assertEqual(self.operator_rbac_fixture()["rules"][0], role["rules"][0])
        self.assertEqual(self.operator_rbac_fixture()["rules"][2], role["rules"][-1])
        self.assertEqual(self.operator_documents_fixture(), inputs, "renderer mutated its input RBAC")

    def test_operator_rbac_renderer_rejects_unexpected_chart_shapes(self):
        module = runpy.run_path(str(ROOT / "scope-cnpg-webhooks.py"))
        for change in ("role_name", "wildcard", "extra_resource", "missing_rule", "foreign_names", "foreign_webhook",
                       "unknown_non_webhook_role", "auxiliary_role_changed", "aggregation"):
            inputs = self.operator_documents_fixture()
            role = inputs[1]
            if change == "role_name":
                role["metadata"]["name"] = "other-cnpg"
            elif change == "wildcard":
                role["rules"][1]["verbs"].append("*")
            elif change == "extra_resource":
                role["rules"][1]["resources"].append("secrets")
            elif change == "missing_rule":
                role["rules"].pop()
            elif change == "foreign_names":
                role["rules"][1]["resourceNames"] = ["old-production-webhook"]
            elif change == "foreign_webhook":
                inputs[2]["metadata"]["name"] = "old-production-webhook"
            elif change in ("unknown_non_webhook_role", "auxiliary_role_changed"):
                inputs.append({"kind": "ClusterRole", "metadata": {
                    "name": "unknown-role" if change == "unknown_non_webhook_role" else "cloudnative-pg-operator-view"},
                    "rules": [{"apiGroups": [""], "resources": ["secrets"], "verbs": ["get"]}]})
            else:
                role["aggregationRule"] = {"clusterRoleSelectors": [{"matchLabels": {"custom-role": "true"}}]}
            with self.subTest(change=change), self.assertRaises(ValueError):
                module["transform"](inputs)

    def test_operator_metrics_proxy_only_allows_authenticated_get_metrics(self):
        config = yaml.safe_load((ROOT / "config/cnpg-envoy.yaml").read_text())
        listener = config["static_resources"]["listeners"][0]
        tls = listener["filter_chains"][0]["transport_socket"]["typed_config"]
        self.assertIs(tls["require_client_certificate"], True)
        rules = [item for item in nodes(listener)
                 if isinstance(item, dict) and item.get("@type", "").endswith(".RBAC")][0]["rules"]
        self.assertEqual("ALLOW", rules["action"])
        self.assertIn({"principal_name": {"exact": IDENTITY + "victoriametrics/vmagent"}}, list(nodes(rules)))
        self.assertIn({"url_path": {"path": {"exact": "/metrics"}}}, list(nodes(rules)))
        self.assertIn({"header": {"name": ":method", "string_match": {"exact": "GET"}}}, list(nodes(rules)))

    def test_nightingale_outbound_uses_exact_peer_identity_and_is_loopback_only(self):
        config = yaml.safe_load((ROOT / "config/envoy.yaml").read_text())["static_resources"]
        listener = next(item for item in config["listeners"] if item["name"] == "n9e-vm-read-outbound")
        self.assertEqual("127.0.0.1", listener["address"]["socket_address"]["address"])
        cluster = next(item for item in config["clusters"] if item["name"] == "vmauth-read")
        tls = cluster["transport_socket"]["typed_config"]["common_tls_context"]
        self.assertEqual({"tls_minimum_protocol_version": "TLSv1_3",
                          "tls_maximum_protocol_version": "TLSv1_3"}, tls["tls_params"])
        self.assertEqual([{"san_type": "URI", "matcher": {
            "exact": IDENTITY + "victoriametrics/vmauth"}}],
            tls["validation_context"]["match_typed_subject_alt_names"])
        self.assertTrue(tls["tls_certificates"])


class NightingaleLiveProbeTests(unittest.TestCase):
    def module(self):
        return runpy.run_path(str(ROOT / "live_probe.py"))

    def test_live_targets_are_exactly_two_private_pods_and_optional_positive_datasource(self):
        validate = self.module()["validate_targets"]
        good = {"pod_ips": ["172.28.1.10", "172.28.1.11"], "datasource_id": 7}
        self.assertEqual(good, validate(good))
        for ips in (["172.28.1.10"] * 2, ["127.0.0.1", "172.28.1.11"],
                    ["example.org", "172.28.1.11"], ["8.8.8.8", "172.28.1.11"]):
            with self.subTest(ips=ips), self.assertRaises(ValueError):
                validate({"pod_ips": ips})
        for value in (0, -1, "7", True):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate({"pod_ips": good["pod_ips"], "datasource_id": value})

    def test_live_resp_parser_is_bounded_and_rejects_errors(self):
        module = self.module()
        read = module["read_resp"]
        self.assertEqual(["master", "6379"], read(io.BytesIO(b"*2\r\n$6\r\nmaster\r\n$4\r\n6379\r\n")))
        self.assertIsNone(read(io.BytesIO(b"$-1\r\n")))
        for wire in (b"-NOAUTH example\r\n", b"$1048577\r\n", b"*1001\r\n", b"$2\r\na\r\n", b"?\r\n"):
            with self.subTest(wire=wire), self.assertRaises(module["ProbeFailure"]):
                read(io.BytesIO(wire))

    def test_live_endpoint_verifies_service_dns_while_connecting_specific_pod(self):
        module = self.module()
        context = mock.Mock()
        wrapped = mock.Mock()
        context.wrap_socket.return_value = wrapped
        with mock.patch("socket.create_connection") as connect:
            endpoint = module["PodHTTPS"]("172.28.1.10", context)
            endpoint.connect()
        connect.assert_called_once_with(("172.28.1.10", 17443), 8)
        context.wrap_socket.assert_called_once_with(connect.return_value,
            server_hostname="nightingale.nightingale.svc")
        self.assertIs(wrapped, endpoint.sock)

    def test_live_profile_requires_real_root_envelope_and_vm_requires_vector_one(self):
        module = self.module()
        self.assertTrue(module["is_root"](200, {"dat": {"username": "root"}, "err": ""}))
        for status, body in ((200, {"dat": None}), (500, {"dat": {"username": "root"}}),
                             (200, {"dat": {"username": "root"}, "err": "backend failure"})):
            self.assertFalse(module["is_root"](status, body))
        good = {"status": "success", "data": {"resultType": "vector", "result": [{"value": [1, "1"]}]}}
        self.assertTrue(module["is_vector_one"](200, good))
        for bad in ({"dat": [7]}, {"status": "success", "data": {"resultType": "vector", "result": []}},
                    {"status": "success", "data": {"resultType": "vector", "result": [{"value": [1, "0"]}]}}):
            self.assertFalse(module["is_vector_one"](200, bad))

    def test_live_negative_network_checks_require_specific_auth_alert_or_refusal(self):
        module = self.module()
        probe_type = module["Probe"]
        # Patch the actual function globals, because run_path returns a copy.
        globals_ = probe_type.boundaries.__globals__
        ssl_error = module["ssl"].SSLError(1, "unrelated TLS error")
        ssl_error.reason = "WRONG_VERSION_NUMBER"
        with mock.patch.dict(globals_, request=mock.Mock(side_effect=ssl_error)):
            with self.assertRaises(module["ProbeFailure"]):
                probe_type().boundaries("172.28.1.10", "pod_0")
        required = module["ssl"].SSLError(1, "certificate required")
        required.reason = "TLSV13_ALERT_CERTIFICATE_REQUIRED"
        for error, accepted in ((TimeoutError(), False), (ConnectionRefusedError(), True)):
            calls = mock.Mock(side_effect=[required, (403, None), (403, None),
                                           (200, None), (403, None), (403, None)])
            with mock.patch.dict(globals_, request=calls), mock.patch("socket.create_connection", side_effect=error):
                if accepted:
                    probe_type().boundaries("172.28.1.10", "pod_0")
                else:
                    with self.assertRaises(TimeoutError):
                        probe_type().boundaries("172.28.1.10", "pod_0")


class LiveDependencyProbeTests(unittest.TestCase):
    def module(self):
        path = ROOT / "live_dependencies.py"
        self.assertTrue(path.is_file(), "live dependency protocol probe is not implemented")
        return runpy.run_path(str(path))

    def targets(self):
        return {"pg_pod_ips": ["172.28.1.10", "172.28.1.11", "172.28.1.12"],
                "operator_pod_ips": ["172.28.2.10", "172.28.2.11"]}

    def test_dependency_targets_require_exact_reviewed_pod_sets(self):
        validate = self.module()["validate_targets"]
        self.assertEqual(self.targets(), validate(self.targets()))
        bad = [dict(self.targets(), other="unreviewed"),
               dict(self.targets(), pg_pod_ips=["172.28.1.10"] * 3),
               dict(self.targets(), operator_pod_ips=["172.28.2.10"]),
               dict(self.targets(), operator_pod_ips=["172.28.1.10", "172.28.2.11"]),
               dict(self.targets(), operator_pod_ips=["127.0.0.1", "172.28.2.11"]),
               dict(self.targets(), operator_pod_ips=["8.8.8.8", "172.28.2.11"])]
        for value in bad:
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate(value)

    def test_dependency_tls_rejection_requires_explicit_peer_authentication_alert(self):
        module = self.module()
        classify = module["is_authentication_alert"]
        for reason in ("TLSV13_ALERT_CERTIFICATE_REQUIRED", "TLSV1_ALERT_UNKNOWN_CA",
                       "SSLV3_ALERT_BAD_CERTIFICATE", "TLSV1_ALERT_INTERNAL_ERROR"):
            error = module["ssl"].SSLError(1, "sensitive peer details")
            error.reason = reason
            self.assertTrue(classify(error))
            self.assertEqual(reason == "TLSV13_ALERT_CERTIFICATE_REQUIRED",
                             classify(error, no_certificate=True))
        error = module["ssl"].SSLError(1, "not an authentication alert")
        error.reason = "WRONG_VERSION_NUMBER"
        for error in (error, TimeoutError(), ConnectionResetError(), BrokenPipeError()):
            self.assertFalse(classify(error))
            self.assertFalse(classify(error, no_certificate=True))

    def test_dependency_pg_requires_minimal_health_http_status_guard_and_metrics_mtls(self):
        module = self.module()
        probe_type = module["DependencyProbe"]
        globals_ = probe_type.pg_instance.__globals__

        def request(ip, port, path, *, ca_role, client_role=None, server_name):
            self.assertEqual("172.28.1.10", ip)
            self.assertEqual("pg-client", ca_role)
            self.assertEqual("nightingale-postgres-rw.nightingale.svc", server_name)
            if port == 8000:
                if path == "/readyz":
                    return 200, b"OK"
                return ((401, b"client certificate required\n") if client_role is None else
                        (503, b"operator certificate not recognized\n"))
            self.assertEqual((9187, "/metrics"), (port, path))
            if client_role == "monitor":
                return 200, b"# TYPE go_goroutines gauge\ngo_goroutines 3\n"
            error = module["ssl"].SSLError(1, "peer rejected client identity")
            error.reason = ("TLSV13_ALERT_CERTIFICATE_REQUIRED" if client_role is None
                            else "TLSV1_ALERT_INTERNAL_ERROR")
            raise error

        with mock.patch.dict(globals_, https_request=mock.Mock(side_effect=request)):
            probe = probe_type()
            probe.pg_instance("172.28.1.10", "pg_0")
            self.assertTrue(probe.checks)
            self.assertTrue(all(probe.checks.values()))
        for bad in ((200, b'{"status":"ready","data":"private"}'), (503, b"OK")):
            def wrong_health(ip, port, path, **kwargs):
                return bad if path == "/readyz" else request(ip, port, path, **kwargs)
            with mock.patch.dict(globals_, https_request=mock.Mock(side_effect=wrong_health)):
                with self.assertRaises(module["ProbeFailure"]):
                    probe_type().pg_instance("172.28.1.10", "pg_0")
        with mock.patch.dict(globals_, https_request=mock.Mock(return_value=(200, b"OK"))):
            with self.assertRaises(module["ProbeFailure"]):
                probe_type().pg_instance("172.28.1.10", "pg_0")
        for bad in ((503, b"unavailable\n"), (503, b"operator certificate not registered\n"),
                    (200, b"operator certificate not recognized\n"),
                    (401, b"client certificate required\n")):
            def wrong_certificate_response(ip, port, path, **kwargs):
                if port == 8000 and path == "/pg/status" and kwargs.get("client_role") == "ui":
                    return bad
                return request(ip, port, path, **kwargs)
            with mock.patch.dict(globals_, https_request=mock.Mock(side_effect=wrong_certificate_response)):
                with self.assertRaises(module["ProbeFailure"]):
                    probe_type().pg_instance("172.28.1.10", "pg_0")

    def test_dependency_report_never_prints_exception_payload_or_credentials(self):
        module = self.module()
        output = io.StringIO()
        sensitive = "fixture-password-private-key-jwt-captcha-answer"
        with mock.patch.object(module["Path"], "read_text", return_value=json.dumps(self.targets())), \
                mock.patch.object(module["DependencyProbe"], "run", side_effect=RuntimeError(sensitive)), \
                redirect_stdout(output):
            code = module["main"](["--targets", "/targets.json"])
        result = json.loads(output.getvalue())
        self.assertEqual(1, code)
        self.assertEqual("failed", result["status"])
        self.assertEqual("RuntimeError", result["error_type"])
        self.assertNotIn(sensitive, output.getvalue())
        self.assertTrue(all(type(value) is bool for value in result["checks"].values()))


if __name__ == "__main__":
    unittest.main()
