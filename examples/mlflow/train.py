"""Small GPU training loop that demonstrates the platform MLflow contract."""

from __future__ import annotations

import json
import math
import os
import time
from pathlib import Path

def required_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"platform did not inject {name}")
    return value


def platform_tags() -> dict[str, str]:
    return {
        "platform.job_id": required_env("RAYTRAIN_JOB_ID"),
        "platform.tenant_id": required_env("RAYTRAIN_TENANT_ID"),
        "platform.submitter_user_id": required_env("RAYTRAIN_SUBMITTER_USER_ID"),
        "platform.provenance": required_env("RAYTRAIN_MLFLOW_PROVENANCE"),
        "platform.cluster_attempt": required_env("RAYTRAIN_CLUSTER_ATTEMPT"),
    }


def main() -> None:
    import mlflow
    import torch

    if not torch.cuda.is_available():
        raise RuntimeError("the allocated training worker has no visible GPU")

    mlflow.set_tracking_uri(required_env("MLFLOW_TRACKING_URI"))
    mlflow.set_experiment(required_env("MLFLOW_EXPERIMENT_NAME"))
    output_dir = Path(required_env("PLATFORM_OUTPUT_PATH"))
    output_dir.mkdir(parents=True, exist_ok=True)

    device = torch.device("cuda:0")
    model = torch.nn.Linear(32, 1).to(device)
    optimizer = torch.optim.AdamW(model.parameters(), lr=0.01)
    generator = torch.Generator(device=device).manual_seed(42)

    with mlflow.start_run(
        run_name=required_env("MLFLOW_RUN_NAME"),
        tags=platform_tags(),
    ):
        mlflow.log_params({"optimizer": "AdamW", "batch_size": 256, "steps": 30})
        for step in range(30):
            features = torch.randn(256, 32, generator=generator, device=device)
            target = features.sum(dim=1, keepdim=True) * 0.1
            prediction = model(features)
            loss = torch.nn.functional.mse_loss(prediction, target)
            optimizer.zero_grad(set_to_none=True)
            loss.backward()
            optimizer.step()

            value = float(loss.detach().cpu())
            mlflow.log_metrics(
                {
                    "loss": value,
                    "learning_rate": optimizer.param_groups[0]["lr"],
                    "epoch": step / 10.0,
                    "gpu_memory_mb": torch.cuda.memory_allocated(device) / 1024**2,
                },
                step=step,
            )
            print(f"step={step:02d} loss={value:.6f}", flush=True)
            time.sleep(1)

        checkpoint = output_dir / "model.pt"
        torch.save(model.state_dict(), checkpoint)
        summary = {
            "job_id": required_env("RAYTRAIN_JOB_ID"),
            "final_loss": value,
            "checkpoint": str(checkpoint),
            "finite": math.isfinite(value),
        }
        (output_dir / "summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
        # All files stay under PLATFORM_OUTPUT_PATH. MLflow is metrics-only for
        # tenant workloads so it cannot become a data-download side channel.


if __name__ == "__main__":
    main()
