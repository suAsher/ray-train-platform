#!/usr/bin/env python3
"""Builder-only VM/Alertmanager protocol acceptance in a private Docker network.

Run from an immutable reviewed candidate as root on the approved Linux builder:
  python3 verify-runtime.py --execute --output /root/<evidence>/vm-runtime.json
No Kubernetes calls, host ports, production credentials, registry push or GPU.
All certificate material and data are temporary; only a redacted report remains.
"""

import argparse
from datetime import datetime, timedelta, timezone
import http.client
import importlib.util
import json
import os
from pathlib import Path
import platform
import secrets
import socket
import ssl
import struct
import sys
import tempfile
import time
import traceback

import yaml

from runtime_lab import DockerLab, LabError, auth_users, await_http_status, command, eventually, private_file


VM_ROOT = Path(__file__).resolve().parent
NS = "observability-metrics"


def load_credentials():
    spec = importlib.util.spec_from_file_location("obs_mtls_credentials", VM_ROOT.parent / "mtls/credentials.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def writable_data(lab, name):
    path = lab.directory / "data" / name
    path.mkdir(mode=0o750, parents=True)
    os.chown(path, 65534, 65534)
    return path


def flags(values):
    return [f"-{key}={value}" for key, value in values.items()]


def start_reloader(lab, pod, name, config_path, port):
    return lab.peer(pod, name + "-reloader", lab.image("configReloader"),
                    ["-http.listenAddr=127.0.0.1:8435", "-watched-dir=/watched",
                     f"-reload-url=http://127.0.0.1:{port}/-/reload"],
                    [(config_path.parent, "/watched", "ro")])


def varint(value):
    encoded = bytearray()
    while value > 127:
        encoded.append((value & 127) | 128)
        value >>= 7
    return bytes(encoded) + bytes([value])


def field(number, payload):
    return varint(number << 3 | 2) + varint(len(payload)) + payload


def remote_write_sample():
    label = field(1, b"__name__") + field(2, b"raytrain_mtls_canary")
    sample = bytes([9]) + struct.pack("<d", 1.0) + bytes([16]) + varint(int(time.time() * 1000))
    message = field(1, field(1, label) + field(2, sample))
    # Snappy raw stream with one literal, no compression dependency required.
    length = len(message) - 1
    if length < 60:
        literal = bytes([length << 2])
    else:
        size = (length.bit_length() + 7) // 8
        literal = bytes([(59 + size) << 2]) + length.to_bytes(size, "little")
    return varint(len(message)) + literal + message


def plain_request(pod, port, path="/metrics"):
    connection = http.client.HTTPConnection(pod["ip"], port, timeout=3)
    try:
        connection.request("GET", path)
        result = connection.getresponse()
        return result.status, result.read(2048)
    finally:
        connection.close()


def authenticated_hop(lab, pod, role, port, name):
    result = await_http_status(
        lambda: lab.request(pod, port, "/metrics", role="vmagent", server=role),
        200, name,
    )
    lab.results.append({"check": name, "result": "pass", **result})


def paused_storage_health(lab, pod):
    """Exercise a no-traffic health cluster; never pause proxies or other labs."""
    app = pod["app"]
    allowed = {f"{lab.prefix}-storage-{index}-app" for index in range(3)}
    if app not in lab.containers or app not in allowed:
        raise LabError("Health pause target is not this lab's storage app")
    inspected = json.loads(command(["docker", "inspect", app]).stdout)
    if len(inspected) != 1 or inspected[0]["Config"]["Labels"].get("raytrain.mtls-lab") != lab.prefix:
        raise LabError("Health pause target ownership label mismatch")

    def statuses():
        return tuple(plain_request(pod, 18081, path)[0] for path in ("/healthz", "/app-healthz"))

    eventually(lambda: statuses() == (200, 200), timeout=20)
    started = time.monotonic()
    try:
        command(["docker", "pause", app])
        # Timeouts, resets, 200, or any other status cannot satisfy this gate.
        eventually(lambda: statuses() == (503, 503), timeout=20)
        failure_seconds = round(time.monotonic() - started, 3)
        if failure_seconds > 20:
            raise LabError("Paused storage health did not fail within 20 seconds")
    finally:
        command(["docker", "unpause", app])
    eventually(lambda: statuses() == (200, 200), timeout=20)
    lab.results.append({"check": "storage-pause-health-fails-within-20s-and-recovers",
                        "result": "pass", "failure_seconds": failure_seconds})


def vm_cluster(lab):
    cluster = next(item["spec"] for item in lab.objects if item["kind"] == "VMCluster")
    stores = []
    for index in range(3):
        args = {**cluster["vmstorage"]["extraArgs"], "storage.minFreeDiskSpaceBytes": "100MiB",
                "storageDataPath": "/data", "retentionPeriod": "1d"}
        alias = f"vmstorage-raytrain-next-{index}.vmstorage-raytrain-next.{NS}.svc"
        pod = lab.native(f"storage-{index}", lab.image("storage"), flags(args), [alias],
                         [(writable_data(lab, f"storage-{index}"), "/data", "rw")])
        lab.proxy("vmstorage", pod, f"storage-{index}")
        stores.append(pod)
    components = {}
    for role, key in (("vminsert", "insert"), ("vmselect", "select")):
        args = {**cluster[role]["extraArgs"], "replicationFactor": "2"}
        pod = lab.native(key, lab.image(key), flags(args), [f"{role}-raytrain-next.{NS}.svc"])
        lab.proxy(role, pod, key)
        components[role] = pod
    # Native health checks do not establish peer DNS, TLS or RBAC readiness.
    # Verify each inbound TLS hop explicitly before the full end-to-end write.
    for index, storage in enumerate(stores):
        authenticated_hop(lab, storage, "vmstorage", 8482, f"hop-storage-{index}-mtls-metrics")
        for port, role in ((8400, "vminsert"), (8401, "vmselect")):
            status, _ = tls_handshake(lab, storage, port, role, "vmstorage." + NS + ".svc", "vmauth")
            if status != 200:
                raise LabError("Authorized storage RPC TLS handshake failed")
            lab.results.append({"check": f"hop-storage-{index}-rpc-{port}-authorized-tls", "result": "pass"})
    for role, port in (("vminsert", 8480), ("vmselect", 8481)):
        authenticated_hop(lab, components[role], role, port, f"hop-{role}-mtls-metrics")
    payload = remote_write_sample()
    write_headers = {"Content-Type": "application/x-protobuf", "Content-Encoding": "snappy",
                     "X-Prometheus-Remote-Write-Version": "0.1.0"}
    direct_write = await_http_status(
        lambda: lab.request(components["vminsert"], 8480, "/insert/1001/prometheus/api/v1/write",
                            role="vmauth", server="vminsert", method="POST", body=payload, headers=write_headers),
        204, "hop-authorized-gateway-identity-to-insert",
    )
    lab.results.append({"check": "hop-authorized-gateway-identity-to-insert", "result": "pass", **direct_write})
    direct_read = await_http_status(
        lambda: lab.request(components["vmselect"], 8481, "/select/1001/prometheus/api/v1/query?query=raytrain_mtls_canary",
                            role="vmauth", server="vmselect"),
        200, "hop-authorized-gateway-identity-to-select",
    )
    lab.results.append({"check": "hop-authorized-gateway-identity-to-select", "result": "pass", **direct_read})
    tokens = {name: secrets.token_urlsafe(32) for name in ("self-read", "self-write")}
    lab.redactions.extend(tokens.values())
    users = auth_users(lab.objects, tokens)
    auth_file = private_file(lab.directory / "auth-config" / "auth.yaml", yaml.safe_dump({"users": users}), True)
    auth_spec = next(item["spec"] for item in lab.objects if item["kind"] == "VMAuth")
    pod = lab.native("auth", lab.image("auth"), flags({**auth_spec["extraArgs"], "auth.config": "/config/auth.yaml"}),
                     [f"vmauth-raytrain-next.{NS}.svc"], [(auth_file, "/config/auth.yaml", "ro")])
    proxy = lab.proxy("vmauth", pod, "auth")
    start_reloader(lab, pod, "auth", auth_file, 8428)
    eventually(lambda: plain_request(pod, 18081, "/healthz")[0] == 200)
    authenticated_hop(lab, pod, "vmauth", 8428, "hop-auth-mtls-internal-metrics")
    headers = {**write_headers, "Authorization": "Bearer " + tokens["self-write"]}
    # A success here also establishes insert -> storage RPC; retry only transient
    # startup transport/5xx, never rejected identity/token or an invalid payload.
    written = await_http_status(
        lambda: lab.request(pod, 8427, "/api/v1/write", "canary", method="POST", body=payload, headers=headers),
        204, "hop-auth-insert-storage-write",
    )
    lab.results.append({"check": "hop-auth-insert-storage-write", "result": "pass", **written})
    read_headers = {"Authorization": "Bearer " + tokens["self-read"]}

    def query():
        status, body = lab.request(pod, 8427, "/api/v1/query?query=raytrain_mtls_canary", headers=read_headers)
        return status == 200 and json.loads(body).get("data", {}).get("result")

    eventually(query)
    lab.results.append({"check": "three-storage-rf2-write-query", "result": "pass"})
    lab.denied("auth-missing-certificate", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", role=None, headers=read_headers))
    lab.denied("auth-bad-token", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", headers={"Authorization": "Bearer invalid"}))
    lab.denied("auth-wrong-role", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", role="vmstorage", headers=read_headers))
    lab.denied("auth-write-role-cannot-query", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", role="vmagent", headers=read_headers))
    lab.denied("auth-read-token-cannot-write", lambda: lab.request(pod, 8427, "/api/v1/write", "canary", method="POST", body=payload,
               headers={**headers, "Authorization": "Bearer " + tokens["self-read"]}))
    lab.denied("auth-write-token-cannot-query", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", "canary", headers={
               "Authorization": "Bearer " + tokens["self-write"]}))
    lab.denied("auth-plaintext", lambda: plain_request(pod, 8427))
    lab.denied("auth-wrong-server-dns", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", server="vmstorage", headers=read_headers))
    lab.issue("canary", alternate=True)
    lab.denied("auth-wrong-ca", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", role="foreign-canary", headers=read_headers))
    lab.issue_expired_canary()
    lab.denied("auth-expired-cert", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", role="expired-canary", headers=read_headers))
    for storage in stores:
        lab.closed(storage, [28400, 28401, 8435])
        lab.denied("storage-anonymous", lambda: lab.request(storage, 8482, "/metrics", role=None, server="vmstorage"))
        lab.denied("storage-agent-cannot-snapshot", lambda: lab.request(storage, 8482, "/snapshot/create", role="vmagent", server="vmstorage"))
        for port in (8400, 8401):
            lab.denied(f"storage-rpc-{port}-wrong-role", lambda: tls_handshake(lab, storage, port, "vmagent", "vmstorage." + NS + ".svc", "vmauth"))
            lab.denied(f"storage-rpc-{port}-anonymous", lambda: tls_handshake(lab, storage, port, None, "vmstorage." + NS + ".svc", "vmauth"))
            lab.denied(f"storage-rpc-{port}-plaintext", lambda: plain_request(storage, port))
        status, _ = lab.request(storage, 8482, "/metrics", role="vmagent", server="vmstorage")
        if status != 200:
            raise LabError("Authorized metrics scrape failed")
    for role, ports in (("vminsert", [18400, 18401, 18402]), ("vmselect", [19400, 19401, 19402])):
        lab.closed(components[role], ports)
    lab.closed(pod, [18480, 18481, 8435])
    command(["docker", "stop", "-t", "2", proxy])
    lab.denied("proxy-down-fails-closed", lambda: lab.request(pod, 8427, "/api/v1/query?query=up", headers=read_headers))
    lab.closed(pod, [8427, 8428])
    # Rotation requires an explicit proxy restart; static Envoy file paths are not SDS.
    lab.issue("vmauth")
    command(["docker", "start", proxy])
    eventually(query)
    command(["docker", "restart", stores[1]["app"]])
    eventually(lambda: plain_request(stores[1], 18081, "/healthz")[0] == 200)
    eventually(query)
    lab.results.append({"check": "proxy-fail-closed-certificate-rotation-storage-restart", "result": "pass"})
    paused_storage_health(lab, stores[0])
    eventually(query)


def tls_handshake(lab, pod, port, role, server_name, ca_role):
    context = ssl.create_default_context(cafile=str(lab.materials[ca_role] / "ca.crt"))
    if role:
        material = lab.materials[role]
        context.load_cert_chain(material / "tls.crt", material / "tls.key")
    with socket.create_connection((pod["ip"], port), timeout=3) as raw:
        with context.wrap_socket(raw, server_hostname=server_name) as secure:
            # TLS1.3 server-side client-cert rejection can arrive after handshake.
            secure.sendall(b"\x00")
            try:
                data = secure.recv(1)
            except socket.timeout:
                return 200, b""
            if not data:
                return 200, b""
            return 200, data


def gossip_handshake(lab, pod, role, server_name="alertmanager.observability-metrics.svc"):
    return tls_handshake(lab, pod, 9094, role, server_name, "alertmanager-gossip")


def alertmanagers(lab):
    peers = []
    config = private_file(lab.directory / "am-config" / "alertmanager.yaml", yaml.safe_dump({
        "route": {"receiver": "blackhole-canary"}, "receivers": [{"name": "blackhole-canary"}]}), True)
    material = lab.materials["alertmanager-gossip"]
    tls = private_file(lab.directory / "am-config" / "gossip.yaml", yaml.safe_dump({
        "tls_server_config": {"cert_file": "/gossip/tls.crt", "key_file": "/gossip/tls.key",
                              "client_ca_file": "/gossip/ca.crt", "client_auth_type": "RequireAndVerifyClientCert", "min_version": "TLS13"},
        "tls_client_config": {"ca_file": "/gossip/ca.crt", "cert_file": "/gossip/tls.crt", "key_file": "/gossip/tls.key",
                              "server_name": f"alertmanager.{NS}.svc", "insecure_skip_verify": False}}), True)
    aliases = [f"vmalertmanager-raytrain-next-{i}.vmalertmanager-raytrain-next.{NS}.svc" for i in range(3)]
    for index in range(3):
        args = ["--config.file=/config/alertmanager.yaml", "--storage.path=/data", "--web.listen-address=127.0.0.1:9093",
                # AM's TLS transport requires a parseable IP, unlike net.Listen.
                "--cluster.listen-address=0.0.0.0:9094", "--cluster.tls-config=/config/gossip.yaml", "--cluster.settle-timeout=15s",
                *["--cluster.peer=" + alias + ":9094" for ordinal, alias in enumerate(aliases) if ordinal != index]]
        pod = lab.native(f"am-{index}", lab.image("alertmanager"), args, [aliases[index]],
                         [(config, "/config/alertmanager.yaml", "ro"), (tls, "/config/gossip.yaml", "ro"),
                          (material, "/gossip", "ro"), (writable_data(lab, f"am-{index}"), "/data", "rw")], "256m")
        lab.proxy("alertmanager", pod, f"am-{index}")
        start_reloader(lab, pod, f"am-{index}", config, 9093)
        peers.append(pod)

    def converged():
        for pod in peers:
            status, body = lab.request(pod, 9093, "/api/v2/status", server="alertmanager")
            if status != 200 or len(json.loads(body).get("cluster", {}).get("peers", [])) != 3:
                return False
        return True

    eventually(converged, 120)
    now = datetime.now(timezone.utc)
    silence = json.dumps({"matchers": [{"name": "alertname", "value": "RuntimeMTLSCanary", "isRegex": False, "isEqual": True}],
                          "startsAt": now.isoformat(), "endsAt": (now + timedelta(minutes=5)).isoformat(),
                          "createdBy": "private-mtls-test", "comment": "isolated protocol acceptance"}).encode()
    status, body = lab.request(peers[0], 9093, "/api/v2/silences", server="alertmanager", method="POST",
                               body=silence, headers={"Content-Type": "application/json"})
    if status != 200:
        raise LabError(f"Synthetic silence creation failed, HTTP {status}")
    silence_id = json.loads(body)["silenceID"]

    def replicated():
        for pod in peers:
            status, body = lab.request(pod, 9093, "/api/v2/silences", server="alertmanager")
            if status != 200 or not any(item["id"] == silence_id for item in json.loads(body)):
                return False
        return True

    eventually(replicated)
    for pod in peers:
        lab.closed(pod, [8435])
        lab.denied("am-http-anonymous", lambda: lab.request(pod, 9093, "/api/v2/status", role=None, server="alertmanager"))
        lab.denied("am-gossip-anonymous", lambda: gossip_handshake(lab, pod, None))
        lab.denied("am-gossip-main-ca-rejected", lambda: gossip_handshake(lab, pod, "vmagent"))
        lab.denied("am-gossip-wrong-server-dns", lambda: gossip_handshake(lab, pod, "alertmanager-gossip", "vmstorage." + NS + ".svc"))
        pid = json.loads(command(["docker", "inspect", pod["name"]]).stdout)[0]["State"]["Pid"]
        for protocol in ("udp", "udp6"):
            for row in Path(f"/proc/{pid}/net/{protocol}").read_text().splitlines()[1:]:
                if int(row.split()[1].rsplit(":", 1)[1], 16) == 9094:
                    raise LabError("Alertmanager retained a UDP gossip listener")
    command(["docker", "restart", peers[1]["app"]])
    eventually(converged, 120)
    eventually(replicated)
    lab.results.append({"check": "three-am-tls-gossip-silence-replication-restart-no-udp", "result": "pass"})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true", help="create temporary private Docker test resources")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not args.execute or platform.system() != "Linux" or os.geteuid() != 0:
        parser.error("requires explicit --execute on the root-owned Linux builder")
    if args.output.exists():
        parser.error("output already exists; choose a new evidence path")
    os.umask(0o077)
    report = {"status": "failed", "scope": "private Docker lab; no Kubernetes", "checks": []}
    try:
        with tempfile.TemporaryDirectory(prefix="raytrain-vm-mtls-") as temporary:
            lab = DockerLab(temporary, VM_ROOT, load_credentials())
            try:
                lab.prepare()
                vm_cluster(lab)
                alertmanagers(lab)
                report["status"] = "passed"
            except Exception:
                report["container_diagnostics"] = lab.diagnostics()
                raise
            finally:
                report["checks"] = lab.results
                lab.cleanup()
    except Exception as error:
        report["status"] = "failed"
        report["failure_type"] = type(error).__name__
        report["failure_location"] = [{"file": Path(frame.filename).name, "line": frame.lineno,
                                        "function": frame.name}
                                       for frame in traceback.extract_tb(error.__traceback__)[-4:]]
        if isinstance(error, LabError):
            report["failure"] = str(error)
    args.output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    private_file(args.output, json.dumps(report, indent=2) + "\n")
    print(json.dumps({"status": report["status"], "checks": len(report["checks"]), "evidence": str(args.output)}))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
