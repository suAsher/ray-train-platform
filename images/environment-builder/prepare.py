#!/usr/bin/env python3
"""Prepare fixed wheel materials from a validated environment capture.

Runs without registry push credentials. The index is an administrator-configured
argument; it never comes from a workspace manifest or user build request.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from urllib.parse import urlsplit

from environment_runtime import BASE_IMAGE, CaptureError, read_manifest, wheel_fingerprint, safe_error

MAX_CONTEXT_BYTES = 4 * 1024 * 1024 * 1024
RUNTIME_ROOT = Path('/usr/local/lib/raytrain-environment')


def pip_environment():
    # Do not pass workspace variables, index credentials or extra-index config.
    return {'PATH': os.environ.get('PATH', '/usr/local/bin:/usr/bin:/bin'),
            'HOME': '/tmp', 'PIP_CONFIG_FILE': os.devnull, 'PIP_DISABLE_PIP_VERSION_CHECK': '1',
            'PYTHONNOUSERSITE': '1', 'LANG': 'C.UTF-8'}


def download_wheels(manifest, context, index):
    parsed = urlsplit(index)
    if parsed.scheme != 'https' or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise CaptureError('Package index must be an administrator-configured HTTPS mirror without embedded credentials')
    wheelhouse = context / 'wheelhouse'
    wheelhouse.mkdir(mode=0o700)
    locked, materials, total = [], [], 0
    for package in manifest['packages']:
        requirement = package['name'] + '==' + package['version']
        before = set(wheelhouse.iterdir())
        result = subprocess.run([sys.executable, '-I', '-m', 'pip', 'download', '--no-deps',
                                 '--only-binary=:all:', '--no-cache-dir', '--index-url', index,
                                 '--dest', str(wheelhouse), requirement], env=pip_environment(),
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=300, check=False)
        downloaded = set(wheelhouse.iterdir()) - before
        if result.returncode or len(downloaded) != 1:
            raise CaptureError('Matching wheel is unavailable from the configured mirror: ' + requirement, 'WHEEL_UNAVAILABLE')
        wheel = downloaded.pop()
        total += wheel.stat().st_size
        if wheel.suffix != '.whl' or total > MAX_CONTEXT_BYTES:
            raise CaptureError('Wheel materials exceed the supported format or 4 GiB size limit')
        if wheel_fingerprint(wheel) != package['filesHash']:
            raise CaptureError('Mirror wheel differs from the installed dependency: ' + requirement + '; reinstall from the configured mirror', 'PACKAGE_MODIFIED')
        digest = hashlib.sha256()
        with wheel.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        value = digest.hexdigest()
        locked.append(requirement + ' --hash=sha256:' + value)
        materials.append({'name': package['name'], 'version': package['version'],
                          'file': wheel.name, 'sha256': value, 'sizeBytes': wheel.stat().st_size})
    (context / 'requirements.lock').write_text('\n'.join(locked) + '\n', encoding='utf-8')
    return materials


def prepare(manifest_path, context, index):
    manifest = read_manifest(manifest_path)
    if manifest['pythonVersion'] != '.'.join(map(str, sys.version_info[:3])):
        raise CaptureError('Materializer Python must exactly match the captured base Python')
    # Parent assigns a fresh per-operation directory on the build PVC.
    context.mkdir(mode=0o700, parents=False, exist_ok=False)
    try:
        materials = download_wheels(manifest, context, index)
        for name in ('environment_runtime.py', 'raytrain-environment', 'ray', 'torchrun', 'environment-shell.sh'):
            shutil.copyfile(RUNTIME_ROOT / name, context / name)
        (context / 'capture.json').write_bytes(Path(manifest_path).read_bytes())
        report = {'schemaVersion': 1, 'baseImage': BASE_IMAGE, 'wheels': materials,
                  'captureSha256': hashlib.sha256((context / 'capture.json').read_bytes()).hexdigest(),
                  'gpuValidation': 'not_run'}
        (context / 'materials.json').write_text(json.dumps(report, sort_keys=True), encoding='utf-8')
        return report
    except Exception:
        shutil.rmtree(context)
        raise


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--manifest', required=True)
    parser.add_argument('--context', required=True)
    parser.add_argument('--index-url', required=True)
    args = parser.parse_args()
    print(json.dumps(prepare(args.manifest, Path(args.context), args.index_url), separators=(',', ':')))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(json.dumps(safe_error(error)), file=sys.stderr)
        raise SystemExit(1)
