import os
import socket
import subprocess

result = {
    "host": socket.gethostname(),
    "cuda_visible_devices": os.environ.get("CUDA_VISIBLE_DEVICES", ""),
    "nvidia_smi": subprocess.check_output(
        [
            "nvidia-smi",
            "--query-gpu=index,name,uuid",
            "--format=csv,noheader",
        ],
        text=True,
        timeout=15,
    ).strip(),
}
print(f"smoke_gpu_probe={result}", flush=True)
print("platform_training_loss=0.123", flush=True)
print("platform_training_throughput=1", flush=True)
print("platform_learning_rate=0.001", flush=True)
print("platform_training_epoch=1", flush=True)
