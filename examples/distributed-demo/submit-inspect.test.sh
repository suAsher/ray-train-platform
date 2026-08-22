#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
entrypoint="$repo_root/examples/distributed-demo/inspect_dataset.py"
submit_script="$repo_root/examples/distributed-demo/submit-inspect.sh"

test -f "$entrypoint"
test -x "$submit_script"
grep -F '@ray.remote(num_gpus=1)' "$entrypoint"
grep -F 'PLATFORM_DATASET_PATH' "$entrypoint"
grep -F 'final_merged_nuscenes_infos_train.pkl' "$entrypoint"
grep -F -- '--input-space public' "$submit_script"
grep -F -- '--output-path' "$submit_script"

echo 'dataset inspector submission contract: ok'
