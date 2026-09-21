"""Bounded, wheel-only environment capture shared by workspace and build jobs."""
from __future__ import annotations

import argparse
import base64
import hashlib
import importlib.metadata as metadata
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import zipfile

BASE_IMAGE = 'harbor.wellspiking.ai/guofeng.su/raytrain-base@sha256:3ec73cf863847a42bcac035309259ad9d08fd74561384cc8476020c62264d6e2'
ENVIRONMENT = Path('/opt/raytrain/environment')
BASELINE = Path('/opt/raytrain/environment-baseline.json')
MAX_MANIFEST_BYTES = 1024 * 1024
MAX_PACKAGES = 128
MAX_WHEEL_BYTES = 2 * 1024 * 1024 * 1024
IGNORED_METADATA = {'RECORD', 'INSTALLER', 'REQUESTED', 'direct_url.json'}
CHECKS = {'baseUnchanged', 'managedOnly', 'installedFilesVerified', 'stableCapture'}


class CaptureError(ValueError):
    """A safe, actionable reason this environment is not reproducible."""


def canonical_name(name):
    return re.sub(r'[-_.]+', '-', name).lower()


def content_hash(entries):
    digest = hashlib.sha256()
    for path, value in sorted(entries):
        digest.update(path.encode('utf-8') + b'\0' + value.encode('ascii') + b'\n')
    return digest.hexdigest()


def ignored_file(name):
    path = PurePosixPath(name)
    return ('__pycache__' in path.parts or path.suffix == '.pyc' or
            (path.parent.name.endswith('.dist-info') and path.name in IGNORED_METADATA))


def checked_file_hash(path, algorithm, recorded):
    if path.is_symlink() or not path.is_file():
        raise CaptureError('Package contains a symlink or missing installed file; reinstall its wheel')
    if algorithm and algorithm not in ('sha256', 'sha384', 'sha512'):
        raise CaptureError('Package uses an unsupported installed-file hash')
    actual = hashlib.sha256()
    check = hashlib.new(algorithm) if algorithm else None
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            actual.update(chunk)
            if check:
                check.update(chunk)
    if check and base64.urlsafe_b64encode(check.digest()).rstrip(b'=').decode() != recorded:
        raise CaptureError('Installed package files were modified; reinstall the original wheel before saving')
    return actual.hexdigest()


def distribution_fingerprint(distribution, *, managed):
    if managed and distribution.read_text('direct_url.json'):
        raise CaptureError('Direct URL, local and editable installs are not supported; install a versioned index wheel')
    files = distribution.files
    if not files:
        raise CaptureError('Package has no installed-file manifest; reinstall it from a wheel')
    root = Path(distribution.locate_file('')).resolve()
    entries = []
    for item in files:
        name = str(item)
        if ignored_file(name):
            continue
        path = Path(distribution.locate_file(item))
        # Console scripts have interpreter-dependent shebangs. Check their RECORD
        # integrity, but exclude them from portable wheel/install fingerprints.
        resolved = path.resolve()
        outside = not resolved.is_relative_to(root)
        if outside and not resolved.is_relative_to(root.parent.parent.parent / 'bin'):
            raise CaptureError('Package installs files outside its Python environment; not supported by wheel capture')
        value = checked_file_hash(path, item.hash.mode if managed and item.hash else '', item.hash.value if managed and item.hash else '')
        if not outside:
            entries.append((name, value))
    return content_hash(entries)


def verify_managed_tree(distributions):
    site = ENVIRONMENT / 'lib' / f'python{sys.version_info.major}.{sys.version_info.minor}' / 'site-packages'
    recorded = set()
    for distribution in distributions:
        root = Path(distribution.locate_file('')).resolve()
        if root.is_relative_to(ENVIRONMENT):
            for item in distribution.files or []:
                recorded.add(str(Path(distribution.locate_file(item)).absolute()))
    if not site.exists():
        raise CaptureError('Managed Python environment is missing')
    for root, directories, files in os.walk(site):
        for directory in directories:
            if Path(root, directory).is_symlink():
                raise CaptureError('Managed environment contains an untracked symlink')
        directories[:] = [name for name in directories if name != '__pycache__']
        for name in files:
            path = Path(root, name)
            if path.suffix != '.pyc' and str(path.absolute()) not in recorded:
                raise CaptureError('Managed environment contains untracked files; reinstall dependencies from wheels')


def inventory():
    distributions = list(metadata.distributions())
    verify_managed_tree(distributions)
    result = {}
    for distribution in distributions:
        name = canonical_name(distribution.metadata.get('Name', ''))
        if not name or name in result:
            continue  # importlib lists the effective venv distribution first.
        root = Path(distribution.locate_file('')).resolve()
        managed = root.is_relative_to(ENVIRONMENT)
        result[name] = {'version': distribution.version, 'filesHash': distribution_fingerprint(distribution, managed=managed),
                        'managed': managed}
    return result


def package_delta(baseline, current):
    for name, original in baseline.items():
        if current.get(name) != original:
            raise CaptureError('Base package changed or removed: ' + name + '; choose another base or restore its original version')
    packages = []
    for name in sorted(set(current) - set(baseline)):
        entry = current[name]
        if not entry['managed']:
            raise CaptureError('Package is outside the managed environment: ' + name)
        packages.append({'name': name, 'version': entry['version'], 'filesHash': entry['filesHash']})
    if len(packages) > MAX_PACKAGES:
        raise CaptureError('Too many added dependencies; limit is 128 packages per environment version')
    return packages


def validate_manifest(value):
    if not isinstance(value, dict) or set(value) != {'schemaVersion', 'baseImage', 'pythonVersion', 'packages', 'checks'}:
        raise CaptureError('Invalid capture manifest fields')
    if value['schemaVersion'] != 1 or value['baseImage'] != BASE_IMAGE:
        raise CaptureError('Capture base image or schema does not match the supported immutable base')
    if not isinstance(value['pythonVersion'], str) or not re.fullmatch(r'3\.10\.[0-9]{1,3}', value['pythonVersion']):
        raise CaptureError('Unsupported Python version')
    if not isinstance(value['checks'], dict) or set(value['checks']) != CHECKS or any(v is not True for v in value['checks'].values()):
        raise CaptureError('Capture checks did not all pass')
    packages = value['packages']
    if not isinstance(packages, list) or len(packages) > MAX_PACKAGES:
        raise CaptureError('Invalid package list')
    names = set()
    for item in packages:
        if not isinstance(item, dict) or set(item) != {'name', 'version', 'filesHash'}:
            raise CaptureError('Invalid package fields')
        name, version, fingerprint = item['name'], item['version'], item['filesHash']
        if not isinstance(name, str) or not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{0,126}[a-z0-9])?', name):
            raise CaptureError('Invalid normalized package name')
        if name in names:
            raise CaptureError('Duplicate package')
        names.add(name)
        if not isinstance(version, str) or not re.fullmatch(r'[0-9][a-zA-Z0-9.!+_-]{0,126}', version):
            raise CaptureError('Invalid fixed package version')
        if not isinstance(fingerprint, str) or not re.fullmatch(r'[0-9a-f]{64}', fingerprint):
            raise CaptureError('Invalid package content fingerprint')
    if len(json.dumps(value).encode()) > MAX_MANIFEST_BYTES:
        raise CaptureError('Capture manifest exceeds limit')
    return value


def read_manifest(path):
    with Path(path).open('rb') as stream:
        raw = stream.read(MAX_MANIFEST_BYTES + 1)
    if len(raw) > MAX_MANIFEST_BYTES:
        raise CaptureError('Capture manifest exceeds limit')
    return validate_manifest(json.loads(raw))


def wheel_fingerprint(path):
    entries, seen = [], set()
    total = 0
    with zipfile.ZipFile(path) as wheel:
        for member in wheel.infolist():
            name = member.filename
            parts = PurePosixPath(name).parts
            if '\\' in name or name.startswith('/') or '..' in parts or not parts:
                raise CaptureError('Wheel contains an unsafe file path')
            if stat.S_ISLNK(member.external_attr >> 16):
                raise CaptureError('Wheel symlinks are unsupported')
            if member.is_dir():
                continue
            if parts[0].endswith('.data'):
                if len(parts) < 3 or parts[1] not in ('purelib', 'platlib'):
                    raise CaptureError('Wheel contains non-library data or scripts; this format is not supported yet')
                name = '/'.join(parts[2:])
            if name in seen:
                raise CaptureError('Wheel contains duplicate installed paths')
            seen.add(name)
            total += member.file_size
            if total > MAX_WHEEL_BYTES:
                raise CaptureError('Wheel extracted size exceeds 2 GiB')
            if ignored_file(name):
                continue
            digest = hashlib.sha256()
            with wheel.open(member) as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                    digest.update(chunk)
            entries.append((name, digest.hexdigest()))
    return content_hash(entries)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=('baseline', 'capture', 'verify'))
    parser.add_argument('--manifest')
    args = parser.parse_args()
    if Path(sys.prefix) != ENVIRONMENT:
        raise CaptureError('Use /opt/raytrain/environment/bin/python; other Python environments cannot be saved')
    current = inventory()
    if args.command == 'baseline':
        if os.geteuid() != 0:
            raise CaptureError('Baseline generation is restricted to the image build')
        BASELINE.write_text(json.dumps(current, sort_keys=True), encoding='utf-8')
        BASELINE.chmod(0o444)
        constraints = Path('/opt/raytrain/environment-constraints.txt')
        constraints.write_text(''.join(name + '==' + entry['version'] + '\n' for name, entry in sorted(current.items())), encoding='utf-8')
        constraints.chmod(0o444)
        return
    baseline = json.loads(BASELINE.read_text(encoding='utf-8'))
    packages = package_delta(baseline, current)
    if args.command == 'verify':
        manifest = read_manifest(args.manifest)
        if packages != manifest['packages'] or manifest['pythonVersion'] != '.'.join(map(str, sys.version_info[:3])):
            raise CaptureError('Rebuilt dependency contents differ from the captured environment')
        print(json.dumps({'verified': True, 'packageCount': len(packages), 'gpuValidation': 'not_run'}))
        return
    if current != inventory():
        raise CaptureError('Dependencies changed during capture; stop package installation and retry')
    value = {'schemaVersion': 1, 'baseImage': BASE_IMAGE, 'pythonVersion': '.'.join(map(str, sys.version_info[:3])),
             'packages': packages, 'checks': {key: True for key in sorted(CHECKS)}}
    print(json.dumps(validate_manifest(value), separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except (CaptureError, OSError, ValueError, zipfile.BadZipFile) as error:
        print(json.dumps({'error': str(error) if isinstance(error, CaptureError) else 'Environment capture failed; inspect dependency installation and retry'}), file=sys.stderr)
        raise SystemExit(1)
