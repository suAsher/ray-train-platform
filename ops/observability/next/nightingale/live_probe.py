#!/usr/bin/env python3
"""Bounded probe for the new alerting namespace; no Kubernetes API or faults.

Mount only the reviewed new Secrets at /certs/{ui,redis-client,wrong,monitor}
and /secrets/{redis,admin}. Every role certificate is required. Targets contain
only two current Pod IPs and an
optional already-created canary datasource ID. No business config is created.
"""

import argparse
import hashlib
import http.client
import ipaddress
import json
from pathlib import Path
import re
import socket
import ssl
import time


HOST = "nightingale.nightingale.svc"
REDIS_HOSTS = tuple(f"nightingale-redis-announce-{i}.nightingale.svc" for i in range(3))
MAXIMUM = 1024 * 1024


class ProbeFailure(Exception):
    """Messages are fixed check names only, never upstream payloads."""


def validate_targets(value):
    if not isinstance(value, dict) or set(value) - {"pod_ips", "datasource_id"}:
        raise ValueError("invalid target fields")
    ips = value.get("pod_ips")
    if not isinstance(ips, list) or len(ips) != 2 or len(set(ips)) != 2:
        raise ValueError("two distinct Pod IPs required")
    for address in ips:
        if ipaddress.ip_address(address) not in ipaddress.ip_network("172.28.0.0/16"):
            raise ValueError("target outside reviewed VPC")
    if "datasource_id" in value and (type(value["datasource_id"]) is not int or value["datasource_id"] <= 0):
        raise ValueError("positive numeric datasource ID required")
    return dict(value)


def read_resp(stream, depth=0):
    if depth > 4:
        raise ProbeFailure("redis_response_depth")
    line = stream.readline(4098)
    if not line.endswith(b"\r\n") or len(line) > 4096:
        raise ProbeFailure("redis_response_line")
    kind, payload = line[:1], line[1:-2]
    if kind == b"+":
        return payload.decode("utf-8")
    if kind == b":":
        return int(payload)
    if kind not in (b"$", b"*"):
        raise ProbeFailure("redis_response_error")
    size = int(payload)
    if size == -1:
        return None
    if size < 0 or size > (MAXIMUM if kind == b"$" else 1000):
        raise ProbeFailure("redis_response_size")
    if kind == b"*":
        return [read_resp(stream, depth + 1) for _ in range(size)]
    data = stream.read(size + 2)
    if len(data) != size + 2 or data[-2:] != b"\r\n":
        raise ProbeFailure("redis_response_bulk")
    return data[:-2].decode("utf-8")


def tls_context(role, with_certificate=True):
    root = Path("/certs") / role
    context = ssl.create_default_context(cafile=str(root / "ca.crt"))
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.maximum_version = ssl.TLSVersion.TLSv1_3
    if with_certificate:
        context.load_cert_chain(str(root / "tls.crt"), str(root / "tls.key"))
    return context


class PodHTTPS(http.client.HTTPSConnection):
    def __init__(self, ip, context):
        super().__init__(HOST, 17443, timeout=8, context=context)
        self.target_ip = ip

    def connect(self):
        connection = socket.create_connection((self.target_ip, 17443), 8)
        try:
            self.sock = self._context.wrap_socket(connection, server_hostname=HOST)
        except Exception:
            connection.close()
            raise


def request(ip, path, *, role="ui", body=None, token=None, certificate=True):
    connection = PodHTTPS(ip, tls_context(role, certificate))
    headers = {"Accept-Encoding": "identity", "Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    try:
        try:
            connection.request("POST" if body is not None else "GET", path,
                               body=json.dumps(body) if body is not None else None, headers=headers)
        except BrokenPipeError:
            # TLS 1.3 can defer certificate_required until the first I/O. Read
            # the peer alert; a plain broken pipe is never authentication proof.
            if connection.sock is not None:
                connection.sock.recv(1)
            raise
        response = connection.getresponse()
        data = response.read(MAXIMUM + 1)
        if len(data) > MAXIMUM:
            raise ProbeFailure("http_response_size")
        try:
            payload = json.loads(data)
        except ValueError:
            payload = None
        return response.status, payload
    finally:
        connection.close()


def redis(host, *args, sentinel=False):
    if host not in REDIS_HOSTS:
        raise ProbeFailure("redis_discovered_host")
    key = "sentinel-password" if sentinel else "auth"
    password = (Path("/secrets/redis") / key).read_text().strip()
    context = tls_context("redis-client")
    with socket.create_connection((host, 26379 if sentinel else 6379), 8) as raw:
        with context.wrap_socket(raw, server_hostname=host) as secure:
            with secure.makefile("rb") as stream:
                for command in (("AUTH", password), args):
                    parts = [str(part).encode() for part in command]
                    wire = b"*" + str(len(parts)).encode() + b"\r\n"
                    wire += b"".join(b"$" + str(len(part)).encode() + b"\r\n" + part + b"\r\n" for part in parts)
                    secure.sendall(wire)
                    reply = read_resp(stream)
                return reply


def is_root(status, payload):
    return (status == 200 and isinstance(payload, dict) and not payload.get("err")
            and isinstance(payload.get("dat"), dict) and payload["dat"].get("username") == "root")


def is_vector_one(status, payload):
    if status != 200 or not isinstance(payload, dict) or payload.get("status") != "success":
        return False
    data = payload.get("data")
    if not isinstance(data, dict) or data.get("resultType") != "vector":
        return False
    result = data.get("result")
    return (isinstance(result, list) and len(result) == 1 and isinstance(result[0], dict)
            and isinstance(result[0].get("value"), list) and len(result[0]["value"]) == 2
            and result[0]["value"][1] == "1")


class Probe:
    def __init__(self):
        self.checks = {}
        self.stage = "initialization"
        self.not_tested = []

    def check(self, name, condition):
        self.stage = name
        self.checks[name] = bool(condition)
        if not condition:
            raise ProbeFailure(name)

    def master(self):
        masters = [redis(host, "SENTINEL", "get-master-addr-by-name", "n9e", sentinel=True)
                   for host in REDIS_HOSTS]
        self.check("sentinels_agree_verified_dns_master", all(
            isinstance(reply, list) and len(reply) == 2 and reply[0] in REDIS_HOSTS
            and reply[1] == "6379" and reply == masters[0] for reply in masters))
        return masters[0][0]

    def redis_health(self):
        self.stage = "redis_topology"
        master = self.master()
        for index, host in enumerate(REDIS_HOSTS):
            quorum = redis(host, "SENTINEL", "CKQUORUM", "n9e", sentinel=True)
            self.check(f"sentinel_{index}_quorum", isinstance(quorum, str) and quorum.startswith("OK 3 "))
            rows = redis(host, "SENTINEL", "REPLICAS", "n9e", sentinel=True)
            peers = [dict(zip(row[::2], row[1::2])) for row in rows]
            self.check(f"sentinel_{index}_two_healthy_replicas", len(peers) == 2 and
                {peer.get("ip") for peer in peers} == set(REDIS_HOSTS) - {master} and all(
                    not ({"s_down", "o_down", "disconnected"} & set(peer.get("flags", "").split(",")))
                    and peer.get("master-link-status") == "ok" for peer in peers))
            info = redis(host, "INFO", "replication")
            fields = dict(line.split(":", 1) for line in info.splitlines() if ":" in line)
            expected = ({"role": "master", "connected_slaves": "2"} if host == master else
                        {"role": "slave", "master_host": master, "master_link_status": "up", "master_sync_in_progress": "0"})
            self.check(f"redis_{index}_actual_replication", all(fields.get(k) == v for k, v in expected.items()))

    def boundaries(self, ip, label):
        self.stage = label + "_boundaries"
        try:
            request(ip, "/ping", certificate=False)
        except ssl.SSLError as error:
            rejected = error.reason == "TLSV13_ALERT_CERTIFICATE_REQUIRED"
        else:
            rejected = False
        self.check(label + "_no_client_certificate_tls_rejected", rejected)
        self.check(label + "_ui_cannot_read_metrics", request(ip, "/metrics")[0] == 403)
        self.check(label + "_wrong_role_cannot_read_ui", request(ip, "/ping", role="wrong")[0] == 403)
        try:
            with socket.create_connection((ip, 17000), 3):
                reachable = True
        except ConnectionRefusedError:
            reachable = False
        self.check(label + "_native_listener_not_exposed", not reachable)
        self.check(label + "_monitor_get_metrics", request(ip, "/metrics", role="monitor")[0] == 200)
        self.check(label + "_monitor_cannot_read_ui", request(ip, "/ping", role="monitor")[0] == 403)
        self.check(label + "_monitor_cannot_post_metrics", request(ip, "/metrics", role="monitor", body={})[0] == 403)

    def frontend(self, ip, label, lock):
        from frontend_probe import verify_frontend

        def fetch(path, maximum, include_body):
            connection = PodHTTPS(ip, tls_context("ui"))
            try:
                connection.request("GET", path, headers={"Accept-Encoding": "identity"})
                response = connection.getresponse()
                if response.getheader("Content-Encoding", "identity") != "identity":
                    raise ProbeFailure("frontend_encoding")
                digest, size, body = hashlib.sha256(), 0, bytearray()
                while chunk := response.read(65536):
                    size += len(chunk)
                    if size > maximum:
                        raise ProbeFailure("frontend_size")
                    digest.update(chunk)
                    if include_body:
                        body.extend(chunk)
                return {"status": response.status, "content_type": response.headers.get_content_type(),
                        "sha256": digest.hexdigest(), "bytes": size,
                        "body": body.decode() if include_body else None}
            finally:
                connection.close()
        self.stage = label + "_frontend"
        result = verify_frontend(fetch, lock)
        for key, value in result["checks"].items():
            self.check(label + "_" + key, value)

    def challenge(self, ip, label):
        self.stage = label + "_captcha_redis_roundtrip"
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            status, payload = request(ip, "/api/n9e/auth/captcha", body={})
            challenge = payload.get("dat") if isinstance(payload, dict) else None
            if (status == 200 and not payload.get("err") and isinstance(challenge, dict)
                    and isinstance(challenge.get("captchaid"), str) and challenge["captchaid"]
                    and str(challenge.get("imgdata", "")).startswith("data:image/png;base64,")):
                answer = redis(self.master(), "GET", challenge["captchaid"])
                if isinstance(answer, str) and re.fullmatch(r"-?\d+", answer):
                    self.check(label + "_captcha_redis_roundtrip", True)
                    return challenge["captchaid"], answer
            time.sleep(1)
        raise ProbeFailure(label + "_captcha_redis_roundtrip")

    def login(self, ip, label):
        status, payload = request(ip, "/api/n9e/auth/ifshowcaptcha")
        self.check(label + "_captcha_enabled", status == 200 and isinstance(payload, dict)
                   and isinstance(payload.get("dat"), dict) and payload["dat"].get("show") is True)
        password = Path("/secrets/admin/password").read_text().strip()
        for correct in (False, True):
            challenge, answer = self.challenge(ip, label)
            status, payload = request(ip, "/api/n9e/auth/login", body={"username": "root", "password": password,
                "captchaid": challenge, "verifyvalue": answer if correct else "not-the-generated-answer"})
            data = payload.get("dat") if isinstance(payload, dict) else None
            token = data.get("access_token") if isinstance(data, dict) else None
            if not correct:
                self.check(label + "_wrong_captcha_rejected", status == 200 and not token
                           and "incorrect verification code" in str(payload.get("err", "")))
            else:
                self.check(label + "_real_login", status == 200 and not payload.get("err")
                           and isinstance(token, str) and bool(token))
                return token

    def run(self, targets, lock):
        self.redis_health()
        ips = targets["pod_ips"]
        for index, ip in enumerate(ips):
            label = f"pod_{index}"
            self.boundaries(ip, label)
            self.frontend(ip, label, lock)
            token = self.login(ip, label)
            for peer, destination in enumerate(ips):
                self.check(f"pod_{index}_jwt_valid_on_pod_{peer}", is_root(*request(
                    destination, "/api/n9e/self/profile", token=token)))
            if "datasource_id" in targets:
                path = f"/api/n9e/proxy/{targets['datasource_id']}/api/v1/query?query=vector%281%29"
                self.check(label + "_actual_vm_query_vector_one", is_vector_one(*request(ip, path, token=token)))
            else:
                self.not_tested.append(label + "_actual_vm_query")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--targets", required=True, type=Path)
    parser.add_argument("--lock", required=True, type=Path)
    args = parser.parse_args()
    probe = Probe()
    result = {"scope": "new alerting read/authentication only; no faults or configuration writes"}
    try:
        targets = validate_targets(json.loads(args.targets.read_text()))
        probe.run(targets, json.loads(args.lock.read_text()))
        result["status"] = "passed"
    except Exception as error:
        # Do not print exception text, bodies, credentials, captcha IDs or JWTs.
        result.update(status="failed", stage=probe.stage, error_type=type(error).__name__)
    result.update(checks=probe.checks, not_tested=probe.not_tested)
    print(json.dumps(result, sort_keys=True))
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
