#!/usr/bin/env python3
"""Builder-only, separately invoked gRPC Export and file_storage crash recovery.

Run with --output NEW_ABSOLUTE_PRIVATE_DIRECTORY on the approved Linux builder.
Imports the existing CH full verifier to create a fresh, owned internal Docker
lab. A separate candidate Collector and Envoy share a stable sleep-anchor netns.
Only this lab's CH0 proxy and Collector app are stopped. Nothing targets a live
gateway. The sender never retries a request. Result IDs are synthetic data.

The minimal protobuf wire fixture follows opentelemetry-proto v1.9.0:
https://github.com/open-telemetry/opentelemetry-proto/tree/v1.9.0/opentelemetry/proto
The pinned Collector v0.162.0 accepts these stable logs/traces Export messages.
curl's HTTP/2 implementation handles framing/HPACK; this is a complete unary RPC,
not just ALPN negotiation or an HTTP/2 preface.
"""

import argparse
from collections import Counter
import json
import math
import os
from pathlib import Path
import re
import struct
import sys
import time
import traceback
import uuid


HERE = Path(__file__).resolve().parent
SERVER_NAME = "otel-gateway.observability-otel.svc.cluster.local"
METHODS = {
    "logs": "/opentelemetry.proto.collector.logs.v1.LogsService/Export",
    "traces": "/opentelemetry.proto.collector.trace.v1.TraceService/Export",
}


def _varint(value):
    result = bytearray()
    while value > 127:
        result.append((value & 127) | 128)
        value >>= 7
    result.append(value)
    return bytes(result)


def _bytes(number, value):
    value = value.encode() if isinstance(value, str) else value
    return _varint(number << 3 | 2) + _varint(len(value)) + value


def _int(number, value):
    return _varint(number << 3) + _varint(value)


def _fixed64(number, value):
    return _varint(number << 3 | 1) + struct.pack("<Q", value)


def _attribute(key, value):
    return _bytes(1, key) + _bytes(2, _bytes(1, str(value)))


def _validate_record(record):
    for key, length in (("run_id", 32), ("trace_id", 32), ("span_id", 16)):
        value = record[key]
        if not re.fullmatch("[0-9a-f]{" + str(length) + "}", value) or int(value, 16) == 0:
            raise ValueError("Synthetic IDs must be nonzero canonical hex")
    if not re.fullmatch(r"\d{1,4}", record["sequence"]):
        raise ValueError("Invalid synthetic sequence")
    if not 0 < record["timestamp_ns"] < 2 ** 64 - 1000000:
        raise ValueError("Invalid synthetic timestamp")


def encode_export(signal, records):
    """Encode the actual OTLP Export request, excluding its gRPC envelope."""
    if signal not in METHODS or not records or len(records) > 64:
        raise ValueError("Only bounded nonempty synthetic logs/traces are allowed")
    encoded = []
    for record in records:
        _validate_record(record)
        attrs = [_attribute("canary.run_id", record["run_id"]),
                 _attribute("canary.sequence", record["sequence"]),
                 _attribute("canary.marker", "grpc-process-recovery")]
        trace, span = bytes.fromhex(record["trace_id"]), bytes.fromhex(record["span_id"])
        now = record["timestamp_ns"]
        if signal == "logs":
            item = (_fixed64(1, now) + _fixed64(11, now) + _int(2, 9) + _bytes(3, "INFO")
                    + _bytes(5, _bytes(1, "isolated gRPC durable queue canary"))
                    + _bytes(9, trace) + _bytes(10, span))
            item += b"".join(_bytes(6, attr) for attr in attrs)
        else:
            item = (_bytes(1, trace) + _bytes(2, span) + _bytes(5, "grpc-process-recovery")
                    + _int(6, 1) + _fixed64(7, now) + _fixed64(8, now + 1000000)
                    + _bytes(15, _int(3, 1)))
            item += b"".join(_bytes(9, attr) for attr in attrs)
        encoded.append(_bytes(2, item))
    scope = _bytes(1, _bytes(1, "builder-protocol-recovery")) + b"".join(encoded)
    return _bytes(1, _bytes(2, scope))


def _read_varint(raw, offset):
    value = 0
    for shift in range(0, 70, 7):
        if offset >= len(raw):
            raise AssertionError("Truncated protobuf response")
        byte = raw[offset]
        offset += 1
        value |= (byte & 127) << shift
        if byte < 128:
            return value, offset
    raise AssertionError("Oversized protobuf varint")


def _fields(raw):
    fields, offset = {}, 0
    while offset < len(raw):
        key, offset = _read_varint(raw, offset)
        number, wire = key >> 3, key & 7
        if number == 0:
            raise AssertionError("Invalid protobuf field")
        if wire == 0:
            value, offset = _read_varint(raw, offset)
        elif wire == 2:
            length, offset = _read_varint(raw, offset)
            if offset + length > len(raw):
                raise AssertionError("Truncated protobuf field")
            value, offset = raw[offset:offset + length], offset + length
        else:
            raise AssertionError("Unexpected Export response field type")
        if number in fields:
            raise AssertionError("Duplicate Export response field")
        fields[number] = (wire, value)
    return fields


def validate_grpc_response(headers, body):
    """Require curl-captured HTTP/2 headers, DATA frame and success trailers."""
    initial, separator, trailers = headers.replace(b"\r\n", b"\n").partition(b"\n\n")
    lines = [line.strip().lower() for line in initial.splitlines()]
    if not separator or not lines or lines[0] != b"http/2 200":
        raise AssertionError("Export did not complete with HTTP/2 200")
    if not any(line.startswith(b"content-type: application/grpc") for line in lines):
        raise AssertionError("Export response is not gRPC")
    if any(line.startswith(b"grpc-status:") for line in lines):
        raise AssertionError("Expected unary response DATA followed by trailers")
    statuses = re.findall(rb"(?im)^grpc-status:\s*(\d+)\s*$", trailers)
    if statuses != [b"0"]:
        raise AssertionError("Export success trailer missing or nonzero")
    if len(body) < 5 or body[0] != 0 or int.from_bytes(body[1:5], "big") != len(body) - 5:
        raise AssertionError("Export must return exactly one uncompressed gRPC message")
    response = _fields(body[5:])
    if response and (set(response) != {1} or response[1][0] != 2):
        raise AssertionError("Unexpected Export response")
    partial = _fields(response[1][1]) if response else {}
    if (set(partial) - {1, 2} or partial.get(1, (0, 0)) != (0, 0)
            or partial.get(2, (2, b"")) != (2, b"")):
        raise AssertionError("Export rejected records or returned a partial-success warning")
    return {"grpc_status": 0, "rejected": 0}


def queue_sizes(metrics, metric="otelcol_exporter_queue_size"):
    """Missing, ambiguous or invalid gauges can never stand in for zero."""
    result = {}
    pattern = re.compile(r"^" + re.escape(metric) + r"\{([^}]*)\}\s+(\S+)(?:\s+\d+)?$")
    for line in metrics.splitlines():
        match = pattern.fullmatch(line)
        if not match:
            continue
        labels = dict(re.findall(r'(\w+)="([^"\\]*)"', match[1]))
        if labels.get("exporter") != "clickhouse/canary":
            continue
        signal = labels.get("data_type")
        if signal not in METHODS or signal in result:
            raise AssertionError("Unexpected or duplicated exporter signal gauge")
        try:
            value = float(match[2])
        except ValueError:
            raise AssertionError("Invalid exporter queue gauge") from None
        if not math.isfinite(value) or value < 0:
            raise AssertionError("Invalid exporter queue gauge")
        result[signal] = value
    if set(result) != set(METHODS):
        raise AssertionError("Both logs and traces exporter gauges are required")
    return result


def assert_exact_records(rows, records):
    keys = ("sequence", "trace_id", "span_id")
    expected = Counter(tuple(record[key] for key in keys) for record in records)
    actual = Counter(tuple(row[key] for key in keys) for row in rows)
    if actual != expected:
        raise AssertionError("Synthetic ID/sequence rows lost, duplicated or changed")
    return len(rows)


def validate_owned(name, prefix, registered, record):
    if (name not in registered or not name.startswith(prefix + "-")
            or record["labels"].get("observability-test") != prefix
            or record["network_mode"] != prefix):
        raise AssertionError("Refusing to mutate an unowned or nonisolated container")


def _state(sandbox, name):
    template = ('{"name":{{json .Name}},"id":{{json .Id}},"labels":{{json .Config.Labels}},'
                '"network_mode":{{json .HostConfig.NetworkMode}},"pid":{{.State.Pid}},'
                '"started_at":{{json .State.StartedAt}},"running":{{.State.Running}}}')
    state = json.loads(sandbox.run(["docker", "inspect", "--format", template, name]).stdout)
    mode = state["network_mode"]
    if mode.startswith("container:"):
        owner = json.loads(sandbox.run(["docker", "inspect", "--format", template,
                                       mode.removeprefix("container:")]).stdout)
        validate_owned(owner["name"].lstrip("/"), sandbox.prefix, sandbox.containers, owner)
        checked = {**state, "network_mode": owner["network_mode"]}
    else:
        checked = state
    validate_owned(name, sandbox.prefix, sandbox.containers, checked)
    return state


def _mutate(sandbox, name, verb, *args):
    _state(sandbox, name)
    sandbox.run(["docker", verb, *args, name])


def _records(count):
    run_id, now = uuid.uuid4().hex, time.time_ns()
    return [{"run_id": run_id, "sequence": str(index), "trace_id": uuid.uuid4().hex,
             "span_id": uuid.uuid4().hex[:16], "timestamp_ns": now + index}
            for index in range(count)]


def _grpc_export(sandbox, base, address, signal, records):
    body = encode_export(signal, records)
    stem = signal + "-" + records[0]["run_id"] + "-" + records[0]["sequence"]
    diagnostics = sandbox.work / "private-diagnostics"
    diagnostics.mkdir(mode=0o700, exist_ok=True)
    request = base / (stem + ".request")
    headers, response = (diagnostics / (stem + suffix) for suffix in (".headers", ".response"))
    request.write_bytes(b"\0" + len(body).to_bytes(4, "big") + body)
    request.chmod(0o600)
    leaf = sandbox.leaves["canary"]
    result = sandbox.run([
        "curl", "--disable", "--noproxy", "*", "--http2", "--silent", "--show-error",
        "--connect-timeout", "5", "--max-time", "20", "--tlsv1.2",
        "--cacert", leaf / "ca.crt", "--cert", leaf / "tls.crt", "--key", leaf / "tls.key",
        "--resolve", f"{SERVER_NAME}:4317:{address}", "--header", "Content-Type: application/grpc",
        "--header", "TE: trailers", "--data-binary", "@" + str(request),
        "--dump-header", headers, "--output", response, "--write-out", "%{http_version}",
        f"https://{SERVER_NAME}:4317{METHODS[signal]}"], check=False, timeout=25)
    if result.returncode or result.stdout.strip() != "2":
        raise AssertionError("Synthetic gRPC Export transport failed")
    if headers.stat().st_size > 65536 or response.stat().st_size > 65536:
        raise AssertionError("Synthetic gRPC response exceeded bounded evidence size")
    return validate_grpc_response(headers.read_bytes(), response.read_bytes())


def _query(sandbox, harness, signal, records, replica):
    attr = {"logs": "LogAttributes", "traces": "SpanAttributes"}[signal]
    run_id = records[0]["run_id"]
    if not re.fullmatch(r"[0-9a-f]{32}", run_id):
        raise ValueError("Invalid run ID")
    sql = (f"SELECT {attr}['canary.sequence'] AS sequence, TraceId AS trace_id, SpanId AS span_id "
           f"FROM observability_canary.otel_{signal} WHERE {attr}['canary.run_id']='{run_id}' "
           "AND ServiceName='observability-canary' "
           "AND ResourceAttributes['raytrain.telemetry.source']='synthetic-canary' FORMAT JSONEachRow")
    raw = harness.sql_client(sandbox, sql, replica=replica, role="query-admin")
    return [json.loads(row) for row in raw.splitlines() if row]


def _all_records(sandbox, harness, records):
    counts = {}
    for replica in (0, 1):
        counts[str(replica)] = {signal: assert_exact_records(
            _query(sandbox, harness, signal, records, replica), records) for signal in METHODS}
    return counts


def _metrics(sandbox, address):
    status, payload = sandbox.request(address, SERVER_NAME, 8888, "/metrics", "vmagent")
    if status != 200:
        raise AssertionError("Cannot read authorized Collector queue metrics")
    return payload.decode()


def _wait(sandbox, description, probe, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = probe()
            if value:
                return value
        except (AssertionError, OSError, sandbox.RuntimeCheckError):
            pass
        time.sleep(2)
    raise AssertionError("Timed out: " + description)


def _start_collector(sandbox, otel):
    ctx = {"work": sandbox.work}
    base, queue = otel._prepare(ctx)
    # Stable network namespace: SIGKILL of the app cannot orphan its Envoy.
    anchor = sandbox.start("recovery-netns", sandbox.images["clickhouse"], ["infinity"], [],
                           entrypoint="/bin/sleep", memory="32m", cpus="0.05")
    address = sandbox.ip(anchor)
    leaf = sandbox.leaves["otel-gateway"]
    native = sandbox.start("recovery-collector", sandbox.images["otel"],
                           ["--config=/etc/otelcol/config/collector.yaml"],
                           [(base / "collector.yaml", "/etc/otelcol/config/collector.yaml", False),
                            (queue, "/var/lib/otelcol", True), (leaf, "/etc/obs-mtls", False)],
                           env={"CLICKHOUSE_PASSWORD": sandbox.passwords["otel_ingest"],
                                "GOMEMLIMIT": "768MiB"}, network_mode="container:" + anchor,
                           memory="1g", cpus="1")
    proxy_path = base / "proxy-otel.yaml"
    otel._write(proxy_path, (HERE / "proxy-otel.yaml").read_text().replace("__POD_IP__", address))
    proxy = sandbox.start("recovery-envoy", sandbox.images["envoy"],
                          ["-c", "/etc/envoy/config.yaml", "--concurrency", "1", "--disable-hot-restart"],
                          [(proxy_path, "/etc/envoy/config.yaml", False), (leaf, "/etc/obs-mtls", False)],
                          network_mode="container:" + anchor, memory="256m", cpus="0.25")
    _wait(sandbox, "recovery Collector health", lambda: otel._healthy(address), 90)
    return base, queue, anchor, native, proxy, address


def _positive_queue(sandbox, address):
    values = queue_sizes(_metrics(sandbox, address))
    return values if all(value > 0 for value in values.values()) else None


def _empty_queue(sandbox, address):
    metrics = _metrics(sandbox, address)
    values = queue_sizes(metrics)
    # v0.162.0 internal/obs_report_sender.go:74 pins exporter + data_type on
    # inFlightMetricAttr; internal/queue/obs_queue.go:41 does so on queue gauges.
    inflight = queue_sizes(metrics, "otelcol_exporter_in_flight_requests")
    return {"queue": values, "in_flight": inflight} if not any([*values.values(), *inflight.values()]) else None


def _restart_app(sandbox, native, proxy, anchor, address, otel):
    before = _state(sandbox, native)
    stable = {name: _state(sandbox, name) for name in (proxy, anchor)}
    _mutate(sandbox, native, "kill", "--signal", "SIGKILL")
    _mutate(sandbox, native, "start")
    _wait(sandbox, "restarted Collector healthy while downstream blocked",
          lambda: otel._healthy(address), 90)
    after = _state(sandbox, native)
    if not after["running"] or before["pid"] == after["pid"] or before["started_at"] == after["started_at"]:
        raise AssertionError("Collector process did not actually restart")
    for name, previous in stable.items():
        current = _state(sandbox, name)
        if (current["pid"], current["started_at"]) != (previous["pid"], previous["started_at"]):
            raise AssertionError("An app-only restart changed the proxy or network anchor")
    return before, after


def verify_protocol_recovery(sandbox, harness, otel):
    sandbox.stage = "grpc_recovery_collector_start"
    base, queue, anchor, native, proxy, address = _start_collector(sandbox, otel)
    sandbox.stage = "grpc_unary_export_and_exact_rows"
    baseline = _records(3)
    rpc = {signal: _grpc_export(sandbox, base, address, signal, baseline) for signal in METHODS}
    baseline_counts = _wait(sandbox, "gRPC IDs on both replicas", lambda: _all_records(sandbox, harness, baseline))
    _wait(sandbox, "baseline queues drained", lambda: _empty_queue(sandbox, address))
    blocked = sandbox.prefix + "-ch-0-envoy"
    records = _records(12)
    sandbox.stage = "persistent_queue_downstream_blocked"
    _mutate(sandbox, blocked, "stop", "--time", "5")
    _wait(sandbox, "configured CH0 exporter endpoint closed",
          lambda: harness.port_closed(sandbox.ip(sandbox.native_ch[0]), 9440), 30)
    # More than two batch flushes exhaust candidate num_consumers=2. The
    # spacing crosses its 1s flush timeout so records remain in the disk queue.
    for record in records:
        for signal in METHODS:
            _grpc_export(sandbox, base, address, signal, [record])
        time.sleep(1.2)
    queued_before = _wait(sandbox, "both signal queues contain accepted records",
                          lambda: _positive_queue(sandbox, address))
    for signal in METHODS:
        if _query(sandbox, harness, signal, records, 1):
            raise AssertionError("Recovery fixture reached downstream before crash")
    sandbox.stage = "collector_SIGKILL_and_same_storage_restart"
    before, after = _restart_app(sandbox, native, proxy, anchor, address, otel)
    queued_after = _wait(sandbox, "both persistent queues recovered after process replacement",
                         lambda: _positive_queue(sandbox, address))
    for signal in METHODS:
        if _query(sandbox, harness, signal, records, 1):
            raise AssertionError("Downstream became available before explicit restore")
    sandbox.stage = "restore_downstream_and_drain_recovered_records"
    _mutate(sandbox, blocked, "start")
    recovered = _wait(sandbox, "all accepted crash-period IDs on both replicas",
                      lambda: _all_records(sandbox, harness, records), 180)
    drained = _wait(sandbox, "persistent queues and export retries drained",
                    lambda: _empty_queue(sandbox, address), 120)
    # Re-read after draining to catch replays/duplicates following the first read.
    recovered = _all_records(sandbox, harness, records)
    return {"status": "passed", "protocol": "gRPC HTTP/2 protobuf unary Export",
            "baseline_rpc": rpc, "baseline_replica_counts": baseline_counts,
            "baseline_ids": baseline, "accepted_before_crash_per_signal": len(records),
            "recovered_ids": records, "recovered_replica_counts": recovered,
            "queue_before_crash": queued_before, "queue_after_restart": queued_after,
            "queue_drained": drained, "collector_process_before": before["pid"],
            "collector_process_after": after["pid"], "collector_restart_signal": "SIGKILL",
            "proxy_and_network_anchor_unchanged": True,
            "sender_retries": 0, "exact_id_loss_or_duplicates": 0,
            "scope": "isolated Docker process-crash test; not a production HA claim"}


def _load_harness():
    sys.path.insert(0, str(HERE.parent / "clickhouse"))
    import runtime_support
    harness = runtime_support.load_module("obs_protocol_ch", HERE.parent / "clickhouse" / "verify-runtime.py")
    otel = runtime_support.load_module("obs_protocol_otel", HERE / "verify_runtime.py")
    return runtime_support, harness, otel


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or sys.platform != "linux":
        raise ValueError("Run only on the approved Linux Docker builder as root")
    if not args.output.is_absolute() or args.output.exists() or args.output.is_symlink():
        raise ValueError("Output must be a new absolute directory")
    support, harness, otel = _load_harness()
    os.umask(0o077)
    args.output.mkdir(mode=0o700, parents=True)
    sandbox = support.Sandbox(args.output)
    sandbox.phase, sandbox.run, sandbox.RuntimeCheckError = "full", support.run, support.RuntimeCheckError
    evidence = {"status": "failed", "scope": "fresh disposable Docker CH/Keeper/OTel lab"}
    try:
        sandbox.setup()
        evidence["existing_full_baseline"] = harness.verify(sandbox)
        evidence["protocol_recovery"] = verify_protocol_recovery(sandbox, harness, otel)
        evidence["status"] = "passed"
    except BaseException as exc:
        evidence["failure"] = {"stage": sandbox.stage, "type": type(exc).__name__,
                               "frames": [{"file": Path(frame.filename).name, "line": frame.lineno,
                                           "function": frame.name}
                                          for frame in traceback.extract_tb(exc.__traceback__)]}
        raise
    finally:
        try:
            sandbox.cleanup()
            remaining = support.run(["docker", "ps", "-aq", "--filter",
                                     "label=observability-test=" + sandbox.prefix]).stdout.strip()
            networks = support.run(["docker", "network", "ls", "--filter",
                                    "label=observability-test=" + sandbox.prefix,
                                    "--format", "{{.ID}}"]).stdout.strip()
            if remaining or networks:
                raise AssertionError("Owned runtime cleanup left test resources")
            evidence["owned_cleanup"] = "verified"
        except Exception:
            evidence["status"] = "failed"
            evidence["owned_cleanup"] = "failed"
            raise
        finally:
            (args.output / "result.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        sys.exit("Isolated protocol/recovery verification failed: " + type(error).__name__)
