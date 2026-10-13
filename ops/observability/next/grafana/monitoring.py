"""Three verified-TLS scrape targets with namespace-scoped discovery only."""
from common import resource

VM_NAMESPACE = 'victoriametrics'
TARGET_LABEL = 'observability.raytrain.io/grafana-metrics'


def vm_resource(kind, name, body, api):
    document = resource(kind, name, body, api)
    return {**document, 'metadata': {**document['metadata'], 'namespace': VM_NAMESPACE}}


def target(component, selector, port, server_name, ca='obs-mtls-vmagent'):
    label = {TARGET_LABEL: component}
    service = resource('Service', component + '-metrics', {'spec': {
        'type': 'ClusterIP', 'selector': selector,
        'ports': [{'name': 'https-metrics', 'port': port, 'targetPort': port}]}})
    service = {**service, 'metadata': {**service['metadata'], 'labels': {
        **service['metadata']['labels'], **label}}}
    scrape = vm_resource('VMServiceScrape', 'business-wave1-' + component, {'spec': {
        'namespaceSelector': {'matchNames': ['grafana']}, 'selector': {'matchLabels': label},
        'endpoints': [{'port': 'https-metrics', 'path': '/metrics', 'scheme': 'https',
            'interval': '30s', 'scrapeTimeout': '10s', 'honorLabels': False,
            'tlsConfig': {'ca': {'secret': {'name': ca, 'key': 'ca.crt'}},
                'cert': {'secret': {'name': 'obs-mtls-vmagent', 'key': 'tls.crt'}},
                'keySecret': {'name': 'obs-mtls-vmagent', 'key': 'tls.key'},
                'serverName': server_name, 'insecureSkipVerify': False},
            'relabelConfigs': [{'targetLabel': 'component', 'replacement': component}]}]}},
        'operator.victoriametrics.com/v1beta1')
    scrape = {**scrape, 'metadata': {**scrape['metadata'], 'labels': {
        **scrape['metadata']['labels'], 'observability.raytrain.io/scrape': 'business-wave1'}}}
    return [service, scrape]


def render():
    role = resource('Role', 'grafana-vm-discovery', {'rules': [
        {'apiGroups': [''], 'resources': ['pods', 'services', 'endpoints'], 'verbs': ['get', 'list', 'watch']},
        {'apiGroups': ['discovery.k8s.io'], 'resources': ['endpointslices'], 'verbs': ['get', 'list', 'watch']},
    ]}, 'rbac.authorization.k8s.io/v1')
    binding = resource('RoleBinding', 'grafana-vm-discovery', {
        'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'Role', 'name': 'grafana-vm-discovery'},
        'subjects': [{'kind': 'ServiceAccount', 'name': 'vm-self-scraper', 'namespace': VM_NAMESPACE}],
    }, 'rbac.authorization.k8s.io/v1')
    egress = vm_resource('NetworkPolicy', 'grafana-metrics-vmagent-egress', {'spec': {
        'podSelector': {'matchLabels': {'observability.raytrain.io/component': 'vmagent'}},
        'policyTypes': ['Egress'], 'egress': [{'to': [{'namespaceSelector': {'matchLabels': {
            'kubernetes.io/metadata.name': 'grafana'}}}], 'ports': [
                {'port': port, 'protocol': 'TCP'} for port in (8443, 18443, 9187)]}]}},
        'networking.k8s.io/v1')
    return [role, binding, egress,
        *target('grafana', {'app.kubernetes.io/name': 'grafana'}, 8443, 'grafana.grafana.svc'),
        *target('grafana-cnpg-operator', {'app.kubernetes.io/instance': 'cloudnative-pg-operator'},
                18443, 'cloudnative-pg-operator-metrics.grafana.svc'),
        *target('grafana-postgres', {'cnpg.io/cluster': 'grafana-postgres'},
                9187, 'grafana-postgres-rw.grafana.svc', 'obs-mtls-grafana-postgres-server-ca')]
