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
        cls.values = {**cls.values, 'image': 'harbor.wellspiking.ai/guofeng.su/grafana-observability:13.1.1-clickhouse-4.22.1-test@sha256:' + 'a' * 64}
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

    def test_concurrent_startup_waits_for_the_database_migration_lock(self):
        import configparser
        ini = configparser.ConfigParser()
        ini.read_string(self.resource('ConfigMap', 'grafana-config')['data']['grafana.ini'])
        self.assertTrue(ini.getboolean('database', 'migration_locking'))
        self.assertEqual(120, ini.getint('database', 'locking_attempt_timeout_sec'))

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
        # clickhouse-go adds five seconds to the per-query context deadline.
        self.assertEqual(25, ch['jsonData']['queryTimeout'])
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

    def test_upstream_tls_has_a_valid_explicit_tls13_range(self):
        proxy = yaml.safe_load(self.resource('ConfigMap', 'grafana-envoy')['data']['envoy.yaml'])
        upstreams = [cluster for cluster in proxy['static_resources']['clusters'] if 'transport_socket' in cluster]
        self.assertEqual(2, len(upstreams))
        for cluster in upstreams:
            context = cluster['transport_socket']['typed_config']['common_tls_context']
            self.assertEqual({'tls_minimum_protocol_version': 'TLSv1_3',
                              'tls_maximum_protocol_version': 'TLSv1_3'}, context['tls_params'])

    def test_remote_datasource_dns_uses_cluster_ipv4_family(self):
        proxy = yaml.safe_load(self.resource('ConfigMap', 'grafana-envoy')['data']['envoy.yaml'])
        remote = [cluster for cluster in proxy['static_resources']['clusters'] if cluster['type'] == 'STRICT_DNS']
        self.assertEqual({'vmauth-read', 'clickhouse-query'}, {cluster['name'] for cluster in remote})
        for cluster in remote:
            self.assertEqual('V4_ONLY', cluster['dns_lookup_family'])
            self.assertTrue(cluster['respect_dns_ttl'])

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

    def test_dedicated_postgres_issuers_do_not_trust_nightingale_replication_ca(self):
        issuers = {doc['metadata']['name']: doc for doc in self.docs if doc['kind'] == 'ClusterIssuer'}
        self.assertEqual({'obs-mtls-grafana-postgres-client', 'obs-mtls-grafana-postgres-server'}, set(issuers))
        for name, issuer in issuers.items():
            self.assertEqual(name.replace('obs-mtls-', 'obs-mtls-ca-', 1), issuer['spec']['ca']['secretName'])
        replication = self.resource('Certificate', 'obs-mtls-grafana-postgres-replication')
        self.assertEqual('obs-mtls-grafana-postgres-client', replication['spec']['issuerRef']['name'])

    def test_approver_can_approve_only_the_two_dedicated_postgres_signers(self):
        name = 'grafana-postgres-approver-signers'
        role = self.resource('ClusterRole', name)
        self.assertNotIn('namespace', role['metadata'])
        self.assertEqual([{
            'apiGroups': ['cert-manager.io'], 'resources': ['signers'],
            'verbs': ['approve'], 'resourceNames': [
                'clusterissuers.cert-manager.io/obs-mtls-grafana-postgres-client',
                'clusterissuers.cert-manager.io/obs-mtls-grafana-postgres-server'],
        }], role['rules'])
        binding = self.resource('ClusterRoleBinding', name)
        self.assertNotIn('namespace', binding['metadata'])
        self.assertEqual({'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': name}, binding['roleRef'])
        self.assertEqual([{'kind': 'ServiceAccount', 'name': 'cert-manager-approver-policy',
                          'namespace': 'cert-manager'}], binding['subjects'])
        certificate_docs = module('certificates').render()
        self.assertEqual(role, next(doc for doc in certificate_docs if doc['kind'] == 'ClusterRole'
                                  and doc['metadata']['name'] == name))
        self.assertFalse(any(doc['metadata']['name'] == 'cert-manager-approver-policy'
                             for doc in certificate_docs if doc['kind'] in ('ClusterRole', 'ClusterRoleBinding')))

    def test_unbuilt_image_fails_closed_and_config_changes_roll_deployment(self):
        with self.assertRaises(ValueError):
            self.renderer.render(yaml.safe_load((ROOT / 'values.yaml').read_text()))
        before = self.resource('Deployment', 'grafana')['spec']['template']['metadata']['annotations']
        changed = self.renderer.render({**self.values, 'rootURL': 'https://172.28.1.232:31701/'})
        after = next(d for d in changed if d['kind'] == 'Deployment')['spec']['template']['metadata']['annotations']
        self.assertNotEqual(before, after)

    def test_all_three_metrics_targets_are_selected_with_verified_tls(self):
        scrapes = [doc for doc in self.docs if doc['kind'] == 'VMServiceScrape']
        self.assertEqual(3, len(scrapes))
        names = {doc['metadata']['name'] for doc in scrapes}
        self.assertEqual({'business-wave1-grafana', 'business-wave1-grafana-postgres',
                          'business-wave1-grafana-cnpg-operator'}, names)
        for doc in scrapes:
            self.assertEqual('victoriametrics', doc['metadata']['namespace'])
            self.assertEqual('business-wave1', doc['metadata']['labels']['observability.raytrain.io/scrape'])
            self.assertEqual(['grafana'], doc['spec']['namespaceSelector']['matchNames'])
            endpoint = doc['spec']['endpoints'][0]
            self.assertEqual('https', endpoint['scheme'])
            self.assertEqual('/metrics', endpoint['path'])
            tls = endpoint['tlsConfig']
            self.assertFalse(tls['insecureSkipVerify'])
            self.assertEqual({'secret': {'name': 'obs-mtls-vmagent', 'key': 'tls.crt'}}, tls['cert'])
            self.assertEqual({'name': 'obs-mtls-vmagent', 'key': 'tls.key'}, tls['keySecret'])
            if doc['metadata']['name'].endswith('postgres'):
                self.assertEqual('obs-mtls-grafana-postgres-server-ca', tls['ca']['secret']['name'])
                self.assertEqual('grafana-postgres-rw.grafana.svc', tls['serverName'])
            else:
                self.assertEqual('obs-mtls-vmagent', tls['ca']['secret']['name'])

    def test_metrics_discovery_is_namespaced_read_only_without_secret_access(self):
        role = self.resource('Role', 'grafana-vm-discovery')
        self.assertEqual('grafana', role['metadata']['namespace'])
        for rule in role['rules']:
            self.assertEqual(['get', 'list', 'watch'], rule['verbs'])
            self.assertNotIn('secrets', rule['resources'])
        binding = self.resource('RoleBinding', 'grafana-vm-discovery')
        self.assertEqual([{'kind': 'ServiceAccount', 'name': 'vm-self-scraper', 'namespace': 'victoriametrics'}], binding['subjects'])


class GrafanaLiveGuardTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.live = module('verify_live')

    def cluster(self):
        return {'apiVersion': 'postgresql.cnpg.io/v1', 'kind': 'Cluster',
                'metadata': {'namespace': 'grafana', 'name': 'grafana-postgres', 'uid': 'reviewed', 'resourceVersion': '42'},
                'spec': {'instances': 3, 'primaryUpdateStrategy': 'supervised', 'postgresql': {'synchronous': {
                    'method': 'any', 'number': 1, 'dataDurability': 'required', 'failoverQuorum': True}}},
                'status': {'phase': self.live.HEALTHY, 'instances': 3, 'readyInstances': 3,
                    'currentPrimary': 'grafana-postgres-1', 'targetPrimary': 'grafana-postgres-1',
                    'conditions': [{'type': 'Ready', 'status': 'True'}]}}

    def states(self):
        return {'grafana-postgres-1': {'recovery': False, 'read_only': 'off', 'sync_names': 'ANY 1 ("grafana-postgres-2", "grafana-postgres-3")',
                'sync_commit': 'on', 'replication': [{'name': 'grafana-postgres-' + str(i), 'state': 'streaming',
                    'sync_state': 'quorum', 'flush_lsn': '0/50', 'replay_lsn': '0/50'} for i in (2, 3)]},
                **{'grafana-postgres-' + str(i): {'recovery': True, 'read_only': 'on', 'replay_paused': False, 'wal': '0/50'} for i in (2, 3)}}

    def pod(self):
        return {'metadata': {'namespace': 'grafana', 'name': 'grafana-postgres-2', 'uid': 'pod-2',
            'labels': {'cnpg.io/cluster': 'grafana-postgres'}, 'ownerReferences': [{'kind': 'Cluster',
                'apiVersion': 'postgresql.cnpg.io/v1', 'name': 'grafana-postgres', 'uid': 'reviewed', 'controller': True}]},
            'spec': {'nodeName': 'cpu-node-2'}, 'status': {'phase': 'Running', 'conditions': [{'type': 'Ready', 'status': 'True'}]}}

    def test_switchover_rejects_wrong_scope_uid_or_in_progress_cluster(self):
        self.assertEqual('grafana-postgres-1', self.live.validate_cluster(self.cluster(), 'reviewed'))
        for path, value in [('namespace', 'nightingale'), ('uid', 'other')]:
            cluster = self.cluster(); cluster['metadata'][path] = value
            with self.assertRaises(self.live.VerificationError):
                self.live.validate_cluster(cluster, 'reviewed')
        cluster = self.cluster(); cluster['status']['targetPrimary'] = 'grafana-postgres-2'
        with self.assertRaises(self.live.VerificationError):
            self.live.validate_cluster(cluster)

    def test_switchover_requires_owned_ready_standby_and_replayed_write(self):
        self.assertEqual('grafana-postgres-2', self.live.validate_target(self.cluster(), self.pod(), self.states(), '0/40'))
        for key, value in [('uid', 'wrong-owner'), ('controller', False)]:
            pod = self.pod(); pod['metadata']['ownerReferences'][0][key] = value
            with self.assertRaises(self.live.VerificationError):
                self.live.validate_target(self.cluster(), pod, self.states(), '0/40')
        pod = self.pod(); pod['status']['conditions'][0]['status'] = 'False'
        with self.assertRaises(self.live.VerificationError):
            self.live.validate_target(self.cluster(), pod, self.states(), '0/40')
        states = self.states(); states['grafana-postgres-2']['wal'] = '0/30'
        with self.assertRaises(self.live.VerificationError):
            self.live.validate_target(self.cluster(), self.pod(), states, '0/40')

    def test_promotion_patch_preserves_spec_and_uses_concurrency_guards(self):
        cluster = self.cluster(); original = copy.deepcopy(cluster)
        patch = self.live.promotion_patch(cluster, 'grafana-postgres-2', '2026-10-13T00:00:00Z')
        self.assertEqual(original, cluster)
        tests = {item['path']: item['value'] for item in patch if item['op'] == 'test'}
        self.assertEqual('reviewed', tests['/metadata/uid'])
        self.assertEqual('42', tests['/metadata/resourceVersion'])
        self.assertEqual('grafana-postgres-1', tests['/status/currentPrimary'])
        self.assertEqual('grafana-postgres-1', tests['/status/targetPrimary'])
        changes = {item['path']: item['value'] for item in patch if item['op'] != 'test'}
        self.assertTrue(all(path.startswith('/status/') for path in changes))
        self.assertNotIn('/status/currentPrimary', changes)
        self.assertEqual('grafana-postgres-2', changes['/status/targetPrimary'])
        with self.assertRaises(self.live.VerificationError):
            self.live.promotion_patch(cluster, 'nightingale-postgres-2', '2026-10-13T00:00:00Z')


if __name__ == '__main__':
    unittest.main()
