"""Grafana HA isolation and query contracts; run on the builder only."""
import copy
import importlib.util
from pathlib import Path
import sys
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1] / 'grafana'
sys.path.insert(0, str(ROOT))


def module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / (name + '.py'))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


class GrafanaHATests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.renderer = module('render')
        cls.values = yaml.safe_load((ROOT / 'values.yaml').read_text())
        cls.docs = cls.renderer.render(cls.values)

    def resource(self, kind, name):
        return next(d for d in self.docs if d['kind'] == kind and d['metadata']['name'] == name)

    def test_dedicated_ha_database_and_low_priority_zero_gpu(self):
        db = self.resource('Cluster', 'grafana-postgres')['spec']
        self.assertEqual(3, db['instances'])
        self.assertEqual('20Gi', db['storage']['size'])
        self.assertEqual('ebs-ssd-observability-retain', db['storage']['storageClass'])
        self.assertEqual('raytrain-observability-additive', db['priorityClassName'])
        self.assertEqual('grafana', db['bootstrap']['initdb']['owner'])
        hba = db['postgresql']['pg_hba']
        self.assertIn('hostssl grafana grafana all scram-sha-256 clientcert=verify-full', hba)
        self.assertEqual('hostssl all all all reject', hba[-1])
        self.assertEqual('required', db['postgresql']['synchronous']['dataDurability'])
        quota = self.resource('ResourceQuota', 'grafana-budget')['spec']['hard']
        self.assertEqual('0', quota['requests.nvidia.com/gpu'])

    def test_grafana_is_ha_loopback_with_native_admin_and_secret_refs(self):
        deployment = self.resource('Deployment', 'grafana')['spec']
        self.assertEqual(2, deployment['replicas'])
        pod = deployment['template']['spec']
        self.assertFalse(pod['automountServiceAccountToken'])
        container = next(c for c in pod['containers'] if c['name'] == 'grafana')
        env = {e['name']: e for e in container['env']}
        for key in ('GF_DATABASE_PASSWORD', 'GF_SECURITY_ADMIN_PASSWORD', 'GF_SECURITY_SECRET_KEY',
                    'GRAFANA_VM_TOKEN', 'GRAFANA_CLICKHOUSE_PASSWORD'):
            self.assertIn('secretKeyRef', env[key]['valueFrom'])
        ini = self.resource('ConfigMap', 'grafana-config')['data']['grafana.ini']
        for setting in ('http_addr = 127.0.0.1', 'ssl_mode = verify-full',
                        '[auth.anonymous]\nenabled = false', '[auth.proxy]\nenabled = false',
                        'allow_sign_up = false', 'disable_login_form = false'):
            self.assertIn(setting, ini)
        self.assertEqual('ClusterIP', self.resource('Service', 'grafana')['spec']['type'])
        self.assertNotIn('nodePort', str(self.resource('Service', 'grafana')))
        self.assertEqual(1, self.resource('PodDisruptionBudget', 'grafana')['spec']['minAvailable'])

    def test_provisioned_sources_do_not_embed_keys_or_runtime_download(self):
        sources = yaml.safe_load(self.resource('ConfigMap', 'grafana-provisioning')['data']['datasources.yaml'])['datasources']
        by_uid = {d['uid']: d for d in sources}
        self.assertEqual({'prometheus-original', 'victoriametrics', 'loki', 'clickhouse-platform'}, set(by_uid))
        self.assertEqual('http://127.0.0.1:18427', by_uid['victoriametrics']['url'])
        self.assertEqual('Bearer $GRAFANA_VM_TOKEN', by_uid['victoriametrics']['secureJsonData']['httpHeaderValue1'])
        ch = by_uid['clickhouse-platform']
        self.assertEqual('grafana-clickhouse-datasource', ch['type'])
        self.assertEqual('grafana_platform_query', ch['jsonData']['username'])
        self.assertEqual('127.0.0.1', ch['jsonData']['host'])
        self.assertEqual(19000, ch['jsonData']['port'])
        self.assertEqual('native', ch['jsonData']['protocol'])
        self.assertNotIn('tlsClientKey', str(sources))
        self.assertNotIn('GF_PLUGINS_PREINSTALL', str(self.docs))
        dockerfile = (ROOT / 'image' / 'Dockerfile').read_text()
        self.assertIn('13.1.1', dockerfile)
        self.assertIn('4.22.1', dockerfile)
        self.assertIn('grafana-clickhouse-datasource', dockerfile)

    def test_sds_query_identity_exact_server_auth_and_live_websocket(self):
        data = self.resource('ConfigMap', 'grafana-envoy')['data']
        proxy = yaml.safe_load(data['envoy.yaml'])
        text = str(proxy)
        self.assertIn('tls_certificate_sds_secret_configs', text)
        self.assertNotIn('tls_certificates', text)
        self.assertIn('spiffe://observability.raytrain.local/nightingale/n9e-ui-gateway', text)
        self.assertIn('spiffe://observability.raytrain.local/clickhouse/clickhouse', text)
        self.assertIn('spiffe://observability.raytrain.local/victoriametrics/vmauth', text)
        self.assertIn('websocket', text)
        self.assertNotIn('tls.key', text)
        sds = [v for k, v in data.items() if k.startswith('leaf-')]
        self.assertTrue(sds)
        self.assertIn('/etc/query-identity/tls.key', str(sds))
        self.assertIn('watched_directory', str(sds))

    def test_certificates_are_scoped_and_postgres_common_name_is_exact(self):
        certs = {d['metadata']['name']: d for d in self.docs if d['kind'] == 'Certificate'}
        self.assertEqual('grafana', certs['obs-mtls-grafana-postgres-client']['spec']['commonName'])
        self.assertEqual(['spiffe://observability.raytrain.local/grafana/grafana-query'],
                         certs['obs-mtls-grafana-query']['spec']['uris'])
        for policy in (d for d in self.docs if d['kind'] == 'CertificateRequestPolicy'):
            self.assertEqual(['grafana'], policy['spec']['selector']['namespace']['matchNames'])
            self.assertEqual(1, len(policy['spec']['allowed']['uris']['values']))
            self.assertFalse(policy['spec']['allowed']['isCA'])
        self.assertFalse(any(d['kind'] == 'Secret' for d in self.docs))

    def test_renderer_is_immutable_and_rejects_unsafe_values(self):
        baseline = copy.deepcopy(self.values)
        self.renderer.render(self.values)
        self.assertEqual(baseline, self.values)
        for key, value in (('namespace', 'nightingale'), ('replicas', 1), ('image', 'grafana/grafana:latest')):
            changed = {**self.values, key: value}
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.renderer.render(changed)
        changed = {**self.values, 'rootURL': 'https://grafana.example/;bad'}
        with self.assertRaises(ValueError):
            self.renderer.render(changed)

    def test_dashboard_is_production_scoped(self):
        text = str(self.resource('ConfigMap', 'grafana-dashboards'))
        for name in ('otel_logs_all', 'otel_traces_all', 'ray-train-backend'):
            self.assertIn(name, text)
        self.assertNotIn('canary', text.lower())


if __name__ == '__main__':
    unittest.main()
