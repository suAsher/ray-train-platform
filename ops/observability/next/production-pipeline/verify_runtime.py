"""Private synthetic acceptance; called only by the existing isolated CH harness."""
import copy
import json
import http.client
import socket
import time
from pathlib import Path

from runtime_support import load_module, wait

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
SERVER = 'otel-gateway.opentelemetry.svc.cluster.local'


def attributes(values):
    return [{'key': key, 'value': {'intValue' if isinstance(value, int) else 'stringValue': value}}
            for key, value in values.items()]


def payloads():
    now = time.time_ns()
    attrs = attributes({'http.request.method': 'GET', 'http.route': '/api/v1/jobs/:id',
                        'http.response.status_code': 200, 'authorization': 'sensitive-synthetic-marker',
                        'url.query': 'sensitive-synthetic-marker'})
    span = {'traceId': '00112233445566778899aabbccddeeff', 'spanId': '0123456789abcdef',
            'name': 'sensitive-synthetic-marker', 'kind': 2, 'startTimeUnixNano': str(now),
            'endTimeUnixNano': str(now + 1000000), 'attributes': attrs,
            'traceState': 'synthetic=sensitive-synthetic-marker',
            'status': {'code': 0, 'message': 'sensitive-synthetic-marker'},
            'events': [{'timeUnixNano': str(now), 'name': 'sensitive-synthetic-marker', 'attributes': attrs}],
            'links': [{'traceId': 'ffeeddccbbaa99887766554433221100', 'spanId': 'fedcba9876543210', 'attributes': attrs}]}
    log = {'traceId': span['traceId'], 'spanId': span['spanId'], 'timeUnixNano': str(now),
           'body': {'stringValue': 'http.request.completed'}, 'severityText': 'sensitive-synthetic-marker',
           'eventName': 'sensitive-synthetic-marker', 'attributes': attrs + attributes({'duration_ms': 1})}
    resource = {'attributes': attributes({'service.name': 'ray-train-backend', 'token': 'sensitive-synthetic-marker'})}
    scope = {'name': 'sensitive-synthetic-marker', 'version': 'sensitive-synthetic-marker', 'attributes': attrs}
    traces = {'resourceSpans': [{'resource': resource, 'schemaUrl': 'sensitive-synthetic-marker',
              'scopeSpans': [{'scope': scope, 'schemaUrl': 'sensitive-synthetic-marker', 'spans': [span]}]}]}
    logs = {'resourceLogs': [{'resource': resource, 'schemaUrl': 'sensitive-synthetic-marker',
            'scopeLogs': [{'scope': scope, 'schemaUrl': 'sensitive-synthetic-marker', 'logRecords': [log]}]}]}
    return traces, logs


def verify_http_protobuf(sandbox, address, sql):
    """Exercise the backend's actual OTLP HTTP binary wire protocol."""
    codec = load_module('obs_platform_proto', ROOT / 'otel' / 'verify_protocol_recovery.py')
    field, integer, fixed = codec._bytes, codec._int, codec._fixed64
    trace, span, now = bytes.fromhex('112233445566778899aabbccddeeff00'), bytes.fromhex('123456789abcdef0'), time.time_ns()
    attr = lambda key, value: field(1, key) + field(2, integer(3, value) if isinstance(value, int) else field(1, value))
    attrs = [attr('http.request.method', 'GET'), attr('http.route', '/api/v1/jobs/:id'), attr('http.response.status_code', 200)]
    resource = field(1, field(1, attr('service.name', 'ray-train-backend')))
    for signal in ('logs', 'traces'):
        if signal == 'logs':
            record = fixed(1, now) + integer(2, 9) + field(5, field(1, 'http.request.completed')) + field(9, trace) + field(10, span)
            record += b''.join(field(6, value) for value in attrs + [attr('duration_ms', 1)])
        else:
            record = field(1, trace) + field(2, span) + field(5, 'GET /api/v1/jobs/:id') + integer(6, 2) + fixed(7, now) + fixed(8, now + 1000000)
            record += b''.join(field(9, value) for value in attrs)
        body = field(1, resource + field(2, field(2, record)))
        context = sandbox.context('backend-telemetry')
        connection = http.client.HTTPSConnection(SERVER, 14318, context=context, timeout=5)
        connection.sock = context.wrap_socket(socket.create_connection((address, 14318), 5), server_hostname=SERVER)
        try:
            connection.request('POST', '/v1/' + signal, body=body, headers={'Content-Type': 'application/x-protobuf'})
            response = connection.getresponse()
            assert response.status == 200 and response.read(4096) == b'', 'binary OTLP rejected'
        finally:
            connection.close()
    for table in ('otel_logs_all', 'otel_traces_all', 'otel_traces_trace_id_ts_all'):
        wait('protobuf production row in ' + table, lambda t=table:
             sql(sandbox, 'SELECT count() FROM raytrain_observability.' + t, role='grafana-query') == '2', 60)


def verify(sandbox, context, sql):
    otel = load_module('obs_platform_otel', ROOT / 'otel' / 'verify_runtime.py')
    base, queue = otel._prepare(context)
    native, proxy, address = otel._start(context, base, queue)
    wait('production Collector readiness', lambda: otel._healthy(address), 90)
    for role in (None, 'canary', 'vmagent', 'grafana-query', 'query-admin'):
        assert not sandbox.tls_accepts(address, SERVER, 14318, role), 'wrong-role OTLP accepted'
    assert sandbox.tls_accepts(address, SERVER, 14318, 'backend-telemetry')
    assert not sandbox.tls_accepts(address, SERVER, 4318, 'backend-telemetry')
    ch_address = sandbox.ip(sandbox.native_ch[0])
    ch_server = 'clickhouse-clickhouse.clickhouse.svc.cluster.local'
    assert sandbox.tls_accepts(ch_address, ch_server, 9440, 'grafana-query')
    assert not sandbox.tls_accepts(ch_address, ch_server, 8443, 'grafana-query')
    for path in ('/', '/metrics', '/v1/metrics', '/v1/traces'):
        assert sandbox.request(address, SERVER, 14318, path, 'backend-telemetry')[0] == 404
    assert sandbox.request(address, SERVER, 14318, '/v1/logs', 'backend-telemetry', b'x' * 1048577)[0] == 413
    traces, logs = payloads()
    for signal, payload in (('traces', traces), ('logs', logs)):
        assert sandbox.request(address, SERVER, 14318, '/v1/' + signal, 'backend-telemetry', json.dumps(payload).encode())[0] == 200
    db = 'raytrain_observability.'
    for table in ('otel_logs_all', 'otel_traces_all', 'otel_traces_trace_id_ts_all'):
        wait('production insert and distributed read ' + table,
             lambda t=table: sql(sandbox, 'SELECT count() FROM ' + db + t, role='grafana-query') == '1', 60)
        result = sql(sandbox, 'SELECT * FROM ' + db + table + ' FORMAT JSONEachRow', role='grafana-query')
        assert 'sensitive-synthetic-marker' not in result, 'sensitive free text survived'
        if table != 'otel_traces_trace_id_ts_all':
            record = json.loads(result)
            assert record['ServiceName'] == 'ray-train-backend'
            assert record['ResourceAttributes']['raytrain.telemetry.source'] == 'production'
            assert len(record['ResourceAttributes']) == 5
    for replica in range(2):
        for table in ('otel_logs', 'otel_traces', 'otel_traces_trace_id_ts'):
            wait('replicated production row', lambda r=replica, t=table:
                 sql(sandbox, 'SELECT count() FROM ' + db + t, replica=r, role='schema-admin') == '1', 60)
    assert sql(sandbox, 'SET max_execution_time=30; SELECT 1', role='grafana-query') == '1'
    denied = ['SET max_execution_time=31', 'SET readonly=0',
              "SELECT count() FROM cluster('telemetry_platform_query',raytrain_observability,otel_logs)",
              'SELECT * FROM '  + db + 'otel_logs', 'SELECT * FROM ' + db + 'otel_traces',
              'SELECT * FROM ' + db + 'otel_traces_trace_id_ts', 'SELECT * FROM system.users',
              'SELECT * FROM system.clusters', 'SELECT * FROM system.query_log',
              'INSERT INTO ' + db + "otel_logs_all (Timestamp,Body) VALUES(now64(9),'forbidden')"]
    for query in denied:
        assert sql(sandbox, query, role='grafana-query', check=False).returncode != 0, 'Grafana query privilege exceeded'
    assert sql(sandbox, 'SELECT * FROM ' + db + 'otel_logs', role='platform-ingest', check=False).returncode != 0
    assert sql(sandbox, 'INSERT INTO observability_canary.otel_logs (Timestamp) VALUES(now64(9))', role='platform-ingest', check=False).returncode != 0
    # Wrong Body and malformed bounded attributes must never enter storage.
    bad = copy.deepcopy(logs)
    bad['resourceLogs'][0]['scopeLogs'][0]['logRecords'][0]['body'] = {'stringValue': 'sensitive-synthetic-marker'}
    assert sandbox.request(address, SERVER, 14318, '/v1/logs', 'backend-telemetry', json.dumps(bad).encode())[0] == 200
    bad = copy.deepcopy(traces)
    bad['resourceSpans'][0]['scopeSpans'][0]['spans'][0]['attributes'] = attributes({'http.request.method': 'GET', 'http.route': '/bad?token=secret', 'http.response.status_code': 200})
    assert sandbox.request(address, SERVER, 14318, '/v1/traces', 'backend-telemetry', json.dumps(bad).encode())[0] == 200
    time.sleep(2)
    for table in ('otel_logs_all', 'otel_traces_all'):
        assert sql(sandbox, 'SELECT count() FROM ' + db + table, role='grafana-query') == '1'
    # Exercise actual TTL/merge work with the existing small server pools.
    for table in ('otel_logs', 'otel_traces'):
        sql(sandbox, 'INSERT INTO ' + db + table + " (Timestamp,TraceId) VALUES(now64(9)-INTERVAL 35 DAY,'expired-synthetic')", role='schema-admin')
        sql(sandbox, 'OPTIMIZE TABLE ' + db + table + ' FINAL', role='schema-admin')
        assert sql(sandbox, 'SELECT count() FROM ' + db + table + " WHERE TraceId='expired-synthetic'", role='schema-admin') == '0'
    sql(sandbox, 'OPTIMIZE TABLE ' + db + 'otel_traces_trace_id_ts FINAL', role='schema-admin')
    assert sql(sandbox, 'SELECT count() FROM ' + db + "otel_traces_trace_id_ts WHERE TraceId='expired-synthetic'", role='schema-admin') == '0'
    verify_http_protobuf(sandbox, address, sql)
    return {'otlp_http_protobuf': 'passed', 'ttl_and_merge_small_pool' : 'passed', 'mtls_method_path_body_limits' : 'passed', 'sensitive_field_erasure': 'passed',
            'two_replica_production_ingest': 'passed', 'distributed_read_without_base_grant': 'passed',
            'unexpected_log_body_and_route_rejected': 'passed', 'grafana_write_base_system_denied': 'passed'}
