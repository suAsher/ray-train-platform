"""Native Grafana datasource and dashboard provisioning with external credentials."""
import json
import yaml
from common import ROOT, resource


def source(uid, name, kind, **fields):
    return {'uid': uid, 'name': name, 'type': kind, 'access': 'proxy', 'orgId': 1,
            'editable': False, 'version': 1, **fields}


def render(values):
    sources = [source('prometheus-original', 'Prometheus (original fallback)', 'prometheus',
                      url=values['prometheusURL'], isDefault=False,
                      jsonData={'httpMethod': 'POST', 'timeInterval': '30s'}),
        source('victoriametrics', 'VictoriaMetrics', 'prometheus',
               url='http://127.0.0.1:18427', isDefault=True,
               jsonData={'httpMethod': 'POST', 'timeInterval': '30s', 'prometheusType': 'Prometheus',
                         'prometheusVersion': '3.5.0', 'httpHeaderName1': 'Authorization'},
               secureJsonData={'httpHeaderValue1': 'Bearer $GRAFANA_VM_TOKEN'}),
        source('loki', 'Loki', 'loki', url=values['lokiURL'], jsonData={'maxLines': 1000}),
        source('clickhouse-platform', 'ClickHouse platform logs and traces', 'grafana-clickhouse-datasource',
               jsonData={'host': '127.0.0.1', 'port': 19000, 'protocol': 'native', 'secure': False,
                         'username': 'grafana_platform_query', 'defaultDatabase': 'raytrain_observability',
                         'dialTimeout': 10, 'queryTimeout': 60, 'validateSql': True,
                         'logs': {'defaultDatabase': 'raytrain_observability', 'defaultTable': 'otel_logs_all',
                                  'otelEnabled': True, 'otelVersion': 'latest'},
                         'traces': {'defaultDatabase': 'raytrain_observability', 'defaultTable': 'otel_traces_all',
                                    'otelEnabled': True, 'otelVersion': 'latest'}},
               secureJsonData={'password': '$GRAFANA_CLICKHOUSE_PASSWORD'})]
    provider = {'apiVersion': 1, 'providers': [{'name': 'raytrain-platform', 'orgId': 1,
                'folder': 'RayTrain production', 'type': 'file', 'disableDeletion': True,
                'allowUiUpdates': False, 'updateIntervalSeconds': 30,
                'options': {'path': '/etc/grafana/dashboards'}}]}
    return [resource('ConfigMap', 'grafana-config', {'data': {'grafana.ini':
        (ROOT / 'grafana.ini').read_text().replace('ROOT_URL', values['rootURL']).replace(
            'ALLOWED_ORIGINS', ','.join(values['allowedOrigins']))}}),
        resource('ConfigMap', 'grafana-provisioning', {'data': {
            'datasources.yaml': yaml.safe_dump({'apiVersion': 1, 'prune': False, 'datasources': sources}, sort_keys=False),
            'dashboards.yaml': yaml.safe_dump(provider, sort_keys=False)}}),
        resource('ConfigMap', 'grafana-dashboards', {'data': {
            'raytrain-platform.json': json.dumps(dashboard(), ensure_ascii=False)}})]


def dashboard():
    panels = [
        {'id': 1, 'title': 'Platform API request rate', 'type': 'timeseries', 'datasource': {'type': 'prometheus', 'uid': 'victoriametrics'},
         'targets': [{'refId': 'A', 'expr': 'sum(rate(raytrain_http_requests_total[5m]))'}], 'gridPos': {'x': 0, 'y': 0, 'w': 12, 'h': 8}},
        {'id': 2, 'title': 'GPU utilization', 'type': 'timeseries', 'datasource': {'type': 'prometheus', 'uid': 'victoriametrics'},
         'targets': [{'refId': 'A', 'expr': 'avg by (Hostname) (DCGM_FI_DEV_GPU_UTIL)'}], 'gridPos': {'x': 12, 'y': 0, 'w': 12, 'h': 8}},
        {'id': 3, 'title': 'Platform HTTP request logs', 'type': 'logs', 'datasource': {'type': 'grafana-clickhouse-datasource', 'uid': 'clickhouse-platform'},
         'targets': [{'refId': 'A', 'queryType': 'sql', 'editorType': 'sql', 'format': 1,
                      'rawSql': "SELECT Timestamp AS timestamp, Body AS body, SeverityText AS level, TraceId AS traceID FROM raytrain_observability.otel_logs_all WHERE $__timeFilter(Timestamp) AND ServiceName = 'ray-train-backend' ORDER BY Timestamp DESC LIMIT 500"}],
         'gridPos': {'x': 0, 'y': 8, 'w': 24, 'h': 10}},
        {'id': 4, 'title': 'Platform request traces', 'type': 'table', 'datasource': {'type': 'grafana-clickhouse-datasource', 'uid': 'clickhouse-platform'},
         'targets': [{'refId': 'A', 'queryType': 'sql', 'editorType': 'sql', 'format': 1,
                      'rawSql': "SELECT Timestamp, TraceId, SpanName, Duration / 1000000 AS duration_ms, StatusCode FROM raytrain_observability.otel_traces_all WHERE $__timeFilter(Timestamp) AND ServiceName = 'ray-train-backend' ORDER BY Timestamp DESC LIMIT 200"}],
         'gridPos': {'x': 0, 'y': 18, 'w': 24, 'h': 10}},
    ]
    return {'uid': 'raytrain-production-overview', 'title': 'RayTrain production overview',
            'schemaVersion': 41, 'version': 1, 'editable': False, 'tags': ['raytrain', 'production'],
            'timezone': 'browser', 'refresh': '30s', 'time': {'from': 'now-1h', 'to': 'now'},
            'panels': panels}
