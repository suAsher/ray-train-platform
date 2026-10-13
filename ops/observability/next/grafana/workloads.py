"""Grafana Deployment and dedicated CloudNativePG database resources."""
from common import ENVOY, LABELS, POSTGRES, PRIORITY, URI, affinity, resource, resources, security


def volume(name, secret_ref=None, config=None):
    return {'name': name, **({'secret': {'secretName': secret_ref, 'defaultMode': 0o440}} if secret_ref else
        {'configMap': {'name': config}} if config else {'emptyDir': {'sizeLimit': '1Gi'}})}


def mount(name, path, readonly=True):
    return {'name': name, 'mountPath': path, 'readOnly': readonly}


def secret_env(name, secret, key):
    return {'name': name, 'valueFrom': {'secretKeyRef': {'name': secret, 'key': key}}}


def deployment(values):
    env = [secret_env(name, values[ref], key) for name, ref, key in (
        ('GF_DATABASE_PASSWORD', 'postgresSecret', 'password'),
        ('GF_SECURITY_ADMIN_USER', 'adminSecret', 'admin-user'),
        ('GF_SECURITY_ADMIN_PASSWORD', 'adminSecret', 'admin-password'),
        ('GF_SECURITY_SECRET_KEY', 'adminSecret', 'secret-key'),
        ('GRAFANA_VM_TOKEN', 'vmTokenSecret', 'token'),
        ('GRAFANA_CLICKHOUSE_PASSWORD', 'clickhouseSecret', 'password'))]
    probe = {'exec': {'command': ['sh', '-c', 'wget -q -O /dev/null http://127.0.0.1:3000/api/health']},
             'timeoutSeconds': 5, 'periodSeconds': 10}
    config_volumes = [volume('config', config='grafana-config'),
        {'name': 'datasources', 'configMap': {'name': 'grafana-provisioning', 'items': [{'key': 'datasources.yaml', 'path': 'datasources.yaml'}]}},
        {'name': 'providers', 'configMap': {'name': 'grafana-provisioning', 'items': [{'key': 'dashboards.yaml', 'path': 'dashboards.yaml'}]}},
        volume('dashboards', config='grafana-dashboards'), volume('envoy', config='grafana-envoy')]
    grafana_mounts = [mount('config', '/etc/grafana-config'), mount('datasources', '/etc/grafana/provisioning/datasources'),
        mount('providers', '/etc/grafana/provisioning/dashboards'), mount('dashboards', '/etc/grafana/dashboards'),
        mount('postgres-client', '/etc/postgres-client'), mount('postgres-ca', '/etc/postgres-ca'),
        mount('data', '/var/lib/grafana', False), mount('tmp', '/tmp', False)]
    pod = {'automountServiceAccountToken': False, 'priorityClassName': PRIORITY,
        'securityContext': {'runAsNonRoot': True, 'fsGroup': 472, 'seccompProfile': {'type': 'RuntimeDefault'}},
        'affinity': affinity(), 'tolerations': [], 'terminationGracePeriodSeconds': 60,
        'containers': [
            {'name': 'grafana', 'image': values['image'], 'imagePullPolicy': 'IfNotPresent',
             'args': ['--config=/etc/grafana-config/grafana.ini'], 'env': env,
             'resources': resources('200m', '512Mi'), 'securityContext': security(),
             'readinessProbe': probe, 'livenessProbe': {**probe, 'failureThreshold': 6},
             'startupProbe': {**probe, 'failureThreshold': 60}, 'volumeMounts': grafana_mounts},
            {'name': 'envoy', 'image': ENVOY,
             'command': ['/usr/local/bin/envoy'],
             'args': ['-c', '/etc/grafana-envoy/envoy.yaml', '--concurrency', '1', '--disable-hot-restart', '--log-level', 'warning'],
             'ports': [{'name': 'https-mtls', 'containerPort': 8443}], 'securityContext': security(10001),
             'resources': resources('50m', '64Mi', '300m', '128Mi'),
             'readinessProbe': {'tcpSocket': {'port': 'https-mtls'}, 'periodSeconds': 10},
             'volumeMounts': [mount('envoy', '/etc/grafana-envoy'), mount('query', '/etc/query-identity'),
                              mount('server', '/etc/server-identity')]},
        ], 'volumes': [*config_volumes, volume('data'), volume('tmp'),
            volume('query', secret_ref='obs-mtls-grafana-query'), volume('server', secret_ref='obs-mtls-grafana-server'),
            volume('postgres-client', secret_ref='obs-mtls-grafana-postgres-client'),
            volume('postgres-ca', secret_ref='obs-mtls-ca-postgres-server-public')]}
    return resource('Deployment', 'grafana', {'spec': {'replicas': values['replicas'],
        'selector': {'matchLabels': {'app.kubernetes.io/name': 'grafana'}},
        'strategy': {'type': 'RollingUpdate', 'rollingUpdate': {'maxUnavailable': 0, 'maxSurge': 1}},
        'template': {'metadata': {'labels': dict(LABELS)}, 'spec': pod}}}, 'apps/v1')


def postgres(values):
    spec = {'instances': 3, 'imageName': POSTGRES, 'enableSuperuserAccess': False,
        'priorityClassName': PRIORITY, 'primaryUpdateStrategy': 'supervised',
        'certificates': {'serverTLSSecret': 'obs-mtls-grafana-postgres-server',
                         'serverCASecret': 'obs-mtls-ca-postgres-server-public',
                         'clientCASecret': 'obs-mtls-postgres-client-ca',
                         'replicationTLSSecret': 'obs-mtls-grafana-postgres-replication'},
        'env': [{'name': 'OBS_METRICS_CLIENT_CA_FILE', 'value': '/projected/obs-metrics/ca.crt'},
                {'name': 'OBS_METRICS_ALLOWED_CLIENT_URIS', 'value': URI + '/victoriametrics/vmagent'}],
        'projectedVolumeTemplate': {'sources': [{'secret': {'name': 'obs-mtls-cnpg-metrics-ca',
            'items': [{'key': 'ca.crt', 'path': 'obs-metrics/ca.crt'}]}}]},
        'bootstrap': {'initdb': {'database': 'grafana', 'owner': 'grafana', 'secret': {'name': values['postgresSecret']},
                                'encoding': 'UTF8', 'localeCType': 'C', 'localeCollate': 'C'}},
        'postgresql': {'pg_hba': ['hostnossl all all all reject',
                                  'hostssl grafana grafana all scram-sha-256 clientcert=verify-full',
                                  'hostssl all all all reject'],
                       'parameters': {'max_connections': '100', 'shared_buffers': '256MB',
                                      'log_statement': 'none', 'log_min_error_statement': 'panic'},
                       'synchronous': {'method': 'any', 'number': 1, 'dataDurability': 'required', 'failoverQuorum': True}},
        'storage': {'storageClass': 'ebs-ssd-observability-retain', 'size': '20Gi'},
        'resources': resources('500m', '1Gi', '2', '2Gi'),
        'affinity': {'enablePodAntiAffinity': True, 'podAntiAffinityType': 'required',
                     'topologyKey': 'kubernetes.io/hostname', 'nodeAffinity': affinity()['nodeAffinity']},
        'monitoring': {'enablePodMonitor': False, 'tls': {'enabled': True}}}
    return resource('Cluster', 'grafana-postgres', {'spec': spec}, 'postgresql.cnpg.io/v1')


def render(values):
    selector = {'app.kubernetes.io/name': 'grafana'}
    return [deployment(values), postgres(values),
        resource('Service', 'grafana', {'spec': {'type': 'ClusterIP', 'selector': selector,
            'ports': [{'name': 'https-mtls', 'port': 8443, 'targetPort': 'https-mtls'}]}}),
        resource('PodDisruptionBudget', 'grafana', {'spec': {'minAvailable': 1, 'selector': {'matchLabels': selector}}}, 'policy/v1')]
