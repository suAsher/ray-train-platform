#!/usr/bin/env bash
# Static safety contract; GPU execution must be tested on a disposable GPU cluster.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
grep -Fq 'COPY raytrain-launch.py /usr/local/bin/raytrain-launch' images/workspace/Dockerfile
grep -Fq 'COPY workspace/raytrain-launch.py /usr/local/bin/raytrain-launch' images/train-pytorch/Dockerfile
grep -Fq 'USER 65532:65532' images/source-materializer/Dockerfile
grep -Fq 'platform-safe-extract.py' images/source-materializer/Dockerfile
if grep -E 'wellspiking|westwell|14[.]103[.]' images/workspace/Dockerfile images/train-pytorch/Dockerfile images/source-materializer/Dockerfile; then
  echo 'Private build dependency detected' >&2; exit 1
fi
echo 'Runtime image safety contract passed'
