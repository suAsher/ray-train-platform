#!/usr/bin/env python3
"""Real CPU Ray process contract, isolated from the production cluster."""
import json
import os
import subprocess
import sys

import ray

ENVIRONMENT = '/opt/raytrain/environment'


def main():
    assert os.geteuid() == 1000
    assert sys.prefix == ENVIRONMENT
    for shell in ('/bin/sh', '/bin/bash'):
        prefix = subprocess.check_output(
            [shell, '-lc', 'python -c "import sys; print(sys.prefix)"'],
            text=True, timeout=30).strip()
        assert prefix == ENVIRONMENT, (shell, prefix)
    subprocess.run(['ray', '--version'], check=True, capture_output=True, timeout=30)
    subprocess.run(['torchrun', '--help'], check=True, capture_output=True, timeout=30)
    ray.init(num_cpus=2, include_dashboard=False,
             object_store_memory=100 * 1024 * 1024, _node_ip_address='127.0.0.1')
    try:
        @ray.remote
        class Interpreter:
            def versions(self):
                import sys
                import torch
                import ray
                return {'prefix': sys.prefix, 'torch': torch.__version__,
                        'cudaRuntime': torch.version.cuda, 'ray': ray.__version__}
        actor = Interpreter.remote()
        result = ray.get(actor.versions.remote(), timeout=120)
        assert result == {'prefix': ENVIRONMENT, 'torch': '2.13.0+cu126',
                          'cudaRuntime': '12.6', 'ray': '2.58.0'}, result
        print(json.dumps({'rayActor': 'passed', **result, 'gpuValidation': 'not_run'}))
    finally:
        ray.shutdown()


if __name__ == '__main__':
    main()
