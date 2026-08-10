import os
import socket

import ray


ray.init(address="auto", namespace="ray-platform-smoke")


@ray.remote(num_gpus=1)
def probe_gpu():
    return {
        "host": socket.gethostname(),
        "cuda_visible_devices": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
    }


result = ray.get(probe_gpu.remote())
print(f"smoke_gpu_probe={result}", flush=True)
print("platform_training_loss=0.123", flush=True)
print("platform_training_throughput=1", flush=True)
print("platform_learning_rate=0.001", flush=True)
print("platform_training_epoch=1", flush=True)
