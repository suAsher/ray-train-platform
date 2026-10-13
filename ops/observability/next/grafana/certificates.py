"""Explicit Grafana issuance policies; existing global PKI rendering is untouched."""
from common import ROOT, URI, load_module, resource


def identities():
    pg_dns = [f'grafana-postgres-{role}.grafana.svc{suffix}'
              for role in ('rw', 'ro', 'r') for suffix in ('', '.cluster.local')]
    entries = [
        ('grafana-server', 'mesh', 'grafana-server', ['grafana.grafana.svc', 'grafana.grafana.svc.cluster.local'], ['server auth']),
        ('grafana-query', 'mesh', 'grafana-query', [], ['client auth']),
        ('cnpg-operator', 'mesh', 'cnpg-operator', ['cloudnative-pg-operator-metrics.grafana.svc',
         'cloudnative-pg-operator-metrics.grafana.svc.cluster.local'], ['server auth']),
        ('grafana-postgres-client', 'grafana-postgres-client', 'grafana', [], ['client auth']),
        ('grafana-postgres-replication', 'grafana-postgres-client', 'streaming_replica', [], ['client auth']),
        ('grafana-postgres-server', 'grafana-postgres-server', 'grafana-postgres-server', pg_dns, ['server auth']),
    ]
    return [{'namespace': 'grafana', 'role': role, 'trust_domain': domain, 'common_name': cn,
             'dns_names': dns, 'usages': usages, 'uri': URI + '/grafana/' + role,
             'secret': 'obs-mtls-' + role} for role, domain, cn, dns, usages in entries]


def approver_signer_rbac():
    """Permit the existing approver to approve only these two dedicated issuers."""
    name = 'grafana-postgres-approver-signers'
    role = resource('ClusterRole', name, {'rules': [{
        'apiGroups': ['cert-manager.io'], 'resources': ['signers'], 'verbs': ['approve'],
        'resourceNames': [
            'clusterissuers.cert-manager.io/obs-mtls-grafana-postgres-client',
            'clusterissuers.cert-manager.io/obs-mtls-grafana-postgres-server'],
    }]}, 'rbac.authorization.k8s.io/v1', namespaced=False)
    binding = resource('ClusterRoleBinding', name, {
        'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': name},
        'subjects': [{'kind': 'ServiceAccount', 'name': 'cert-manager-approver-policy',
                      'namespace': 'cert-manager'}],
    }, 'rbac.authorization.k8s.io/v1', namespaced=False)
    return [role, binding]


def render():
    # Reuse the exact policy encoding/constraints, without broadening the global namespace allowlist.
    pki = load_module('grafana_existing_pki', ROOT.parent / 'cert-manager' / 'render.py')
    docs = [*[pki.cluster_issuer(domain) for domain in ('grafana-postgres-client', 'grafana-postgres-server')],
            *approver_signer_rbac()]
    for item in identities():
        certificate = pki.certificate(item)
        if item['role'].startswith('grafana-postgres-'):
            certificate = {**certificate, 'spec': {**certificate['spec'],
                'secretTemplate': {'labels': {'cnpg.io/reload': 'true'}}}}
        docs.extend([certificate, pki.certificate_request_policy(item),
                     pki.policy_use_role(item), pki.policy_use_binding(item)])
    return docs
