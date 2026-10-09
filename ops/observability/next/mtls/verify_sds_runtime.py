#!/usr/bin/env python3
"""Builder-only Envoy 1.39.3 leaf rotation acceptance, no Kubernetes writes.

Use: python3 verify_sds_runtime.py --execute --output /root/evidence/sds.json
A private internal Docker network has no host ports. Generated keys are private
and temporary. The result contains public certificate serials and counters only.
"""

import argparse
import http.client
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import tempfile
import time

from sds import transform_bootstrap


IMAGE = "harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865"
TYPE = "type.googleapis.com/envoy."
ROLE = "spiffe://observability.raytrain.local/victoriametrics/vmagent"
DNS = "sds-canary.victoriametrics.svc"
LABEL = "raytrain.sds-lab"


class LabError(Exception):
    pass


def command(argv, check=True):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=60, check=False)
    if check and result.returncode:
        raise LabError(f"{Path(argv[0]).name} failed; private diagnostics withheld")
    return result


def eventually(operation, timeout=30):
    deadline = time.monotonic() + timeout
    last = "condition false"
    while time.monotonic() < deadline:
        try:
            value = operation()
            if value:
                return value
        except (OSError, http.client.HTTPException) as error:
            last = type(error).__name__
            if isinstance(error, ssl.SSLError):
                last += ": " + str(error.reason)
            elif isinstance(error, OSError):
                last += ": errno=" + str(error.errno)
        time.sleep(0.25)
    raise LabError("Bounded runtime condition did not pass (" + last + ")")


def readable_directory(path):
    path.mkdir(mode=0o750, parents=True, exist_ok=True)
    os.chown(path, 0, 65534)
    path.chmod(0o750)
    return path


def readable_file(path, content):
    path.write_text(content)
    os.chown(path, 0, 65534)
    path.chmod(0o640)


def issue(root, name, serial, role, server=False):
    target = root / name
    target.mkdir(mode=0o700)
    extensions = target / "extensions.cnf"
    usages = "serverAuth" if server else "clientAuth"
    extensions.write_text(f"basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage={usages}\nsubjectAltName=DNS:{DNS},URI:{role}\n")
    command(["openssl", "req", "-new", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
             "-subj", "/CN=" + name, "-keyout", str(target / "tls.key"), "-out", str(target / "csr.pem")])
    command(["openssl", "x509", "-req", "-in", str(target / "csr.pem"), "-CA", str(root / "ca.crt"),
             "-CAkey", str(root / "ca.key"), "-set_serial", str(serial), "-days", "2", "-sha256",
             "-extfile", str(extensions), "-out", str(target / "tls.crt")])
    (target / "tls.key").chmod(0o600)
    shutil.copyfile(root / "ca.crt", target / "ca.crt")
    return target


def project(mount, revision, material, key_material=None):
    """Model kubelet AtomicWriter: stable leaf symlinks, atomic ..data swap."""
    readable_directory(mount)
    directory = readable_directory(mount / (".." + revision))
    for filename in ("tls.crt", "tls.key", "ca.crt"):
        source = key_material if filename == "tls.key" and key_material else material
        readable_file(directory / filename, (source / filename).read_text())
        destination = mount / filename
        if not destination.is_symlink():
            destination.symlink_to("..data/" + filename)
    temporary = mount / "..data_tmp"
    temporary.symlink_to(directory.name)
    os.replace(temporary, mount / "..data")


def common_tls(directory, san_type, name):
    return {"tls_params": {"tls_minimum_protocol_version": "TLSv1_3", "tls_maximum_protocol_version": "TLSv1_3"},
            "tls_certificates": [{"certificate_chain": {"filename": directory + "/tls.crt"},
                                  "private_key": {"filename": directory + "/tls.key"}}],
            "validation_context": {"trusted_ca": {"filename": directory + "/ca.crt"},
                                   "match_typed_subject_alt_names": [{"san_type": san_type, "matcher": {"exact": name}}]}}


def manager(name, route, authenticated):
    filters = []
    if authenticated:
        permissions = [{"url_path": {"path": {"exact": path}}} for path in ("/healthz", "/proxy")]
        filters.append({"name": "envoy.filters.http.rbac", "typed_config": {
            "@type": TYPE + "extensions.filters.http.rbac.v3.RBAC", "rules": {"action": "ALLOW", "policies": {
                "role-and-path": {"permissions": [{"or_rules": {"rules": permissions}}],
                                  "principals": [{"authenticated": {"principal_name": {"exact": ROLE}}}]}}}}})
    filters.append({"name": "envoy.filters.http.router", "typed_config": {"@type": TYPE + "extensions.filters.http.router.v3.Router"}})
    host = {"name": name, "domains": ["*"], "routes": [{"match": {"prefix": "/"}, **route}]}
    if authenticated:
        host["response_headers_to_add"] = [{"header": {"key": "x-peer-serial", "value": "%DOWNSTREAM_PEER_SERIAL%"}, "append_action": "OVERWRITE_IF_EXISTS_OR_ADD"}]
    return {"name": "envoy.filters.network.http_connection_manager", "typed_config": {
        "@type": TYPE + "extensions.filters.network.http_connection_manager.v3.HttpConnectionManager",
        "stat_prefix": name, "codec_type": "HTTP1", "route_config": {"name": name, "virtual_hosts": [host]}, "http_filters": filters}}


def fixture_bootstrap():
    downstream = {"@type": TYPE + "extensions.transport_sockets.tls.v3.DownstreamTlsContext",
                  "require_client_certificate": True, "disable_stateless_session_resumption": True,
                  "disable_stateful_session_resumption": True, "common_tls_context": common_tls("/server", "URI", ROLE)}
    upstream = {"@type": TYPE + "extensions.transport_sockets.tls.v3.UpstreamTlsContext", "sni": DNS,
                "common_tls_context": common_tls("/client", "DNS", DNS)}
    address = lambda port: {"socket_address": {"address": "0.0.0.0", "port_value": port}}
    return {"admin": {"address": {"pipe": {"path": "/diagnostics/admin.sock", "mode": 0o600}}}, "static_resources": {
        "listeners": [
            {"name": "server", "address": address(18443), "filter_chains": [{
                "transport_socket": {"name": "envoy.transport_sockets.tls", "typed_config": downstream},
                "filters": [manager("server", {"direct_response": {"status": 200, "body": {"inline_string": "ok"}}}, True)]}]},
            {"name": "local-client", "address": address(18080), "filter_chains": [{
                "filters": [manager("local-client", {"route": {"cluster": "mtls-server", "timeout": "5s"}}, False)]}]},
        ], "clusters": [{"name": "mtls-server", "type": "STATIC", "connect_timeout": "2s",
            "typed_extension_protocol_options": {"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": {
                "@type": TYPE + "extensions.upstreams.http.v3.HttpProtocolOptions",
                "common_http_protocol_options": {"max_requests_per_connection": 1},
                "explicit_http_config": {"http_protocol_options": {}}}},
            "transport_socket": {"name": "envoy.transport_sockets.tls", "typed_config": upstream},
            "load_assignment": {"cluster_name": "mtls-server", "endpoints": [{"lb_endpoints": [{"endpoint": {
                "address": {"socket_address": {"address": "127.0.0.1", "port_value": 18443}}}}]}]}}]}}


class TLSConnection(http.client.HTTPSConnection):
    def connect(self):
        self.sock = self._context.wrap_socket(socket.create_connection((self.host, self.port), self.timeout), server_hostname=DNS)


def connection(address, ca, material=None):
    context = ssl.create_default_context(cafile=str(ca))
    context.minimum_version = context.maximum_version = ssl.TLSVersion.TLSv1_3
    if material:
        context.load_cert_chain(material / "tls.crt", material / "tls.key")
    return TLSConnection(address, 18443, context=context, timeout=3)


def get(connection, path="/healthz"):
    connection.request("GET", path)
    response = connection.getresponse()
    body = response.read(4096)
    return response.status, response.getheader("x-peer-serial"), body


def serial_at(address, ca, material):
    client = connection(address, ca, material)
    try:
        client.connect()
        serial = int(client.sock.getpeercert()["serialNumber"], 16)
        if client.sock.version() != "TLSv1.3" or get(client)[0] != 200:
            raise LabError("Valid TLS 1.3 identity did not succeed")
        return serial
    finally:
        client.close()


def rejected(address, ca, material):
    client = connection(address, ca, material)
    try:
        get(client)
    except ssl.SSLError as error:
        if "ALERT" in (error.reason or ""):
            return {"tls_alert": error.reason}
        raise LabError("Rejection was not an explicit TLS alert") from None
    finally:
        client.close()
    raise LabError("Wrong or missing client identity was accepted")


def proxy_serial(address):
    client = http.client.HTTPConnection(address, 18080, timeout=4)
    try:
        status, serial, _ = get(client, "/proxy")
        if status != 200 or not serial:
            return None
        return int(serial, 16)
    finally:
        client.close()


def counters(path):
    client = http.client.HTTPConnection("localhost", timeout=3)
    client.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.sock.settimeout(3)
    client.sock.connect(str(path))
    try:
        client.request("GET", "/stats?format=json")
        response = client.getresponse()
        if response.status != 200:
            raise LabError("Lab admin counters unavailable")
        return {item["name"]: item["value"] for item in json.loads(response.read())["stats"]
                if any(term in item["name"] for term in ("key_rotation_failed", "ssl_context_update_by_sds"))}
    finally:
        client.close()


def cleanup(container, network, owner):
    for kind, name in (("container", container), ("network", network)):
        result = command(["docker", kind, "inspect", name], check=False)
        if result.returncode:
            continue
        item = json.loads(result.stdout)[0]
        labels = item["Config"]["Labels"] if kind == "container" else item["Labels"]
        if labels.get(LABEL) != owner:
            raise LabError("Cleanup ownership mismatch")
        command(["docker", kind, "rm", *(["-f"] if kind == "container" else []), name])
    for kind, name in (("container", container), ("network", network)):
        if command(["docker", kind, "inspect", name], check=False).returncode == 0:
            raise LabError("Lab resource still exists after cleanup")



def capture_diagnostics(container, owner, root):
    """Only this fixture's runtime logs; strip generated private key material."""
    result = command(["docker", "inspect", container], check=False)
    if result.returncode:
        return {"container_exists": False}
    item = json.loads(result.stdout)[0]
    if item["Config"]["Labels"].get(LABEL) != owner:
        raise LabError("Diagnostics ownership mismatch")
    logs = command(["docker", "logs", "--tail", "60", container], check=False)
    content = logs.stdout + logs.stderr
    for key in (root / "pki").rglob("*.key"):
        content = content.replace(key.read_text(), "[REDACTED PRIVATE KEY]")
    content = re.sub(r"-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----",
                     "[REDACTED PRIVATE KEY]", content, flags=re.DOTALL)
    return {"running": item["State"]["Running"], "exit_code": item["State"]["ExitCode"],
            "restart_count": item["RestartCount"], "log_tail": content[-12000:]}


def run(root, report):
    owner = "rt-sds-" + secrets.token_hex(6)
    network, container = owner + "-net", owner + "-envoy"
    report["stage"] = "fixture-certificates"
    root.chmod(0o700)
    pki = root / "pki"
    pki.mkdir(mode=0o700)
    command(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
             "-days", "2", "-subj", "/CN=sds-runtime-fixture-ca", "-keyout", str(pki / "ca.key"), "-out", str(pki / "ca.crt")])
    (pki / "ca.key").chmod(0o600)
    leaves = {name: issue(pki, name, serial, role, server) for name, serial, role, server in (
        ("server1", 1001, ROLE + "-server", True), ("server2", 1002, ROLE + "-server", True),
        ("server3", 1003, ROLE + "-server", True), ("client1", 2001, ROLE, False),
        ("client2", 2002, ROLE, False), ("wrong-role", 3001, ROLE + "-wrong", False))}
    for name in ("server", "client"):
        project(root / name, "v1", leaves[name + "1"])
    config = readable_directory(root / "config")
    diagnostics = readable_directory(root / "diagnostics")
    os.chown(diagnostics, 65534, 65534)
    bootstrap, files = transform_bootstrap(fixture_bootstrap(), "/config")
    for filename, content in {"envoy.json": bootstrap, **files}.items():
        readable_file(config / filename, json.dumps(content))
    old = None
    try:
        report["stage"] = "start-envoy"
        command(["docker", "network", "create", "--internal", "--label", LABEL + "=" + owner, network])
        argv = ["docker", "run", "-d", "--name", container, "--network", network, "--label", LABEL + "=" + owner,
                "--cpus", "0.3", "--memory", "128m", "--pids-limit", "64", "--read-only", "--user", "65534:65534",
                "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,noexec,nosuid,size=8m"]
        for name in ("server", "client", "config", "diagnostics"):
            argv += ["-v", f"{root / name}:/{name}:" + ("rw" if name == "diagnostics" else "ro")]
        command([*argv, "--entrypoint", "/usr/local/bin/envoy", IMAGE, "-c", "/config/envoy.json",
                 "--concurrency", "1", "--disable-hot-restart", "--log-level", "warning"])
        before = json.loads(command(["docker", "inspect", container]).stdout)[0]
        address = before["NetworkSettings"]["Networks"][network]["IPAddress"]
        ca = pki / "ca.crt"
        report["stage"] = "initial-tls13-handshake"
        eventually(lambda: serial_at(address, ca, leaves["client1"]) == 1001)
        old = connection(address, ca, leaves["client1"])
        old.connect()
        old_socket = old.sock
        if get(old)[0] != 200:
            raise LabError("Initial persistent authorized request failed")
        report["checks"].append({"check": "initial-tls13-and-leaf", "serial": 1001})
        for role in ("wrong-role", None):
            report["checks"].append({"check": "initial-reject-" + (role or "anonymous"),
                                     **rejected(address, ca, leaves[role] if role else None)})
        report["stage"] = "downstream-secret-rotation"
        project(root / "server", "v2", leaves["server2"])
        eventually(lambda: serial_at(address, ca, leaves["client1"]) == 1002)
        if get(old)[0] != 200 or old.sock is not old_socket or int(old.sock.getpeercert()["serialNumber"], 16) != 1001:
            raise LabError("Existing authorized connection did not survive leaf rotation")
        report["checks"].append({"check": "downstream-renewed-without-restart-existing-connection-survives", "old_serial": 1001, "new_serial": 1002})
        if get(old, "/forbidden")[0] != 403:
            raise LabError("HTTP path RBAC changed during rotation")
        report["checks"].append({"check": "http-rbac-retained", "http_status": 403})
        report["stage"] = "initial-upstream-client-handshake"
        eventually(lambda: proxy_serial(address) == 2001)
        report["stage"] = "upstream-secret-rotation"
        project(root / "client", "v2", leaves["client2"])
        eventually(lambda: proxy_serial(address) == 2002)
        report["checks"].append({"check": "upstream-renewed-without-restart", "old_serial": 2001, "new_serial": 2002})
        for role in ("wrong-role", None):
            report["checks"].append({"check": "renewed-reject-" + (role or "anonymous"),
                                     **rejected(address, ca, leaves[role] if role else None)})
        report["stage"] = "invalid-pair-last-good-identity"
        project(root / "server", "invalid", leaves["server3"], leaves["server1"])
        eventually(lambda: sum(value for name, value in counters(diagnostics / "admin.sock").items() if "key_rotation_failed" in name) > 0)
        if serial_at(address, ca, leaves["client2"]) != 1002:
            raise LabError("Invalid key pair displaced the last valid TLS identity")
        report["stage"] = "recover-valid-pair"
        project(root / "server", "v3", leaves["server3"])
        eventually(lambda: serial_at(address, ca, leaves["client2"]) == 1003)
        report["checks"].append({"check": "invalid-pair-retains-last-good-then-recovers", "last_good_serial": 1002, "recovered_serial": 1003})
        after = json.loads(command(["docker", "inspect", container]).stdout)[0]
        if before["State"]["Pid"] != after["State"]["Pid"] or after["RestartCount"] != 0:
            raise LabError("Proxy restarted during filesystem reload acceptance")
        report["checks"].append({"check": "envoy-process-not-restarted", "restart_count": 0})
        report["counters"] = counters(diagnostics / "admin.sock")
        report["stage"] = "acceptance-passed"
    except Exception:
        report["envoy_diagnostics"] = capture_diagnostics(container, owner, root)
        raise
    finally:
        if old:
            old.close()
        cleanup(container, network, owner)
        report["cleanup"] = "own-container-and-network-absent"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--execute", action="store_true")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not args.execute or platform.system() != "Linux" or os.geteuid() != 0:
        parser.error("Explicit --execute on the approved Linux builder as root is required")
    output = args.output
    if not output.is_absolute() or output.exists() or any(parent.is_symlink() for parent in output.parents):
        parser.error("Choose a new absolute evidence path without symlink parents")
    os.umask(0o077)
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    report = {"image": IMAGE, "status": "failed", "checks": [], "scope": "temporary-leaf-identities-only"}
    try:
        with tempfile.TemporaryDirectory(prefix="raytrain-sds-") as temporary:
            run(Path(temporary), report)
        report["status"] = "passed"
    except Exception as error:
        report["error_type"] = type(error).__name__
        report["error"] = str(error) if isinstance(error, LabError) else "Private runtime diagnostics withheld"
    with output.open("x") as stream:
        json.dump(report, stream, indent=2)
        stream.write("\n")
    print(json.dumps({"status": report["status"], "checks": len(report["checks"]), "evidence": str(output)}))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
