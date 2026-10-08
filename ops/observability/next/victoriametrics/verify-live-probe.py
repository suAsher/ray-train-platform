#!/usr/bin/env python3
"""Stdlib-only probe for coordinator-created, bounded in-cluster test Pods.

Read stdin JSON: {"pods":[{"name":"...","role":"...","ip":"..."}],
"services":{"vmauth":"vmauth-raytrain-next.observability-metrics.svc"}}.
Default --mode operator checks only public health and reads no credentials.
Full mode needs /certs/{vmagent,canary,admin}/{ca.crt,tls.crt,tls.key} and
/tokens/{read,write}/token. These are leaf keys, never signing CA private keys.
Only a uniquely labelled synthetic metric is written; no Kubernetes API calls,
silences, restarts, files or application configuration are created or changed.
Print only check metadata, never response bodies, headers, tokens or exceptions.
"""

import argparse
from collections import Counter
import http.client
import ipaddress
import json
from pathlib import Path
import re
import secrets
import socket
import ssl
import struct
import sys
import time
from urllib.parse import urlencode


NS = "observability-metrics"
FULL_COUNTS = {"operator": 1, "vmstorage": 3, "vminsert": 2, "vmselect": 2,
               "vmauth": 2, "vmagent": 2, "vmalert": 2, "alertmanager": 3}
METRICS = {"operator": 8080, "vmstorage": 8482, "vminsert": 8480, "vmselect": 8481,
           "vmauth": 8428, "vmagent": 8429, "vmalert": 8880, "alertmanager": 9093}
NATIVE = {"operator": (8081, 6060), "vmstorage": (28400, 28401),
          "vminsert": (18400, 18401, 18402), "vmselect": (19400, 19401, 19402),
          "vmauth": (18480, 18481, 8435), "vmagent": (8435,),
          "vmalert": (8435,), "alertmanager": (8435,)}


class ProbeError(Exception):
    """Only fixed, non-sensitive error messages may be supplied."""


def validate_inventory(value, mode):
    if not isinstance(value, dict) or not isinstance(value.get("pods"), list) or len(value["pods"]) > 64:
        raise ProbeError("invalid inventory")
    pods = []
    for pod in value["pods"]:
        if not isinstance(pod, dict) or pod.get("role") not in FULL_COUNTS:
            raise ProbeError("unknown role")
        if not isinstance(pod.get("name"), str) or not re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", pod["name"]):
            raise ProbeError("invalid Pod name")
        try:
            ip = ipaddress.ip_address(pod.get("ip", ""))
        except ValueError:
            raise ProbeError("invalid Pod IP") from None
        if ip.version != 4 or not ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_unspecified or ip.is_multicast:
            raise ProbeError("address is not a private Pod IP")
        pods.append({"name": pod["name"], "role": pod["role"], "ip": str(ip)})
    if len({pod["name"] for pod in pods}) != len(pods) or len({pod["ip"] for pod in pods}) != len(pods):
        raise ProbeError("duplicate Pod inventory")
    if mode == "operator":
        pods = [pod for pod in pods if pod["role"] == "operator"]
        if len(pods) != 1:
            raise ProbeError("exactly one Operator required")
        return {"pods": pods, "services": {}}
    if mode != "full" or Counter(pod["role"] for pod in pods) != Counter(FULL_COUNTS):
        raise ProbeError("full replica inventory required")
    expected = {"vmauth": f"vmauth-raytrain-next.{NS}.svc"}
    if value.get("services") != expected:
        raise ProbeError("exact new VMAuth service required")
    return {"pods": pods, "services": expected}


class TLSConnection(http.client.HTTPSConnection):
    def __init__(self, host, port, context, server):
        super().__init__(host, port, context=context, timeout=5)
        self.server = server

    def connect(self):
        self.sock = self._context.wrap_socket(socket.create_connection((self.host, self.port), self.timeout),
                                               server_hostname=self.server)


def request(target, port, path, identity=None, tls=False, method="GET", body=None, token=None):
    if tls:
        context = ssl.create_default_context(cafile="/certs/vmagent/ca.crt")
        context.minimum_version = ssl.TLSVersion.TLSv1_3
        context.maximum_version = ssl.TLSVersion.TLSv1_3
        if identity:
            if identity not in ("vmagent", "canary", "admin"):
                raise ProbeError("unknown client identity")
            directory = Path("/certs") / identity
            context.load_cert_chain(directory / "tls.crt", directory / "tls.key")
        connection = TLSConnection(target["ip"], port, context, target["role"] + "." + NS + ".svc")
    else:
        connection = http.client.HTTPConnection(target["ip"], port, timeout=5)
    headers = {"Authorization": "Bearer " + token} if token else {}
    if body is not None:
        headers = {**headers, "Content-Type": "application/x-protobuf", "Content-Encoding": "snappy",
                   "X-Prometheus-Remote-Write-Version": "0.1.0"}
    try:
        connection.request(method, path, body=body, headers=headers)
        response = connection.getresponse()
        return response.status, response.read(1048576)
    finally:
        connection.close()


def expect_denied(operation):
    try:
        status, _ = operation()
    except (ssl.SSLError, ConnectionResetError, http.client.RemoteDisconnected):
        return "denied"
    if status not in (400, 401, 403, 404, 405, 426):
        raise ProbeError("negative request was not explicitly denied")
    return "denied"


def read_token(path):
    value = Path(path).read_text().strip()
    if not 16 <= len(value) <= 4096 or any(character.isspace() for character in value):
        raise ProbeError("invalid mounted token")
    return value


def expect_status(operation, status):
    observed, body = operation()
    if observed != status:
        raise ProbeError("unexpected HTTP status")
    return body


def check(results, name, operation):
    # Mark the failing check before evaluating it; no exception text is logged.
    results.append({"check": name, "result": "pending"})
    value = operation()
    results[-1] = {"check": name, "result": "passed"}
    return value


def run_operator(inventory, results):
    for pod in inventory["pods"]:
        for path, expected in (("/healthz", 200), ("/app-healthz", 200), ("/metrics", 404)):
            check(results, pod["name"] + ":health:" + path,
                  lambda: expect_status(lambda: request(pod, 18081, path), expected))


def closed(target, port):
    try:
        with socket.create_connection((target["ip"], port), timeout=3):
            raise ProbeError("native bypass is reachable")
    except ConnectionRefusedError:
        return


def probe_pod(pod, results):
    port, prefix = METRICS[pod["role"]], pod["name"]
    check(results, prefix + ":authorized-metrics", lambda: expect_status(
        lambda: request(pod, port, "/metrics", "vmagent", True), 200))
    cases = (("anonymous", None, "GET", "/metrics"), ("wrong-role", "canary", "GET", "/metrics"),
             ("wrong-method", "vmagent", "POST", "/metrics"), ("wrong-path", "vmagent", "GET", "/not-authorized"))
    for name, identity, method, path in cases:
        check(results, prefix + ":denied:" + name, lambda: expect_denied(
            lambda: request(pod, port, path, identity, True, method)))
    for native_port in NATIVE[pod["role"]]:
        check(results, f"{prefix}:native-closed:{native_port}", lambda: closed(pod, native_port))
    check(results, prefix + ":denied:plaintext", lambda: expect_denied(
        lambda: request(pod, port, "/metrics")))
    if pod["role"] == "vmstorage":
        for rpc_port in (8400, 8401):
            for identity in (None, "vmagent"):
                check(results, f"{prefix}:rpc-{rpc_port}-denied:{identity or 'anonymous'}", lambda: expect_denied(
                    lambda: request(pod, rpc_port, "/metrics", identity, True)))


def varint(value):
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    return bytes(result) + bytes([value])


def field(number, value):
    return varint(number << 3 | 2) + varint(len(value)) + value


def sample_payload(probe_id):
    labels = [("__name__", "raytrain_observability_live_probe"), ("probe_id", probe_id)]
    series = b"".join(field(1, field(1, key.encode()) + field(2, value.encode())) for key, value in labels)
    sample = bytes([9]) + struct.pack("<d", 1.0) + bytes([16]) + varint(int(time.time() * 1000))
    message = field(1, series + field(2, sample))
    length = len(message) - 1
    size = (length.bit_length() + 7) // 8
    literal = bytes([length << 2]) if length < 60 else bytes([(59 + size) << 2]) + length.to_bytes(size, "little")
    return varint(len(message)) + literal + message


def await_sample(target, query, token):
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        body = expect_status(lambda: request(target, 8427, query, "canary", True, token=token), 200)
        value = json.loads(body)
        if value.get("status") == "success" and value.get("data", {}).get("result"):
            return
        time.sleep(2)
    raise ProbeError("new sample did not become queryable")


def probe_gateway(inventory, results):
    read = read_token("/tokens/read/token")
    write = read_token("/tokens/write/token")
    if read == write:
        raise ProbeError("read and write tokens must differ")
    marker = secrets.token_hex(12)
    payload = sample_payload(marker)
    query = "/api/v1/query?" + urlencode({"query": f'raytrain_observability_live_probe{{probe_id="{marker}"}}'})
    service = {"name": "vmauth-service", "ip": inventory["services"]["vmauth"], "role": "vmauth"}
    check(results, "gateway:service-write", lambda: expect_status(
        lambda: request(service, 8427, "/api/v1/write", "canary", True, "POST", payload, write), 204))
    targets = [service, *[pod for pod in inventory["pods"] if pod["role"] == "vmauth"]]
    for target in targets:
        prefix = target["name"]
        check(results, prefix + ":query-new-sample", lambda: await_sample(target, query, read))
        for name, identity, token in (("no-token", "canary", None), ("bad-token", "canary", "invalid-token"),
                                     ("write-token-cannot-read", "canary", write), ("agent-cannot-read", "vmagent", read),
                                     ("no-cert", None, read)):
            check(results, prefix + ":denied:" + name, lambda: expect_denied(
                lambda: request(target, 8427, query, identity, True, token=token)))
        check(results, prefix + ":denied:read-token-cannot-write", lambda: expect_denied(
            lambda: request(target, 8427, "/api/v1/write", "canary", True, "POST", payload, read)))
        check(results, prefix + ":denied:tenant-route", lambda: expect_denied(
            lambda: request(target, 8427, "/select/0/prometheus/api/v1/query?query=up", "canary", True, token=read)))
        check(results, prefix + ":denied:write-method", lambda: expect_denied(
            lambda: request(target, 8427, "/api/v1/write", "canary", True, token=write)))


def probe_alertmanagers(inventory, results):
    memberships = []
    for pod in inventory["pods"]:
        if pod["role"] != "alertmanager":
            continue
        def status():
            body = expect_status(lambda: request(pod, 9093, "/api/v2/status", "admin", True), 200)
            cluster = json.loads(body).get("cluster", {})
            peers = cluster.get("peers", [])
            if cluster.get("status") != "ready" or len(peers) != 3:
                raise ProbeError("Alertmanager mesh not ready with three peers")
            memberships.append(frozenset(peer["name"] for peer in peers))
        check(results, pod["name"] + ":three-peer-ready", status)
    if len(set(memberships)) != 1 or len(next(iter(memberships), ())) != 3:
        raise ProbeError("Alertmanager peers disagree")
    results.append({"check": "alertmanager:consistent-three-peer-membership", "result": "passed"})


def run_full(inventory, results):
    run_operator(inventory, results)
    for pod in inventory["pods"]:
        probe_pod(pod, results)
    probe_gateway(inventory, results)
    probe_alertmanagers(inventory, results)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("operator", "full"), default="operator")
    args = parser.parse_args()
    results = []
    report = {"status": "failed", "mode": args.mode, "checks": results}
    try:
        raw = sys.stdin.read(65537)
        if len(raw) > 65536:
            raise ProbeError("inventory exceeds limit")
        inventory = validate_inventory(json.loads(raw), args.mode)
        (run_full if args.mode == "full" else run_operator)(inventory, results)
        report["status"] = "passed"
    except Exception as error:
        report["failure_type"] = type(error).__name__
    print(json.dumps(report, separators=(",", ":")))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
