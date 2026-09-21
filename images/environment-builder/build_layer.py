#!/usr/bin/env python3
"""Build a bounded dependency layer without nested containers or registry auth."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import sys
import tarfile

from environment_runtime import BASELINE, ENVIRONMENT, CaptureError, read_manifest
from prepare import prepare, pip_environment

MAX_LAYER_BYTES = 8 * 1024 * 1024 * 1024
RUNTIME_ROOT = Path('/usr/local/lib/raytrain-environment')
# These destinations are a contract with the independent trusted OCI assembler.
FIXED_FILES = {
    'environment_runtime.py': 'usr/local/lib/raytrain-environment/environment_runtime.py',
    'raytrain-environment': 'usr/local/bin/raytrain-environment',
    'ray': 'opt/raytrain/environment-wrappers/ray',
    'torchrun': 'opt/raytrain/environment-wrappers/torchrun',
    'environment-shell.sh': 'opt/raytrain/environment-shell.sh',
}


def safe_venv_link(archive_name, target):
    if target.startswith('/'):
        raise CaptureError('Absolute links cannot be exported from the managed environment')
    parts = list(PurePosixPath(archive_name).parent.parts)
    for part in PurePosixPath(target).parts:
        if part == '..':
            if not parts:
                raise CaptureError('Environment link escapes the managed prefix')
            parts.pop()
        elif part != '.':
            parts.append(part)
    if parts[:3] != ['opt', 'raytrain', 'environment']:
        raise CaptureError('Environment link escapes the managed prefix')


def add_path(archive, path, name):
    info = archive.gettarinfo(str(path), arcname=name)
    if not (info.isfile() or info.isdir() or info.issym()):
        raise CaptureError('Environment contains unsupported special files')
    if info.issym():
        safe_venv_link(name, info.linkname)
    info.uid = info.gid = 0
    info.uname = info.gname = ''
    info.mtime = 0
    info.mode = 0o777 if info.issym() else (0o755 if info.isdir() or (info.isfile() and info.mode & 0o111) else 0o644)
    if info.isfile():
        with path.open('rb') as stream:
            archive.addfile(info, stream)
    else:
        archive.addfile(info)
    return info.size


def export_layer(destination, context):
    total = 0
    with tarfile.open(destination, 'w', format=tarfile.PAX_FORMAT, dereference=False) as archive:
        for root, directories, files in os.walk(ENVIRONMENT, followlinks=False):
            directories.sort()
            files.sort()
            # Python cache is regenerated; it is not a dependency material.
            directories[:] = [item for item in directories if item != '__pycache__']
            root_path = Path(root)
            total += add_path(archive, root_path, root_path.relative_to('/').as_posix())
            for item in list(directories):
                path = root_path / item
                if path.is_symlink():
                    total += add_path(archive, path, path.relative_to('/').as_posix())
                    directories.remove(item)
            for name in files:
                if name.endswith('.pyc'):
                    continue
                path = root_path / name
                total += add_path(archive, path, path.relative_to('/').as_posix())
                if total > MAX_LAYER_BYTES:
                    raise CaptureError('Managed environment layer exceeds 8 GiB')
        for source, name in FIXED_FILES.items():
            total += add_path(archive, RUNTIME_ROOT / source, name)
        total += add_path(archive, RUNTIME_ROOT / 'environment-shell.sh', 'etc/profile.d/raytrain-environment.sh')
        total += add_path(archive, BASELINE, 'opt/raytrain/environment-baseline.json')
        for name in ('capture.json', 'requirements.lock', 'materials.json'):
            total += add_path(archive, context / name, 'opt/raytrain/environment-materials/' + name)
    digest = hashlib.sha256()
    with destination.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return 'sha256:' + digest.hexdigest()


def build(manifest_path, artifacts, index):
    manifest = read_manifest(manifest_path)
    if Path(sys.prefix) != ENVIRONMENT:
        raise CaptureError('Builder must use the fixed managed Python environment')
    if artifacts.is_symlink() or not artifacts.is_dir():
        raise CaptureError('Build artifacts must use the assigned operation volume')
    if (artifacts / 'layer.tar').exists() or (artifacts / 'context').exists():
        raise CaptureError('This build operation already has materials; resume its recorded stage')
    context = artifacts / 'context'
    materials = prepare(manifest_path, context, index)
    environment = pip_environment()
    environment['PATH'] = str(ENVIRONMENT / 'bin') + ':' + environment['PATH']
    commands = [
        [sys.executable, '-I', '-m', 'pip', 'install', '--no-index', '--no-deps', '--require-hashes',
         '--find-links=' + str(context / 'wheelhouse'), '-r', str(context / 'requirements.lock')],
        [sys.executable, '-I', '-m', 'pip', 'check'],
        ['/usr/local/bin/raytrain-environment', 'verify', '--manifest', str(context / 'capture.json')],
        ['/usr/local/bin/raytrain-selfcheck'],
        [sys.executable, '-I', '-c', 'import ray, torch, ray.train, raytrain_runtime.managed_driver'],
    ]
    for command in commands:
        result = subprocess.run(command, env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=600, check=False)
        if result.returncode:
            raise CaptureError('Offline dependency or platform compatibility validation failed; inspect the added dependencies')
    temporary = artifacts / 'layer.tar.partial'
    try:
        digest = export_layer(temporary, context)
        size = temporary.stat().st_size
        if size > MAX_LAYER_BYTES:
            raise CaptureError('Managed environment layer exceeds 8 GiB')
        temporary.rename(artifacts / 'layer.tar')
    finally:
        temporary.unlink(missing_ok=True)
    result = {'schemaVersion': 1, 'layerSha256': digest, 'layerSizeBytes': size,
              'captureSha256': materials['captureSha256'], 'packageCount': len(manifest['packages']),
              'checks': {'wheelHashesVerified': True, 'rebuiltFilesVerified': True,
                         'pipCheck': True, 'platformCPU': True, 'gpuValidation': 'not_run'}}
    (artifacts / 'build-result.json').write_text(json.dumps(result, sort_keys=True), encoding='utf-8')
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--manifest', required=True)
    parser.add_argument('--artifacts', required=True)
    parser.add_argument('--index-url', required=True)
    parser.add_argument('--result', default='/dev/termination-log')
    args = parser.parse_args()
    result = json.dumps(build(args.manifest, Path(args.artifacts), args.index_url), separators=(',', ':'))
    Path(args.result).write_text(result, encoding='utf-8')
    print(result)


if __name__ == '__main__':
    try:
        main()
    except (CaptureError, OSError, ValueError, subprocess.SubprocessError, tarfile.TarError) as error:
        print(json.dumps({'error': str(error) if isinstance(error, CaptureError) else 'Environment layer build failed; retry with a fresh build operation'}), file=sys.stderr)
        raise SystemExit(1)
