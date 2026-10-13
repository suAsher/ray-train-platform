"""Production HTTP telemetry security and storage contracts; builder only."""
import unittest
from pathlib import Path
import xml.etree.ElementTree as ET
import yaml

ROOT = Path(__file__).resolve().parents[1]
URI = 'spiffe://observability.raytrain.local/'


def config(component, filename):
    return yaml.safe_load((ROOT / component / filename).read_text())


class PlatformPipelineTests(unittest.TestCase):
    def test_backend_only_bounded_http_ingress(self):
        listeners = config('otel', 'proxy-otel.yaml')['static_resources']['listeners']
        listener = next(x for x in listeners if x['address']['socket_address']['port_value'] == 14318)
        chain = listener['filter_chains'][0]
        tls = chain['transport_socket']['typed_config']
        self.assertTrue(tls['require_client_certificate'])
        self.assertEqual([{'san_type': 'URI', 'matcher': {'exact': URI + 'ray-train-platform/backend-telemetry'}}],
                         tls['common_tls_context']['validation_context']['match_typed_subject_alt_names'])
        hcm = chain['filters'][0]['typed_config']
        routes = hcm['route_config']['virtual_hosts'][0]['routes']
        self.assertEqual({'/v1/logs', '/v1/traces'}, {r['match']['path'] for r in routes if 'route' in r})
        for route in (r for r in routes if 'route' in r):
            self.assertEqual([{'name': ':method', 'string_match': {'exact': 'POST'}}], route['match']['headers'])
        buffer = next(f for f in hcm['http_filters'] if f['name'] == 'envoy.filters.http.buffer')
        self.assertEqual(1048576, buffer['typed_config']['max_request_bytes'])

    def test_platform_has_separate_receiver_queue_credentials_and_processors(self):
        doc = config('otel', 'collector.yaml')
        receiver = doc['receivers']['otlp/platform']['protocols']
        self.assertEqual({'http'}, set(receiver))
        self.assertEqual('127.0.0.1:14318', receiver['http']['endpoint'])
        self.assertEqual(1048576, receiver['http']['max_request_body_size'])
        exporter = doc['exporters']['clickhouse/platform']
        self.assertEqual('raytrain_observability', exporter['database'])
        self.assertEqual('platform_otel_ingest', exporter['username'])
        self.assertEqual('${env:PLATFORM_CLICKHOUSE_PASSWORD}', exporter['password'])
        self.assertFalse(exporter['create_schema'])
        self.assertFalse(exporter['sending_queue']['block_on_overflow'])
        self.assertLessEqual(exporter['sending_queue']['batch']['max_size'], 512)
        for signal in ('traces', 'logs'):
            pipe = doc['service']['pipelines'][signal + '/platform']
            self.assertEqual(['otlp/platform'], pipe['receivers'])
            self.assertEqual(['memory_limiter', 'filter/platform', 'transform/platform', 'resource/platform'], pipe['processors'])
            self.assertEqual(['clickhouse/platform'], pipe['exporters'])
            self.assertEqual(['otlp/canary'], doc['service']['pipelines'][signal + '/canary']['receivers'])

    def test_sensitive_structured_and_free_text_fields_are_erased(self):
        doc = config('otel', 'collector.yaml')
        trans = doc['processors']['transform/platform']
        span = '\n'.join(s for g in trans['trace_statements'] for s in g['statements'])
        logs = '\n'.join(s for g in trans['log_statements'] for s in g['statements'])
        for text in (span, logs):
            self.assertIn('keep_keys(resource.attributes, [])', text)
            self.assertIn('keep_keys(instrumentation_scope.attributes, [])', text)
            self.assertIn('set(instrumentation_scope.name, "")', text)
            self.assertIn('set(instrumentation_scope.version, "")', text)
        for stmt in ('set(events, nil)', 'set(links, nil)', 'set(status.message, "")', 'set(trace_state, "")'):
            self.assertIn(stmt, span)
        filters = doc['processors']['filter/platform']
        self.assertEqual('propagate', filters['error_mode'])
        self.assertIn('body != "http.request.completed"', filters['logs']['log_record'])
        self.assertIn('set(severity_text, "INFO")', logs)
        self.assertIn('set(event_name, "http.request.completed")', logs)
        resources = doc['processors']['resource/platform']['attributes']
        self.assertIn({'key': 'raytrain.telemetry.source', 'value': 'production', 'action': 'upsert'}, resources)

    def test_replicated_storage_ttl_and_dedicated_distributed_reader(self):
        sql = (ROOT / 'production-pipeline/schema.sql').read_text()
        self.assertNotIn('observability_canary', sql)
        self.assertEqual(3, sql.count('ENGINE = ReplicatedMergeTree'))
        self.assertEqual(3, sql.count('ENGINE = Distributed(telemetry_platform_query,'))
        self.assertEqual(2, sql.count('INTERVAL 30 DAY DELETE'))
        self.assertEqual(1, sql.count('INTERVAL 7 DAY DELETE'))
        chi = config('clickhouse', 'clickhouse.yaml')['spec']['configuration']
        users = chi['users']
        self.assertEqual(['GRANT INSERT, SHOW COLUMNS ON raytrain_observability.otel_logs',
                          'GRANT INSERT, SHOW COLUMNS ON raytrain_observability.otel_traces'],
                         users['platform_otel_ingest/grants/query'])
        self.assertEqual(['GRANT SELECT ON raytrain_observability.' + x for x in
                          ('otel_logs_all', 'otel_traces_all', 'otel_traces_trace_id_ts_all')],
                         users['grafana_platform_query/grants/query'])
        cluster = ET.fromstring(chi['files']['config.d/zz-platform-query.xml']).find('remote_servers/telemetry_platform_query')
        self.assertIsNone(cluster.find('secret'))
        for replica in cluster.findall('shard/replica'):
            self.assertEqual('platform_distributed_reader', replica.findtext('user'))
            self.assertEqual('PLATFORM_DISTRIBUTED_PASSWORD', replica.find('password').attrib['from_env'])
            self.assertEqual('1', replica.findtext('secure'))

    def test_grafana_native_only_and_secrets_are_references(self):
        proxy = config('clickhouse', 'proxy-clickhouse.yaml')
        for listener in proxy['static_resources']['listeners']:
            port = listener['address']['socket_address']['port_value']
            if port not in (8443, 9440):
                continue
            matches = listener['filter_chains'][0]['transport_socket']['typed_config']['common_tls_context']['validation_context']['match_typed_subject_alt_names']
            identities = [m['matcher']['exact'] for m in matches]
            self.assertEqual(port == 9440, URI + 'grafana/grafana-query' in identities)
        gateway = next(d for d in yaml.safe_load_all((ROOT/'otel/gateway.yaml').read_text()) if d['kind'] == 'StatefulSet')
        env = gateway['spec']['template']['spec']['containers'][0]['env']
        entry = next(e for e in env if e['name'] == 'PLATFORM_CLICKHOUSE_PASSWORD')
        self.assertEqual({'name': 'otel-platform-clickhouse-auth', 'key': 'password'}, entry['valueFrom']['secretKeyRef'])


if __name__ == '__main__':
    unittest.main()
