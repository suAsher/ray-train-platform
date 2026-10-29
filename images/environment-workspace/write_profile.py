"""Image-build-only runtime profile writer; never run in a user workspace."""
import json
import os
from pathlib import Path
import sys

import ray
import torch

import environment_runtime


def main():
    if os.geteuid() != 0:
        raise SystemExit('The runtime profile must be created by the image builder as root')
    if sys.version_info[:2] != (3, 10):
        raise SystemExit('Environment capture currently requires Python 3.10')
    interpreter = os.environ.get('ENVIRONMENT_BASE_PYTHON', '/home/ray/anaconda3/bin/python3')
    if interpreter not in ('/home/ray/anaconda3/bin/python3', '/usr/bin/python3.10'):
        raise SystemExit('Unsupported base interpreter')
    profile = {
        'schemaVersion': 1,
        'baseImage': os.environ.get('ENVIRONMENT_BASE_IMAGE', ''),
        'basePython': interpreter,
        'versions': {
            'python': '.'.join(map(str, sys.version_info[:3])),
            'pytorch': torch.__version__,
            'cuda': str(torch.version.cuda),
            'ray': ray.__version__,
        },
    }
    target = Path('/etc/raytrain/environment-profile.json')
    target.write_text(json.dumps(profile, separators=(',', ':')) + '\n')
    target.chmod(0o444)
    # Reuse the runtime's pinned reference, metadata and ownership checks.
    environment_runtime.load_profile()


if __name__ == '__main__':
    main()
