#!/usr/bin/env bash
set -euo pipefail

# Submit a real one-GPU RayTrain preflight from a checked-out BEVFusion branch.
# It validates source packaging, Ray, GPU visibility, the retained mmdet3d
# extension and the chosen data mount, then writes a small JSON report under
# the task output directory. It does not calculate business loss: that needs
# a labelled dataset satisfying docs/BEVFUSION_RUNBOOK.md.
#
# Usage:
#   SPK_RAYJOB_BIN=/tmp/spk-rayjob SPK_RAYJOB_CONFIG=/tmp/spk-rayjob.yaml \
#   examples/bevfusion/submit-preflight.sh \
#     /path/to/bevfusion <image@sha256:...> <job-name> <public-subpath>

source_dir="${1:?source directory is required}"
image="${2:?pinned image reference is required}"
job_name="${3:?DNS-safe job name is required}"
input_path="${4:?public data subpath is required}"

spk_rayjob_bin="${SPK_RAYJOB_BIN:-spk-rayjob}"
spk_rayjob_config="${SPK_RAYJOB_CONFIG:-}"
output_path="${OUTPUT_PATH:-e2e/${job_name}}"

if [[ ! -d "$source_dir/.git" ]]; then
  echo "source directory must be a Git checkout: $source_dir" >&2
  exit 2
fi
if [[ "$image" != *@sha256:* ]]; then
  echo "image must be a pinned digest reference" >&2
  exit 2
fi

python_source=$(cat <<'PY'
import json
import os
import pathlib
import pickle
import shutil

import ray


def install_image_extensions(runtime_root):
    image_root = pathlib.Path("/opt/bevfusion/mmdet3d")
    target_root = runtime_root / "mmdet3d"
    if not target_root.is_dir():
        return
    for extension in image_root.rglob("*.so"):
        target = target_root / extension.relative_to(image_root)
        if not target.exists():
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(extension, target)


@ray.remote(num_gpus=1)
def gpu_probe(dataset_path, output_path):
    install_image_extensions(pathlib.Path.cwd())

    import torch
    import mmdet3d
    from mmdet3d.ops import Voxelization

    assert torch.cuda.is_available(), "Ray GPU worker has no visible CUDA device"
    root = pathlib.Path(dataset_path)
    pkls = sorted(root.rglob("*.pkl"))
    assert pkls, f"no pkl under {root}"
    with pkls[0].open("rb") as handle:
        payload = pickle.load(handle)
    infos = payload.get("infos", payload) if isinstance(payload, dict) else payload
    first = infos[0] if isinstance(infos, (list, tuple)) and infos else {}
    result = {
        "ray": ray.__version__,
        "torch": torch.__version__,
        "cuda": torch.cuda.is_available(),
        "gpu": torch.cuda.get_device_name(0),
        "mmdet3d": mmdet3d.__file__,
        "voxelization": Voxelization.__name__,
        "pkl": str(pkls[0].relative_to(root)),
        "pkl_top_level": sorted(payload.keys()) if isinstance(payload, dict) else type(payload).__name__,
        "first_info_fields": sorted(first.keys()) if isinstance(first, dict) else type(first).__name__,
    }
    output = pathlib.Path(output_path)
    output.mkdir(parents=True, exist_ok=True)
    (output / "preflight.json").write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    return result


ray.init(address="auto")
result = ray.get(gpu_probe.remote(os.environ["PLATFORM_DATASET_PATH"], os.environ["PLATFORM_OUTPUT_PATH"]))
print(json.dumps(result, ensure_ascii=False, sort_keys=True))
PY
)
encoded_source="$(printf '%s' "$python_source" | base64 | tr -d '\n')"
entrypoint="raytrain-bevfusion-prepare python3 -c \"import base64;exec(compile(base64.b64decode('${encoded_source}'), 'bevfusion-preflight', 'exec'))\""

args=(submit)
if [[ -n "$spk_rayjob_config" ]]; then
  args+=(--config "$spk_rayjob_config")
fi
args+=(
  --dir "$source_dir"
  --name "$job_name"
  --image "$image"
  --entrypoint "$entrypoint"
  --workers 1
  --gpus-per-worker 1
  --cpu-per-worker 8
  --memory-per-worker 32Gi
  --input-space public
  --input-path "$input_path"
  --output-path "$output_path"
)

exec "$spk_rayjob_bin" "${args[@]}"
