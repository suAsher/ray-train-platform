#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

torchrun_plan="$(python3 "$root/raytrain-launch.py" --mode torchrun --workers 1 --gpus-per-worker 2 --print-plan -- python train.py)"
python3 - "$torchrun_plan" <<'PY'
import json
import sys

plan = json.loads(sys.argv[1])
assert plan["mode"] == "torchrun", plan
assert plan["command"] == ["torchrun", "--standalone", "--nproc_per_node=2", "--no_python", "python", "train.py"], plan
PY

distributed_plan="$(python3 "$root/raytrain-launch.py" --mode ray_train --workers 2 --gpus-per-worker 8 --print-plan -- python train.py)"
python3 - "$distributed_plan" <<'PY'
import json
import sys

plan = json.loads(sys.argv[1])
assert plan["mode"] == "ray_train", plan
assert plan["workers"] == 2 and plan["gpusPerWorker"] == 8, plan
assert plan["placementStrategy"] == "STRICT_SPREAD", plan
assert plan["placementBundles"] == [{"CPU": 1, "GPU": 8}, {"CPU": 1, "GPU": 8}], plan
PY

echo 'raytrain launcher contract: ok'
