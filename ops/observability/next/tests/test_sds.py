"""Filesystem SDS conversion contracts; run only on the Linux builder."""

from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("obs_filesystem_sds", ROOT / "mtls/sds.py")
sds = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sds)


def tls_context(cert_directory="/etc/obs-mtls"):
    return {
        "@type": "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext",
        "require_client_certificate": True,
        "disable_stateless_session_resumption": True,
        "common_tls_context": {
            "tls_params": {"tls_minimum_protocol_version": "TLSv1_3", "tls_maximum_protocol_version": "TLSv1_3"},
            "alpn_protocols": ["h2", "http/1.1"],
            "tls_certificates": [{
                "certificate_chain": {"filename": cert_directory + "/tls.crt"},
                "private_key": {"filename": cert_directory + "/tls.key"},
            }],
            "validation_context": {
                "trusted_ca": {"filename": cert_directory + "/ca.crt"},
                "match_typed_subject_alt_names": [
                    {"san_type": "URI", "matcher": {"exact": "spiffe://observability.raytrain.local/victoriametrics/vmagent"}},
                    {"san_type": "DNS", "matcher": {"exact": "vmauth.victoriametrics.svc"}},
                ],
            },
        },
    }


def bootstrap():
    inbound = tls_context()
    outbound = tls_context()
    outbound["@type"] = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext"
    outbound.pop("require_client_certificate")
    outbound.pop("disable_stateless_session_resumption")
    outbound["sni"] = "vmauth.victoriametrics.svc"
    return {"static_resources": {
        "listeners": [{"filter_chains": [{
            "transport_socket": {"name": "envoy.transport_sockets.tls", "typed_config": inbound},
            "filters": [{"name": "envoy.filters.http.rbac", "typed_config": {
                "rules": {"action": "ALLOW", "policies": {"only_metrics": {
                    "permissions": [{"url_path": {"path": {"exact": "/metrics"}}}],
                    "principals": [{"authenticated": {"principal_name": {"exact": "expected-role"}}}],
                }}}}}],
        }]}],
        "clusters": [{"transport_socket": {"name": "envoy.transport_sockets.tls", "typed_config": outbound}}],
    }}


def contexts(document):
    resources = document["static_resources"]
    inbound = resources["listeners"][0]["filter_chains"][0]["transport_socket"]["typed_config"]
    outbound = resources["clusters"][0]["transport_socket"]["typed_config"]
    return inbound, outbound


class FilesystemSDSTests(unittest.TestCase):
    def test_input_is_unchanged_and_output_has_no_shared_objects(self):
        source = bootstrap()
        before = deepcopy(source)
        converted, files = sds.transform_bootstrap(source)
        self.assertEqual(before, source)
        contexts(converted)[0]["common_tls_context"]["validation_context"]["trusted_ca"]["filename"] = "/changed"
        self.assertEqual(before, source)
        self.assertTrue(files)

    def test_both_directions_use_one_shared_discovery_resource(self):
        converted, files = sds.transform_bootstrap(bootstrap())
        self.assertEqual(1, len(files))
        filename, discovery = next(iter(files.items()))
        self.assertTrue(filename.endswith(".json"))
        self.assertNotIn("/", filename)
        self.assertEqual(1, len(discovery["resources"]))
        secret = discovery["resources"][0]
        self.assertEqual("type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret", secret["@type"])
        self.assertEqual({"path": "/etc/obs-mtls"}, secret["tls_certificate"]["watched_directory"])
        for context in contexts(converted):
            common = context["common_tls_context"]
            self.assertNotIn("tls_certificates", common)
            self.assertEqual([{"name": secret["name"], "sds_config": {
                "path_config_source": {"path": "/etc/envoy-sds/" + filename, "watched_directory": {"path": "/etc/envoy-sds"}},
                "resource_api_version": "V3",
            }}], common["tls_certificate_sds_secret_configs"])

    def test_tls_san_client_certificate_and_http_rbac_are_exactly_preserved(self):
        original = bootstrap()
        converted, _ = sds.transform_bootstrap(original)
        expected = deepcopy(original)
        for before, after in zip(contexts(expected), contexts(converted)):
            before["common_tls_context"].pop("tls_certificates")
            before["common_tls_context"]["tls_certificate_sds_secret_configs"] = after["common_tls_context"]["tls_certificate_sds_secret_configs"]
        self.assertEqual(expected, converted)

    def test_trusted_ca_remains_static_and_is_not_claimed_as_auto_rotated(self):
        converted, files = sds.transform_bootstrap(bootstrap())
        self.assertTrue(all("validation_context_sds_secret_config" not in c["common_tls_context"] for c in contexts(converted)))
        self.assertTrue(all("validation_context" not in f["resources"][0] for f in files.values()))

    def test_distinct_leaf_mounts_have_distinct_resources(self):
        source = bootstrap()
        contexts(source)[1]["common_tls_context"] = tls_context("/etc/client-mtls")["common_tls_context"]
        converted, files = sds.transform_bootstrap(source, sds_directory="/config/sds")
        self.assertEqual(2, len(files))
        self.assertEqual({"/etc/obs-mtls", "/etc/client-mtls"}, {
            f["resources"][0]["tls_certificate"]["watched_directory"]["path"] for f in files.values()})
        self.assertTrue(all(c["common_tls_context"]["tls_certificate_sds_secret_configs"][0]["sds_config"]["path_config_source"]["path"].startswith("/config/sds/") for c in contexts(converted)))

    def test_deterministic_name_does_not_depend_on_json_key_order(self):
        one = bootstrap()
        two = json.loads(json.dumps(one, sort_keys=True))
        self.assertEqual(sds.transform_bootstrap(one), sds.transform_bootstrap(two))

    def test_existing_dynamic_config_is_untouched(self):
        first, _ = sds.transform_bootstrap(bootstrap())
        second, files = sds.transform_bootstrap(first)
        self.assertEqual(first, second)
        self.assertEqual({}, files)

    def test_static_and_dynamic_leaf_sources_cannot_be_mixed(self):
        source = bootstrap()
        contexts(source)[0]["common_tls_context"]["tls_certificate_sds_secret_configs"] = [{"name": "existing"}]
        with self.assertRaisesRegex(sds.SDSError, "static.*dynamic"):
            sds.transform_bootstrap(source)

    def test_certificate_and_key_must_be_files_in_same_projected_mount(self):
        for invalid in ({"inline_string": "PRIVATE FIXTURE"}, {"filename": "relative.key"},
                        {"filename": "/different/tls.key"}, {"filename": "/etc/obs-mtls/../tls.key"}):
            source = bootstrap()
            contexts(source)[0]["common_tls_context"]["tls_certificates"][0]["private_key"] = invalid
            with self.subTest(kind=list(invalid)), self.assertRaises(sds.SDSError):
                sds.transform_bootstrap(source)

    def test_existing_watch_must_point_to_common_mount_parent(self):
        source = bootstrap()
        leaf = contexts(source)[0]["common_tls_context"]["tls_certificates"][0]
        leaf["watched_directory"] = {"path": "/elsewhere"}
        with self.assertRaisesRegex(sds.SDSError, "watched_directory"):
            sds.transform_bootstrap(source)

    def test_leaf_optional_fields_are_preserved(self):
        source = bootstrap()
        contexts(source)[0]["common_tls_context"]["tls_certificates"][0]["ocsp_staple"] = {"filename": "/etc/obs-mtls/ocsp.der"}
        _, files = sds.transform_bootstrap(source)
        self.assertEqual(2, len(files))
        self.assertTrue(any(f["resources"][0]["tls_certificate"].get("ocsp_staple") == {"filename": "/etc/obs-mtls/ocsp.der"} for f in files.values()))

    def test_invalid_or_empty_bootstrap_is_rejected(self):
        for malformed in (None, [], "config", {}, {"static_resources": []}):
            with self.subTest(value=type(malformed).__name__), self.assertRaises(sds.SDSError):
                sds.transform_bootstrap(malformed)

    def test_invalid_sds_directory_fails_before_generating_configs(self):
        for directory in ("relative", "/", "/etc/../sds", "/etc/sds/", "/etc/sds\nsecret"):
            with self.subTest(directory=directory), self.assertRaises(sds.SDSError):
                sds.transform_bootstrap(bootstrap(), sds_directory=directory)


if __name__ == "__main__":
    unittest.main()
