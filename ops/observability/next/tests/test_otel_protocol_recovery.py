"""Builder-only contracts for actual gRPC Export and durable queue recovery."""

import importlib.util
from pathlib import Path
import struct
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "otel" / "verify_protocol_recovery.py"
# curl --dump-header stores initial headers and subsequent HTTP/2 trailers;
# --output stores the binary DATA separately. Trailers have no HTTP status line.
HEADERS = b"HTTP/2 200\r\ncontent-type: application/grpc\r\n\r\ngrpc-status: 0\r\ngrpc-message: \r\n"
RECORD = {"run_id": "a" * 32, "sequence": "0", "trace_id": "1" * 32,
          "span_id": "2" * 16, "timestamp_ns": 1780000000000000000}


def decode_fields(raw):
    """Independent fixture decoder; checks actual protobuf wire field numbers."""
    def varint(offset):
        value, shift = 0, 0
        while True:
            byte = raw[offset]
            value |= (byte & 127) << shift
            offset += 1
            if byte < 128:
                return value, offset
            shift += 7

    fields, offset = {}, 0
    while offset < len(raw):
        key, offset = varint(offset)
        field, wire = key >> 3, key & 7
        if wire == 0:
            value, offset = varint(offset)
        elif wire == 1:
            value = struct.unpack("<Q", raw[offset:offset + 8])[0]
            offset += 8
        elif wire == 2:
            length, offset = varint(offset)
            value = raw[offset:offset + length]
            offset += length
        else:
            raise AssertionError("Unexpected fixture wire type")
        fields.setdefault(field, []).append(value)
    return fields


class ProtocolRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SCRIPT.is_file(), "Missing real gRPC / process-restart recovery harness")
        spec = importlib.util.spec_from_file_location("otel_protocol_recovery", SCRIPT)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)

    def record_fields(self, signal):
        encoded = self.module.encode_export(signal, [RECORD])
        resource = decode_fields(decode_fields(encoded)[1][0])
        scope = decode_fields(resource[2][0])
        return decode_fields(scope[2][0])

    def test_logs_export_contains_native_trace_span_and_timestamp_fields(self):
        fields = self.record_fields("logs")
        self.assertEqual(bytes.fromhex(RECORD["trace_id"]), fields[9][0])
        self.assertEqual(bytes.fromhex(RECORD["span_id"]), fields[10][0])
        self.assertEqual(RECORD["timestamp_ns"], fields[1][0])
        self.assertEqual(RECORD["timestamp_ns"], fields[11][0])
        self.assertEqual(9, fields[2][0])
        attrs = {decode_fields(item)[1][0].decode():
                 decode_fields(decode_fields(item)[2][0])[1][0].decode()
                 for item in fields[6]}
        self.assertEqual(RECORD["run_id"], attrs["canary.run_id"])
        self.assertEqual("0", attrs["canary.sequence"])

    def test_traces_export_contains_native_ids_and_valid_span_timing(self):
        fields = self.record_fields("traces")
        self.assertEqual(bytes.fromhex(RECORD["trace_id"]), fields[1][0])
        self.assertEqual(bytes.fromhex(RECORD["span_id"]), fields[2][0])
        self.assertEqual(RECORD["timestamp_ns"], fields[7][0])
        self.assertGreater(fields[8][0], fields[7][0])
        self.assertTrue(fields[5][0])
        self.assertEqual(1, fields[6][0])

    def test_exports_reject_invalid_ids_signal_or_empty_records(self):
        for signal, records in (("metrics", [RECORD]), ("logs", []),
                                ("logs", [{**RECORD, "trace_id": "bad"}]),
                                ("traces", [{**RECORD, "span_id": "0" * 16}])):
            with self.subTest(signal=signal, records=records):
                with self.assertRaises(ValueError):
                    self.module.encode_export(signal, records)

    def test_grpc_success_requires_http2_frame_and_status(self):
        self.assertEqual({"grpc_status": 0, "rejected": 0},
                         self.module.validate_grpc_response(HEADERS, b"\0" * 5))
        self.assertEqual({"grpc_status": 0, "rejected": 0},
                         self.module.validate_grpc_response(HEADERS, b"\0\0\0\0\2\x0a\0"))

    def test_grpc_handshake_or_http_success_alone_cannot_pass(self):
        cases = [(HEADERS.replace(b"grpc-status: 0\r\n", b""), b"\0" * 5),
                 (b"HTTP/2 200\r\ncontent-type: application/grpc\r\ngrpc-status: 0\r\n\r\n", b"\0" * 5),
                 (HEADERS.replace(b"grpc-status: 0", b"grpc-status: 7"), b"\0" * 5),
                 (HEADERS.replace(b"HTTP/2", b"HTTP/1.1"), b"\0" * 5),
                 (HEADERS.replace(b"application/grpc", b"application/json"), b"\0" * 5),
                 (HEADERS, b""), (HEADERS, b"\0" * 10),
                 (HEADERS, b"\1\0\0\0\0"), (HEADERS, b"\0\0\0\0\3\x0a")]
        for headers, payload in cases:
            with self.subTest(headers=headers, payload=payload):
                with self.assertRaises(AssertionError):
                    self.module.validate_grpc_response(headers, payload)

    def test_grpc_partial_rejection_or_warning_fails(self):
        for response in (b"\x0a\x02\x08\x01", b"\x0a\x03\x12\x01x"):
            frame = b"\0" + len(response).to_bytes(4, "big") + response
            with self.assertRaises(AssertionError):
                self.module.validate_grpc_response(HEADERS, frame)

    def test_queue_metrics_require_both_scoped_signal_series(self):
        metrics = ('# HELP otelcol_exporter_queue_size queue\n'
                   'otelcol_exporter_queue_size{exporter="other",data_type="logs"} 99\n'
                   'otelcol_exporter_queue_size{exporter="clickhouse/canary",data_type="logs"} 42\n'
                   'otelcol_exporter_queue_size{data_type="traces",exporter="clickhouse/canary"} 17\n')
        self.assertEqual({"logs": 42, "traces": 17}, self.module.queue_sizes(metrics))
        self.assertEqual({"logs": 42, "traces": 17},
                         self.module.queue_sizes(metrics.replace(" 42", " 4.2e1").replace(" 17", " 17.0")))
        self.assertEqual({"logs": 0, "traces": 0},
                         self.module.queue_sizes(metrics.replace(" 42", " 0").replace(" 17", " 0")))
        # Locked v0.162.0 also labels in-flight requests by exporter + data_type.
        name = "otelcol_exporter_in_flight_requests"
        self.assertEqual({"logs": 42, "traces": 17},
                         self.module.queue_sizes(metrics.replace("otelcol_exporter_queue_size", name), name))

    def test_absent_or_invalid_queue_metric_is_not_empty_queue(self):
        row = 'otelcol_exporter_queue_size{exporter="clickhouse/canary",data_type="logs"} '
        for metrics in ("", row + "0", row + "NaN", row + "Inf", row + "-1", row + "0\n" + row + "0"):
            with self.subTest(metrics=metrics):
                with self.assertRaises(AssertionError):
                    self.module.queue_sizes(metrics)

    def test_precise_records_match_ids_and_sequences(self):
        rows = [{"sequence": "0", "trace_id": RECORD["trace_id"], "span_id": RECORD["span_id"]}]
        self.assertEqual(1, self.module.assert_exact_records(rows, [RECORD]))
        for bad in ([], rows + rows, [{**rows[0], "trace_id": "f" * 32}],
                    [{**rows[0], "sequence": "1"}]):
            with self.subTest(rows=bad):
                with self.assertRaises(AssertionError):
                    self.module.assert_exact_records(bad, [RECORD])

    def test_mutation_target_must_be_owned_registered_and_isolated(self):
        prefix, name = "obs-ch-mtls-123", "obs-ch-mtls-123-otel"
        record = {"labels": {"observability-test": prefix}, "network_mode": prefix}
        self.module.validate_owned(name, prefix, [name], record)
        for changed in ({**record, "labels": {}}, {**record, "network_mode": "host"}):
            with self.assertRaises(AssertionError):
                self.module.validate_owned(name, prefix, [name], changed)
        with self.assertRaises(AssertionError):
            self.module.validate_owned(name, prefix, [], record)
        with self.assertRaises(AssertionError):
            self.module.validate_owned("production", prefix, ["production"], record)

    def test_source_gate_requires_actual_process_restart_and_owned_cleanup(self):
        source = SCRIPT.read_text()
        self.assertIn("validate_grpc_response", source)
        self.assertIn("assert_exact_records", source)
        self.assertIn("SIGKILL", source)
        self.assertIn("sandbox.cleanup()", source)
        self.assertNotIn("kubectl", source)
        self.assertNotIn("--publish", source)
        self.assertNotIn("--gpus", source)
        self.assertNotIn("insecure_skip_verify", source)


if __name__ == "__main__":
    unittest.main()
