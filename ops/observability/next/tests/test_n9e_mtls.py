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
import unittest
from unittest import mock

import yaml


ROOT = Path(__file__).resolve().parents[1] / "nightingale"
ALERTING = "observability-alerting"
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
        self.assertEqual("n9e-redis-auth", values["existingSecret"])
        self.assertIs(values["sentinel"]["auth"], True)
        self.assertEqual("n9e-redis-auth", values["sentinel"]["existingSecret"])
        self.assertFalse(values.get("redisPassword"))
        self.assertFalse(values["sentinel"].get("password"))

    def test_nightingale_native_http_cannot_be_reached_by_pod_ip(self):
        http = section(self.template(), "HTTP")
        self.assertEqual("127.0.0.1", http.get("Host"))
        self.assertEqual(17000, http.get("Port"))
        self.assertIs(http.get("PProf"), False)

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
        self.assertTrue(all(address.endswith(".observability-alerting.svc:26379")
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
        real_path, real_copy = Path, shutil.copyfile
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
        for route, role in ((metrics, "observability-metrics/vmagent"),
                            (ui, "observability-alerting/n9e-ui-gateway")):
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
        self.assertIs(spec["enableSuperuserAccess"], False)
        self.assertEqual(3, spec["instances"])
        certificates = spec.get("certificates", {})
        for key in ("clientCASecret", "replicationTLSSecret"):
            self.assertTrue(certificates.get(key), f"missing dedicated {key}")
        rules = spec["postgresql"].get("pg_hba", [])
        self.assertIn("hostnossl all all all reject", rules)
        self.assertIn("hostssl n9e n9e all scram-sha-256 clientcert=verify-full", rules)
        self.assertIn("hostssl all all all reject", rules)

    def test_cnpg_instance_metrics_enforces_the_monitoring_identity(self):
        spec = documents("20-postgres.yaml")[0]["spec"]
        self.assertIs(spec["monitoring"].get("tls", {}).get("enabled"), True)
        env = {item["name"]: item.get("value") for item in spec.get("env", [])}
        self.assertEqual("/projected/obs-metrics/ca.crt", env.get("OBS_METRICS_CLIENT_CA_FILE"))
        self.assertEqual(IDENTITY + "observability-metrics/vmagent",
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
            "name": "n9e-cnpg", "namespace": ALERTING}, "spec": {
                "selector": {"matchLabels": {"app.kubernetes.io/instance": "n9e-cnpg"}},
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

    def test_operator_metrics_proxy_only_allows_authenticated_get_metrics(self):
        config = yaml.safe_load((ROOT / "config/cnpg-envoy.yaml").read_text())
        listener = config["static_resources"]["listeners"][0]
        tls = listener["filter_chains"][0]["transport_socket"]["typed_config"]
        self.assertIs(tls["require_client_certificate"], True)
        rules = [item for item in nodes(listener)
                 if isinstance(item, dict) and item.get("@type", "").endswith(".RBAC")][0]["rules"]
        self.assertEqual("ALLOW", rules["action"])
        self.assertIn({"principal_name": {"exact": IDENTITY + "observability-metrics/vmagent"}}, list(nodes(rules)))
        self.assertIn({"url_path": {"path": {"exact": "/metrics"}}}, list(nodes(rules)))
        self.assertIn({"header": {"name": ":method", "string_match": {"exact": "GET"}}}, list(nodes(rules)))

    def test_nightingale_outbound_uses_exact_peer_identity_and_is_loopback_only(self):
        config = yaml.safe_load((ROOT / "config/envoy.yaml").read_text())["static_resources"]
        listener = next(item for item in config["listeners"] if item["name"] == "n9e-vm-read-outbound")
        self.assertEqual("127.0.0.1", listener["address"]["socket_address"]["address"])
        cluster = next(item for item in config["clusters"] if item["name"] == "vmauth-read")
        tls = cluster["transport_socket"]["typed_config"]["common_tls_context"]
        self.assertEqual([{"san_type": "URI", "matcher": {
            "exact": IDENTITY + "observability-metrics/vmauth"}}],
            tls["validation_context"]["match_typed_subject_alt_names"])
        self.assertTrue(tls["tls_certificates"])


if __name__ == "__main__":
    unittest.main()
