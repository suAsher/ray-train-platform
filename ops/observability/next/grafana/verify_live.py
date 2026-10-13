#!/usr/bin/env python3
"""Builder-only acceptance for the dedicated grafana namespace; read-only by default.

--persistence creates a uniquely marked dashboard at a reserved UID and removes
only that dashboard. --switchover additionally requests one planned CNPG switch.
Local Pod port-forwards verify Grafana/PostgreSQL, not the external mTLS UI path.
No Secret API reads, credential output, Pod deletion, SQL writes, or spec patches.
"""
import argparse
import base64
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

NAMESPACE = 'grafana'
CLUSTER = 'grafana-postgres'
CLUSTER_RESOURCE = 'clusters.postgresql.cnpg.io'
DASHBOARD_UID = 'ha-live-persistence-proof'
OVERVIEW_UID = 'raytrain-production-overview'
HEALTHY = 'Cluster in healthy state'
POD_NAME = re.compile(r'grafana-postgres-[1-9][0-9]*')
LSN = re.compile(r'[0-9A-F]+/[0-9A-F]+')
DATASOURCES = {'prometheus-original': 'prometheus', 'victoriametrics': 'prometheus',
               'loki': 'loki', 'clickhouse-platform': 'grafana-clickhouse-datasource'}
SQL_STATE = """SET statement_timeout = '5s';
SELECT json_build_object(
 'recovery', pg_is_in_recovery(), 'read_only', current_setting('transaction_read_only'),
 'timeline', (pg_control_checkpoint()).timeline_id,
 'replay_paused', CASE WHEN pg_is_in_recovery() THEN pg_is_wal_replay_paused() ELSE false END,
 'wal', CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn()
             ELSE pg_current_wal_flush_lsn() END,
 'sync_names', current_setting('synchronous_standby_names'),
 'sync_commit', current_setting('synchronous_commit'),
 'replication', COALESCE((SELECT json_agg(json_build_object(
   'name', application_name, 'state', state, 'sync_state', sync_state,
   'flush_lsn', flush_lsn, 'replay_lsn', replay_lsn)) FROM pg_stat_replication), '[]'::json));"""


class VerificationError(RuntimeError):
    """Messages contain only fixed descriptions and public object metadata."""


def require(condition, message):
    if not condition:
        raise VerificationError(message)


def report(check, **metadata):
    print(json.dumps({'time': datetime.now(timezone.utc).isoformat(),
                      'check': check, **metadata}, sort_keys=True), flush=True)


def ready(resource):
    return any(item.get('type') == 'Ready' and item.get('status') == 'True'
               for item in resource.get('status', {}).get('conditions', []))


def validate_cluster(cluster, expected_uid=None):
    """Pure guard: only an idle, healthy, three-instance Grafana Cluster qualifies."""
    meta, spec, status = (cluster.get(key, {}) for key in ('metadata', 'spec', 'status'))
    require(cluster.get('apiVersion') == 'postgresql.cnpg.io/v1' and cluster.get('kind') == 'Cluster'
            and meta.get('namespace') == NAMESPACE and meta.get('name') == CLUSTER,
            'unexpected PostgreSQL Cluster identity')
    require(meta.get('uid') and meta.get('resourceVersion') and not meta.get('deletionTimestamp'),
            'Cluster identity/version missing or Cluster is deleting')
    require(not expected_uid or meta['uid'] == expected_uid, 'Cluster UID changed')
    require(spec.get('instances') == 3 and spec.get('primaryUpdateStrategy') == 'supervised',
            'expected three instances and supervised primary updates')
    synchronous = spec.get('postgresql', {}).get('synchronous', {})
    require(all(synchronous.get(key) == value for key, value in
                {'method': 'any', 'number': 1, 'dataDurability': 'required', 'failoverQuorum': True}.items()),
            'synchronous replication contract changed')
    require(status.get('phase') == HEALTHY and ready(cluster)
            and status.get('instances') == 3 and status.get('readyInstances') == 3,
            'PostgreSQL Cluster is not healthy with three ready instances')
    primary = status.get('currentPrimary', '')
    require(POD_NAME.fullmatch(primary) and status.get('targetPrimary') == primary,
            'PostgreSQL primary transition is already in progress')
    try:
        fenced = json.loads(meta.get('annotations', {}).get('cnpg.io/fencedInstances', '[]'))
    except (TypeError, ValueError):
        raise VerificationError('invalid fenced-instance annotation') from None
    require(fenced == [], 'fenced PostgreSQL instances forbid this acceptance switch')
    return primary


def validate_pg_pod(cluster, pod):
    meta = pod.get('metadata', {})
    require(meta.get('namespace') == NAMESPACE and POD_NAME.fullmatch(meta.get('name', ''))
            and meta.get('labels', {}).get('cnpg.io/cluster') == CLUSTER,
            'candidate Pod is outside the dedicated Grafana Cluster')
    require(any(owner.get('kind') == 'Cluster' and owner.get('name') == CLUSTER
                and owner.get('uid') == cluster['metadata']['uid'] and owner.get('controller') is True
                and owner.get('apiVersion') == 'postgresql.cnpg.io/v1'
                for owner in meta.get('ownerReferences', [])), 'PostgreSQL Pod owner UID mismatch')
    require(meta.get('uid') and not meta.get('deletionTimestamp') and ready(pod)
            and pod.get('status', {}).get('phase') == 'Running' and pod.get('spec', {}).get('nodeName'),
            'PostgreSQL Pod is not a running ready instance')
    return meta['name']


def lsn_number(value):
    require(isinstance(value, str) and LSN.fullmatch(value), 'invalid or unavailable WAL position')
    high, low = value.split('/')
    return (int(high, 16) << 32) + int(low, 16)


def validate_replication(primary, states, barrier=None):
    """Pure SQL-state guard; ANY 1 replication uses quorum, not literal sync."""
    require(len(states) == 3 and primary in states, 'expected SQL state from exactly three instances')
    standbys = set(states) - {primary}
    require(states[primary].get('recovery') is False and states[primary].get('read_only') == 'off',
            'reported primary is not writable')
    require(all(states[name].get('recovery') is True and states[name].get('read_only') == 'on'
                and states[name].get('replay_paused') is False
                for name in standbys), 'expected exactly two SQL standbys with replay active')
    leader = states[primary]
    require(re.match(r'^ANY\s+1\s*\(', leader.get('sync_names', ''))
            and leader.get('sync_commit') == 'on', 'synchronous PostgreSQL settings do not match')
    replication = leader.get('replication', [])
    require(len(replication) == 2 and {row.get('name') for row in replication} == standbys,
            'primary does not report exactly the two expected streaming standbys')
    require(all(row.get('state') == 'streaming' and row.get('sync_state') == 'quorum'
                for row in replication), 'both PostgreSQL standbys must be streaming quorum members')
    if barrier is not None:
        position = lsn_number(barrier)
        require(all(lsn_number(states[name].get('wal')) >= position for name in standbys),
                'standby replay has not reached the accepted-write WAL barrier')
        require(all(lsn_number(row.get('flush_lsn')) >= position
                    and lsn_number(row.get('replay_lsn')) >= position for row in replication),
                'primary replication view has not reached the accepted-write WAL barrier')


def validate_target(cluster, pod, states, barrier):
    primary = validate_cluster(cluster)
    name = validate_pg_pod(cluster, pod)
    require(name != primary, 'promotion target must be a different standby')
    validate_replication(primary, states, barrier)
    require(states[name].get('recovery') is True, 'promotion target is not a SQL standby')
    return name


def promotion_patch(cluster, target, timestamp):
    """CNPG v1.30.1 promote/status protocol, with stronger JSON test preconditions.

    Upstream: internal/cmd/plugin/promote/promote.go and
    pkg/resources/status/{patch,transactions}.go at tag v1.30.1.
    Never patch spec or currentPrimary; the operator performs the transition.
    """
    primary = validate_cluster(cluster)
    require(POD_NAME.fullmatch(target) and target != primary, 'invalid promotion target')
    conditions = [{**condition, 'status': 'False', 'reason': 'ClusterIsNotReady',
                   'message': 'Cluster Is Not Ready', 'lastTransitionTime': timestamp}
                  if condition.get('type') == 'Ready' else dict(condition)
                  for condition in cluster['status']['conditions']]
    tests = [('metadata/uid', cluster['metadata']['uid']),
             ('metadata/resourceVersion', cluster['metadata']['resourceVersion']),
             ('status/currentPrimary', primary), ('status/targetPrimary', primary),
             ('status/phase', HEALTHY)]
    values = [('targetPrimary', target), ('targetPrimaryTimestamp', timestamp),
              ('phase', 'Switchover in progress'), ('phaseReason', 'Switching over to ' + target),
              ('conditions', conditions)]
    return ([{'op': 'test', 'path': '/' + path, 'value': value} for path, value in tests]
            + [{'op': 'add', 'path': '/status/' + key, 'value': value} for key, value in values])


def run(command, *, data=None, timeout=30):
    try:
        result = subprocess.run(command, input=data, text=True, capture_output=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise VerificationError('subprocess could not complete; outcome may require inspection') from None
    require(result.returncode == 0, 'kubectl or PostgreSQL command failed; inspect the scoped resource')
    return result.stdout


class Kubernetes:
    def __init__(self, context):
        self.context = context or run(['kubectl', 'config', 'current-context']).strip()
        require(bool(self.context), 'no Kubernetes context selected')
        self.base = ['kubectl', '--context', self.context, '--namespace', NAMESPACE, '--request-timeout=20s']

    def get(self, kind, name=None, selector=None):
        args = ['get', kind, *([name] if name else []), *(['-l', selector] if selector else []), '-o', 'json']
        return json.loads(run(self.base + args))

    def sql(self, pod):
        return json.loads(run(self.base + ['exec', pod, '-c', 'postgres', '--', 'psql', '-X', '-q', '-A',
            '-t', '-U', 'postgres', '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-c', SQL_STATE]))

    def snapshot(self, expected_uid=None, barrier=None):
        cluster = self.get(CLUSTER_RESOURCE, CLUSTER)
        primary = validate_cluster(cluster, expected_uid)
        pods = self.get('pods', selector='cnpg.io/cluster=' + CLUSTER)['items']
        require(len(pods) == 3, 'expected exactly three dedicated PostgreSQL Pods')
        names = [validate_pg_pod(cluster, pod) for pod in pods]
        require(len(set(names)) == 3 and len({pod['spec']['nodeName'] for pod in pods}) == 3,
                'PostgreSQL instances must use three distinct nodes')
        require(primary in names, 'current primary Pod is absent')
        states = {name: self.sql(name) for name in names}
        validate_replication(primary, states, barrier)
        endpoints = self.get('endpointslices.discovery.k8s.io',
                             selector='kubernetes.io/service-name=' + CLUSTER + '-rw')['items']
        targets = {entry.get('targetRef', {}).get('name') for group in endpoints
                   for entry in group.get('endpoints', []) if entry.get('conditions', {}).get('ready') is True}
        require(targets == {primary}, 'rw Service does not target exactly the current ready primary')
        return cluster, pods, states

    def grafana_pods(self):
        deployment = self.get('deployment', 'grafana')
        require(deployment['spec']['replicas'] == 2 and deployment['status'].get('readyReplicas') == 2
                and deployment['status'].get('updatedReplicas') == 2
                and deployment['status'].get('observedGeneration', 0) >= deployment['metadata']['generation'],
                'Grafana Deployment is not fully rolled out with two ready replicas')
        pods = self.get('pods', selector='app.kubernetes.io/name=grafana')['items']
        require(len(pods) == 2 and len({pod['spec'].get('nodeName') for pod in pods}) == 2,
                'expected exactly two Grafana Pods on distinct nodes')
        for pod in pods:
            meta = pod['metadata']
            require(meta.get('namespace') == NAMESPACE and ready(pod) and not meta.get('deletionTimestamp'),
                    'Grafana Pod is not ready')
            owners = [owner for owner in meta.get('ownerReferences', [])
                      if owner.get('kind') == 'ReplicaSet' and owner.get('controller') is True]
            require(len(owners) == 1, 'Grafana Pod does not have a unique ReplicaSet controller')
            replica_set = self.get('replicaset', owners[0]['name'])
            require(replica_set['metadata']['uid'] == owners[0]['uid'] and any(
                owner.get('kind') == 'Deployment' and owner.get('name') == 'grafana'
                and owner.get('uid') == deployment['metadata']['uid'] and owner.get('controller') is True
                for owner in replica_set['metadata'].get('ownerReferences', [])),
                'Grafana Pod owner chain does not reach the dedicated Deployment')
        return sorted(pods, key=lambda pod: pod['metadata']['name'])


class Forward:
    def __init__(self, kube, pod):
        self.process = subprocess.Popen(['kubectl', '--context', kube.context, '--namespace', NAMESPACE,
            'port-forward', '--address=127.0.0.1', 'pod/' + pod, ':3000'], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        self.port = None
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(self.process.stdout, selectors.EVENT_READ)
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline and self.process.poll() is None:
                    if selector.select(timeout=0.5):
                        match = re.fullmatch(r'Forwarding from 127\.0\.0\.1:(\d+) -> 3000\n?',
                                             self.process.stdout.readline())
                        if match:
                            self.port = int(match.group(1))
                            return
            raise VerificationError('Pod-specific loopback port-forward did not become ready')
        except BaseException:
            self.close()
            raise

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.process.stdout.close()


def load_admin(path):
    flags = os.O_RDONLY | os.O_NOFOLLOW
    descriptor = os.open(path, flags)
    with os.fdopen(descriptor) as stream:
        metadata = os.fstat(stream.fileno())
        require(stat.S_ISREG(metadata.st_mode) and metadata.st_uid == 0
                and stat.S_IMODE(metadata.st_mode) & 0o077 == 0 and metadata.st_size <= 65536,
                'credentials must be a small root-owned file without group/other access')
        data = json.load(stream)
    require(isinstance(data, dict) and isinstance(data.get('admin'), str) and bool(data['admin']),
            'credentials JSON must contain a nonempty admin password string')
    return data['admin']


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        # An API redirect is a failed check, never permission to forward Basic credentials.
        return None


class Grafana:
    def __init__(self, forward, password):
        self.forward = forward
        self.header = 'Basic ' + base64.b64encode(('admin:' + password).encode()).decode()
        # Ignore ambient HTTP proxy settings: these credentials stay on loopback.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def api(self, path, payload=None, method='GET', allowed=(200,)):
        require(self.forward.process.poll() is None, 'Grafana port-forward terminated')
        body = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request('http://127.0.0.1:' + str(self.forward.port) + path,
            data=body, method=method, headers={'Authorization': self.header, 'Content-Type': 'application/json'})
        try:
            response = self.opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        except (OSError, urllib.error.URLError):
            raise VerificationError('Grafana loopback HTTP request failed') from None
        with response:
            status_code = response.code
            require(status_code in allowed, 'Grafana HTTP status ' + str(status_code))
            content = response.read(8 * 1024 * 1024 + 1)
        require(len(content) <= 8 * 1024 * 1024, 'unexpected oversized Grafana response')
        return status_code, json.loads(content) if content else {}

    def checks(self):
        _, health = self.api('/api/health')
        require(health.get('database') == 'ok' and health.get('version') == '13.1.1',
                'Grafana database health/version mismatch')
        _, plugins = self.api('/api/plugins')
        plugin = next((value for value in plugins if value.get('id') == 'grafana-clickhouse-datasource'), {})
        require(plugin.get('signature') == 'valid' and plugin.get('info', {}).get('version') == '4.22.1',
                'signed ClickHouse plugin version mismatch')
        _, sources = self.api('/api/datasources')
        require(len(sources) == 4 and {value.get('uid'): value.get('type') for value in sources} == DATASOURCES,
                'expected all four provisioned datasource identities')
        require([value.get('uid') for value in sources if value.get('isDefault')] == ['victoriametrics'],
                'VictoriaMetrics must be the default datasource')
        _, overview = self.api('/api/dashboards/uid/' + OVERVIEW_UID)
        require(overview.get('dashboard', {}).get('uid') == OVERVIEW_UID,
                'production overview is unavailable')


class Persistence:
    def __init__(self, clients):
        self.clients = clients
        self.marker = 'ha-live-owner-' + uuid.uuid4().hex
        self.attempted = False
        self.version = None
        self.title = 'Disposable Grafana HA proof'

    def read(self, client, title=None):
        _, result = client.api('/api/dashboards/uid/' + DASHBOARD_UID)
        dashboard = result.get('dashboard', {})
        require(dashboard.get('uid') == DASHBOARD_UID and self.marker in dashboard.get('tags', [])
                and dashboard.get('title') == (title or self.title), 'acceptance dashboard ownership/content mismatch')
        return dashboard

    def start(self):
        for client in self.clients:
            status_code, _ = client.api('/api/dashboards/uid/' + DASHBOARD_UID, allowed=(200, 404))
            require(status_code == 404, 'reserved acceptance dashboard already exists; refusing to overwrite it')
        self.attempted = True  # A timeout may follow a committed create; cleanup checks the private run marker.
        _, result = self.clients[0].api('/api/dashboards/db', {'dashboard': {
            'id': None, 'uid': DASHBOARD_UID, 'title': self.title, 'tags': [self.marker],
            'schemaVersion': 41, 'panels': []}, 'overwrite': False}, method='POST')
        require(result.get('status') == 'success' and result.get('uid') == DASHBOARD_UID,
                'acceptance dashboard creation was not acknowledged')
        self.read(self.clients[1])
        self.update(1, 'before-switchover')

    def update(self, writer, suffix):
        dashboard = self.read(self.clients[writer])
        new_title = 'Disposable Grafana HA proof ' + suffix
        updated = {**dashboard, 'title': new_title}
        _, result = self.clients[writer].api('/api/dashboards/db',
            {'dashboard': updated, 'overwrite': False}, method='POST')
        require(result.get('status') == 'success', 'acceptance dashboard update was not acknowledged')
        self.title = new_title
        for client in self.clients:
            stored = self.read(client)
            require(stored.get('version') == result.get('version'), 'cross-replica dashboard version mismatch')
        self.version = result['version']

    def cleanup(self):
        if not self.attempted:
            return
        # Never delete a dashboard merely because it has the reserved UID.
        reachable = False
        for client in self.clients:
            try:
                status_code, data = client.api('/api/dashboards/uid/' + DASHBOARD_UID, allowed=(200, 404))
            except VerificationError:
                continue
            reachable = True
            if status_code == 200:
                require(self.marker in data.get('dashboard', {}).get('tags', []),
                        'cleanup refused: dashboard does not belong to this run')
                client.api('/api/dashboards/uid/' + DASHBOARD_UID, method='DELETE')
                break
        require(reachable, 'no Grafana replica was reachable for acceptance dashboard cleanup')
        for client in self.clients:
            status_code, _ = client.api('/api/dashboards/uid/' + DASHBOARD_UID, allowed=(200, 404))
            require(status_code == 404, 'acceptance dashboard cleanup was not visible on both replicas')
        report('acceptance_dashboard_removed', ok=True, uid=DASHBOARD_UID)


def wait_for(operation, timeout, description):
    deadline = time.monotonic() + timeout
    while True:
        try:
            return operation()
        except VerificationError:
            if time.monotonic() >= deadline:
                raise VerificationError('timed out: ' + description) from None
            time.sleep(2)


def switch(kube, args, baseline, persistence, clients):
    cluster, pods, states = baseline
    uid, primary = cluster['metadata']['uid'], cluster['status']['currentPrimary']
    require(uid == args.expected_cluster_uid, 'explicit expected Cluster UID does not match')
    barrier = states[primary]['wal']
    lsn_number(barrier)
    cluster, pods, states = wait_for(lambda: kube.snapshot(uid, barrier), args.timeout, 'standby WAL replay barrier')
    require(cluster['status']['currentPrimary'] == primary, 'primary changed before requested switchover')
    target_pod = next((pod for pod in pods if pod['metadata']['name'] == args.target), None)
    require(target_pod is not None, 'requested target is not one of the dedicated Cluster Pods')
    validate_target(cluster, target_pod, states, barrier)
    current_target = kube.get('pod', args.target)
    require(current_target['metadata']['uid'] == target_pod['metadata']['uid'], 'target Pod UID changed')
    validate_pg_pod(cluster, current_target)
    before_timeline = states[primary]['timeline']
    if shutil.which('kubectl-cnpg'):
        version = run(['kubectl', 'cnpg', 'version'])
        require(re.search(r'\b1\.30\.1\b', version), 'installed CNPG plugin must be v1.30.1')
        method = 'cnpg-v1.30.1-promote'
        command = ['kubectl', 'cnpg', 'promote', CLUSTER, args.target,
                   '--namespace', NAMESPACE, '--context', kube.context]
        data = None
    else:
        method = 'cnpg-v1.30.1-status-protocol'
        timestamp = datetime.now(timezone.utc).isoformat(timespec='seconds').replace('+00:00', 'Z')
        command = kube.base + ['patch', CLUSTER_RESOURCE, CLUSTER, '--subresource=status',
                              '--type=json', '--patch-file=/dev/stdin', '-o', 'name']
        data = json.dumps(promotion_patch(cluster, args.target, timestamp))
    report('planned_switchover_request', cluster_uid=uid, old_primary=primary,
           target=args.target, barrier=barrier, method=method)
    started = time.monotonic()
    # Exactly one request. Never retry an ambiguous mutation or overwrite a concurrent switch.
    run(command, data=data)

    def recovered():
        result = kube.snapshot(uid)
        require(result[0]['status']['currentPrimary'] == args.target, 'target is not yet current primary')
        require(result[2][args.target]['timeline'] > before_timeline, 'PostgreSQL timeline has not advanced')
        for client in clients:
            _, health = client.api('/api/health')
            require(health.get('database') == 'ok', 'Grafana has not reconnected to PostgreSQL')
            dashboard = persistence.read(client)
            require(dashboard.get('version') == persistence.version, 'pre-switch accepted dashboard write was lost')
        return result

    wait_for(recovered, args.timeout, 'planned switchover and both Grafana replicas')
    persistence.update(0, 'after-switchover')
    report('planned_switchover_complete', ok=True, old_primary=primary, new_primary=args.target,
           recovery_observed_seconds=round(time.monotonic() - started, 3))


def pod_fingerprint(pods):
    return {pod['metadata']['uid']: {item['name']: item.get('restartCount', 0)
                                   for item in pod['status'].get('containerStatuses', [])} for pod in pods}


def verify(args):
    password = load_admin(args.credentials)
    kube = Kubernetes(args.context)
    baseline = kube.snapshot(args.expected_cluster_uid)
    pods = kube.grafana_pods()
    report('scope', namespace=NAMESPACE, cluster=CLUSTER, cluster_uid=baseline[0]['metadata']['uid'],
           primary=baseline[0]['status']['currentPrimary'], grafana_pods=[pod['metadata']['name'] for pod in pods])
    forwards, clients, persistence = [], [], None
    failure = None
    try:
        for pod in pods:
            forward = Forward(kube, pod['metadata']['name'])
            forwards.append(forward)
            client = Grafana(forward, password)
            client.checks()
            clients.append(client)
            report('grafana_read_checks', ok=True, pod=pod['metadata']['name'], pod_uid=pod['metadata']['uid'])
        if args.persistence or args.switchover:
            persistence = Persistence(clients)
            persistence.start()
            report('cross_replica_persistence', ok=True, uid=DASHBOARD_UID, version=persistence.version,
                   ownership_tag=persistence.marker)
        if args.switchover:
            # Capture the WAL barrier only after the dashboard writes were acknowledged.
            switch(kube, args, kube.snapshot(baseline[0]['metadata']['uid']), persistence, clients)
        require(pod_fingerprint(kube.grafana_pods()) == pod_fingerprint(pods),
                'Grafana Pods or containers restarted during acceptance')
    except BaseException as error:
        failure = error
    finally:
        if persistence is not None:
            try:
                persistence.cleanup()
            except Exception:
                report('acceptance_dashboard_removed', ok=False, uid=DASHBOARD_UID,
                       action='inspect reserved UID; never delete without verifying its run ownership tag')
                if failure is None:
                    failure = VerificationError('acceptance dashboard cleanup failed')
        for forward in reversed(forwards):
            forward.close()
    if failure is not None:
        raise failure
    report('complete', ok=True, mode='switchover' if args.switchover else 'persistence' if args.persistence else 'read-only',
           scope='Grafana API and native PostgreSQL; external UI mTLS and unplanned failover are separate checks')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--credentials', required=True, type=Path,
                        help='root-only new-passwords.json; reads only its admin password field')
    parser.add_argument('--context', help='explicit kubectl context; otherwise lock the current context at startup')
    parser.add_argument('--persistence', action='store_true', help='create and remove the reserved acceptance dashboard')
    parser.add_argument('--switchover', action='store_true', help='also request one planned switch after persistence checks')
    parser.add_argument('--target', help='exact ready grafana-postgres-N standby; required with --switchover')
    parser.add_argument('--expected-cluster-uid', help='Cluster UID reviewed by the operator; required with --switchover')
    parser.add_argument('--timeout', type=int, default=300, help='seconds for each bounded recovery wait (default: 300)')
    args = parser.parse_args(argv)
    if args.switchover and (not args.target or not args.expected_cluster_uid):
        parser.error('--switchover requires --target and --expected-cluster-uid')
    if args.target and not POD_NAME.fullmatch(args.target):
        parser.error('--target must be an exact grafana-postgres-N instance name')
    if args.target and not args.switchover:
        parser.error('--target requires --switchover')
    if not 10 <= args.timeout <= 1800:
        parser.error('--timeout must be 10..1800 seconds')
    try:
        require(sys.platform.startswith('linux') and os.geteuid() == 0,
                'run only as root on the authorized Linux builder')
        signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
        verify(args)
        return 0
    except KeyboardInterrupt:
        report('verification_failed', ok=False, error='interrupted; inspect any requested switchover before retrying')
    except VerificationError as error:
        report('verification_failed', ok=False, error=str(error))
    except Exception as error:
        # Do not print exception messages, argv, responses or tracebacks containing credentials.
        report('verification_failed', ok=False, error='unexpected ' + type(error).__name__)
    return 1


if __name__ == '__main__':
    sys.exit(main())
