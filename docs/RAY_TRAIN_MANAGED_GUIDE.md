# Ray Train 托管训练使用指南

本文说明如何在同一平台上选择兼容的 **Ray 编排 DDP** 与 **Ray Train 托管** 引擎。当前生产集群已部署 **KubeRay 1.6.2**，Ray Train 生产运行时固定为 **Ray 2.56.1**，并向所有当前和未来团队开放；Ray 2.58.0 仍只作为关闭状态的 canary 版本。既有 Ray DDP 任务和镜像不会被原地改写。

2026-08-29 的生产验收任务 `job-861f7b92a2acc28ca2317277` 由普通团队用户通过 `spk-rayjob` 提交并成功结束。真实 S1H 数据 I/O 验收任务 `job-66808647523a42b4726f013a` 又覆盖了 20,010 个文件、19.03 GB 输入的 Ray Data 枚举、双 NVMe 预热和本地读取。两项验收覆盖 KubeRay 自动创建 RayCluster、Ray Train Worker、Ray Data、GPU collective、用户代码随任务上传、个人结果写入和 Portal 数据 API 可见性；它们是平台通用链路与真实数据 I/O 验收，不代表 BEVFusion 模型本身已切换到托管镜像。

| 引擎 | CLI 参数 | 适用代码 | 运行语义 |
| --- | --- | --- | --- |
| Ray 编排 DDP | `--engine ray-ddp` | 现有 `torch.distributed`、MMCV runner 等项目 | 平台编排 Ray Worker，用户代码继续管理 DDP。 |
| Ray Train 托管 | `--engine ray-train` | 可由 Ray Train 启动并上报 checkpoint/指标的 Python 项目 | 平台管理 Worker、失败恢复、checkpoint 与训练指标。 |

`engine` 与历史的执行配置不是同一概念。已有 `ray-ddp` 任务和镜像不被原地改写；切换引擎必须新建任务。管理员启用方式和 KubeRay 升级门禁见[生产运维手册](OPERATIONS_GUIDE.md)。

## 发布门禁与运行时版本

当前实现使用部署环境变量 allowlist，不应写成数据库里的 capability record。`ray-train` 的新提交按下面的矩阵判定；`ray-ddp` 不受 managed allowlist 影响：

| 全局开关与租户 | Ray 2.56.1 `ray-train` | Ray 2.58.0 canary |
| --- | --- | --- |
| `RAY_TRAIN_MANAGED_ENABLED=true`，tenant 非空 | 全局允许 | canary 还必须同时满足 `RAY_TRAIN_CANARY_ENABLED=true` 且 tenant 在 `RAY_TRAIN_CANARY_TENANTS` |
| 全局开关关闭、租户在 managed allowlist `RAY_TRAIN_MANAGED_TENANTS` | 仅该租户允许 | canary 还必须同时满足 canary 开关和 canary allowlist |
| 全局开关关闭、租户不在 managed allowlist | 拒绝 | 拒绝 |
| tenant 为空 | fail closed；不能提交托管任务 | fail closed |
| managed allowlist 为空 | 全局开关开启时允许所有非空团队；全局开关关闭时不授予任何团队 | canary 仍按独立开关与 allowlist 判定 |

生产发布值为 `RAY_TRAIN_MANAGED_ENABLED=true` 且 managed tenant allowlist 为空，因此所有非空团队都可提交 Ray 2.56.1 托管任务。`/api/v1/limits` 的 `runtime.availableEngines` 应包含 `ray-ddp` 和 `ray-train`，`runtime.productionRayVersion` 应为 `2.56.1`。协议字段 `version` 始终是 Ray Jobs API 协议版本 `4`，不要把它与 Ray 运行时版本混为一谈。

## 数据读取模式

Portal 的运行环境步骤提供三种模式；训练代码始终读取 `PLATFORM_DATASET_PATH`，不需要为缓存改造路径：

| Portal 选项 | CLI `--data-mode` | 行为 | 适用场景 |
| --- | --- | --- | --- |
| 直接读取 | `mount` | 直接读取所选 TOS/IDC 挂载 | 大文件、一次性任务、启动速度优先 |
| NVMe 预热 | `cache` | 每个 Worker 启动前由兼容预热器复制到本地缓存 | 既有 Ray DDP 任务、大量小文件 |
| Ray Data + NVMe | `ray-data-stage` | Ray Data 分布式枚举并复制到每个训练节点的两块 NVMe，建立稳定本地视图后再启动原 DataLoader | Ray Train 托管、大量小文件、希望看到预热统计 |

`ray-data-stage` 必须选择具体的数据集子目录，禁止选择整个公共、团队或个人根目录，避免误把多 TB 根目录一次性复制到每个节点。两块盘分别挂载为 `/mnt/cache` 与 `/mnt/cache2`，按文件摘要稳定分布；任务结束后临时卷随 Pod 回收，数据真相仍在 TOS/IDC，输出和 checkpoint 始终写持久存储。

CLI 示例：

```bash
spk-rayjob submit \
  --engine ray-train \
  --data-mode ray-data-stage \
  --image 'harbor.wellspiking.ai/guofeng.su/ray-train-pytorch-ray-train@sha256:46bd487ccaa6b19a0ceac2089942fd39e00697494325155d50f2e2bc2e7fdf0a' \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --cache-mode runtime \
  --cache-size 1Ti \
  --input-space public \
  --input-path labeled/<具体数据集版本> \
  --output-path managed-example \
  --entrypoint 'python train.py --config configs/train.yaml' \
  --watch
```

日志中的 `RAYTRAIN_RAY_DATA_STAGE_COMPLETE files=... bytes=... seconds=...` 表示预热完成。之后用户入口看到的 `PLATFORM_DATASET_PATH` 是双 NVMe 本地视图；用户不接触 TOS AK/SK，也不填写节点路径。

平台会自动为 Ray Data 算子保留 Ray 逻辑 CPU：64 CPU/8 GPU 的 Worker 节点中，每个 Train Worker 申请 7 CPU，给数据读取留下 8 CPU；16 CPU/1 GPU 时 Train Worker 申请 12 CPU。Pod 的 CPU request/limit 不变，原生 DataLoader 仍可使用容器 CPU。若节点连“每 GPU 1 CPU”之外的一个空闲 CPU 都没有，提交会在启动前明确失败，而不是永久等待。

同一批真实 S1H 文件的生产结果如下。预热是一次性冷启动成本，后续 epoch 重复读取才体现收益：

| 路径 | 文件/容量 | 冷启动或读取耗时 | 有效吞吐 |
| --- | ---: | ---: | ---: |
| 直接挂载读取 | 20,010 / 19.03 GB | 93.50 秒 | 203.5 MB/s，214 files/s |
| Ray Data 写入双 NVMe | 20,010 / 19.03 GB | 134.91 秒 | 一次性预热 |
| 双 NVMe 热读取 | 20,010 / 19.03 GB | 1.24 秒 | 15.38 GB/s，16,173 files/s |

Ray 在 Worker 和数据算子刚启动的数秒内可能打印一次 `Cluster resources are not enough...` 瞬时告警；只要随后出现 `RAYTRAIN_RAY_DATA_STAGE_PROGRESS` 就表示任务已获得资源。若 60 秒仍无任何 progress，再按资源不足处理。

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
  --output-path managed-example \
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

平台网关必须从认证主体和持久化元数据建立任务，客户端不能指定租户 namespace、RayCluster 或 Kubernetes selector。Portal 持久化为 `submissionOrigin=portal`；`spk-rayjob` 与原生 Ray API 均持久化为 `submissionOrigin=ray-cli`。后两种入口通过接入方式与原生提交的 `externalSubmissionId` 区分，不能通过伪造新的 origin 值区分。

对原生 managed 提交，服务端固定创建 `my-runs/native-ray/<job-id>` 输出，并只向训练容器提供 `PLATFORM_OUTPUT_PATH=/mnt/data/output`。调用方不能通过 metadata 指定 output、namespace、RayCluster 或 Kubernetes selector；未知的 placement/output metadata 会被拒绝。`ray-ddp` 的既有原生行为保持兼容，不会被强制改成 managed output。

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

父任务成功写出完整 manifest 后，使用 `spk-rayjob submit --engine ray-train --resume-from-job <old-job-id>` 创建子任务。客户端先调用 owner/tenant-scoped 的 `GET /api/v1/jobs/<old-job-id>/checkpoints`，选择服务端按 epoch/step 倒序返回的第一个 complete checkpoint；用户不能提供 checkpoint ID 或 ObjectPath。提交路径由已验证身份派生为父任务输出目录下的 `.platform/ray-train/<old-job-id>/checkpoints/<checkpoint-id>`，容器中的 `PLATFORM_CHECKPOINT_PATH` 直接指向含 `manifest.json` 的该目录。

服务端再次核对父任务、owner、checkpoint 和精确路径，然后持久化 `spec.parentJobId`（JSON 字段 `parentJobId`）与 `resumeCheckpointId`。没有完整 checkpoint 时提交失败；跨 owner/tenant、manifest 摘要无效、ObjectPath 不精确或目录不完整也必须失败，不能回退成从零训练而伪装成功。子任务日志或结果应证明 `resumed=True`，且首个 step 等于父任务 step 加一。可独立运行的参考见 [ray_train_checkpoint_smoke.py](../examples/distributed-demo/ray_train_checkpoint_smoke.py)。

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
| 提交被拒绝为功能未启用 | 全局托管开关被关闭，或目标环境不是当前生产 Profile | 使用 `--engine ray-ddp`，并让管理员核对生产 Profile；不要修改已有任务。 |
| `PENDING` | 等待 Kueue 准入、配额或节点 | 查看队列原因；不要删除别人的 Workload。 |
| worker restart / 新 cluster attempt | Ray Train 正在受策略限制地恢复 | 核对 recovery timeline、checkpoint 和首个恢复 step。 |
| checkpoint incomplete | complete manifest 缺失或校验失败 | 修复持久化路径后新建任务，不得选择不完整 checkpoint。 |
| 指标为 `null` | 对应 exporter/series 暂时不可用 | 查看 unavailable metrics 与日志，不能解释为使用量为零。 |
| `FAILED` | 入口、数据、代码、资源或恢复策略最终失败 | 保留任务 ID、attempt、rank 日志和产物摘要后排障。 |

## 发布与回退边界

生产环境已经完成 KubeRay 1.6.2 和 Ray Train 2.56.1 验收。以后升级控制面时仍须选择零训练负载窗口，并先走独立 canary。关闭 `RAY_TRAIN_MANAGED_ENABLED` 只阻止新托管任务，不会、也不得改写运行中的 RayJob。回退时停止新准入，等待或保留现有任务，恢复已备份的控制面版本；不要把运行中任务转换成另一引擎。
