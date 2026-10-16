#!/usr/bin/env python3
"""Offline CPU gate. A CUDA wheel import never proves a working GPU driver."""
import contextlib
import importlib
import importlib.metadata
import json
import os
import platform
import re
import shutil
import subprocess
import sys

ENVIRONMENT = '/opt/raytrain/environment'
EXPECTED = {'ray': '2.58.0', 'torch': '2.13.0+cu126',
            'torchvision': '0.28.0+cu126', 'pyarrow': '25.0.1',
            'mlflow-skinny': '3.14.0', 'ipykernel': '6.29.5',
            'pip': '26.2.1', 'setuptools': '84.0.0', 'wheel': '0.48.0'}
MODULES = ('ray', 'ray.train', 'ray.data', 'ray.train.torch', 'torch',
           'torchvision', 'pyarrow', 'mlflow', 'raytrain_runtime.managed_driver',
           'raytrain_runtime.ray_data', 'opentelemetry.exporter.prometheus')


def check(version=importlib.metadata.version, importer=importlib.import_module,
          run=subprocess.run, which=shutil.which, python_version=None,
          prefix=None):
    versions = {'python': python_version or platform.python_version()}
    errors = []
    if not versions['python'].startswith('3.10.'):
        errors.append('Python 3.10 required')
    if (prefix or sys.prefix) != ENVIRONMENT:
        errors.append('Managed Python interpreter required')
    for name, expected in EXPECTED.items():
        try:
            versions[name] = version(name)
            if versions[name] != expected:
                errors.append(f'{name}: expected {expected}, got {versions[name]}')
        except Exception as exc:
            errors.append(f'{name}: {type(exc).__name__}')
    for name in MODULES:
        try:
            module = importer(name)
            if name == 'torch':
                versions['cudaRuntime'] = module.version.cuda
                if module.version.cuda != '12.6':
                    errors.append('PyTorch CUDA runtime 12.6 required')
        except Exception as exc:
            errors.append(f'import {name}: {type(exc).__name__}')
    for name, path in {'python': ENVIRONMENT + '/bin/python',
                       'ray': '/opt/raytrain/environment-wrappers/ray',
                       'torchrun': '/opt/raytrain/environment-wrappers/torchrun',
                       'nvcc': '/usr/local/cuda/bin/nvcc',
                       'raytrain-launch': '/usr/local/bin/raytrain-launch',
                       'raytrain-managed': '/usr/local/bin/raytrain-managed'}.items():
        if which(name) != path:
            errors.append(f'{name}: unexpected executable path')
    try:
        result = run(['nvcc', '--version'], capture_output=True, text=True,
                     timeout=30, check=False)
        match = re.search(r'release ([0-9]+\.[0-9]+), V([0-9.]+)', result.stdout)
        versions['nvcc'] = match.group(2) if match else 'unknown'
        if result.returncode or not match or match.group(1) != '12.6':
            errors.append('CUDA nvcc toolkit 12.6 required')
    except (OSError, subprocess.TimeoutExpired):
        errors.append('nvcc could not complete')
    try:
        result = run([sys.executable, '-m', 'pip', 'check'], capture_output=True,
                     text=True, timeout=120, check=False)
        pip_passed = result.returncode == 0
        if not pip_passed:
            errors.append('pip check failed')
    except (OSError, subprocess.TimeoutExpired):
        pip_passed = False
        errors.append('pip check could not complete')
    return {'schemaVersion': 1, 'cpuPassed': not errors, 'versions': versions,
            'pipCheckPassed': pip_passed, 'gpuValidation': 'not_run',
            'multiNodeValidation': 'not_run', 'errors': errors}


if __name__ == '__main__':
    with contextlib.redirect_stdout(sys.stderr):
        report = check()
    print(json.dumps(report, sort_keys=True))
    sys.exit(0 if report['cpuPassed'] else 1)
