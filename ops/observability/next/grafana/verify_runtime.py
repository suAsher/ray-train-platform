#!/usr/bin/env python3
"""Builder-only disposable two-Grafana/PostgreSQL TLS lab. No Kubernetes access.

Generates ephemeral test credentials/certs, uses an internal Docker network,
and cleans up only its unique containers/network/temp directory on every exit.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import time
import yaml
from common import ENVOY, POSTGRES, ROOT
import render


def command(args, data=None, check=True):
    completed = subprocess.run(args, input=data, text=True, capture_output=True)
    if check and completed.returncode:
        # Never include argv or container output: these can contain synthetic credentials.
        raise RuntimeError('lab command failed: ' + args[0])
    return completed


def wait_for(check, description):
    for _ in range(90):
        if check():
            return
        time.sleep(1)
    raise RuntimeError('timed out waiting for ' + description)


def issue(directory, name, common_name, usage, dns=None):
    key, cert = directory / (name + '.key'), directory / (name + '.crt')
    request = directory / (name + '.csr')
    extension = directory / (name + '.ext')
    extension.write_text('extendedKeyUsage=' + usage + '\n' + ('subjectAltName=DNS:' + dns + '\n' if dns else ''))
    command(['openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=' + common_name,
             '-keyout', str(key), '-out', str(request)])
    command(['openssl', 'x509', '-req', '-in', str(request), '-CA', str(directory / 'ca.crt'),
             '-CAkey', str(directory / 'ca.key'), '-CAcreateserial', '-days', '1',
             '-extfile', str(extension), '-out', str(cert)])
    key.chmod(0o600)


def api(container, password, path, payload=None, check=True):
    header = base64.b64encode(('admin:' + password).encode()).decode()
    args = ['docker', 'exec', container, 'wget', '-q', '-O', '-',
            '--header=Authorization: Basic ' + header]
    if payload:
        args += ['--header=Content-Type: application/json', '--post-data=' + json.dumps(payload)]
    result = command([*args, 'http://127.0.0.1:3000' + path], check=check)
    return json.loads(result.stdout) if result.returncode == 0 else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='local builder candidate image tag')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit('Run only as root on the existing Linux Docker builder')
    name = 'grafana-lab-' + secrets.token_hex(5)
    directory = Path(tempfile.mkdtemp(prefix=name + '-'))
    containers = []
    try:
        prepare(directory)
        values = yaml.safe_load((ROOT / 'values.yaml').read_text())
        image_contract = 'harbor.wellspiking.ai/guofeng.su/grafana-observability:13.1.1-clickhouse-4.22.1-lab@sha256:' + 'a' * 64
        docs = render.render({**values, 'image': image_contract})
        write_configs(directory, docs)
        command(['docker', 'network', 'create', '--internal', name])
        password, admin, encryption = (secrets.token_hex(24) for _ in range(3))
        pg = start_postgres(directory, name, containers, password)
        for index in range(2):
            container = name + '-grafana-' + str(index)
            containers.append(container)
            start_grafana(directory, name, container, args.image, password, admin, encryption)
            wait_for(lambda: api(container, admin, '/api/health', check=False) is not None, 'Grafana')
            sources = api(container, admin, '/api/datasources')
            assert len(sources) == 4, 'all four provisioned sources must load'
            plugins = api(container, admin, '/api/plugins')
            plugin = next(item for item in plugins if item['id'] == 'grafana-clickhouse-datasource')
            assert plugin['signature'] == 'valid', 'official plugin signature must verify offline'
            assert plugin['info']['version'] == '4.22.1'
            api(container, admin, '/api/dashboards/uid/raytrain-production-overview')
        first, second = containers[-2:]
        saved = api(first, admin, '/api/dashboards/db', {'dashboard': {
            'id': None, 'uid': 'ha-persistence-proof', 'title': 'Disposable HA proof',
            'schemaVersion': 41, 'panels': []}, 'overwrite': False})
        assert saved['status'] == 'success'
        assert api(second, admin, '/api/dashboards/uid/ha-persistence-proof')['dashboard']['uid'] == 'ha-persistence-proof'
        command(['docker', 'stop', first])
        api(second, admin, '/api/dashboards/uid/ha-persistence-proof')
        validate_pg_rejections(pg, password)
        validate_envoy(directory)
        print('PASS: PostgreSQL 17 TLS+SCRAM, two Grafana 13.1.1 replicas, signed plugin 4.22.1, four datasources, shared dashboard, one-replica loss, negative DB auth, Envoy config')
    finally:
        for container in reversed(containers):
            command(['docker', 'rm', '-f', container], check=False)
        command(['docker', 'network', 'rm', name], check=False)
        shutil.rmtree(directory)


def prepare(directory):
    for child in ('pgdata', 'pg-tls', 'client', 'config', 'datasources', 'providers', 'dashboards', 'envoy'):
        (directory / child).mkdir(mode=0o755)
    tls = directory / 'pg-tls'
    command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
             '-subj', '/CN=Disposable Grafana Lab', '-keyout', str(tls / 'ca.key'), '-out', str(tls / 'ca.crt')])
    issue(tls, 'server', 'postgres', 'serverAuth', 'postgres')
    issue(tls, 'client', 'grafana', 'clientAuth')
    for source, target in (('client.key', 'tls.key'), ('client.crt', 'tls.crt'), ('ca.crt', 'ca.crt')):
        shutil.copy(tls / source, directory / 'client' / target)
        os.chown(directory / 'client' / target, 472, 0)
    os.chown(directory / 'pgdata', 26, 102)
    for file in tls.iterdir():
        os.chown(file, 26, 102)
    (tls / 'pg_hba.conf').write_text('local all all trust\nhostnossl all all all reject\nhostssl grafana grafana all scram-sha-256 clientcert=verify-full\nhostssl all all all reject\n')


def write_configs(directory, docs):
    destinations = {'grafana-config': 'config', 'grafana-dashboards': 'dashboards', 'grafana-envoy': 'envoy'}
    for doc in docs:
        name = doc['metadata']['name']
        if doc['kind'] != 'ConfigMap':
            continue
        for filename, content in doc['data'].items():
            target = destinations.get(name) or ('datasources' if filename == 'datasources.yaml' else 'providers')
            (directory / target / filename).write_text(content)


def start_postgres(directory, network, containers, password):
    data = str(directory / 'pgdata') + ':/data'
    command(['docker', 'run', '--rm', '--network', 'none', '-v', data, POSTGRES,
             'initdb', '-D', '/data', '--auth-local=trust', '--auth-host=reject'])
    name = network + '-pg'
    containers.append(name)
    command(['docker', 'run', '-d', '--name', name, '--network', network, '--network-alias', 'postgres',
             '-v', data, '-v', str(directory / 'pg-tls') + ':/tls:ro', POSTGRES,
             'postgres', '-D', '/data', '-c', 'listen_addresses=*', '-c', 'ssl=on',
             '-c', 'ssl_cert_file=/tls/server.crt', '-c', 'ssl_key_file=/tls/server.key',
             '-c', 'ssl_ca_file=/tls/ca.crt', '-c', 'hba_file=/tls/pg_hba.conf'])
    wait_for(lambda: command(['docker', 'exec', name, 'pg_isready'], check=False).returncode == 0, 'PostgreSQL')
    sql = "CREATE USER grafana PASSWORD '" + password + "'; CREATE DATABASE grafana OWNER grafana;"
    command(['docker', 'exec', '-i', name, 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres'], sql)
    return name


def start_grafana(directory, network, container, image, password, admin, encryption):
    environment = {'GF_DATABASE_HOST': 'postgres:5432', 'GF_DATABASE_PASSWORD': password,
        'GF_SECURITY_ADMIN_USER': 'admin', 'GF_SECURITY_ADMIN_PASSWORD': admin,
        'GF_SECURITY_SECRET_KEY': encryption, 'GRAFANA_VM_TOKEN': secrets.token_hex(16),
        'GRAFANA_CLICKHOUSE_PASSWORD': secrets.token_hex(16)}
    command_args = ['docker', 'run', '-d', '--name', container, '--network', network, '--read-only',
        '--tmpfs', '/tmp:uid=472,gid=0', '--tmpfs', '/var/lib/grafana:uid=472,gid=0']
    for key, value in environment.items():
        command_args += ['-e', key + '=' + value]
    for source, target in (('config', '/etc/grafana'), ('datasources', '/etc/grafana/provisioning/datasources'),
        ('providers', '/etc/grafana/provisioning/dashboards'), ('dashboards', '/etc/grafana/dashboards'),
        ('client', '/etc/postgres-client'), ('client', '/etc/postgres-ca')):
        command_args += ['-v', str(directory / source) + ':' + target + ':ro']
    command([*command_args, image, '--config=/etc/grafana/grafana.ini'])


def validate_pg_rejections(container, password):
    base = ['docker', 'exec']
    cases = [({'PGSSLMODE': 'disable'}, False), ({'PGSSLMODE': 'require'}, False),
             ({'PGSSLMODE': 'verify-full', 'PGSSLCERT': '/tls/client.crt', 'PGSSLKEY': '/tls/client.key',
               'PGSSLROOTCERT': '/tls/ca.crt', 'PGPASSWORD': 'wrong-lab-password'}, False),
             ({'PGSSLMODE': 'verify-full', 'PGSSLCERT': '/tls/client.crt', 'PGSSLKEY': '/tls/client.key',
               'PGSSLROOTCERT': '/tls/ca.crt'}, True)]
    for settings, succeeds in cases:
        environment = {'PGPASSWORD': password, **settings}
        envargs = [arg for key, value in environment.items() for arg in ('-e', key + '=' + value)]
        result = command([*base, *envargs, container, 'psql', '-U', 'grafana', '-d', 'grafana',
                          '-h', 'postgres', '-c', 'SELECT 1'], check=False)
        assert (result.returncode == 0) == succeeds, 'PostgreSQL authentication boundary failed'


def validate_envoy(directory):
    command(['docker', 'run', '--rm', '--network', 'none', '-v', str(directory / 'envoy') + ':/etc/grafana-envoy:ro',
        '-v', str(directory / 'client') + ':/etc/query-identity:ro', '-v', str(directory / 'client') + ':/etc/server-identity:ro',
        '--entrypoint', '/usr/local/bin/envoy', ENVOY, '--mode', 'validate', '-c', '/etc/grafana-envoy/envoy.yaml'])


if __name__ == '__main__':
    main()
