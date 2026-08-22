#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
grep -F 'dist.init_process_group(backend="nccl")' "$root/ddp_smoke.py"
grep -F 'DDP_SMOKE' "$root/ddp_smoke.py"
grep -F -- '--execution-mode torchrun' "$root/submit-ddp-demo.sh"
grep -F -- '--execution-mode ray_train' "$root/submit-ray-train-demo.sh"
echo 'distributed smoke submission contracts: ok'
