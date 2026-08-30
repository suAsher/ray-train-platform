# 版本化 Ray Data 流式训练设计

日期：2026-08-30

状态：待规格确认
首个接入对象：`bev_3dod_s1h` / 用户 `yihan.she` 的真实训练配置

## 1. 背景与结论

`tos://shanghai-data-transfer/ray-train/public/labeled/` 是持续增量同步的原始训练数据池。它目前包含大量按场景或来源划分的小文件目录，例如 `cnfzhjyg`、`cnzshytg`、`mxvlkica`、`mxvlkica128` 等。现有 BEVFusion S1H 训练通过 PKL 保存的绝对路径，使用 PyTorch DataLoader 逐个执行 `np.fromfile()`，在启用相机时还会逐个执行 `Image.open()`。

现有四种能力有明确边界：

1. `mount` 可以读取任意容量，但大量小文件受到单文件延迟影响。
2. `cache + preload` 能得到最快热读，但启动前必须复制完整输入，受每任务 5 TiB 缓存上限约束。
3. `ray-data-stage` 使用 Ray Data 分布式复制完整输入，仍然要求完整数据能够放入节点缓存。
4. 当前 `ray-data` 只提供 Parquet/图片流式 Dataset；旧 BEVFusion DataLoader 没有消费 `train.get_dataset_shard()`，因此不能仅靠切换提交参数获得 Ray Data 加速。

生产默认方案确定为：

> 原始数据持续同步，平台增量发布不可变数据版本；训练使用 Parquet 大分片、Ray Data 流式分片与预取，双 NVMe 只保存有界工作集和 Ray 临时数据，不要求缓存装下完整数据集。

Lance 保留为后续 canary，不作为第一版默认格式。现有 PKL 路径继续兼容，直到主要训练代码完成迁移。

## 2. 设计目标

- 支持持续增长且超过 10 TiB 的 public/team 数据。
- 训练任务启动时固定数据版本，运行期间不受新同步文件影响。
- 用户不接触 TOS AK/SK，也不输入 TOS/PVC 路径。
- 不要求 NVMe 容量覆盖完整数据集。
- 真正使用 Ray Train `TorchTrainer` 和 Ray Data worker shard，而不是仅使用旧 Ray launcher。
- 保持 S1H 的样本数、类别平衡、增强、batch、loss、checkpoint 和评估语义。
- 保留现有 `mount`、`cache`、`ray-ddp` 任务，迁移不影响正在运行或历史任务。
- 代码随任务上传，训练镜像只提供稳定运行环境和平台适配器。

## 3. 非目标

- 第一版不删除 `public/labeled` 原始小文件。
- 第一版不强制所有用户代码改用 Ray Data。
- 第一版不把 Ray object spilling 当成持久数据缓存。
- 第一版不在正在运行的任务中动态加入新同步样本。
- 第一版不把用户可见的 `public` 或 `team` 目录变为可写。
- 第一版不以 Lance 替换 Parquet，除非对照测试证明收益并完成兼容验收。

## 4. 不可变原则

### 4.1 一个任务固定一个数据版本

用户可以选择“最新可用”，但提交服务必须在创建任务时把它解析成具体版本 ID 和 manifest 摘要。后续同步只产生下一版本，不改变运行中任务。

### 4.2 原始数据与派生数据分离

用户可见原始目录保持：

```text
ray-train/public/labeled/
ray-train/tenants/<tenant>/shared/
```

平台派生数据放在不出现在用户文件浏览器中的内部前缀：

```text
ray-train/platform/datasets/<dataset-id>/
├── objects/sha256/<前两位>/<digest>.parquet
├── manifests/<version-id>.parquet
├── versions/<version-id>.json
└── publication/<run-id>.json
```

训练任务只能通过数据集版本绑定获得只读访问，不能自行选择内部对象前缀。

### 4.3 版本不复制旧版本

Parquet 分片按内容摘要存储。版本 manifest 只引用分片：

- 首个版本会新增一次被选中训练载荷的打包空间。
- 后续版本复用未变化分片，只写新增或变化的场景分片。
- 删除版本只删除 manifest；只有不再被任何版本引用的分片才进入延迟回收。

## 5. 空间占用

版本不是一次完整快照复制，但首次格式转换需要保存派生载荷。

| 内容 | 典型额外空间 |
|---|---:|
| 版本 JSON/Parquet manifest | 通常低于数据体积的 0.1% |
| PKL 兼容索引 | 通常低于 1% |
| 未压缩 lidar Parquet 载荷 | 被选中 lidar 字节的约 100%～105% |
| 已压缩 JPEG/PNG 打包 | 被选中图片字节的约 100%～103% |
| 新版本 | 仅新增/变化分片 + manifest |

如果完整 10 TiB 原始载荷全部进入打包版本，第一版最坏约增加 10～10.5 TiB。S1H 当前是 lidar-only，只打包训练索引实际引用的点云和标注，不复制无关相机文件，因此额外空间通常显著小于整个 `public/labeled` 原始池。

平台必须在发布前展示预计新增字节，并支持以下保留策略：

- 保留最近 N 个 ready 版本。
- 被运行中任务、checkpoint 或复现实验引用的版本禁止回收。
- 未引用分片进入不少于 7 天的延迟删除队列。
- 管理员可以执行 dry-run 查看可回收空间。

## 6. 数据集与版本模型

### 6.1 原始集合与逻辑数据集

`public/labeled` 是原始集合，不直接等同于单个训练数据集。平台在其上登记逻辑数据集：

- `labeled-full`：所有已验证场景。
- `s1h-yihan`：与 yihan.she 当前 PKL/配置等价的真实训练集合。
- 其他团队/项目子集：引用同一批内容寻址分片，不重复载荷。

一个逻辑数据集包含：

- 原始空间和根目录。
- 场景/分区识别规则。
- train/val/test 定义。
- 必须存在的文件和标注规则。
- 格式、schema 版本和发布策略。
- 所有者、可见团队和审批状态。

### 6.2 版本状态

```text
DISCOVERING -> STABILIZING -> VALIDATING -> PACKING -> READY
                                      \-> FAILED
READY -> DEPRECATED -> RETIRED
```

- `DISCOVERING`：发现新增或变化对象。
- `STABILIZING`：等待文件大小、mtime/ETag 在稳定窗口内不再变化。
- `VALIDATING`：检查 PKL、样本路径、点云维度、标注和 train/val 重叠。
- `PACKING`：只处理新增/变化分区。
- `READY`：可提交训练。
- `FAILED`：保留错误文件、缺失路径和重试入口。

若上游能够提供 `_SUCCESS` 或批次完成标记，发布器优先使用标记；否则采用稳定窗口加完整性检查。默认稳定窗口为 30 分钟，允许管理员调整。

### 6.3 版本身份

版本 ID 采用可读时间和内容摘要组合，例如：

```text
labeled-full-20260830.2+sha256-12ab34cd
```

版本摘要覆盖：

- 原始对象 key、size、ETag/校验摘要。
- 分片摘要。
- train/val/test 样本 token。
- schema 和发布器版本。
- PKL/配置来源摘要。

## 7. Parquet v1 数据格式

### 7.1 分片

- 目标文件大小：256～512 MiB。
- 不使用全局压缩；点云和图片按原始字节保存，避免重复 CPU 压缩成本。
- Row Group 目标：32～128 MiB；不沿用实验版“每样本一个 row group”的默认值。
- 按场景边界打包，场景变化只重写相关分片。
- 分片文件名使用内容摘要，保证幂等和复用。

### 7.2 行 schema

第一版至少包含：

| 字段 | 类型 | 用途 |
|---|---|---|
| `token` | string | 样本唯一 ID |
| `scene` | string | 场景/发布分区 |
| `split` | string | train/val/test |
| `class_ids` | list<int16> | CBGS 类别平衡 |
| `timestamp` | int64 | 顺序和复现 |
| `points` | large_binary | lidar 原始字节 |
| `info` | binary/struct | 标注、位姿和标定 |
| `source_digest` | fixed binary/string | 回溯和校验 |

S1H lidar-only 第一版不打包未使用相机。相机融合扩展增加固定相机列或嵌套列表，并要求单行尽量低于 10 MiB；超大样本改用独立 blob 引用。

第一版可对平台可信发布器生成的 `info` 使用受控 pickle 兼容旧结构，但 schema v2 应迁移到 Arrow struct，禁止反序列化用户上传的任意 pickle。

## 8. Ray Train 与 Ray Data 执行模型

### 8.1 Runtime

- 先构建 Ray 2.58.0 canary 的 S1H 运行镜像。
- 优先保持 Torch 1.10.1 + CUDA 11.3 算法环境，使用 Python 3.9 安装 Ray 2.58，并重新编译 MMCV/mmdet3d CUDA 扩展。
- 若依赖无法可靠组合，才进入 Torch/Python 升级分支，并用同权重、同输入做数值回归后决定。
- 镜像必须包含平台 `raytrain-managed`、MMCV hook、Ray Data 适配器和 PyArrow。
- 镜像按 digest 登记；用户修改代码不重新构建镜像。

### 8.2 Trainer

平台使用 `TorchTrainer`：

- 1×8：8 个 Train worker，每个 worker 1 GPU。
- 2×8：16 个 Train worker，每个 worker 1 GPU。
- Ray Train 提供 rank/world size、故障重试、checkpoint 和工作目录分发。
- 一个 GPU rank 只消费自己的 Ray Data shard，不再叠加 MMDetection DistributedSampler。

### 8.3 S1H 数据适配器

新增 `RayDataNuScenesDataset`/训练桥接层：

1. 使用 `train.get_dataset_shard("train")` 获取当前 rank 的数据流。
2. 使用带预取的 `iter_batches()`，保留可控本地 shuffle buffer。
3. 将 `points` 字节转换为与 `np.fromfile(..., float32)` 相同的 NumPy 数组。
4. 把 `info` 转换为现有 pipeline 所需字典。
5. 继续复用现有增强、voxelization、模型、loss 和 optimizer。
6. Ray Data 已完成分片时禁用旧 DistributedSampler，避免双重切分。
7. `samples_per_gpu` 仍表示每 GPU batch；旧 `workers_per_gpu` 在原生 Ray Data 模式中改为平台的预取/变换并发参数。

### 8.4 CBGS

不能简单删除 `CBGSDataset`。发布 manifest 保存每个样本类别，Ray Data 在载荷解码前执行等价的轻量引用重复/采样：

- 每 epoch 使用 `seed + epoch` 生成确定性采样计划。
- 只重复轻量样本引用，不在对象存储中复制 Parquet 载荷。
- 采样完成后再进行 worker shard 分割。
- 验收时比较旧实现和新实现的每类抽样数、总 step 数及固定 seed token 序列。

## 9. 双 NVMe 使用方式

### 9.1 两种用途必须分开

1. Ray 临时目录和 object spilling：用于内存压力兜底，不视为性能缓存。
2. 数据分片工作集：按内容摘要缓存当前任务使用的 Parquet 分片。

### 9.2 有界缓存

- 每个 GPU 节点继续挂载 `/mnt/cache` 和 `/mnt/cache2`。
- 分片摘要哈希决定落到 data1 或 data2。
- 原子下载、文件锁和摘要校验，8 个本地 GPU rank 复用同一分片。
- 高水位 85%，低水位 70%；达到高水位时按 LRU 回收至低水位。
- 失败、空间不足或校验失败时回退到 TOS/FSX 流式读取。
- 任务完成可以清理缓存卷；第一版不跨租户复用缓存，避免泄漏团队数据。

当前 5 TiB 是一个任务可申请的 NVMe 工作集上限，不是数据集容量上限。10 TiB、20 TiB 数据仍可训练，只是不能全部变成热缓存。大于缓存的数据按 Parquet 大分片持续顺序流式读取。

### 9.3 预取

- 默认预取 2～4 个 batch 或有限数量分片。
- 根据 GPU 消费速度进行背压，不执行全量 preload。
- 暴露 source read、cache hit、cache bytes、eviction、prefetch wait 和 Ray Data backpressure 指标。

## 10. 发布服务

新增平台控制器 `dataset-publisher`：

- 使用独立 Kubernetes ServiceAccount 与 IRSA。
- 对原始 public/team 只读，对内部 `ray-train/platform/datasets` 前缀可写。
- 不向用户 Pod 注入任何 TOS 凭据。
- 首次发布可以并行运行多个分区打包任务。
- 增量发布只处理新增/变化场景。
- 发布任务使用 Kueue 低优先级队列；训练任务优先，不占用 GPU 请求。
- CPU 节点资源不足时允许调度到 GPU 节点 CPU/内存，但不得申请 GPU。
- 所有写入先落临时 key，校验后原子提交 manifest，避免半成品成为 READY。

## 11. 平台 API、CLI 与 UI

### 11.1 数据模型/API

新增资源：

- Dataset
- DatasetVersion
- DatasetPartition
- DatasetPublicationRun
- DatasetCacheObservation

提交任务保存：

- `datasetId`
- `datasetVersionId`
- `datasetManifestDigest`
- `dataMode`
- `cachePolicy`
- runtime image digest

历史任务不反向修改。

### 11.2 用户 UI

“数据集”页面展示：

- 数据集名称、来源空间和说明。
- 最新 READY 版本、样本数、文件数、逻辑容量。
- train/val/test 数量。
- 原始同步时间、发布进度、失败样本。
- 版本差异：新增、变化、删除。
- 格式：原始 / Parquet 流式。

提交页默认只要求：

- 数据集和版本；“最新可用”在提交时解析。
- 单机 8 卡或多机 16 卡。
- 读取策略：自动、原始兼容、全量缓存。

高级选项才显示缓存工作集和 Ray Data 参数。普通用户不输入 TOS 路径、PVC、AK/SK、Parquet 路径。

任务详情展示：

- 固定数据版本与摘要。
- source throughput、files/s、cache hit、NVMe 使用量。
- Ray Data operator/backpressure。
- GPU 利用率、显存、功率、CPU、内存、网络。
- step time、data_time、loss、mAP/NDS、MLflow run。

### 11.3 CLI

目标命令：

```bash
spk-rayjob submit \
  --engine ray-train \
  --dataset labeled-full:latest \
  --data-mode streaming \
  --workers 2 \
  --gpus-per-worker 8 \
  --watch
```

CLI 先调用 preflight，把 `latest` 解析成具体版本，并显示版本、样本、容量、预计 GPU 和缓存策略后提交。

## 12. PKL 兼容策略

- 原始/缓存模式继续使用现有 PKL 和 `DatasetPathResolver`。
- 发布器过渡期可以从同一可信数据定义生成 PKL 与 Parquet manifest。
- Ray Data 原生 S1H 适配器不依赖独立 PKL 文件。
- 只有在主要训练代码全部迁移、回归和复现通过后，才讨论停止生成 PKL。

## 13. S1H 真实验收

### 13.1 代码基线

- 从 `gitlab.qomolo.com/dl/bevfusion` 全新拉取 `bev_3dod_s1h`。
- 记录 commit，应用文档化的平台适配补丁。
- 不复用 `/root/work/bevfusion` 的脏工作区作为交付基线。
- 使用 yihan.she 当前任务的配置、PKL、batch、epoch、优化器和数据范围作为对照。

### 13.2 对比矩阵

使用相同代码 commit、镜像依赖、seed、GPU、batch 和训练数据：

| 组 | 执行 | 数据 | NVMe | 目的 |
|---|---|---|---|---|
| A | 旧 ray-ddp | 原始小文件 | 关闭 | 用户现状基线 |
| B | Ray Train | 原始小文件 | 关闭 | 分离 Train 改造影响 |
| C | Ray Train + Ray Data | Parquet 流式 | 仅临时/溢写 | 流式主方案 |
| D | Ray Train + Ray Data | Parquet 流式 | 有界热缓存 | 热缓存收益 |

先对整个真实 train split 跑满 1 epoch，不能使用 smoke/sample 截断。通过后，使用推荐组运行原配置完整 epoch 数。

### 13.3 验收指标

- 数据：样本总数、每类 CBGS 数量、缺失文件必须一致。
- 训练：step 数、loss 趋势、NaN/OOM、checkpoint 可恢复。
- 性能：启动时间、首 batch、samples/s、GB/s、P50/P95 data wait、step time。
- 资源：GPU 利用率、GPU stall、CPU、内存、网络、TOS read、NVMe hit/eviction。
- 正确性：固定 seed 的首批 token、首批 points 摘要、1 epoch checkpoint 和评估结果在允许误差内。
- 平台：Portal、CLI、日志、Ray Dashboard、MLflow、取消和断点续训都可用。

## 14. 发布和回滚

1. 只提交代码、迁移和关闭状态的 UI，不改变默认行为。
2. 为平台管理员和 yihan.she 开启 Ray 2.58/S1H canary。
3. 完成 A/B/C/D 全量 1 epoch 对比。
4. 完成长训练和 checkpoint 恢复。
5. 默认策略从 `mount` 切换为 `streaming`，旧模式仍可选。
6. 全团队开放。

回滚时只关闭 dataset version/Ray Data feature flag，任务继续使用已存在的 `mount` 或 `cache`；不删除原始 public/team 数据和旧镜像。

## 15. 安全与隔离

- public/team 权限由平台数据绑定决定。
- 用户不能指定内部派生 URI。
- team 数据版本只对授权成员可见。
- 内部 publisher 写权限与训练只读权限分离。
- 缓存按任务/租户隔离，禁止普通用户浏览其他任务缓存。
- manifest 和数据摘要参与任务 provenance。
- pickle 只接受可信平台发布器输出；用户数据不能触发任意反序列化。

## 16. 已知风险

- 旧 Torch/MMCV 与 Ray 2.58 的 Python 兼容需要通过镜像构建验证。
- CBGS 到流式采样的等价性必须先测试，不能靠性能结果代替正确性。
- Parquet 大二进制行需要控制单行和 block 大小，避免 Ray heap/object store 峰值。
- FSX/TOS 故障必须保留明确错误归因和重试，不能表现为无日志。
- `public/labeled` 目录很大，不能在每次发布时全量递归扫描；增量发现需要 TOS ListObjects/Inventory、稳定窗口和已处理游标。
- 旧实验版 `PackedNuScenesDataset`、打包脚本和镜像不是干净 Git 基线，不能直接作为生产交付物。

## 17. 最终决策摘要

- 原始数据位置不变：`ray-train/public/labeled` 持续同步。
- 派生数据不放在用户可见 public 下，而放平台内部内容寻址前缀。
- 一个训练任务固定一个不可变版本。
- v1 默认 Parquet；Lance 只做 canary 对比。
- 首版本有一次载荷级空间成本；后续版本只增加变化分片。
- PKL 过渡期保留。
- Ray Data 负责流式、采样、分片和预取；Ray Train 负责多机多卡、重试、checkpoint 和生命周期。
- NVMe 是有界工作集，不是数据集容量边界。
- 以 yihan.she 的 S1H 真实训练做 A/B/C/D 全流程验收。
