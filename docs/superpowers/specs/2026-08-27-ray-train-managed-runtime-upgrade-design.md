# Ray Train 托管与运行时升级设计

**状态：** 已批准设计  
**日期：** 2026-08-27  
**生产目标：** Ray 2.56.1、KubeRay 1.6.2  
**灰度目标：** Ray 2.58.0  

## 1. 背景

平台当前以 Ray 2.35.0、KubeRay 1.3.x、Kueue 和按任务创建的 RayCluster
运行分布式训练。现有 `raytrain-launch` 通过 Ray Core Actor、Placement Group 和
`torchrun` 启动 PyTorch DDP。该路径已经支持 BEVFusion 单机多卡和多机多卡，但
Ray 主要承担资源编排，训练 Worker、指标、Checkpoint 和恢复仍由平台自定义逻辑
与用户代码管理。

Ray 官方升级指南明确建议不要使用 2.11.0 至 2.37.0，原因是该区间存在 Dashboard
Agent 卡死并影响健康探针的问题。Ray 2.35.0 位于此区间。设计批准时的最新正式版
为 Ray 2.58.0，KubeRay 最新正式版为 1.6.2。由于 Ray 2.58.0 发布较新，本次先以
Ray 2.56.1 作为生产基线，2.58.0 完成同一验收矩阵后再决定是否切换。

官方依据：

- [Ray Releases](https://github.com/ray-project/ray/releases)
- [KubeRay Releases](https://github.com/ray-project/kuberay/releases)
- [KubeRay 升级与兼容说明](https://docs.ray.io/en/latest/cluster/kubernetes/user-guides/upgrade-guide.html)
- [Ray Train 故障恢复](https://docs.ray.io/en/latest/train/user-guides/fault-tolerance.html)

## 2. 目标

1. 在不修改或中断现有训练任务的前提下升级 Ray 和 KubeRay。
2. 保留已跑通的 Ray 编排 DDP 路径，新增 Ray Train 托管路径。
3. 让 Ray 真正管理训练 Worker、分布式上下文、指标、Checkpoint 和故障恢复。
4. 用户修改代码后直接随任务上传，不因代码变化重新构建镜像。
5. 继续使用 Kueue 管理团队配额、排队和准入，保持一任务一 RayCluster 的隔离方式。
6. 统一平台、Ray Dashboard、MLflow、Loki 和 Prometheus 的职责与权限。
7. 为后续 Ray Data、Ray Tune 和 Ray History Server 留出稳定扩展边界。

## 3. 非目标

本次不做以下事项：

- 不将所有旧任务一次性强制迁移到 Ray Train。
- 不建立所有租户共享的长期 RayCluster。
- 不让 Ray 取代 Kubernetes、Kueue、TOS、MLflow、Loki 或 Prometheus。
- 不在第一阶段强制 BEVFusion 重写为 Ray Data Dataset。
- 不把 NVMe 作为 Checkpoint 或训练数据的唯一副本。
- 不把仍处于 alpha 阶段的 Ray History Server 作为平台核心依赖。
- 不在存在运行中 RayJob 或调试环境时升级 KubeRay CRD 和 Operator。

## 4. 架构决策

采用双引擎渐进托管。

### 4.1 Ray 编排 DDP

保留现有 Actor、Placement Group 和 `torchrun` 路径，正式命名为“Ray 编排 DDP”。
它用于现有 BEVFusion、MMDet 和尚未适配 Ray Train 的旧代码。

### 4.2 Ray Train 托管

新增以 `ray.train.torch.TorchTrainer` 为核心的托管路径。Ray Train 负责：

- 为每张 GPU 创建训练 Worker；
- 建立 Rank、Local Rank、World Size 和 PyTorch Process Group；
- 汇总训练指标；
- 管理 Checkpoint；
- 检测 Worker 和节点故障；
- 按策略重建 Worker Group，并从最近的有效 Checkpoint 恢复。

### 4.3 控制边界

| 组件 | 职责 |
|---|---|
| Kubernetes/VKE | Pod、节点、GPU、网络、卷和容器运行时 |
| Kueue | 团队配额、排队、准入和并发限制 |
| KubeRay | RayJob 和任务专属 RayCluster 生命周期 |
| Ray Train | 分布式 Worker、训练上下文、恢复和 Checkpoint |
| Ray Data | 可选的数据读取、分片、流式预取和背压 |
| MLflow | 实验参数、训练指标、比较和模型记录 |
| Loki/Prometheus | 长期日志与系统、资源、训练性能指标 |
| 平台 | 用户、团队、权限、提交、统一状态和用户体验 |

平台继续为每个训练任务创建独立 RayCluster。该方式保留租户隔离、独立资源回收、
独立 Dashboard 和明确的故障边界。固定规模同步 DDP 继续由 Kueue 进行 Gang 准入，
不在第一阶段启用 Ray Autoscaler 改变训练规模。

## 5. 版本策略

### 5.1 生产版本

- Ray 2.56.1；
- KubeRay 1.6.2；
- Python、CUDA、PyTorch 和 MMCV 由运行环境目录分别锁定；
- 正式任务固定镜像 digest，不依赖可漂移的 `latest`。

### 5.2 灰度版本

Ray 2.58.0 进入运行环境目录，但仅允许灰度团队使用。它必须通过与 2.56.1 相同的
功能、故障和性能验收矩阵，才能成为新的生产默认。

Canary 准入同时受全局主开关和团队白名单约束：只有
`RAY_TRAIN_CANARY_ENABLED=true` 且当前团队显式出现在
`RAY_TRAIN_CANARY_TENANTS` 中时，新提交才能选择 Ray 2.58.0。白名单为空时
没有任何团队获得 Canary 能力；能力接口只返回当前身份所在团队的有效开关，
不暴露白名单。

### 5.3 旧版本退出

Ray 2.35.0 只服务升级前已创建的任务，并保留一个发布周期作为紧急回退镜像。
生产切换后，平台禁止创建新的 Ray 2.35.0 任务。现有任务不原地升级、不替换镜像、
不修改 RayJob 或 RayCluster。

## 6. 提交与运行环境

代码与运行镜像继续分离：

- 运行镜像提供 Ray、PyTorch、CUDA、MMCV 和平台运行时；
- 用户代码通过 workspace archive/working directory 随任务上传；
- 训练参数随任务保存；
- 数据通过平台授权挂载或数据集版本提供；
- 只有系统依赖、CUDA 扩展或基础 Python 环境变化才需要构建新镜像。

运行环境目录至少保留：

| 环境 | 用途 |
|---|---|
| `pytorch-legacy` | Ray 2.35.0 现有任务和短期回退 |
| `pytorch-ray-ddp` | Ray 2.56.1 兼容 DDP |
| `pytorch-ray-train` | Ray 2.56.1 Ray Train 托管 |

任务模型新增以下不可变快照字段：

- `training_engine`: `ray-ddp` 或 `ray-train`；
- `ray_version`；
- `runtime_image_digest`；
- `nodes`；
- `gpus_per_node`；
- `max_failures`；
- `checkpoint_policy`；
- `data_mode`；
- `dataset_version_id`。

这些字段在任务创建后不能被普通更新接口修改。重新提交或续训生成新的任务记录，
并通过 `parent_job_id` 或 `resume_from_job_id` 建立来源关系。

## 7. 代码适配等级

### 7.1 基础托管

平台在 Ray Train Worker 内执行用户入口，并提供 Rank、Local Rank、World Size、Master
地址和设备上下文。用户不再手动执行 `torchrun`。现有 PyTorch DDP 代码可通过少量
入口调整运行。

基础托管具备 Worker 生命周期和重建能力。如果用户代码未向 Ray Train 上报
Checkpoint，故障后只能使用代码自身的恢复机制或从头运行。

### 7.2 完整托管

完整托管要求训练循环或框架 Hook：

- 使用 `ray.train.report()` 上报指标和可选 Checkpoint；
- 使用 `ray.train.get_checkpoint()` 获取恢复点；
- 所有 Worker 以相同调用次数参与报告；
- 仅 Rank 0 生成需要全局唯一的模型文件和元数据。

平台运行镜像提供 MMCV 适配 Hook。BEVFusion 不需要重写整个训练循环，只需在配置中
启用该 Hook。Hook 将 Runner 的 epoch、step、loss、mAP、NDS、学习率和 Checkpoint
转换为 Ray Train 报告。

## 8. 任务生命周期

平台使用以下用户可见状态：

1. 已创建；
2. 排队中；
3. 资源已准入；
4. 集群创建中；
5. Worker 启动中；
6. 训练中；
7. 正在保存 Checkpoint；
8. 故障恢复中；
9. 已成功、已失败或已取消。

平台分别记录排队时间、集群启动时间、训练开始时间、训练结束时间和资源回收时间。
RayCluster Ready 不等于训练已经开始。

任务的幂等键、RayJob 名称和平台任务 ID 一一对应。重试必须更新尝试次数，不能创建
用户不可见的重复任务记录。

## 9. 故障恢复

### 9.1 Worker 与节点故障

托管任务默认 `max_failures=2`。Worker 进程、Ray Worker Pod 或 GPU 节点故障后，
Ray Train 停止当前 Worker Group，等待 RayCluster 恢复所需资源，重建完整 Worker
Group，并从最新有效 Checkpoint 恢复。

同步 DDP 不允许在丢失一个节点后静默降低 World Size 继续运行。如果没有空闲 GPU
节点，任务保持恢复等待状态，直到故障节点恢复或新容量加入。Ray 保护训练进度，
但不能替代实际备用硬件。

### 9.2 Driver、Head 与整集群故障

Ray Train 处理 Worker Group 内故障。Driver、Head 或整个 RayCluster 故障由平台和
KubeRay 外层恢复：平台增加 `cluster_attempt`，创建新的任务集群，并把最近一次外部
持久化 Checkpoint 传入新的 Ray Train Run。

平台分别记录：

- `worker_restart_count`；
- `cluster_attempt`；
- `resume_checkpoint_id`；
- `last_failure_class`；
- `last_failure_message`。

## 10. Checkpoint

Checkpoint 的最终副本保存在用户 TOS 私有空间，通过 Pod 内共享路径访问：

```text
/mnt/storage/me/checkpoints/<project>/<job-id>/
├── checkpoint-epoch-001/
├── checkpoint-epoch-002/
├── checkpoint-best/
└── manifest.json
```

Checkpoint 至少包含：

- 模型、优化器、学习率调度器和 AMP Scaler 状态；
- epoch、step 和随机数状态；
- 数据集版本、代码摘要和镜像 digest；
- World Size 和训练参数摘要；
- 完整性摘要和完成标记。

只有完整上传并写入完成标记的 Checkpoint 可用于恢复。NVMe 只作为临时上传、下载和
解压缓存，不作为唯一副本。

默认策略：

- 每个 epoch 保存一次；
- 保留最近三个；
- 额外保留一个最佳指标 Checkpoint；
- 失败任务最后一个有效 Checkpoint 不自动删除；
- 删除任务不等于删除 Checkpoint；
- Checkpoint 删除需要单独确认和权限校验。

## 11. 数据路径与数据模式

现有路径保持兼容：

| 路径 | 语义 |
|---|---|
| `/mnt/storage/public` | 全平台公共只读数据 |
| `/mnt/storage/me` | 用户私有读写空间 |
| `/mnt/storage/team` | 团队共享空间，按角色授权 |
| `/mnt/cache`、`/mnt/cache2` | 节点两块 NVMe 临时缓存 |

新增稳定训练接口：

- `/mnt/data/input`：当前任务选定的数据集；
- `/mnt/data/output`：当前任务输出目录。

平台根据数据模式把稳定接口映射到 TOS 或 NVMe。新代码应优先使用稳定接口；直接
访问 `/mnt/storage/public` 的旧代码继续可用，但不会自动获得路径切换和缓存加速。

### 11.1 `mount`

直接读取授权的 TOS 挂载，启动快、不占 NVMe，适合预检、小任务和顺序读取。

### 11.2 `cache`

Ray 在每个训练节点运行数据准备任务，根据数据集版本清单预热本节点所需文件。
同一节点和同一数据集版本只保存一份。任务使用期间缓存被租约保护；任务结束后继续
保留供后续任务复用，空间不足时按 LRU 回收。

两块 NVMe 不做 RAID，分别使用独立目录。平台保留安全水位，不设置固定 500G 上限；
单盘故障只丢失该盘缓存，未命中数据回退到 TOS。缓存支持完整预热、按清单预热、
分节点预热和流式填充。

### 11.3 `ray-data`

Ray Data 用于已适配任务的分布式读取、Worker 分片、流式预处理、预取和背压。
对象溢写可使用 NVMe。Ray Data 与 NVMe 互补，不互相替代。

BEVFusion 第一阶段使用 Ray Train、现有 PyTorch DataLoader 和 NVMe 预热，不强制改写
Dataset。第二阶段选择一个数据集进行 Ray Data 试点，只有在相同代码、参数和数据集
版本下得到明确收益才推广。

超过缓存容量的数据集不要求完整落盘。平台优先按训练清单和节点分片缓存工作集，
其余数据从 TOS 流式读取。大量小文件数据集后续可登记派生的 Parquet、WebDataset 或
其他分片格式版本，原始数据仍保留为数据真相。

## 12. UI 与可观测性

任务详情页是统一入口，包含：

- 任务阶段、排队/启动/训练时长；
- epoch、step、World Size、Ray 版本和训练引擎；
- Worker Rank、节点、GPU、状态和重启次数；
- GPU 利用率、显存、功率和温度；
- Worker CPU、内存、共享内存和网络；
- NCCL 时间和错误；
- TOS/NVMe 吞吐、files/s、P95、缓存命中和 `data_time`；
- loss、mAP、NDS、学习率和 MLflow Run；
- Checkpoint、恢复来源和恢复次数；
- Loki 日志和受控的 Ray Dashboard 入口。

平台根据数据等待比例、GPU 空闲和 NCCL 时间给出可解释的瓶颈提示，但不自动修改用户
训练参数。

### 12.1 日志

Loki 是长期日志来源。日志携带平台任务 ID、Ray Train Run ID、Worker Rank、节点、
尝试次数和恢复次数。任务结束并删除 RayCluster 后，日志仍可查询。

### 12.2 MLflow

Ray Train 报告同步到现有 MLflow，保存参数、指标、数据集版本、代码摘要、镜像
digest 和 Checkpoint 引用。同一平台任务的 Worker 恢复继续使用同一个 MLflow Run，
整集群重建记录新的 attempt 标签而不是创建不可关联的孤立实验。

### 12.3 Ray Dashboard

运行期间通过平台鉴权代理访问任务专属 Ray Dashboard。相关 Service 为 ClusterIP，
不开放 NodePort。平台在代理层校验任务可见权限，用户不能通过 Ray API 绕过平台权限。

### 12.4 Ray History Server

KubeRay 1.6 的 History Server 只做灰度高级诊断。它仍处于 alpha，不参与平台任务状态、
日志或指标的核心读取路径；故障时不得影响提交和训练。

## 13. 权限

| 角色 | 可见和可操作范围 |
|---|---|
| 普通用户 | 自己的任务、调试环境、数据、实验和 Checkpoint |
| 团队管理员 | 本团队任务、成员、配额和团队共享数据 |
| 平台管理员 | 全部团队、任务、队列、节点和平台组件 |

停止、重试、删除、恢复、访问 Dashboard 和删除 Checkpoint 必须使用同一套平台鉴权。
Ray Head、TOS AK/SK、Kubernetes 凭据和底层缓存路径不暴露给用户。

## 14. 零中断升级步骤

### 14.1 冻结基线

记录当前任务、RayCluster、CRD、Operator、Helm values、数据库 schema、镜像 digest、
BEVFusion 性能和可观测性基线。

### 14.2 平台支持双版本

先上线向后兼容的数据模型、运行环境目录和功能开关。数据库迁移只增加字段和表。
默认仍提交 Ray 2.35.0 的 Ray 编排 DDP；平台滚动更新不得修改已创建的 RayJob。

### 14.3 并行发布 Ray 2.56.1 镜像

构建新 digest，不覆盖旧镜像。在现有 KubeRay 1.3.x 上先完成 1 GPU、1×8 和 2×8
灰度，验证 Kueue、GPU、TOS、NVMe、Loki、MLflow 和 Dashboard。

### 14.4 升级 KubeRay

仅在没有运行中 RayJob 和调试环境时：

1. 备份现有 CRD；
2. 显式升级 Ray CRD 到 1.6.2；
3. 升级 KubeRay Operator 到 1.6.2；
4. 验证 Operator 高可用、RBAC、Kueue 集成和状态条件；
5. 重跑 1 GPU 和 2×8 验收。

Helm 不会自动升级已安装 CRD，因此 CRD 更新必须作为独立、可审计步骤。

### 14.5 切换默认

验收后，新任务默认 Ray 2.56.1。Ray Train 先对灰度团队开放，再全平台开放。Ray
2.35.0 禁止新建任务。Ray 2.58.0 通过相同矩阵后再评估切换。

## 15. 验收矩阵

功能矩阵覆盖：

| 引擎 | 规模 | 数据模式 |
|---|---:|---|
| Ray 编排 DDP | 1 GPU、1×8、2×8 | `mount`、`cache` |
| Ray Train | 1 GPU、1×8、2×8 | `mount`、`cache` |
| Ray Train 试点 | 1 GPU、1×8、2×8 | `ray-data` |

提交入口覆盖网页、`spk-rayjob` 和原生 Ray Job API。两个 BEVFusion 分支都必须从全新
checkout 按公开文档改造，代码随任务上传，并完成指标、日志、MLflow、Checkpoint 和
续训验证。

故障注入覆盖：

1. 训练 Worker 进程退出；
2. Ray Worker Pod 删除；
3. TOS 短时错误；
4. DNS 短时错误；
5. GPU 节点重启；
6. Driver/Head 故障；
7. 恢复期间查看日志、指标和状态。

节点重启等有影响操作只在专用测试任务中执行，并在执行前单独取得授权。

通过条件包括：没有重复任务、恢复次数准确、不丢失已持久化 epoch、使用最新完整
Checkpoint、MLflow Run 可关联、权限正确，且无 GPU、PVC、Service 或 RayCluster
泄漏。

## 16. 性能门槛

兼容模式从 Ray 2.35.0 升级到 2.56.1 后，在相同代码、参数、数据集版本和节点上：

- 单步时间回退不超过 5%；
- GPU 利用率不显著下降；
- 数据读取吞吐不下降；
- 记录 2×8 相对 1×8 的扩展效率。

托管模式额外记录 Ray Train 管理开销、Checkpoint 上传时间、故障恢复时间，以及 TOS
冷读、NVMe 热读和 Ray Data 试点的差异。

## 17. 回滚

平台提供独立回滚开关：

- 默认引擎从 `ray-train` 切回 `ray-ddp`；
- 默认 Ray 从 2.56.1 切回已验证运行环境；
- Ray Data 关闭并回到 `mount` 或 `cache`。

回滚不得修改正在运行的任务，不删除新增数据库字段，不覆盖镜像 tag，不删除
Checkpoint 或 MLflow Run。Operator 回滚必须先验证升级后 CRD 与旧 Operator 的兼容性，
不能把降级作为未经测试的默认方案。

## 18. 生产切换门槛

以下条件全部满足后，Ray 2.56.1 才能成为生产默认：

- 两个 BEVFusion 分支在双引擎下跑通；
- 1 GPU、1×8、2×8 跑通；
- 三种提交方式跑通；
- Worker、Pod、节点、Driver 故障恢复跑通；
- Checkpoint 续训跑通；
- 用户、团队管理员、平台管理员权限正确；
- Loki、Prometheus、MLflow 和 Ray Dashboard 正常；
- 现有训练任务未被修改或中断；
- 构建、部署、升级、回滚文档能由另一位运维人员独立执行。
