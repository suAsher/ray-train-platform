# 新训练代码接入与改造手册

本文用于把一个普通的 PyTorch 仓库接入 RayTrain。用户修改 Python 或配置后直接重新提交当前目录；只有 CUDA、PyTorch、系统库或编译扩展变化时才需要新镜像。

## 1. 容器内的稳定契约

训练代码只依赖以下环境变量，不要写 TOS 地址、AK/SK、PVC 名或节点路径：

| 变量 | 权限 | 用途 |
| --- | --- | --- |
| `PLATFORM_DATASET_PATH` | 只读 | 页面或 CLI 选中的输入数据目录 |
| `PLATFORM_OUTPUT_PATH` | 读写 | 当次任务的日志摘要、checkpoint 和结果 |
| `PLATFORM_CHECKPOINT_PATH` | 只读，可为空 | 续训时选中的历史任务结果 |
| `PLATFORM_CACHE_PATH` | 临时读写，可为空 | 本地 NVMe 缓存；任务结束可回收 |

最小 Python 入口：

```python
import os
from pathlib import Path

dataset = Path(os.environ["PLATFORM_DATASET_PATH"])
output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
checkpoint = os.environ.get("PLATFORM_CHECKPOINT_PATH", "")
cache = Path(os.environ.get("PLATFORM_CACHE_PATH", "/tmp"))

output.mkdir(parents=True, exist_ok=True)
cache.mkdir(parents=True, exist_ok=True)
```

## 2. 选择执行模式

| 资源形状 | 执行模式 | 用户入口 |
| --- | --- | --- |
| `1 × 1 GPU` | `single_gpu` | `python3 train.py ...` |
| `1 × N GPU` | `torchrun` | 仍然写 `python3 train.py ...` |
| `N × M GPU` | `ray_train` | 仍然写 `python3 train.py ...` |

不要在 entrypoint 中再包一层 `torchrun`、`raytrain-launch` 或 `ray.init()`。平台根据 Worker 和 GPU 数生成 rendezvous、Ray placement group 与每节点进程。

## 3. 让单机代码支持 DDP

如果当前代码已经支持 `torchrun`，通常无需修改分布式启动方式。自己维护训练循环时，使用 `torchrun` 注入的标准环境变量：

```python
import os
import torch
import torch.distributed as dist


def init_distributed():
    world_size = int(os.environ.get("WORLD_SIZE", "1"))
    distributed = world_size > 1
    local_rank = int(os.environ.get("LOCAL_RANK", "0"))
    if distributed:
        torch.cuda.set_device(local_rank)
        dist.init_process_group(backend="nccl", init_method="env://")
    return distributed, local_rank
```

模型包装与 `DistributedSampler` 示例：

```python
distributed, local_rank = init_distributed()
model = model.cuda(local_rank)
if distributed:
    model = torch.nn.parallel.DistributedDataParallel(
        model, device_ids=[local_rank], output_device=local_rank
    )

sampler = torch.utils.data.DistributedSampler(dataset, shuffle=True) if distributed else None
loader = torch.utils.data.DataLoader(
    dataset,
    sampler=sampler,
    shuffle=sampler is None,
    num_workers=8,
    pin_memory=True,
)

for epoch in range(start_epoch, max_epochs):
    if sampler is not None:
        sampler.set_epoch(epoch)
    train_one_epoch(model, loader, epoch)
```

## 4. checkpoint 与断点续训

只允许 global rank 0 写最终文件；其他 rank 不要同时向对象存储创建同名对象。

```python
from pathlib import Path
import os
import torch


def is_rank_zero():
    return int(os.environ.get("RANK", "0")) == 0


def save_checkpoint(model, optimizer, epoch, step):
    if not is_rank_zero():
        return
    output = Path(os.environ["PLATFORM_OUTPUT_PATH"])
    output.mkdir(parents=True, exist_ok=True)
    target = output / "latest.pth"
    torch.save({
        "model": model.module.state_dict() if hasattr(model, "module") else model.state_dict(),
        "optimizer": optimizer.state_dict(),
        "epoch": epoch,
        "step": step,
    }, target)
```

启动时优先从 `PLATFORM_CHECKPOINT_PATH` 读取：

```python
resume_root = os.environ.get("PLATFORM_CHECKPOINT_PATH", "")
resume_file = Path(resume_root) / "latest.pth" if resume_root else None
if resume_file and resume_file.is_file():
    state = torch.load(resume_file, map_location="cpu")
    model.load_state_dict(state["model"])
```

页面中选择历史任务 checkpoint，或使用：

```bash
spk-rayjob submit --resume-from-job <历史任务ID> --watch
```

## 5. 记录 MLflow 参数和指标

MLflow 是可选能力，不改变训练启动方式。平台已经注入 `MLFLOW_TRACKING_URI`、`MLFLOW_EXPERIMENT_NAME`、`MLFLOW_RUN_NAME` 及四个 `RAYTRAIN_*` 归属字段；代码只在 global rank 0 创建一次 run 并记录标量指标。不要让 16 个 rank 分别创建 16 条 run。

可直接参考 `examples/mlflow/train.py`，集成时至少满足：

1. run tags 包含平台注入的 job ID、tenant ID、submitter user ID 和 provenance；
2. batch size、学习率、模型版本等写 params；Loss、lr、吞吐、mAP/NDS 等带 step 写 metrics；
3. 模型和 Checkpoint 不调用 `mlflow.log_artifact`，仍写 `PLATFORM_OUTPUT_PATH`；
4. 异常退出时把 run 标记为 `FAILED`，正常结束标记为 `FINISHED`。

完整代码片段见[用户使用手册的 MLflow 实验中心章节](USER_GUIDE.md#mlflow-实验中心)。

## 6. 数据路径与索引文件

数据索引应保存相对于数据集根的路径，不要把制作机器上的 `/mnt/...` 绝对路径写入 pkl/json。

推荐发布布局：

```text
<dataset>/<version>/
├── manifest.json
├── annotations/
│   ├── train.pkl
│   └── val.pkl
└── raw/
```

`manifest.json` 至少记录版本、样本数、train/val/test 切分、生成代码 commit 和文件校验摘要。已有绝对路径索引时，需在 Dataset 层将其重定位到 `PLATFORM_DATASET_PATH`；BEVFusion 的实例见 [BEVFUSION_CODE_CHANGES.md](BEVFUSION_CODE_CHANGES.md)。

## 7. 代码包与忽略规则

提交前确认关键源码没有被 `.gitignore` 或 `.rayignore` 排除：

```bash
git check-ignore -v path/to/critical_module.py || true
git status --short
```

忽略规则尽量锚定到仓库根。例如用 `/run*/` 排除根目录运行输出；`run*/` 会在任意层级匹配，可能错误排除 `package/runner/`。

不应上传：`.git/`、本地数据集、checkpoint、编译缓存、私钥、PAT 或 Git Token。

## 8. 镜像与代码的边界

放进镜像：

- CUDA / cuDNN / NCCL；
- 固定 PyTorch 和 Ray 版本；
- `apt` 系统库、Python 基础依赖；
- 需编译的 C++ / CUDA 扩展。

随任务上传：

- Python 训练逻辑；
- YAML/JSON 参数；
- 评估与数据加载代码。

镜像必须以 `repository@sha256:<digest>` 登记和提交；不要使用可变 `latest` tag。

## 9. 提交前检查表

1. 单机单卡能在调试环境读取 `$PLATFORM_DATASET_PATH` 并写入 `$PLATFORM_OUTPUT_PATH`。
2. `torchrun --nproc-per-node=2 ...` 能在单机完成至少数个 iteration。
3. 所有 rank 都使用 `DistributedSampler`，只有 rank 0 写 checkpoint。
4. 任务可以从上一个 `latest.pth` 恢复 epoch/step/optimizer。
5. 关键模块未被 ignore，源码包不包含密钥和大数据。
6. 入口是普通 `python3 ...`，没有自己再启动 `torchrun`。
7. 先用 `1×1`，再用 `1×N`，最后用 `2×N`；逐级核对 loss、样本数和 checkpoint。
8. 需要实验对比时，仅由 rank 0 写 MLflow params/metrics，Checkpoint 仍写个人结果目录。

完成改造后，按 [多方式提交手册](SUBMIT_GUIDE.md) 从 Portal、`spk-rayjob` 或原生 Ray CLI 提交。
