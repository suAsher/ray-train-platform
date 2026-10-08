"""Bounded synthetic protocol diagnostics; never retain credentials or payloads."""

import http.client
import json
import socket
import subprocess

from runtime_support import RuntimeCheckError, run


def varuint(value):
    result = bytearray()
    while value >= 128:
        result.append((value & 127) | 128)
        value >>= 7
    return bytes(result + bytes([value]))


def binary_string(value):
    data = value.encode()
    return varuint(len(data)) + data


def protocol_hops(sandbox, native, server_name):
    """Authenticate through the real proxy, independent of the native CLI/DNS.

    Hello uses revision 54429 before optional chunk/addendum negotiation. Only
    the response packet type is retained; the temporary password stays in RAM.
    This is a diagnostic, not a replacement for the actual native SQL gate.
    """
    address = sandbox.ip(native)
    username = "schema_admin"
    password = sandbox.passwords[username]
    record = {"server_name": server_name}
    sandbox.hop_results.append(record)
    for tls12 in (True, False):
        sandbox.stage = "native_hello_tls12" if tls12 else "native_hello_negotiated_tls"
        packet = (varuint(0) + binary_string("ClickHouse diagnostic") + varuint(26)
                  + varuint(8) + varuint(54429) + binary_string("")
                  + binary_string(username) + binary_string(password))
        with socket.create_connection((address, 9440), timeout=5) as sock:
            with sandbox.context("schema-admin", tls12=tls12).wrap_socket(
                    sock, server_hostname=server_name) as stream:
                stream.settimeout(8)
                stream.sendall(packet)
                response = stream.recv(1)
                record[sandbox.stage] = {"tls": stream.version(),
                                         "packet_type": response[0] if response else None}
                if response != b"\x00":
                    raise RuntimeCheckError("Native Hello did not return authenticated Server Hello")
    sandbox.stage = "http_sql_preflight"
    connection = http.client.HTTPSConnection(server_name, 8443,
                                            context=sandbox.context("schema-admin"), timeout=8)
    connection.sock = sandbox.context("schema-admin").wrap_socket(
        socket.create_connection((address, 8443), timeout=5), server_hostname=server_name)
    try:
        connection.request("POST", "/", body=b"SELECT 1", headers={
            "X-ClickHouse-User": username, "X-ClickHouse-Key": password})
        response = connection.getresponse()
        record["http_sql"] = {"status": response.status,
                              "expected_scalar": response.read(65536).strip() == b"1"}
        if record["http_sql"] != {"status": 200, "expected_scalar": True}:
            raise RuntimeCheckError("Authenticated HTTP SQL preflight failed")
    finally:
        connection.close()


def capture_client_timeout(sandbox, container):
    """Record state and kernel wait/socket metadata, never argv or environment."""
    commands = {
        "state": ["docker", "inspect", "--format", "{{json .State}}", container],
        "processes": ["docker", "top", container, "-eo", "pid,ppid,stat,comm,wchan"],
        "kernel": ["docker", "exec", container, "sh", "-c",
                   "cat /proc/1/wchan; head -24 /proc/1/status; "
                   "cat /proc/1/net/tcp /proc/1/net/tcp6 /proc/1/net/udp"],
    }
    diagnostics = sandbox.work / "private-diagnostics"
    diagnostics.mkdir(mode=0o700, exist_ok=True)
    evidence = {}
    for name, command in commands.items():
        try:
            result = run(command, check=False, timeout=5)
            evidence[name] = {"returncode": result.returncode,
                              "output": (result.stdout + result.stderr)[-32768:]}
        except subprocess.TimeoutExpired:
            evidence[name] = {"error": "timeout"}
    output = diagnostics / (container + "-timeout-state.json")
    output.write_text(json.dumps(evidence, indent=2) + "\n")
    output.chmod(0o600)


def private_output(sandbox, container, stdout, stderr):
    diagnostics = sandbox.work / "private-diagnostics"
    diagnostics.mkdir(mode=0o700, exist_ok=True)
    parts = [part.decode(errors="replace") if isinstance(part, bytes) else part or ""
             for part in (stdout, stderr)]
    output = diagnostics / (container + "-result.log")
    output.write_text("".join(parts)[-65536:])
    output.chmod(0o600)
