"""Defence in depth; certificate/application authorization remains authoritative."""
from common import resource


def pod(labels, ns=None):
    return {'podSelector': {'matchLabels': labels}, **({'namespaceSelector': {
        'matchLabels': {'kubernetes.io/metadata.name': ns}}} if ns else {})}


def ns(name):
    return {'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': name}}}


def rule(direction, peers, ports):
    return {direction: peers, 'ports': [{'port': port, 'protocol': protocol} for port, protocol in ports]}


def policy(name, selector, incoming, outgoing):
    return resource('NetworkPolicy', name, {'spec': {'podSelector': {'matchLabels': selector},
        'policyTypes': ['Ingress', 'Egress'], 'ingress': incoming, 'egress': outgoing}}, 'networking.k8s.io/v1')


def render():
    app = pod({'app.kubernetes.io/name': 'grafana'})
    pg = pod({'cnpg.io/cluster': 'grafana-postgres'})
    operator = pod({'app.kubernetes.io/instance': 'cloudnative-pg-operator'})
    dns = rule('to', [pod({'k8s-app': 'kube-dns'}, 'kube-system')], [(53, 'UDP'), (53, 'TCP')])
    api = [rule('to', [{'ipBlock': {'cidr': '10.0.96.1/32'}}], [(443, 'TCP')]),
           rule('to', [{'ipBlock': {'cidr': ip + '/32'}} for ip in ('172.28.2.64', '172.28.5.50')], [(6443, 'TCP')])]
    return [policy('grafana-isolated', app['podSelector']['matchLabels'],
        [rule('from', [ns('nightingale'), ns('victoriametrics')], [(8443, 'TCP')])],
        [dns, rule('to', [pg], [(5432, 'TCP')]),
         rule('to', [ns('victoriametrics')], [(8427, 'TCP')]),
         rule('to', [ns('clickhouse')], [(9440, 'TCP')]),
         rule('to', [ns('monitoring')], [(9090, 'TCP')]), rule('to', [ns('loki')], [(3100, 'TCP')])]),
        policy('grafana-postgres-isolated', pg['podSelector']['matchLabels'],
          [rule('from', [pg, app], [(5432, 'TCP')]), rule('from', [operator], [(8000, 'TCP')]),
           rule('from', [ns('victoriametrics')], [(9187, 'TCP')])],
          [dns, rule('to', [pg], [(5432, 'TCP'), (8000, 'TCP')]), *api]),
        policy('grafana-cnpg-isolated', operator['podSelector']['matchLabels'],
          [rule('from', [{'ipBlock': {'cidr': ip + '/32'}} for ip in ('172.28.2.64', '172.28.5.50')], [(9443, 'TCP')]),
           rule('from', [ns('victoriametrics')], [(18443, 'TCP')])],
          [dns, rule('to', [pg], [(8000, 'TCP')]), *api])]
