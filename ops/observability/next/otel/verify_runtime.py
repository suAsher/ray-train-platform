"""Builder-only OTel acceptance module for the isolated CH runtime harness.

This module has no CLI and must never target Kubernetes or a live gateway.
The caller owns a fresh internal Docker network, leaf-only certificate mounts,
synthetic SQL credentials and cleanup of every container it starts.

Required context callbacks:
  start(name, image, args, mounts, aliases=(), env=None, memory=..., cpus=...,
        network_mode=None) -> owned container name
    mounts are (Path, container_path, writable_bool) tuples. The caller makes
    writable mount ownership suitable for its container UID.
  request(address, server_name, port, path, role, body=None) -> (status, payload)
    body is JSON bytes; non-None means POST. None role omits the client cert
    while still verifying the server certificate and server_name.
  wait(description, predicate, timeout=90), ip(container) -> bridge IP,
  port_closed(address, port) -> bool,
  sql(query, replica=0, role='query-admin') -> scalar text,
  run(argv, check=True) -> CompletedProcess.

No certificate bytes, credential values or native process logs are returned.
"""

import http.client
import json
import os
from pathlib import Path
import socket
import ssl
import time
import urllib.error
import uuid

import yaml


HERE = Path(__file__).resolve().parent
SERVER_NAME = "otel-gateway.observability-otel.svc.cluster.local"
CH_SERVER = "clickhouse-observability.observability-analytics.svc.cluster.local"
NETWORK_ERRORS = (OSError, http.client.HTTPException, urllib.error.URLError)


def _write(path, text):
    path.write_text(text, encoding="utf-8")
    # The containing workspace is private; the config itself has no secret.
    path.chmod(0o644)


def _prepare(ctx):
    base = Path(ctx["work"]) / ("otel-runtime-" + uuid.uuid4().hex[:12])
    base.mkdir(mode=0o700)
    queue = base / "queue"
    queue.mkdir(mode=0o700)
    os.chown(queue, 10001, 10001)
    collector = yaml.safe_load((HERE / "collector.yaml").read_text())
    exporter = collector["exporters"]["clickhouse/canary"]
    if exporter["endpoint"] != f"tcp://{CH_SERVER}:9440":
        raise AssertionError("OTel candidate no longer targets the reviewed CH alias")
    if exporter["password"] != "${env:CLICKHOUSE_PASSWORD}":
        raise AssertionError("OTel candidate must source its synthetic SQL password from env")
    _write(base / "collector.yaml", yaml.safe_dump(collector, sort_keys=False))
    return base, queue


def _start(ctx, base, queue):
    leaf = Path(ctx["leaf_dirs"]["otel-gateway"])
    native = ctx["start"](
        "otel-native-" + base.name.removeprefix("otel-runtime-"),
        ctx["images"]["otel"], ["--config=/etc/otelcol/config/collector.yaml"],
        [(base / "collector.yaml", "/etc/otelcol/config/collector.yaml", False),
         (queue, "/var/lib/otelcol", True), (leaf, "/etc/obs-mtls", False)],
        aliases=(SERVER_NAME,),
        env={"CLICKHOUSE_PASSWORD": ctx["ingest_password"], "GOMEMLIMIT": "768MiB"},
        memory="1g", cpus="1")
    address = ctx["ip"](native)
    proxy_text = (HERE / "proxy-otel.yaml").read_text().replace("__POD_IP__", address)
    proxy_path = base / "proxy-otel.yaml"
    _write(proxy_path, proxy_text)
    proxy = ctx["start"](
        "otel-proxy-" + base.name.removeprefix("otel-runtime-"),
        ctx["images"]["envoy"],
        ["-c", "/etc/envoy/config.yaml", "--concurrency", "1", "--disable-hot-restart"],
        [(proxy_path, "/etc/envoy/config.yaml", False), (leaf, "/etc/obs-mtls", False)],
        network_mode="container:" + native, memory="256m", cpus="0.25")
    return native, proxy, address


def _health(address, path, method="GET"):
    connection = http.client.HTTPConnection(address, 18080, timeout=3)
    try:
        connection.request(method, path)
        response = connection.getresponse()
        status = response.status
        body = response.read(4097)
        if len(body) > 4096:
            raise AssertionError("OTel status listener returned an oversized body")
        return status, body
    finally:
        connection.close()


def _healthy(address, expected=200):
    try:
        return _health(address, "/readyz")[0] == expected
    except NETWORK_ERRORS:
        return False


def _verify_status_only(address):
    for path in ("/readyz", "/livez"):
        status, body = _health(address, path)
        if status != 200 or body:
            raise AssertionError("OTel healthy status endpoint must return 200 without body")
        if _health(address, path, "POST")[0] != 404:
            raise AssertionError("OTel status endpoint accepted an unsupported method")
    for path in ("/", "/metrics", "/config_dump", "/stats", "/v1/logs", "/v1/traces"):
        status, body = _health(address, path)
        if status != 404 or body:
            raise AssertionError("OTel status listener exposed an unexpected route or body")


def _reject(ctx, address, port, path, role):
    try:
        ctx["request"](address, SERVER_NAME, port, path, role)
    except urllib.error.HTTPError:
        raise AssertionError(f"OTel port {port} completed TLS for a disallowed client identity") from None
    except NETWORK_ERRORS:
        return
    # A completed HTTP exchange means the role passed TLS. A 404 is not proof
    # that the exact-role mTLS check ran, so it must not satisfy this gate.
    raise AssertionError(f"OTel port {port} accepted a disallowed client identity")


def _assert_plaintext_rejected(address, port):
    connection = http.client.HTTPConnection(address, port, timeout=3)
    try:
        connection.request("GET", "/metrics")
        response = connection.getresponse()
        response.read(4096)
    except NETWORK_ERRORS:
        return
    finally:
        connection.close()
    raise AssertionError(f"OTel data port {port} answered plaintext HTTP")


def _verify_access(ctx, address):
    for port, path in ((4317, "/"), (4318, "/v1/logs")):
        for role in (None, "query-admin", "vmagent"):
            _reject(ctx, address, port, path, role)
        _assert_plaintext_rejected(address, port)
    for role in (None, "canary", "query-admin"):
        _reject(ctx, address, 8888, "/metrics", role)
    _assert_plaintext_rejected(address, 8888)
    if not ctx["port_closed"](address, 13133):
        raise AssertionError("OTel native health port is reachable outside its namespace")
    status, payload = ctx["request"](address, SERVER_NAME, 8888, "/metrics", "vmagent")
    if status != 200 or not payload:
        raise AssertionError("Authorized vmagent could not scrape OTel metrics")
    for path in ("/", "/readyz", "/config_dump", "/v1/logs"):
        status, _ = ctx["request"](address, SERVER_NAME, 8888, path, "vmagent")
        if status != 404:
            raise AssertionError("OTel metrics listener exposed a non-metrics path")
    status, _ = ctx["request"](address, SERVER_NAME, 8888, "/metrics", "vmagent", b"{}")
    if status != 404:
        raise AssertionError("OTel metrics listener accepted a write method")
    for path in ("/", "/metrics", "/config_dump"):
        status, _ = ctx["request"](address, SERVER_NAME, 4318, path, "canary")
        if status != 404:
            raise AssertionError("OTel HTTP receiver proxy exposed a non-OTLP path")


def _verify_grpc_transport(ctx, address):
    # Standard-library HTTP/2 preface is enough to verify the native gRPC
    # transport behind Envoy; OTLP record ingestion is exercised over HTTP.
    leaf = Path(ctx["leaf_dirs"]["canary"])
    context = ssl.create_default_context(cafile=str(leaf / "ca.crt"))
    context.load_cert_chain(str(leaf / "tls.crt"), str(leaf / "tls.key"))
    context.set_alpn_protocols(["h2"])
    with socket.create_connection((address, 4317), timeout=5) as connection:
        with context.wrap_socket(connection, server_hostname=SERVER_NAME) as stream:
            if stream.selected_alpn_protocol() != "h2":
                raise AssertionError("OTel gRPC listener did not negotiate HTTP/2")
            stream.sendall(b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" + b"\x00\x00\x00\x04\x00\x00\x00\x00\x00")
            header = b""
            while len(header) < 9:
                part = stream.recv(9 - len(header))
                if not part:
                    raise AssertionError("OTel gRPC transport closed before server SETTINGS")
                header += part
            if header[3] != 4 or int.from_bytes(header[5:9], "big") != 0:
                raise AssertionError("OTel gRPC transport returned no initial server SETTINGS")


def _attribute(key, value):
    return {"key": key, "value": {"stringValue": value}}


def _canary_payloads(run_id):
    trace_id, span_id = uuid.uuid4().hex, uuid.uuid4().hex[:16]
    now = time.time_ns()
    attrs = [_attribute("canary.run_id", run_id),
             _attribute("canary.marker", "builder-runtime"),
             _attribute("untrusted.attribute", "must-be-erased")]
    resource = {"attributes": [_attribute("service.name", "must-be-overwritten"),
                                _attribute("untrusted.resource", "must-be-erased")]}
    log = {"timeUnixNano": str(now), "observedTimeUnixNano": str(now),
           "severityNumber": 9, "severityText": "INFO",
           "body": {"stringValue": "isolated synthetic OTel runtime acceptance"},
           "traceId": trace_id, "spanId": span_id, "attributes": attrs}
    span = {"traceId": trace_id, "spanId": span_id, "name": "builder-runtime-canary",
            "kind": 1, "startTimeUnixNano": str(now),
            "endTimeUnixNano": str(now + 1000000), "attributes": attrs,
            "status": {"code": 1}}
    logs = {"resourceLogs": [{"resource": resource, "scopeLogs": [
        {"scope": {"name": "builder-runtime"}, "logRecords": [log]}]}]}
    traces = {"resourceSpans": [{"resource": resource, "scopeSpans": [
        {"scope": {"name": "builder-runtime"}, "spans": [span]}]}]}
    return trace_id, {"/v1/logs": logs, "/v1/traces": traces}


def _send_canary(ctx, address, payloads):
    for path, payload in payloads.items():
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        status, response = ctx["request"](address, SERVER_NAME, 4318, path, "canary", body)
        if status != 200:
            raise AssertionError(f"OTel synthetic {path} export was not accepted")
        result = json.loads(response or b"{}")
        partial = result.get("partialSuccess", {})
        rejected = partial.get("rejectedLogRecords", partial.get("rejectedSpans", 0))
        if int(rejected) != 0 or partial.get("errorMessage"):
            raise AssertionError("OTel returned partial-success rejection for the synthetic record")


def _count_queries(run_id, trace_id):
    result = {}
    for signal, attr in (("logs", "LogAttributes"), ("traces", "SpanAttributes")):
        result[signal] = (
            f"SELECT count() FROM observability_canary.otel_{signal} "
            f"WHERE {attr}['canary.run_id'] = '{run_id}' "
            "AND ServiceName = 'observability-canary' "
            "AND ResourceAttributes['raytrain.telemetry.source'] = 'synthetic-canary' "
            "AND NOT mapContains(ResourceAttributes, 'untrusted.resource') "
            f"AND NOT mapContains({attr}, 'untrusted.attribute') FORMAT TSVRaw")
    result["trace_lookup"] = (
        "SELECT count() FROM observability_canary.otel_traces_trace_id_ts "
        f"WHERE TraceId = '{trace_id}' FORMAT TSVRaw")
    return result


def _replica_counts(ctx, queries):
    return {f"replica_{replica}": {
        signal: int(ctx["sql"](query, replica=replica, role="query-admin").strip())
        for signal, query in queries.items()} for replica in (0, 1)}


def _verify_health_transition(ctx, native, address):
    # Pause only the isolated test Collector; its Envoy runs in another
    # container sharing the same network namespace and stays responsive.
    ctx["run"](["docker", "pause", native])
    try:
        ctx["wait"]("OTel local process failure produces status 503",
                    lambda: _healthy(address, 503), timeout=30)
    finally:
        ctx["run"](["docker", "unpause", native])
    ctx["wait"]("OTel local process resumes and produces status 200",
                lambda: _healthy(address), timeout=30)


def verify_otel(ctx):
    """Exercise this candidate on the caller's isolated Docker test network."""
    required = ("work", "network", "images", "leaf_dirs", "start", "request", "wait",
                "ip", "port_closed", "sql", "run", "ingest_password")
    if any(key not in ctx for key in required):
        raise ValueError("Incomplete isolated OTel runtime context")
    for role in ("otel-gateway", "canary", "query-admin", "vmagent"):
        if role not in ctx["leaf_dirs"]:
            raise ValueError(f"Missing synthetic leaf certificate for role {role}")
    base, queue = _prepare(ctx)
    native, proxy, address = _start(ctx, base, queue)
    ctx["wait"]("OTel proxy and loopback Collector are healthy",
                lambda: _healthy(address), timeout=90)
    _verify_status_only(address)
    _verify_access(ctx, address)
    _verify_grpc_transport(ctx, address)
    run_id = uuid.uuid4().hex
    trace_id, payloads = _canary_payloads(run_id)
    _send_canary(ctx, address, payloads)
    queries = _count_queries(run_id, trace_id)
    ctx["wait"]("Synthetic log, trace and trace lookup replicate to both CH nodes",
                lambda: all(count > 0 for node in _replica_counts(ctx, queries).values()
                            for count in node.values()), timeout=120)
    counts = _replica_counts(ctx, queries)
    _verify_health_transition(ctx, native, address)
    queue_files = [p for p in queue.rglob("*") if p.is_file() and p.stat().st_size > 0]
    if not queue_files:
        raise AssertionError("OTel persistent queue wrote no nonempty local storage files")
    return {"status": "passed", "run_id": run_id, "trace_id": trace_id,
            "containers": {"collector": native, "envoy": proxy},
            "canary_records_sent": {"logs": 1, "traces": 1}, "replica_counts": counts,
            "role_matrix": "canary OTLP and vmagent metrics accepted; other tested roles rejected",
            "grpc_transport": "canary mTLS and native HTTP/2 SETTINGS accepted",
            "otlp_ingestion_protocol": "HTTP/JSON; gRPC Export RPC not exercised",
            "plaintext_data_ports": "rejected", "native_health_port": "closed",
            "status_only_paths": "passed", "health_transition": "200 -> 503 -> 200",
            "persistent_queue_files": len(queue_files),
            "queue_restart_recovery": "not exercised; queue files retained for caller cleanup"}
