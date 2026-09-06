#!/usr/bin/env python3
"""Offline CPU compatibility check; never starts Ray or accesses credentials."""
import contextlib
import importlib
import importlib.metadata
import json
import os
import platform
import subprocess
import sys

EXPECTED = {'ray': '2.58.0', 'torch': '2.4.1', 'torchvision': '0.19.1',
            'pyarrow': '25.0.1', 'mlflow-skinny': '3.14.0'}
MODULES = ('ray', 'ray.train', 'ray.data', 'ray.train.torch', 'torch', 'torchvision',
           'pyarrow', 'mlflow', 'raytrain_runtime.managed_driver',
           'raytrain_runtime.ray_data', 'raytrain_runtime.shard_cache')


def check(version=importlib.metadata.version, importer=importlib.import_module,
          run=subprocess.run, executable=None, python_version=None):
    executable = executable or (lambda path: os.path.isfile(path) and os.access(path, os.X_OK))
    versions = {'python': python_version or platform.python_version()}
    errors = []
    if not versions['python'].startswith('3.10.'):
        errors.append('Python 3.10 required')
    for package, expected in EXPECTED.items():
        try:
            versions[package] = version(package)
            if versions[package].split('+')[0] != expected:
                errors.append(f'{package}: expected {expected}, got {versions[package]}')
        except Exception as exc:
            errors.append(f'{package}: {type(exc).__name__}')
    for name in MODULES:
        try:
            module = importer(name)
            if name == 'torch':
                versions['cuda'] = module.version.cuda
                if versions['cuda'] != '12.1':
                    errors.append('PyTorch CUDA runtime 12.1 required')
            if name == 'raytrain_runtime.managed_driver':
                versions['site_selection_protocol'] = module.SITE_SELECTION_PROTOCOL
                if module.SITE_SELECTION_PROTOCOL != 1:
                    errors.append('Site selection protocol 1 required')
        except Exception as exc:
            errors.append(f'import {name}: {type(exc).__name__}')
    for name in ('raytrain-launch', 'raytrain-managed'):
        if not executable('/usr/local/bin/' + name):
            errors.append(f'{name}: executable missing')
    try:
        result = run([sys.executable, '-m', 'pip', 'check'], capture_output=True, text=True,
                     timeout=120, check=False)
        pip_passed = result.returncode == 0
        if not pip_passed:
            errors.append('pip check failed; run python3 -m pip check for dependency details')
    except (OSError, subprocess.TimeoutExpired):
        pip_passed = False
        errors.append('pip check could not complete')
    return {'schema_version': 1, 'cpu_passed': not errors, 'versions': versions,
            'pip_check_passed': pip_passed, 'gpu_validation': 'not_run',
            'distributed_training_validation': 'not_run', 'errors': errors}


if __name__ == '__main__':
    # Imports may print diagnostics; keep stdout a single machine-readable JSON object.
    with contextlib.redirect_stdout(sys.stderr):
        report = check()
    print(json.dumps(report, ensure_ascii=False, sort_keys=True))
    sys.exit(0 if report['cpu_passed'] else 1)
