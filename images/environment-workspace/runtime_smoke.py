#!/usr/bin/env python3
"""CPU environment contract. GPU and multi-node training need separate acceptance."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys

ENVIRONMENT = '/opt/raytrain/environment'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--ray-local', action='store_true')
    args = parser.parse_args()
    import ray
    import torch
    import ray.train
    import raytrain_runtime.managed_driver
    assert sys.prefix == ENVIRONMENT, (sys.prefix, ENVIRONMENT)
    assert ray.__version__ == '2.58.0', ray.__version__
    assert torch.__version__.split('+')[0] == '2.4.1', torch.__version__
    assert torch.version.cuda == '12.1', torch.version.cuda
    assert shutil.which('python') == ENVIRONMENT + '/bin/python'
    assert shutil.which('ray') == '/opt/raytrain/environment-wrappers/ray'
    assert shutil.which('torchrun') == '/opt/raytrain/environment-wrappers/torchrun'
    for executable in ('raytrain-launch', 'raytrain-managed', 'raytrain-selfcheck'):
        assert shutil.which(executable), executable
    command = [ENVIRONMENT + '/bin/python', '-c', 'import sys,json; print(json.dumps({"prefix":sys.prefix}))']
    child = json.loads(subprocess.check_output(command, text=True, timeout=30))
    assert child['prefix'] == ENVIRONMENT
    for shell in ('/bin/sh', '/bin/bash'):
        prefix = subprocess.check_output([shell, '-lc', 'python -c "import sys; print(sys.prefix)"'], text=True, timeout=30).strip()
        assert prefix == ENVIRONMENT, (shell, prefix)
    subprocess.run(['ray', '--version'], check=True, capture_output=True, timeout=30)
    subprocess.run(['torchrun', '--help'], check=True, capture_output=True, timeout=30)
    kernel = Path('/usr/local/share/jupyter/kernels/raytrain-environment/kernel.json')
    if kernel.exists():
        assert json.loads(kernel.read_text())['argv'][0] == ENVIRONMENT + '/bin/python'
    actor_check = 'not_run'
    if args.ray_local:
        ray.init(num_cpus=1, include_dashboard=False)
        try:
            @ray.remote
            def interpreter():
                import sys
                return sys.prefix
            assert ray.get(interpreter.remote()) == ENVIRONMENT
            actor_check = 'passed'
        finally:
            ray.shutdown()
    print(json.dumps({'python': sys.version.split()[0], 'prefix': sys.prefix,
                      'ray': ray.__version__, 'torch': torch.__version__,
                      'cudaRuntime': torch.version.cuda, 'rayActor': actor_check,
                      'gpuValidation': 'not_run', 'multiNodeValidation': 'not_run'}))


if __name__ == '__main__':
    main()
