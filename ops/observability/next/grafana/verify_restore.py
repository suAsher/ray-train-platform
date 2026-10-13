#!/usr/bin/env python3
"""Dump only the new Grafana database and restore into a networkless local container.

Explicit --create-backup, reviewed Cluster UID and root-only builder destination
are required. No Kubernetes Secret read or private certificate export is used.
The sensitive custom-format dump stays mode 0600 at the specified destination.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import stat
import subprocess
import sys
import time

from common import POSTGRES
from verify_live import CLUSTER, CLUSTER_RESOURCE, Kubernetes, VerificationError, require

CHECK_SQL = """SELECT json_build_object(
 'dashboards', (SELECT count(*) FROM dashboard),
 'production_overview', (SELECT count(*) FROM dashboard WHERE uid='raytrain-production-overview'),
 'datasources', (SELECT count(*) FROM data_source),
 'native_admin', (SELECT count(*) FROM \"user\" WHERE login='admin' AND is_admin));"""


def command(args, *, stdin=None, stdout=subprocess.PIPE, timeout=120):
    try:
        result = subprocess.run(args, stdin=stdin, stdout=stdout, stderr=subprocess.PIPE, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise VerificationError('backup command did not complete') from None
    require(result.returncode == 0, 'backup or restore command failed; private output was suppressed')
    return result.stdout


def private_directory(path):
    require('..' not in path.parts, 'backup destination cannot contain parent traversal')
    path = path.absolute()
    require(path.is_relative_to('/root') and path != Path('/root'), 'backup destination must be beneath /root')
    # Reject every symlink component, not just the final destination.
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


def restore(args):
    require(args.create_backup and os.geteuid() == 0 and sys.platform.startswith('linux'),
            'explicit backup opt-in on the authorized root Linux builder is required')
    directory = private_directory(args.output)
    kube = Kubernetes(args.context)
    cluster, _, _ = kube.snapshot(args.expected_cluster_uid)
    primary = cluster['status']['currentPrimary']
    source = json.loads(command(kube.base + ['exec', primary, '-c', 'postgres', '--', 'psql', '-XqAt',
        '-U', 'postgres', '-d', 'grafana', '-v', 'ON_ERROR_STOP=1', '-c', CHECK_SQL]))
    require(source['production_overview'] == 1 and source['datasources'] == 4 and source['native_admin'] == 1,
            'new Grafana source inventory does not match the expected initialized deployment')
    suffix = secrets.token_hex(6)
    partial = directory / ('grafana-' + suffix + '.partial')
    backup = directory / ('grafana-' + suffix + '.dump')
    container = 'grafana-restore-' + suffix
    attempted = False
    result = None
    try:
        with partial.open('xb') as stream:
            os.chmod(partial, 0o600)
            command(kube.base + ['exec', primary, '-c', 'postgres', '--', 'pg_dump', '-U', 'postgres',
                '-d', 'grafana', '--format=custom', '--no-owner', '--no-acl'], stdout=stream, timeout=300)
            stream.flush(); os.fsync(stream.fileno())
        require(partial.stat().st_size > 0, 'dump is unexpectedly empty')
        partial.rename(backup)
        # Verify the live Cluster was not recreated or switched during the dump.
        current, _, _ = kube.snapshot(args.expected_cluster_uid)
        require(current['status']['currentPrimary'] == primary, 'primary changed during backup; inspect this dump')
        attempted = True
        command(['docker', 'run', '--rm', '-d', '--name', container, '--label', 'raytrain.grafana.restore=' + suffix,
            '--network', 'none', '--read-only', '--user', '26:102', '--cap-drop', 'ALL',
            '--security-opt', 'no-new-privileges', '--cpus', '1', '--memory', '1g',
            '--tmpfs', '/data:uid=26,gid=102,mode=0700,size=2g',
            '--tmpfs', '/tmp:uid=26,gid=102,mode=0700,size=128m',
            '--entrypoint', '/bin/sh', POSTGRES, '-c',
            'initdb -D /data --auth-local=trust --auth-host=reject >/dev/null && '
            'exec postgres -D /data -c listen_addresses= -c unix_socket_directories=/tmp'])
        for _ in range(60):
            result = subprocess.run(['docker', 'exec', container, 'pg_isready', '-h', '/tmp'],
                capture_output=True, timeout=10)
            if result.returncode == 0:
                break
            time.sleep(1)
        else:
            raise VerificationError('networkless restore PostgreSQL did not become ready')
        command(['docker', 'exec', container, 'createdb', '-h', '/tmp', '-U', 'postgres', 'grafana'])
        with backup.open('rb') as stream:
            command(['docker', 'exec', '-i', container, 'pg_restore', '--exit-on-error', '--no-owner', '--no-acl',
                '-h', '/tmp', '-U', 'postgres', '-d', 'grafana'], stdin=stream, timeout=300)
        restored = json.loads(command(['docker', 'exec', container, 'psql', '-h', '/tmp', '-XqAt',
            '-U', 'postgres', '-d', 'grafana', '-v', 'ON_ERROR_STOP=1', '-c', CHECK_SQL]))
        require(restored == source, 'restored Grafana inventory differs from the new source')
        digest = hashlib.sha256()
        with backup.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        result = {'ok': True, 'cluster_uid': args.expected_cluster_uid, 'primary': primary,
            'backup_path': str(backup), 'mode': '0600', 'bytes': backup.stat().st_size,
            'sha256': digest.hexdigest(), 'restored_inventory': restored, 'restore_network': 'none',
            'scope': 'new Grafana database only; logical restore, not PITR or credential decryption validation'}
    finally:
        partial.unlink(missing_ok=True)
        if attempted:
            inspected = subprocess.run(['docker', 'inspect', '--format',
                '{{ index .Config.Labels "raytrain.grafana.restore" }}', container], capture_output=True, timeout=30)
            if inspected.returncode == 0:
                require(inspected.stdout.decode().strip() == suffix,
                        'refusing to remove a restore container without matching ownership')
                command(['docker', 'rm', '-f', container])
            else:
                require(b'No such' in inspected.stderr, 'could not confirm restore container cleanup')
    (directory / ('grafana-' + suffix + '-verification.json')).write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--create-backup', action='store_true', required=True)
    parser.add_argument('--expected-cluster-uid', required=True)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--context')
    args = parser.parse_args()
    os.umask(0o077)
    try:
        restore(args)
        return 0
    except VerificationError as error:
        print(json.dumps({'ok': False, 'error': str(error)}))
    except Exception as error:
        print(json.dumps({'ok': False, 'error': type(error).__name__}))
    return 1


if __name__ == '__main__':
    sys.exit(main())
