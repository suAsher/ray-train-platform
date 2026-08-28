# Ray Train 托管训练使用指南

本文说明如何在同一平台上选择兼容的 **Ray 编排 DDP** 与新的 **Ray Train 托管** 引擎。生产托管运行时固定为 **Ray 2.56.1**，Kubernetes 控制器升级目标固定为 **KubeRay 1.6.2**；Ray 2.58.0 只用于显式租户 canary。托管能力由 `RAY_TRAIN_MANAGED_ENABLED` 控制，默认关闭。本文是发布与验收契约，不代表任一集群已经升级或完成生产验证。

| 引擎 | CLI 参数 | 适用代码 | 运行语义 |
| --- | --- | --- | --- |
| Ray 编排 DDP | `--engine ray-ddp` | 现有 `torch.distributed`、MMCV runner 等项目 | 平台编排 Ray Worker，用户代码继续管理 DDP。 |
| Ray Train 托管 | `--engine ray-train` | 可由 Ray Train 启动并上报 checkpoint/指标的 Python 项目 | 平台管理 Worker、失败恢复、checkpoint 与训练指标。 |

`engine` 与历史的执行配置不是同一概念。已有 `ray-ddp` 任务和镜像不被原地改写；切换引擎必须新建任务。管理员启用方式和 KubeRay 升级门禁见[生产运维手册](OPERATIONS_GUIDE.md)。

## 代码、镜像与入口契约

代码随任务上传：Portal 使用交互会话创建工作区快照，`spk-rayjob` 和原生 Ray CLI 上传当前 working directory。镜像只承载 CUDA、PyTorch、Ray 和系统依赖，**修改 Python 或配置代码无需重建镜像**；系统包、CUDA 或固定 Python 依赖变化才需要新镜像 digest。

托管引擎只接受两种 Python 入口：

```text
python file.py
python -m package.module
```

不要在入口中写 `torchrun`、`python3`、shell 管道或重定向。平台会建立 Ray Train worker group；用户入口再次启动分布式进程会造成重复 rank。提交包应排除数据集、checkpoint、虚拟环境和构建产物，但必须包含入口、配置及其导入的源码。

## 三种提交方式

### Web Portal

1. 使用浏览器交互会话登录；Portal 工作区快照不接受 PAT。
2. 在调试工作区确认 `python file.py` 可导入依赖，将代码保存到 `/workspace`。
3. 新建训练任务，选择“Ray Train 托管”、Ray 2.56.1 镜像、数据模式、Worker 数和每 Worker GPU 数。
4. 入口填写 `python train.py --config configs/train.yaml`，选择“当前工作区快照”，再提交。
5. 在任务详情核对 `engine=ray-train`、资源拓扑及 `submissionOrigin=portal`，然后查看日志、性能和产物。

Portal 会把快照作为不可变代码制品绑定到任务；后续修改工作区不会改变已提交任务。

### spk-rayjob

在项目根目录登录后提交当前代码：

```bash
spk-rayjob submit \
  --dir . \
  --engine ray-train \
  --image 'registry.example.invalid/training/ray:2.56.1@sha256:<digest>' \
  --workers 2 \
  --gpus-per-worker 8 \
  --input-space public \
  --input-path datasets/example/v1 \
  --output-path runs/managed-example \
  --max-failures 2 \
  --checkpoint-every-epochs 1 \
  --checkpoint-keep-latest 3 \
  --checkpoint-keep-best 1 \
  --entrypoint 'python train.py --config configs/train.yaml' \
  --watch
```

服务端持久化的来源应为 `submissionOrigin=ray-cli`。继续运行未经 Ray Train 适配的项目时，显式使用兼容引擎：

```bash
spk-rayjob submit --dir . --engine ray-ddp --workers 2 --gpus-per-worker 8 \
  --entrypoint 'python tools/train.py configs/train.yaml'
```

不要在普通用户命令中使用验收脚本的内部标记或故障注入参数。

### 原生 Ray API

原生入口适合已有 Ray 自动化。客户端版本与生产运行时保持一致：

```bash
python3 -m pip install 'ray[default]==2.56.1'
export RAY_ADDRESS='https://<platform-host>/ray'
export RAY_JOB_HEADERS='{"Authorization":"Bearer <platform-pat>"}'

ray job submit \
  --address "$RAY_ADDRESS" \
  --working-dir . \
  --metadata-json '{"platform.training.engine":"ray-train","ray-platform.worker-replicas":"2","ray-platform.gpus-per-worker":"8","ray-platform.cpu-per-worker":"64","ray-platform.memory-per-worker":"256Gi","ray-platform.image":"registry.example.invalid/training/ray:2.56.1@sha256:<digest>","ray-platform.queue":"tenant-gpu","platform.data.input-space":"public","platform.data.input-path":"datasets/example/v1"}' \
  -- python train.py --config configs/train.yaml
```

平台网关必须从认证主体和持久化元数据建立任务，客户端不能指定租户 namespace、RayCluster 或 Kubernetes selector。原生入口的产品来源语义是 `submissionOrigin=ray-native`；若目标环境仍返回其他来源值，应视为版本漂移并停止验收，而不是在文档中假定已经升级。

## MMCV/MMEngine 适配

现有 runner 只需在初始化处安装平台提供的恢复与上报 Hook；不要再次初始化已经由 Ray Train 建立的进程组。仓库中的完整适配参考 [ray_train_managed.py](../examples/bevfusion/patches/ray_train_managed.py)。核心形状如下：

```python
import os


def configure_ray_train_managed_hook(cfg):
    if os.environ.get("PLATFORM_TRAINING_ENGINE") != "ray-train":
        return cfg

    from raytrain_runtime.mmcv_hook import build_hook_config, build_restore_hook_config

    restore = {**build_restore_hook_config(), "priority": "VERY_HIGH"}
    reporting = {
        **build_hook_config(
            interval=int(cfg.log_config.get("interval", 10)),
            checkpoint_every_epochs=int(
                os.environ.get("RAYTRAIN_CHECKPOINT_EVERY_EPOCHS", "1")
            ),
            keep_latest=int(os.environ.get("RAYTRAIN_CHECKPOINT_KEEP_LATEST", "3")),
            keep_best=int(os.environ.get("RAYTRAIN_CHECKPOINT_KEEP_BEST", "1")),
            best_metric=os.environ.get("RAYTRAIN_CHECKPOINT_BEST_METRIC", ""),
            best_mode=os.environ.get("RAYTRAIN_CHECKPOINT_BEST_MODE", "max"),
        ),
        "priority": "VERY_LOW",
    }
    existing = [
        dict(hook)
        for hook in cfg.get("custom_hooks", [])
        if hook.get("type")
        not in {"RayTrainManagedRestoreHook", "RayTrainManagedHook"}
    ]
    cfg.custom_hooks = [*existing, restore, reporting]
    return cfg
```

构造 `Config` 后调用 `cfg = configure_ray_train_managed_hook(cfg)`；初始化分布式环境时使用 `if distributed and not torch.distributed.is_initialized(): init_dist(...)`。项目可以保留原来的 MMCV optimizer、sampler 和 checkpoint 配置；适配层只负责从 Ray Train session 恢复，并在 rank 安全的边界上报。

## checkpoint 与 resume

训练循环应使用 Ray Train API 报告进度，并以原子方式生成 checkpoint 内容和 complete manifest：

```python
import os
from pathlib import Path
from ray import train
from raytrain_runtime.reporting import report_metrics
from ray_train_checkpoint_smoke import (
    load_complete_checkpoint,
    resume_start_step,
    write_checkpoint_atomic,
)

context = train.get_context()
rank = int(context.get_world_rank())
resume_path = os.environ.get("RAYTRAIN_RESUME_CHECKPOINT_PATH") or os.environ.get(
    "PLATFORM_CHECKPOINT_PATH"
)
state = load_complete_checkpoint(Path(resume_path)) if resume_path else None
output_root = Path(os.environ["RAYTRAIN_CHECKPOINT_OUTPUT_PATH"])
for step in range(resume_start_step(state), max_steps):
    metrics = train_one_step(step)
    checkpoint_path = None
    if rank == 0:
        checkpoint_path = Path(output_root) / f"checkpoint-step-{step:09d}"
        write_checkpoint_atomic(
            checkpoint_path,
            step=step,
            world_size=int(context.get_world_size()),
            rank=rank,
            metrics=metrics,
        )
    report_metrics(metrics, checkpoint_path, world_rank=rank, train_api=train)
```

父任务成功写出完整 manifest 后，才能创建 resume 子任务。子任务详情必须记录父任务、恢复 checkpoint 和集群 attempt；日志或结果应证明 `resumed=True`，且首个 step 等于父任务 step 加一。缺少 manifest、摘要不一致或文件不完整都表示不可恢复，不能回退成从零训练而伪装成功。可独立运行的参考见 [ray_train_checkpoint_smoke.py](../examples/distributed-demo/ray_train_checkpoint_smoke.py)。

## 数据模式

| 模式 | 引擎 | 行为与边界 |
| --- | --- | --- |
| `mount` | `ray-ddp`、`ray-train` | 从 TOS、FSX 或已登记 IDC 数据源挂载只读输入；输出写个人结果空间。 |
| `cache` | `ray-ddp`、`ray-train` | 在任务级 NVMe PVC 中预热或运行期缓存；缓存可消失，TOS/FSX/IDC 仍是数据真相。 |
| `ray-data` | 仅 `ray-train` | 由 Ray Data 构建 shard 并交给每个 Train worker；不要与用户手工 rank 切分叠加。 |

缓存命中不改变输入版本；checkpoint 永远不能只写 NVMe。容量、清理和性能验收见 [NVMe 缓存指南](NVME_CACHE_GUIDE.md)。

## 观测、日志与性能诊断

- **Ray Dashboard**：任务运行期间查看 actor、placement group、对象存储和 worker；任务回收后不作为历史系统。
- **MLflow**：记录参数、loss、评估结果和模型产物；平台视图按授权过滤，原生管理界面权限边界以部署策略为准。
- **Loki**：保存 head、worker 和 submitter 日志；先从平台任务详情的聚合日志定位 rank 与 Pod。
- **Prometheus/Grafana**：查看 CPU、内存、网络、GPU、Ray object store、spill、缓存和训练循环指标。

`report_metrics` 支持 `step`、`step_time`、`data_time` 与 NCCL duration。性能页按 worker rank/GPU 显示序列；缺失指标保持 `null` 并列入 unavailable metrics，不能当作零。诊断顺序是：先确认 worker 数与 GPU 身份，再比较 `data_time`/`step_time`，然后查看 NCCL、GPU 利用率/显存、网络、object spilling 和 cache hit/miss。全局 step 取最大值，时间和 GPU 指标使用各 worker 最新值的确定性平均，真实累计量求和。

## 失败含义

| 现象 | 含义 | 处理 |
| --- | --- | --- |
| 提交被拒绝为功能未启用 | 租户未通过 feature gate | 使用 `--engine ray-ddp`，或由管理员按 canary 流程启用；不要修改已有任务。 |
| `PENDING` | 等待 Kueue 准入、配额或节点 | 查看队列原因；不要删除别人的 Workload。 |
| worker restart / 新 cluster attempt | Ray Train 正在受策略限制地恢复 | 核对 recovery timeline、checkpoint 和首个恢复 step。 |
| checkpoint incomplete | complete manifest 缺失或校验失败 | 修复持久化路径后新建任务，不得选择不完整 checkpoint。 |
| 指标为 `null` | 对应 exporter/series 暂时不可用 | 查看 unavailable metrics 与日志，不能解释为使用量为零。 |
| `FAILED` | 入口、数据、代码、资源或恢复策略最终失败 | 保留任务 ID、attempt、rank 日志和产物摘要后排障。 |

## 发布与回退边界

管理员先在零训练负载窗口升级并核验 KubeRay 1.6.2，再以租户 canary 启用托管引擎。关闭 `RAY_TRAIN_MANAGED_ENABLED` 只阻止新托管任务，不会、也不得改写运行中的 RayJob。回退时停止新准入，等待或保留现有任务，恢复已备份的控制面版本；不要把运行中任务转换成另一引擎。
