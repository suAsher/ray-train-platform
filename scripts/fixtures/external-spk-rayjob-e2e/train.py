import json
import os
from pathlib import Path

import ray


ray.init()


@ray.remote(num_gpus=1)
def gpu_probe():
    return {
        "cuda_visible_devices": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
        "gpu_ids": ray.get_gpu_ids(),
    }


result = ray.get(gpu_probe.remote())
output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
output.mkdir(parents=True, exist_ok=True)
(output / "external-spk-rayjob-e2e.json").write_text(json.dumps(result), encoding="utf-8")
print("EXTERNAL_SPK_RAYJOB_E2E", json.dumps(result), flush=True)
