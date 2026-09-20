package helpdocs

import _ "embed"

// The page and executable tests use the exact same source, avoiding drift
// between a shortened prose example and the integration users actually copy.
//
//go:embed platform_mlflow.py
var platformMLflowPython string

const platformMLflowUsagePython = `import os
import torch
from platform_mlflow import PlatformMLflow

world_size = int(os.environ.get("WORLD_SIZE", "1"))
if world_size > 1 and "RANK" not in os.environ:
    raise RuntimeError("分布式任务缺少全局 RANK，请从训练框架获取，不要使用 LOCAL_RANK")
rank = int(os.environ.get("RANK", "0"))
if rank < 0 or rank >= world_size:
    raise RuntimeError("RANK 与 WORLD_SIZE 不一致")

reporter = PlatformMLflow(global_rank=rank)
try:
    torch.manual_seed(42)
    model = torch.nn.Linear(4, 1)
    optimizer = torch.optim.SGD(model.parameters(), lr=0.01)
    reporter.params({"optimizer": "SGD", "learning_rate": 0.01, "steps": 3})
    for step in range(3):
        x, y = torch.randn(8, 4), torch.randn(8, 1)
        optimizer.zero_grad()
        loss = torch.nn.functional.mse_loss(model(x), y)
        loss.backward()
        optimizer.step()
        reporter.metrics({"train/loss": loss.detach().item()}, step=step)
except BaseException:
    reporter.finish("FAILED")
    raise
else:
    reporter.finish("FINISHED")
`
