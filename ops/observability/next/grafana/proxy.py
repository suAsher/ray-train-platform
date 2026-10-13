"""Grafana browser mTLS and loopback datasource proxies with rotating SDS leaves."""
import json
from common import ROOT, URI, load_module, resource

TYPE = 'type.googleapis.com/envoy.'


def tls(directory, upstream=None):
    common = {'tls_params': {'tls_minimum_protocol_version': 'TLSv1_3'},
              'tls_certificates': [{'certificate_chain': {'filename': directory + '/tls.crt'},
                                    'private_key': {'filename': directory + '/tls.key'}}],
              'validation_context': {'trusted_ca': {'filename': directory + '/ca.crt'}}}
    if upstream:
        common['validation_context']['match_typed_subject_alt_names'] = [
            {'san_type': 'URI', 'matcher': {'exact': URI + '/' + upstream}}]
    context = {'@type': TYPE + 'extensions.transport_sockets.tls.v3.' +
               ('UpstreamTlsContext' if upstream else 'DownstreamTlsContext'), 'common_tls_context': common}
    if not upstream:
        context['require_client_certificate'] = True
    return {'name': 'envoy.transport_sockets.tls', 'typed_config': context}


def cluster(name, address, port, identity=None):
    return {'name': name, 'type': 'STATIC' if address == '127.0.0.1' else 'STRICT_DNS',
            'connect_timeout': '5s', 'load_assignment': {'cluster_name': name,
                'endpoints': [{'lb_endpoints': [{'endpoint': {'address': {'socket_address': {
                    'address': address, 'port_value': port}}}}]}]},
            **({'transport_socket': tls('/etc/query-identity', identity)} if identity else {})}


def hcm(name, routes, filters=None, websocket=False):
    return {'name': 'envoy.filters.network.http_connection_manager', 'typed_config': {
        '@type': TYPE + 'extensions.filters.network.http_connection_manager.v3.HttpConnectionManager',
        'stat_prefix': name, 'normalize_path': True, 'merge_slashes': True,
        'path_with_escaped_slashes_action': 'REJECT_REQUEST', 'request_headers_timeout': '10s',
        'stream_idle_timeout': '300s', **({'upgrade_configs': [{'upgrade_type': 'websocket'}]} if websocket else {}),
        'route_config': {'name': name, 'virtual_hosts': [{'name': name, 'domains': ['*'], 'routes': routes}]},
        'http_filters': [*(filters or []), {'name': 'envoy.filters.http.router', 'typed_config': {
            '@type': TYPE + 'extensions.filters.http.router.v3.Router'}}]}}


def route_rbac(role):
    return {'envoy.filters.http.rbac': {'@type': TYPE + 'extensions.filters.http.rbac.v3.RBACPerRoute',
        'rbac': {'rules': {'action': 'ALLOW', 'policies': {'identity': {'permissions': [{'any': True}],
            'principals': [{'authenticated': {'principal_name': {'exact': URI + '/' + role}}}]}}}}}}


def listener(name, address, port, filters, transport=None):
    return {'name': name, 'address': {'socket_address': {'address': address, 'port_value': port}},
            'filter_chains': [{'filters': filters, **({'transport_socket': transport} if transport else {})}]}


def render():
    routes = [{'match': {'path': '/metrics', 'headers': [{'name': ':method', 'string_match': {'exact': 'GET'}}]},
               'route': {'cluster': 'grafana-local', 'timeout': '30s'},
               'typed_per_filter_config': route_rbac('victoriametrics/vmagent')},
              {'match': {'prefix': '/'}, 'route': {'cluster': 'grafana-local', 'timeout': '0s'},
               'typed_per_filter_config': route_rbac('nightingale/n9e-ui-gateway')}]
    rbac = {'name': 'envoy.filters.http.rbac', 'typed_config': {
        '@type': TYPE + 'extensions.filters.http.rbac.v3.RBAC', 'rules': {'action': 'ALLOW', 'policies': {}}}}
    vm_routes = [{'match': {'safe_regex': {'regex': '/api/v1/(query|query_range|labels|label/[^/]+/values|series|metadata)'},
                           'headers': [{'name': ':method', 'string_match': {'safe_regex': {'regex': 'GET|POST'}}}]},
                  'route': {'cluster': 'vmauth-read', 'timeout': '60s'}},
                 {'match': {'prefix': '/'}, 'direct_response': {'status': 403}}]
    config = {'static_resources': {'listeners': [
        listener('grafana-inbound', '0.0.0.0', 8443, [hcm('grafana_ui', routes, [rbac], True)], tls('/etc/server-identity')),
        listener('vm-read', '127.0.0.1', 18427, [hcm('grafana_vm_read', vm_routes)]),
        listener('clickhouse-query', '127.0.0.1', 19000, [{'name': 'envoy.filters.network.tcp_proxy',
            'typed_config': {'@type': TYPE + 'extensions.filters.network.tcp_proxy.v3.TcpProxy',
                             'stat_prefix': 'grafana_clickhouse_query', 'cluster': 'clickhouse-query',
                             'idle_timeout': '300s'}}]),
    ], 'clusters': [cluster('grafana-local', '127.0.0.1', 3000),
                   cluster('vmauth-read', 'vmauth-victoriametrics-cluster.victoriametrics.svc', 8427, 'victoriametrics/vmauth'),
                   cluster('clickhouse-query', 'clickhouse-clickhouse.clickhouse.svc.cluster.local', 9440, 'clickhouse/clickhouse')]}}
    sds = load_module('grafana_sds', ROOT.parent / 'mtls' / 'sds.py')
    bootstrap, files = sds.transform_bootstrap(config, '/etc/grafana-envoy', 'grafana', 'grafana')
    return resource('ConfigMap', 'grafana-envoy', {'data': {
        'envoy.yaml': json.dumps(bootstrap, sort_keys=True),
        **{name: json.dumps(value, sort_keys=True) for name, value in files.items()}}})
