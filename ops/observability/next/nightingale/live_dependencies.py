#!/usr/bin/env python3
"""Read-only protocol checks for the new CNPG and Redis endpoints.

Mount reviewed certificates at /certs/{pg-client,monitor,ui,redis-client}.
pg-client/ca.crt must contain the PostgreSQL server CA; monitoring and UI
client certificates use the mesh CA. No passwords or Kubernetes token are used.
Only fixed check names, Boolean results and exception types reach stdout.
"""

import argparse
import http.client
import ipaddress
import json
from pathlib import Path
import re
import socket
import ssl


PG_HOST = "nightingale-postgres-rw.nightingale.svc"
OPERATOR_HOST = "cloudnative-pg-operator-metrics.nightingale.svc"
REDIS_HOSTS = tuple(f"nightingale-redis-announce-{i}.nightingale.svc" for i in range(3))
TIMEOUT = 8
MAXIMUM = 1024 * 1024
AUTH_ALERTS = frozenset(("TLSV13_ALERT_CERTIFICATE_REQUIRED", "TLSV1_ALERT_UNKNOWN_CA",
                         "SSLV3_ALERT_BAD_CERTIFICATE", "TLSV1_ALERT_INTERNAL_ERROR"))
FOREIGN_CA_ALERTS = frozenset(("TLSV1_ALERT_UNKNOWN_CA", "SSLV3_ALERT_BAD_CERTIFICATE"))


class ProbeFailure(Exception):
    """Carries a fixed check name, never a response or credential."""


def validate_targets(value):
    if not isinstance(value, dict) or set(value) != {"pg_pod_ips", "operator_pod_ips"}:
        raise ValueError("invalid dependency target fields")
    result, seen = {}, set()
    for key, count in (("pg_pod_ips", 3), ("operator_pod_ips", 2)):
        addresses = value[key]
        if not isinstance(addresses, list) or len(addresses) != count:
            raise ValueError("unexpected dependency target count")
        checked = []
        for address in addresses:
            if not isinstance(address, str):
                raise ValueError("dependency target must be an IPv4 string")
            parsed = ipaddress.ip_address(address)
            if parsed.version != 4 or parsed not in ipaddress.ip_network("172.28.0.0/16") or parsed in seen:
                raise ValueError("dependency target outside reviewed unique Pod addresses")
            checked.append(str(parsed))
            seen.add(parsed)
        result[key] = checked
    return result


def is_authentication_alert(error, *, no_certificate=False):
    if not isinstance(error, ssl.SSLError):
        return False
    reasons = {"TLSV13_ALERT_CERTIFICATE_REQUIRED"} if no_certificate else AUTH_ALERTS
    return getattr(error, "reason", None) in reasons


def tls_context(ca_role, client_role=None):
    if ca_role not in {"pg-client", "monitor", "redis-client"} or client_role not in {None, "monitor", "ui", "redis-client"}:
        raise ProbeFailure("certificate_role")
    context = ssl.create_default_context(cafile=str(Path("/certs") / ca_role / "ca.crt"))
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.maximum_version = ssl.TLSVersion.TLSv1_3
    if client_role is not None:
        root = Path("/certs") / client_role
        context.load_cert_chain(str(root / "tls.crt"), str(root / "tls.key"))
    return context


class PodHTTPS(http.client.HTTPSConnection):
    def __init__(self, ip, port, server_name, context):
        super().__init__(server_name, port, timeout=TIMEOUT, context=context)
        self.target_ip = ip

    def connect(self):
        raw = socket.create_connection((self.target_ip, self.port), TIMEOUT)
        try:
            self.sock = self._context.wrap_socket(raw, server_hostname=self.host)
        except Exception:
            raw.close()
            raise


def https_request(ip, port, path, *, ca_role, client_role=None, server_name):
    connection = PodHTTPS(ip, port, server_name, tls_context(ca_role, client_role))
    try:
        try:
            connection.request("GET", path, headers={"Accept-Encoding": "identity"})
        except BrokenPipeError:
            # TLS 1.3 may deliver certificate_required on the first read after
            # a write fails. A bare broken pipe or EOF never proves rejection.
            if connection.sock is not None:
                connection.sock.recv(1)
            raise
        response = connection.getresponse()
        body = response.read(MAXIMUM + 1)
        if len(body) > MAXIMUM:
            raise ProbeFailure("http_response_size")
        return response.status, body
    finally:
        connection.close()


def redis_ping(host, port, *, client_role):
    if host not in REDIS_HOSTS or port not in (6379, 26379):
        raise ProbeFailure("redis_target")
    context = tls_context("redis-client", client_role)
    with socket.create_connection((host, port), TIMEOUT) as raw:
        with context.wrap_socket(raw, server_hostname=host) as secure:
            try:
                secure.sendall(b"*1\r\n$4\r\nPING\r\n")
            except BrokenPipeError:
                secure.recv(1)
                raise
            with secure.makefile("rb") as stream:
                reply = stream.readline(4097)
            if len(reply) > 4096 or not reply.endswith(b"\r\n"):
                raise ProbeFailure("redis_response_size_or_termination")
            return reply


def has_metrics(status, body):
    # Both pinned CNPG handlers register Go process collectors. An arbitrary
    # successful HTML page or an empty metrics response is not sufficient.
    return status == 200 and isinstance(body, bytes) and re.search(
        rb"(?m)^go_goroutines [0-9]+(?:\.[0-9]+)?\r?$", body) is not None


class DependencyProbe:
    def __init__(self):
        self.checks = {}
        self.stage = "initialization"
        self.not_tested = ["postgres_5432_psql_separate_probe", "operator_9443_admission_separate_probe",
                           "redis_plaintext_no_unambiguous_authentication_response"]

    def check(self, name, condition):
        self.stage = name
        self.checks[name] = bool(condition)
        if not condition:
            raise ProbeFailure(name)

    def tls_rejected(self, name, action, *, no_certificate=False, foreign_ca=False):
        self.stage = name
        try:
            action()
        except ssl.SSLError as error:
            rejected = is_authentication_alert(error, no_certificate=no_certificate)
            if foreign_ca:
                rejected = rejected and getattr(error, "reason", None) in FOREIGN_CA_ALERTS
        else:
            rejected = False
        self.check(name, rejected)

    def pg_instance(self, ip, label):
        self.stage = label + "_minimal_ready"

        def request(port, path, role=None):
            return https_request(ip, port, path, ca_role="pg-client", client_role=role, server_name=PG_HOST)

        self.check(label + "_minimal_ready", request(8000, "/readyz") == (200, b"OK"))
        # The locked CNPG guard uses 503 for a presented but unrecognized
        # Operator public key. Require its exact refusal body; neither a
        # generic 503 nor an uninitialized fingerprint counts as auth proof.
        rejections = ((None, "no_certificate", (401, b"client certificate required\n")),
                      ("ui", "wrong_identity", (503, b"operator certificate not recognized\n")))
        for role, suffix, expected in rejections:
            self.stage = label + "_status_" + suffix + "_unauthorized"
            self.check(self.stage, request(8000, "/pg/status", role) == expected)
        self.stage = label + "_metrics_monitor"
        self.check(self.stage, has_metrics(*request(9187, "/metrics", "monitor")))
        self.tls_rejected(label + "_metrics_no_certificate", lambda: request(9187, "/metrics"), no_certificate=True)
        # The monitor positive control and UI's valid same-mesh certificate
        # isolate the patched URI check, whose Go TLS failure may be internal_error.
        self.tls_rejected(label + "_metrics_wrong_uri", lambda: request(9187, "/metrics", "ui"))

    def operator(self, ip, label):
        self.stage = label + "_metrics_monitor"

        def request(path, role=None):
            return https_request(ip, 18443, path, ca_role="monitor", client_role=role, server_name=OPERATOR_HOST)

        self.check(self.stage, has_metrics(*request("/metrics", "monitor")))
        self.tls_rejected(label + "_metrics_no_certificate", lambda: request("/metrics"), no_certificate=True)
        self.stage = label + "_metrics_wrong_role"
        self.check(self.stage, request("/metrics", "ui")[0] == 403)
        self.stage = label + "_monitor_non_metrics_path_denied"
        self.check(self.stage, request("/", "monitor")[0] == 403)
        self.stage = label + "_native_metrics_connection_refused"
        try:
            with socket.create_connection((ip, 8080), TIMEOUT):
                refused = False
        except ConnectionRefusedError:
            refused = True
        self.check(self.stage, refused)

    def redis_member(self, host, label):
        for port in (6379, 26379):
            prefix = f"{label}_{port}"
            self.stage = prefix + "_valid_mtls_still_requires_password"
            self.check(self.stage, redis_ping(host, port, client_role="redis-client") ==
                       b"-NOAUTH Authentication required.\r\n")
            self.tls_rejected(prefix + "_no_certificate", lambda: redis_ping(host, port, client_role=None),
                              no_certificate=True)
            self.tls_rejected(prefix + "_foreign_ca", lambda: redis_ping(host, port, client_role="ui"),
                              foreign_ca=True)

    def run(self, targets):
        for index, ip in enumerate(targets["pg_pod_ips"]):
            self.pg_instance(ip, f"pg_{index}")
        for index, ip in enumerate(targets["operator_pod_ips"]):
            self.operator(ip, f"operator_{index}")
        for index, host in enumerate(REDIS_HOSTS):
            self.redis_member(host, f"redis_{index}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--targets", required=True, type=Path)
    args = parser.parse_args(argv)
    probe = DependencyProbe()
    result = {}
    try:
        targets = validate_targets(json.loads(args.targets.read_text()))
        probe.run(targets)
        result["status"] = "passed"
    except Exception as error:
        result.update(status="failed", stage=probe.stage, error_type=type(error).__name__)
    result.update(checks=probe.checks, not_tested=probe.not_tested)
    print(json.dumps(result, sort_keys=True))
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
