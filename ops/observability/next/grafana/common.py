"""Shared immutable resource constructors for the isolated Grafana deployment."""
from pathlib import Path
import importlib.util

ROOT = Path(__file__).resolve().parent
NAMESPACE = 'grafana'
PRIORITY = 'raytrain-observability-additive'
URI = 'spiffe://observability.raytrain.local'
ENVOY = 'harbor.wellspiking.ai/hub/envoyproxy/envoy:v1.39.3@sha256:dd85940439de19a0b6ae8419610363ea0ad351d9a994ea007161c206ec1e1865'
POSTGRES = 'harbor.wellspiking.ai/guofeng.su/postgresql-observability:17.11-202608170816-source-6b7e6a12@sha256:6b7e6a1267254902840efa9936bbeb0a398a79b5b9cb2e82218e4a92b602e97c'
LABELS = {'app.kubernetes.io/name': 'grafana', 'app.kubernetes.io/part-of': 'raytrain-observability-additive'}


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def resource(kind, name, body=None, api='v1', namespaced=True):
    return {'apiVersion': api, 'kind': kind, 'metadata': {
        'name': name, **({'namespace': NAMESPACE} if namespaced else {}),
        'labels': dict(LABELS)}, **(body or {})}


def resources(cpu, memory, limit_cpu='1', limit_memory='1Gi'):
    return {'requests': {'cpu': cpu, 'memory': memory},
            'limits': {'cpu': limit_cpu, 'memory': limit_memory}}


def security(uid=472):
    return {'runAsNonRoot': True, 'runAsUser': uid, 'runAsGroup': uid,
            'readOnlyRootFilesystem': True, 'allowPrivilegeEscalation': False,
            'capabilities': {'drop': ['ALL']}, 'seccompProfile': {'type': 'RuntimeDefault'}}


def affinity():
    return {'nodeAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': {'nodeSelectorTerms': [
        {'matchExpressions': [{'key': 'kubernetes.io/hostname', 'operator': 'NotIn',
                              'values': ['172.28.1.222', '172.28.3.32']}]}]}},
        'podAntiAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': [
            {'labelSelector': {'matchLabels': {'app.kubernetes.io/name': 'grafana'}},
             'topologyKey': 'kubernetes.io/hostname'}]}}
