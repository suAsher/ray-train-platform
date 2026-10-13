#!/usr/bin/env python3
"""Restore only the dedicated Grafana database, then prove its isolated API state.

Requires explicit --create-backup, reviewed Cluster UID, the exact live Grafana
image and a root-only credentials file. PG owns a Docker --network none netns;
Grafana shares that same isolated netns. Nothing publishes ports or joins a
production network. Credentials travel through stdin, never Docker env or argv.
The private 0600 dump remains; only this run's labeled containers are removed.
"""
import argparse
import base64
import hashlib
import http.cookiejar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

from common import POSTGRES
from verify_live import (DATASOURCES, Forward, Grafana, Kubernetes, NoRedirect,
                         OVERVIEW_UID, VerificationError, require)

IMAGE = re.compile(r'harbor\.wellspiking\.ai/guofeng\.su/grafana-observability:13\.1\.1-clickhouse-4\.22\.1[-a-z0-9]*@sha256:[a-f0-9]{64}')
OWNER_LABEL = 'raytrain.grafana.restore'
DASHBOARD_API = '/apis/dashboard.grafana.app/v1/namespaces/default/dashboards/' + OVERVIEW_UID
CHECK_SQL = """SELECT json_build_object(
 'dashboards', (SELECT count(*) FROM resource WHERE "group"='dashboard.grafana.app'
                AND resource='dashboards' AND namespace='default'),
 'production_overview', (SELECT count(*) FROM resource WHERE "group"='dashboard.grafana.app'
                AND resource='dashboards' AND namespace='default' AND name='raytrain-production-overview'),
 'datasources', (SELECT count(*) FROM data_source),
 'native_admin', (SELECT count(*) FROM "user" WHERE login='admin' AND is_admin));"""
# No dashboard definitions or datasource provisioning are injected. The existing
# provider name prevents Grafana's orphan cleanup from deleting restored data.
EMPTY_PROVIDER = {'apiVersion': 1, 'providers': [{'name': 'raytrain-platform', 'orgId': 1,
    'folder': 'RayTrain production', 'type': 'file', 'disableDeletion': True,
    'allowUiUpdates': False, 'updateIntervalSeconds': 3600,
    'options': {'path': '/tmp/restore-empty-dashboards'}}]}
GRAFANA_ENV = {
    'GF_DATABASE_TYPE': 'postgres', 'GF_DATABASE_HOST': '127.0.0.1:5432',
    'GF_DATABASE_NAME': 'grafana', 'GF_DATABASE_USER': 'grafana', 'GF_DATABASE_SSL_MODE': 'disable',
    'GF_DATABASE_MAX_OPEN_CONN': '10', 'GF_SERVER_HTTP_ADDR': '127.0.0.1',
    'GF_SERVER_PROTOCOL': 'http', 'GF_SERVER_ROOT_URL': 'http://127.0.0.1:3000/',
    'GF_SECURITY_DISABLE_INITIAL_ADMIN_CREATION': 'true', 'GF_SECURITY_COOKIE_SECURE': 'false',
    'GF_AUTH_BASIC_ENABLED': 'true', 'GF_AUTH_ANONYMOUS_ENABLED': 'false',
    'GF_USERS_ALLOW_SIGN_UP': 'false', 'GF_UNIFIED_ALERTING_ENABLED': 'false',
    'GF_ANALYTICS_REPORTING_ENABLED': 'false', 'GF_ANALYTICS_CHECK_FOR_UPDATES': 'false',
    'GF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES': 'false', 'GF_PLUGINS_PREINSTALL_DISABLED': 'true',
    'GF_PLUGINS_PLUGIN_ADMIN_ENABLED': 'false', 'GF_PATHS_PLUGINS': '/opt/grafana/plugins',
    'GF_PATHS_PROVISIONING': '/tmp/restore-provisioning', 'GF_LOG_LEVEL': 'warn',
}


def command(args, *, data=None, stdin=None, stdout=subprocess.PIPE, timeout=120):
    try:
        result = subprocess.run(args, input=data, stdin=stdin, stdout=stdout,
                                stderr=subprocess.PIPE, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise VerificationError('backup command did not complete; inspect only this run resources') from None
    require(result.returncode == 0, 'backup or restore command failed; private output was suppressed')
    return result.stdout


def private_directory(path):
    require('..' not in path.parts, 'backup destination cannot contain parent traversal')
    path = path.absolute()
    require(path.is_relative_to('/root') and path != Path('/root'), 'backup destination must be beneath /root')
    for component in [path, *path.parents]:
        require(not component.is_symlink(), 'backup destination contains a symlink')
        if component.exists():
            info = component.stat()
            require(info.st_uid == 0 and stat.S_IMODE(info.st_mode) & 0o022 == 0,
                    'backup path ancestors must be root-owned and not writable by others')
    path.mkdir(mode=0o700, parents=False, exist_ok=True)
    info = path.stat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and stat.S_IMODE(info.st_mode) & 0o077 == 0,
            'backup directory must be root-owned and private')
    return path


def load_credentials(path):
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW)) as stream:
        metadata = os.fstat(stream.fileno())
        require(stat.S_ISREG(metadata.st_mode) and metadata.st_uid == 0
                and stat.S_IMODE(metadata.st_mode) & 0o077 == 0 and metadata.st_size <= 65536,
                'credentials must be a small root-owned private regular file')
        data = json.load(stream)
    require(isinstance(data, dict) and all(isinstance(data.get(key), str) and data[key]
                and not any(character in data[key] for character in '\r\n\x00')
                for key in ('admin', 'encryption', 'database')),
            'credentials require nonempty single-line admin, encryption and database strings')
    return {'admin': data['admin'], 'encryption': data['encryption'], 'db': data['database']}


def inventory_valid(inventory):
    return (inventory.get('dashboards', 0) >= 1 and inventory.get('production_overview') == 1
            and inventory.get('datasources') == 4 and inventory.get('native_admin') == 1)


def digest_json(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def api_fingerprint(api):
    _, health = api('/api/health')
    require(health.get('database') == 'ok' and health.get('version') == '13.1.1',
            'Grafana health/version is not the expected restored version')
    _, unified = api(DASHBOARD_API)
    require(unified.get('kind') == 'Dashboard' and unified.get('metadata', {}).get('name') == OVERVIEW_UID
            and unified.get('apiVersion') == 'dashboard.grafana.app/v1', 'unified dashboard API identity mismatch')
    _, overview = api('/api/dashboards/uid/' + OVERVIEW_UID)
    require(overview.get('dashboard', {}).get('uid') == OVERVIEW_UID, 'legacy dashboard bridge is unavailable')
    _, sources = api('/api/datasources')
    require(len(sources) == 4 and {source.get('uid'): source.get('type') for source in sources} == DATASOURCES,
            'four restored datasource identities do not match')
    source_fields = ('uid', 'name', 'type', 'access', 'url', 'isDefault', 'readOnly',
                     'basicAuth', 'withCredentials', 'jsonData', 'secureJsonFields')
    source_view = sorted([{key: source[key] for key in source_fields if key in source}
                          for source in sources], key=lambda source: source['uid'])
    _, user = api('/api/user')
    require(user.get('login') == 'admin' and user.get('isGrafanaAdmin') is True,
            'restored native administrator authentication failed')
    _, plugins = api('/api/plugins')
    plugin = next((item for item in plugins if item.get('id') == 'grafana-clickhouse-datasource'), {})
    require(plugin.get('signature') == 'valid' and plugin.get('info', {}).get('version') == '4.22.1',
            'restored ClickHouse plugin signature/version mismatch')
    return {'unified_dashboard': digest_json(unified['spec']),
            'legacy_dashboard': digest_json({key: value for key, value in overview['dashboard'].items()
                                            if key not in ('id', 'version')}),
            'datasources': digest_json(source_view),
            'native_admin': digest_json({key: user.get(key) for key in ('id', 'login', 'orgId', 'isGrafanaAdmin')})}


class IsolatedHTTP:
    def __init__(self, password):
        self.password = password
        self.authorization = 'Basic ' + base64.b64encode(('admin:' + password).encode()).decode()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(),
                                                 urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def api(self, path, payload=None, method='GET', authenticated=True):
        request = urllib.request.Request('http://127.0.0.1:3000' + path, method=method,
            data=None if payload is None else json.dumps(payload).encode(),
            headers={'Content-Type': 'application/json', **({'Authorization': self.authorization}
                                                           if authenticated else {})})
        try:
            with self.opener.open(request, timeout=10) as response:
                require(response.code == 200, 'isolated Grafana API did not return HTTP 200')
                data = response.read(8 * 1024 * 1024 + 1)
        except (OSError, urllib.error.URLError):
            raise VerificationError('isolated Grafana HTTP request failed') from None
        require(len(data) <= 8 * 1024 * 1024, 'isolated Grafana API response too large')
        return 200, json.loads(data)

    def login(self):
        self.api('/login', {'user': 'admin', 'password': self.password}, 'POST', authenticated=False)
        _, user = self.api('/api/user', authenticated=False)
        require(user.get('login') == 'admin' and user.get('isGrafanaAdmin') is True,
                'restored login cookie did not authenticate the native administrator')


def vm_decryption_probe(client):
    received = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            authorization = self.headers.get('Authorization', '')
            received.append(authorization.startswith('Bearer ') and len(authorization) > len('Bearer '))
            body = b'{"status":"success","data":{"resultType":"vector","result":[]}}'
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    server = ThreadingHTTPServer(('127.0.0.1', 18427), Handler)
    worker = threading.Thread(target=server.serve_forever, daemon=True)
    worker.start()
    try:
        client.api('/api/datasources/proxy/uid/victoriametrics/api/v1/query?query=vector%281%29')
        require(received and all(received), 'restored VM authorization secret was not decrypted into the request')
    finally:
        server.shutdown()
        server.server_close()
        worker.join(timeout=5)


def isolated_worker():
    """Invoked via nsenter in the owned network-none namespace; no host HTTP probes."""
    try:
        require({name for _, name in socket.if_nameindex()} == {'lo'}, 'probe requires loopback-only network namespace')
        payload = json.loads(sys.stdin.buffer.read(65537))
        require(payload.get('mode') in ('health', 'snapshot') and isinstance(payload.get('admin'), str),
                'invalid internal API probe request')
        client = IsolatedHTTP(payload['admin'])
        if payload['mode'] == 'health':
            _, health = client.api('/api/health')
            result = {'ok': health.get('database') == 'ok'}
        else:
            fingerprint = api_fingerprint(client.api)
            client.login()
            vm_decryption_probe(client)
            result = {'ok': True, 'fingerprint': fingerprint, 'native_login': True, 'vm_secret_decrypted': True}
        print(json.dumps(result))
    except Exception:
        print(json.dumps({'ok': False}))
    return 0


def container_info(name, suffix):
    values = json.loads(command(['docker', 'inspect', name]))
    require(len(values) == 1 and values[0].get('Config', {}).get('Labels', {}).get(OWNER_LABEL) == suffix,
            'container ownership label mismatch')
    return values[0]


def isolated_probe(pg, suffix, credentials, mode):
    info = container_info(pg, suffix)
    require(info['HostConfig']['NetworkMode'] == 'none' and info['State']['Running']
            and isinstance(info['State']['Pid'], int) and info['State']['Pid'] > 0,
            'restore PostgreSQL does not own a running network-none namespace')
    args = ['nsenter', '--target', str(info['State']['Pid']), '--net', '--', sys.executable,
            str(Path(__file__).resolve()), '--_isolated-api-worker']
    payload = json.dumps({'admin': credentials['admin'], 'mode': mode}).encode()
    return json.loads(command(args, data=payload, timeout=90))


def docker_base(name, suffix):
    return ['docker', 'run', '--rm', '-d', '--name', name, '--label', OWNER_LABEL + '=' + suffix,
            '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
            '--log-driver', 'none', '--cpus', '1', '--memory', '1g']


def start_postgres(name, suffix, credentials):
    command([*docker_base(name, suffix), '--network', 'none', '--user', '26:102',
        '--tmpfs', '/data:uid=26,gid=102,mode=0700,size=2g',
        '--tmpfs', '/tmp:uid=26,gid=102,mode=0700,size=128m',
        '--entrypoint', '/bin/sh', POSTGRES, '-c',
        'initdb -D /data --auth-local=trust --auth-host=reject >/dev/null && '
        "printf 'local all all trust\\nhost grafana grafana 127.0.0.1/32 scram-sha-256\\nhost all all all reject\\n' >/data/pg_hba.conf && "
        'exec postgres -D /data -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp'])
    for _ in range(60):
        result = subprocess.run(['docker', 'exec', name, 'pg_isready', '-h', '/tmp'], capture_output=True, timeout=10)
        if result.returncode == 0:
            break
        time.sleep(1)
    else:
        raise VerificationError('networkless restore PostgreSQL did not become ready')
    # Literal escaping is safe with standard_conforming_strings explicitly on.
    password = credentials['db'].replace("'", "''")
    sql = "SET standard_conforming_strings=on; CREATE ROLE grafana LOGIN PASSWORD '" + password + "'; CREATE DATABASE grafana OWNER grafana;"
    command(['docker', 'exec', '-i', name, 'psql', '-h', '/tmp', '-Xq', '-U', 'postgres',
             '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-f', '-'], data=sql.encode())


def start_grafana(name, pg, suffix, image, credentials):
    environment = [argument for key, value in GRAFANA_ENV.items() for argument in ('-e', key + '=' + value)]
    command([*docker_base(name, suffix), '--network', 'container:' + pg, '--user', '472:0',
        '--tmpfs', '/tmp:uid=472,gid=0,mode=0700,size=128m',
        '--tmpfs', '/var/lib/grafana:uid=472,gid=0,mode=0700,size=512m',
        *environment, '--entrypoint', '/bin/sh', image, '-c', 'exec sleep 86400'])
    command(['docker', 'exec', '-i', name, '/bin/sh', '-c',
        'mkdir -p /tmp/restore-empty-dashboards /tmp/restore-provisioning/dashboards && '
        'cat > /tmp/restore-provisioning/dashboards/provider.yaml'], data=json.dumps(EMPTY_PROVIDER).encode())
    # Secrets are read after container creation; Docker Config.Env and process argv contain no values.
    process = subprocess.Popen(['docker', 'exec', '-i', name, '/bin/sh', '-c',
        'set -eu; IFS= read -r GF_DATABASE_PASSWORD; IFS= read -r GF_SECURITY_SECRET_KEY; '
        'export GF_DATABASE_PASSWORD GF_SECURITY_SECRET_KEY; exec /run.sh'],
        stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        process.stdin.write((credentials['db'] + '\n' + credentials['encryption'] + '\n').encode())
        process.stdin.close()
    except BaseException:
        process.terminate()
        process.wait(timeout=10)
        raise
    return process


def cleanup_container(name, suffix):
    inspected = subprocess.run(['docker', 'inspect', '--format', '{{ index .Config.Labels "' + OWNER_LABEL + '" }}',
                                name], capture_output=True, timeout=30)
    if inspected.returncode == 0:
        require(inspected.stdout.decode().strip() == suffix, 'refusing cleanup of a container without this run label')
        command(['docker', 'rm', '-f', name])
    else:
        require(b'No such' in inspected.stderr, 'could not confirm restore container cleanup')


def dump_database(kube, primary, partial):
    with partial.open('xb') as stream:
        os.chmod(partial, 0o600)
        command(kube.base + ['exec', primary, '-c', 'postgres', '--', 'pg_dump', '-U', 'postgres',
            '-d', 'grafana', '--format=custom', '--no-owner', '--no-acl'], stdout=stream, timeout=300)
        stream.flush()
        os.fsync(stream.fileno())
    require(partial.stat().st_size > 0, 'dump is unexpectedly empty')


def restore(args):
    require(args.create_backup and os.geteuid() == 0 and sys.platform.startswith('linux'),
            'explicit backup opt-in on the authorized root Linux builder is required')
    require(shutil.which('nsenter'), 'builder nsenter is required for private loopback-only API verification')
    require(IMAGE.fullmatch(args.image), 'restore requires the reviewed immutable Grafana image reference')
    credentials = load_credentials(args.credentials)
    directory = private_directory(args.output)
    kube = Kubernetes(args.context)
    deployment = kube.get('deployment', 'grafana')
    require(next(item['image'] for item in deployment['spec']['template']['spec']['containers']
                 if item['name'] == 'grafana') == args.image, 'restore image must exactly match the live Grafana image')
    cluster, _, _ = kube.snapshot(args.expected_cluster_uid)
    primary = cluster['status']['currentPrimary']
    source = json.loads(command(kube.base + ['exec', primary, '-c', 'postgres', '--', 'psql', '-XqAt',
        '-U', 'postgres', '-d', 'grafana', '-v', 'ON_ERROR_STOP=1', '-c', CHECK_SQL]))
    require(inventory_valid(source), 'unified Grafana source inventory is not the expected initialized deployment')
    suffix = secrets.token_hex(6)
    partial, backup = (directory / ('grafana-' + suffix + extension) for extension in ('.partial', '.dump'))
    pg, grafana = 'grafana-restore-pg-' + suffix, 'grafana-restore-api-' + suffix
    attempted, forwards, clients, process = [], [], [], None
    result = None
    try:
        for pod in kube.grafana_pods():
            forward = Forward(kube, pod['metadata']['name'])
            forwards.append(forward)
            clients.append(Grafana(forward, credentials['admin']))
        fingerprints = [api_fingerprint(client.api) for client in clients]
        require(fingerprints[0] == fingerprints[1], 'source Grafana replicas disagree before backup')
        dump_database(kube, primary, partial)
        partial.rename(backup)
        current, _, _ = kube.snapshot(args.expected_cluster_uid)
        require(current['status']['currentPrimary'] == primary, 'primary changed during backup; inspect this dump')
        require(all(api_fingerprint(client.api) == fingerprints[0] for client in clients),
                'source Grafana API state changed during backup; retry when stable')
        attempted.append(pg)
        start_postgres(pg, suffix, credentials)
        with backup.open('rb') as stream:
            command(['docker', 'exec', '-i', pg, 'pg_restore', '--exit-on-error', '--no-owner', '--no-acl',
                '--role=grafana', '-h', '/tmp', '-U', 'postgres', '-d', 'grafana'], stdin=stream, timeout=300)
        restored = json.loads(command(['docker', 'exec', pg, 'psql', '-h', '/tmp', '-XqAt',
            '-U', 'postgres', '-d', 'grafana', '-v', 'ON_ERROR_STOP=1', '-c', CHECK_SQL]))
        require(restored == source, 'restored unified Grafana SQL inventory differs from source')
        attempted.append(grafana)
        process = start_grafana(grafana, pg, suffix, args.image, credentials)
        for _ in range(90):
            require(process.poll() is None, 'isolated Grafana exited during startup')
            if isolated_probe(pg, suffix, credentials, 'health').get('ok'):
                break
            time.sleep(2)
        else:
            raise VerificationError('isolated restored Grafana did not become healthy')
        restored_api = isolated_probe(pg, suffix, credentials, 'snapshot')
        require(restored_api.get('ok') is True and restored_api.get('fingerprint') == fingerprints[0],
                'restored dashboard, four datasources or administrator API differ from source')
        digest = hashlib.sha256()
        with backup.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        result = {'ok': True, 'cluster_uid': args.expected_cluster_uid, 'primary': primary,
            'backup_path': str(backup), 'mode': '0600', 'bytes': backup.stat().st_size,
            'sha256': digest.hexdigest(), 'restore_network': 'none with shared loopback',
            'checks': {'unified_sql_matches': True, 'dashboard_api_matches': True, 'four_datasources_match': True,
                       'native_admin_matches': True, 'native_login': restored_api['native_login'],
                       'vm_secret_decrypted': restored_api['vm_secret_decrypted']},
            'scope': 'logical restore and isolated Grafana API; VM credential decrypted; no production datasource queries, ClickHouse authentication or PITR proof'}
    finally:
        partial.unlink(missing_ok=True)
        cleanup_failed = False
        for name in reversed(attempted):
            try:
                cleanup_container(name, suffix)
            except Exception:
                cleanup_failed = True
        if process is not None:
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.terminate()
                process.wait(timeout=10)
        for forward in reversed(forwards):
            forward.close()
        require(not cleanup_failed, 'owned restore container cleanup could not be confirmed')
    (directory / ('grafana-' + suffix + '-verification.json')).write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, sort_keys=True))


def main():
    if sys.argv[1:] == ['--_isolated-api-worker']:
        return isolated_worker()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--create-backup', action='store_true', required=True)
    parser.add_argument('--expected-cluster-uid', required=True)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--credentials', required=True, type=Path, help='root-only new-passwords.json with admin/encryption/database')
    parser.add_argument('--image', required=True, help='exact immutable image currently used by live Grafana')
    parser.add_argument('--context')
    args = parser.parse_args()
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try:
        restore(args)
        return 0
    except KeyboardInterrupt:
        print(json.dumps({'ok': False, 'error': 'interrupted; inspect only this run labeled containers'}))
    except VerificationError as error:
        print(json.dumps({'ok': False, 'error': str(error)}))
    except Exception as error:
        print(json.dumps({'ok': False, 'error': type(error).__name__}))
    return 1


if __name__ == '__main__':
    sys.exit(main())
