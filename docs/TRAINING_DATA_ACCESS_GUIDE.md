# 训练数据读取方案对比与使用指南

> 面向在平台上跑 BEVFusion / mmdet3d 类训练的算法同学。
> 全部数据来自 2026-08-29 在生产集群（2 × 8 RTX 4090）上的实测，数据集为
> `public/labeled/mxvlkica128`（30 场景 / 42,363 样本 / 351 GB / 30 万个小文件）。

> **2026-09-08 当前平台状态（先读这一段）**：本文第 2 节的 I/O 基准仍然有效，
> 但下文早期“Ray Train / Ray Data 尚未就绪”的表述已经过期。当前已实际验证
> `Ray Train + Ray Data + 版本化 Parquet + 有界 NVMe` 能完成 2 Worker × 8 GPU 的
> 调度、16 个 DDP rank 启动、15,228 个训练样本的 Ray Data shuffle/split 和训练 step。
> 这不等于任意 BEVFusion 分支都已自动兼容：训练入口必须消费平台 streaming 适配器，
> 完整 epoch、故障恢复、指标上报和特定模型的 A/B 吞吐仍要按本指南验收。本文中的历史
> 打包布局与代码片段是研究记录；实际提交以平台「使用说明」中的 streaming 命令与
> READY 数据集 schema 为准。

---

## 1. 结论速览

| 问题 | 结论 |
|---|---|
| 小文件直读慢吗？ | **慢**。治理挂载单线程只有 7.4–14.2 文件/秒（每文件约 135 ms），且速度有近 2 倍波动 |
| 训练会被拖慢吗？ | **看负载**。lidar-only（每样本 1 个大文件）不会；**camera+lidar 融合（每样本 1 点云 + 5 张图）会，实测慢 1.66 倍** |
| NVMe 缓存有用吗？ | **融合训练有用（1.66 倍）**，lidar-only 几乎无用（0.3%）。代价是 5 TB 上限 + 约 50 分钟预热 |
| Ray Data 直读小文件？ | **反而慢 6.6 倍**，不要用 |
| 有没有既快又没上限的？ | **有：打包成 Parquet 分片**。实测与 NVMe 缓存性能持平（0.573 vs 0.570 s/步），但**无需预热、无容量上限** |
| 数据超过 10 TB 怎么办？ | 只能走打包分片。NVMe 缓存到 5 TB 就失效 |

**一句话**：文件越小、每样本文件数越多，读取瓶颈越严重；解决办法是把小文件打包成大分片，
而不是把小文件搬到更快的盘上。

---

## 2. 实测数据

### 2.1 存储层：小文件读取吞吐

冷读，互不重叠的场景目录，单节点。

**点云 `.bin`（7.4 MB/文件）**

| 并发 | 文件/秒 | MB/s |
|---|---|---|
| 1 | 8.5 | 61.7 |
| 8 | 39.3 | 303.8 |
| 16 | 61.5 | 453.6 |
| 32（≈ `workers_per_gpu=4` × 8 卡） | 81.0 | 603.2 |
| 64 | 103.3 | 764.3 |
| 128 | 105.5 | 790.3 |
| 256 | **122.8（饱和）** | **902.5** |

**相机图片（~140 KB/文件）**

| 挂载 | 文件/秒 | MB/s |
|---|---|---|
| 治理挂载 `/mnt/data/input` | 30.5 | 4.6 |
| NVMe 缓存 `/mnt/cache/dataset-view` | **1,712.6** | **206.9** |

瓶颈是**每文件延迟**，不是带宽：单线程吞吐与文件大小无关，恒定在 8–30 文件/秒。
文件越小，差距越大（小图片 56 倍，大点云 5 倍）。

### 2.2 三种读取方式的吞吐对比

| 读取方式 | 样本/秒 | MB/s |
|---|---|---|
| 原始小文件 · 线程池 conc=32（当前默认） | 60.0 | 444 |
| 原始小文件 · 线程池 conc=256（调优上限） | 122.8 | 902 |
| 原始小文件 · **Ray Data** | **18.7** | 140 |
| **Parquet 分片 · Ray Data 流式** | **247.5** | **1,761** |
| Parquet 分片 · pyarrow 单线程 | 45.3 | 329 |

**Ray Data 直读原始小文件比朴素线程池还慢 6.6 倍**——每个 block 都要走 Ray 对象存储
（Plasma）往返，对二进制小文件全是纯开销。平台的校验也印证了这点：`ray-data` 流式模式
**显式拒绝 `files` 格式**，只接受 `parquet` / `images`。

打包之后，Ray Data 达到 **1,761 MB/s**，反超原始小文件最优值 2 倍。

### 2.3 端到端训练对比（真实训练，非采样）

全部为 BEVFusion `bev_3dod_s1h` 分支，2 节点 × 8 卡，`engine: ray-ddp`。

#### A. lidar-only（`lidar/voxelnet_0p075.yaml`，每样本读 1 个 7.4 MB 点云）

`samples_per_gpu=8`，`workers_per_gpu=4`：

| 数据来源 | 单步耗时 | data_time | 预热 |
|---|---|---|---|
| 原始小文件（治理挂载直读） | 1.549 s | 0.017 | 无 |
| NVMe 缓存 | 1.552 s | 0.020 | 约 50 分钟 |
| Parquet 分片 | 1.550 s | 0.021 | 无 |

**三者一致。** 单步计算约 1.53 s，数据等待仅 0.02 s——这个负载 I/O 不在关键路径上。
即使把 `workers_per_gpu` 降到 1，`data_time` 仍只有 0.015。

完整一轮（Parquet 分片，1440 步，42,363 样本）：**45 分 13 秒，loss 2838.38 → 0.5173，
单步 1.55–1.56 s 全程稳定。**

#### B. camera+lidar 融合（`camera+lidar/resnet50/convfuser.yaml`，每样本读 1 点云 + 5 张图）

`samples_per_gpu=2`，`workers_per_gpu=1`（每节点 8 路并发）：

| 数据来源 | 单步耗时 | data_time | ETA | 预热 |
|---|---|---|---|---|
| **原始小文件（治理挂载直读）** | **0.899–1.000 s** | 0.008 / 0.023 / 0.037 / **0.058 剧烈抖动** | 1 天 1:58 | 无 |
| **NVMe 缓存** | **0.570–0.577 s** | 0.009 恒定 | 16:47 | **56 分钟** |
| **Parquet 分片** | **0.573–0.578 s** | 0.009 恒定 | 17:29 | **无** |

**缓存与打包都比原始小文件快 1.66 倍，且两者性能完全持平。**

同配置把 `workers_per_gpu` 提到 4（每节点 32 路并发）时，原始小文件可回到 0.587 s——
说明是**读取并发不足以覆盖每文件 135 ms 的延迟**，而不是带宽不够。

#### 为什么 lidar-only 看不出差别，融合就很明显

每节点每步的文件需求 vs 治理挂载的供给能力：

| 负载 | 每步文件数/节点 | 挂载能力 | 余量 |
|---|---|---|---|
| lidar-only, `workers=4` | 64 个大文件 | ~123 文件/s | 3 倍 |
| 融合, `workers=4` | 96 个混合 | ~305 文件/s | 1.9 倍 |
| **融合, `workers=1`** | **96 个混合** | **~120 文件/s** | **贴边 → 卡住** |

余量一旦贴边，存储的正常波动（实测单线程 7.4–14.2 文件/s，近 2 倍）就会直接变成训练变慢。

#### 一个容易误判的现象

上表中不用缓存那组，`data_time` 平均值不高（0.008–0.058），**但方差很大**。
在 DDP 下，每一步由**最慢的那个 rank** 决定：16 个 rank 里只要有一个撞上慢读，
其余 15 个在梯度同步处空等——这段等待计入的是**单步耗时**，而不是它们各自的 `data_time`。

所以 **`data_time` 平均值低，不能用来排除 I/O 问题**。要看它的波动幅度，
以及同配置下单步耗时的历史对比。

### 2.4 打包成本

| 数据集 | 样本 | 体积 | 分片数 | 耗时 | 吞吐 |
|---|---|---|---|---|---|
| cnzshytg | 4,698 | 33.4 GB | 62 | 130 s | 256.6 MB/s |
| mxvlkica128 | 42,363 | 312.1 GB | 578 | 30.8 min | 169.1 MB/s |

一次性成本，且**增量执行**——新同步进来的场景只打新增部分。
按 200 MB/s 估算，每天新增 100 GB 约需 8 分钟。

---

## 3. 四种方案对比

| | A. Parquet 分片 | B. Lance | C. NVMe 全量预热 | D. 原始小文件直读 |
|---|---|---|---|---|
| 容量上限 | **无** | **无** | 5 TB（配额）/ 7 TB（物理） | 无 |
| 每次任务预热 | **不需要** | **不需要** | 约 50 分钟 | 不需要 |
| 读取吞吐（基准） | **1,761 MB/s** | 未实测 | 1,879 MB/s | 902 MB/s（大文件）/ 4.6 MB/s（小图片） |
| 融合训练单步（实测） | **0.573 s** | 未实测 | 0.570 s | 0.899–1.000 s |
| 随机取单样本 | 需 `row_group_size=1` | **按行号直接取** | 直接 | 直接 |
| 增量追加 | 加新分片 + 自建 manifest | **原生 append** | 每次重新全量热 | 天然 |
| 版本管理 | **需自建** | **原生** | 无 | 无 |
| 训练侧改动 | 换一个 `dataset_type` | 同左 | 无 | 无 |
| 小文件延迟抖动 | **消除** | **消除** | **消除** | 存在（近 2 倍波动） |

**推荐 A（Parquet）——已端到端验证；Lance 作为 v2 演进方向**（它原生支持
"持续同步的增量追加"和"数据集版本"这两件事，而 Parquet 需要自建，但尚未实测）。

C 方案（NVMe 预热）的三个包袱在数据量增长后会变成硬伤：5 TB 上限、
50 分钟预热、每节点存全量副本（同一份数据在 2 个节点各存一遍）。

---

## 4. 推荐方案：Parquet 分片

### 4.1 数据布局

```
public/labeled/<数据集>/            原始小文件（持续同步进来，只读）
public-packed/<数据集>/v<日期>/     平台打包产出（只读）
        ├── shard-00000.parquet     每片约 512 MB
        ├── ...
        └── _manifest.json          已打包场景清单 + 分片列表 + 版本
```

每个分片包含四列：

| 列 | 类型 | 内容 |
|---|---|---|
| `scene` | string | 场景 ID，用于按场景过滤 |
| `token` | string | 样本 token |
| `points` | large_binary | 点云原始字节 |
| `info` | large_binary | 标注记录（gt_boxes / gt_names / 标定 / 位姿等） |

**分片自包含**：训练时一次顺序读同时拿到点云和标注，不需要再打开 pkl。
（pkl 仍是打包环节的输入，见 §4.2。）

写入时设置 `row_group_size=1`（一个样本一个 row group），这样训练打乱顺序后
随机取某个样本只读它自己那一行，而不是整个 512 MB 分片。

### 4.2 pkl 还需要吗

**训练时不需要，打包时仍然需要。** 准确说，pkl 从「每次训练的热路径」退到了
「打包的一次性输入」。

| 环节 | pkl 的角色 |
|---|---|
| **打包时** | **仍是必需输入**——打包脚本读它拿到样本清单和标注，写进分片的 `info` 列 |
| **训练时** | **不再读取**。`ann_file` 参数传了也会被忽略，数据和标注都来自分片 |

打包后消失的是这些历史问题：pkl 里写死绝对路径、换数据位置要重新生成、
缓存版和普通版不能混用、路径层级多一层少一层——因为训练已经不看 pkl 里的路径了。

选训练子集改用**过滤条件**：分片有 `scene` 列，按场景白名单过滤即可。

> **一个待定的设计问题**：当前实现把标注一起打进了分片，单用户使用没问题。
> 但原始数据是共享不变的，标注是用户私有且会变的——多个用户共用同一批数据时，
> 每人都要重打一遍几百 GB。更合理的分层是让平台只打包传感器数据（按
> `(scene, token)` 索引），标注仍留在各自的 pkl 里、只存 `(scene, token)` 引用。
> 平台化时会按这个方向调整。

---

## 4.5 当前的 Ray Train / Ray Data 托管路径

当前平台已提供 `engine: ray-train` + `data-mode: streaming`。它不是给旧的
`DataLoader + pkl` 入口换一个命令行开关：训练镜像和入口必须实现平台的 streaming
适配器，才能把版本 manifest 的分片交给 Ray Data 并交给每个 Train rank。

一次成功启动时，链路是：

```
READY 不可变版本（manifest + 内容摘要）
  → Ray Data 读取、shuffle 并等量 split
  → Ray Train 创建 N 个训练进程（每 GPU 一个 rank）
  → DDP 前向 / 反向 / AllReduce
  → rank 0 写 checkpoint、结构化指标与 MLflow（若代码已接入）
```

| 组件 | 它负责什么 | 它不负责什么 |
| --- | --- | --- |
| Ray Train | 创建和协调多机 rank、失败重试策略、托管 checkpoint 生命周期 | 不会把任意训练脚本自动改造成 Ray Data 或 MLflow 代码 |
| Ray Data | 按 manifest 分片、shuffle、等量分给训练 worker | 不会自动理解旧 pkl 的绝对路径或替用户解码任意业务载荷 |
| Parquet / 分片版本 | 将数据与 manifest 固化、降低小文件请求次数、提供摘要可追溯性 | SHA256 只能证明发布字节没有变，不能证明标签语义一定正确 |
| 本地 NVMe | 保存临时、有界的热分片与 Ray 临时数据，避免全量复制超过磁盘容量 | 不保存唯一数据副本，也不会增加模型显存 |
| MLflow | 记录代码显式上报的参数、标量和产物，用于跨任务对比 | 注入 `MLFLOW_TRACKING_URI` 本身不会产生一条 loss |

**显存低不代表 Ray 没有工作。** 显存主要由模型参数、激活、精度和
`samples_per_gpu` 决定；Ray Train、Ray Data、Parquet 和 NVMe 优化的是调度、分片与 I/O。
例如 2 × 8 卡、`samples_per_gpu=1` 的全局 batch 是 16，单卡微批仍是 1，所以显存可能只有
数 GiB。确认数值正确后，可用固定数据版本做 300–500 step A/B，逐级提高
`samples_per_gpu` 并按全局 batch 调整学习率；不要把 `workers_per_gpu` 当成 GPU batch。

**数据没有丢失应这样验收**：只选择 READY 版本，记录版本号与 manifest SHA256；核对
train / val / test 样本数与预期；先对固定场地或小范围跑一次读取、解码和一个 epoch；再与
发布前基线比较样本 ID 集合、每 split 数量和标签统计。发布器的分片 digest 能发现截断或
被替换，不能替代这一层语义和训练适配器校验。

**pkl 与 Parquet 不能只改后缀互换。** pkl 是 Python 序列化的索引/标注，旧训练通常从中取
路径再逐个打开小文件；Parquet 是列式、可分片的不可变数据/manifest 表，由兼容适配器并行
读取。继续用 pkl + `mount` 是兼容性最高的传统路径，但不会自动获得 Ray Data 分片；选择
streaming 则需要兼容镜像、READY 版本和适配器，换来可追溯版本与大分片读取能力。

---

## 5. 用户怎么用

整体是三步，前两步各做一次，之后日常只用第三步。

```
① 改代码（一次）  →  ② 打包数据集（每个数据集一次，之后增量）  →  ③ 提交训练（日常）
```

### 5.1 第一步：改代码（一次性）

在你的 BEVFusion 代码库里改 5 处，**完整代码见附录 A**：

| 文件 | 改什么 | 为什么 |
|---|---|---|
| `mmdet3d/datasets/packed_nuscenes_dataset.py` | **新增**，读分片的数据集类 | 核心 |
| `mmdet3d/datasets/__init__.py` | 加一行 import 注册 | 让 `dataset_type` 能找到它 |
| `mmdet3d/datasets/nuscenes_dataset.py` | 加 `resolve_data_path()` | 相对路径按环境变量解析 |
| `mmdet3d/datasets/pipelines/loading.py` | 点云和图片加载各加一个分支 | 从分片内存读，**不改旧行为** |
| `tools/westwell_train.py` | 3 处小改 | 修 tensorboard 崩溃 + 治理存储的并发写限制 |

这些改动都是**向后兼容**的：不加 `dataset_type=PackedNuScenesDataset` 时，
所有行为和原来完全一样。

### 5.2 第二步：打包数据集（一次性，之后增量）

打包脚本见**附录 B**。把它放在代码库根目录，用一个单卡任务跑：

```yaml
# .spk-rayjob.yaml
name: pack-my-dataset
image: harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-packed@sha256:dba620167db021356abc8057104800a093589a9ffaa81644f0ffe108bd3a538d
engine: ray-ddp
dataMode: mount
executionMode: single_gpu
workers: 1
gpusPerWorker: 1
cpuPerWorker: 64
memoryPerWorker: 200Gi
input:
  space: public
  path: labeled/mxvlkica128          # 要打包的原始数据集
output:
  path: packed-mxvlkica128           # 分片输出到「我的运行结果」
entrypoint: python3 pack_dataset.py
```

脚本默认读 `/mnt/storage/me/pkl/<你的pkl目录>/infos_train.pkl` 取样本清单和标注，
需要按自己的 pkl 路径改脚本顶部的 `ANNOTATIONS`。

提交后在日志里能看到进度，结束时打印：

```
PACK DONE scenes=30 shards=590 samples=39933 bytes=318.7 GB elapsed=1844 s -> 172.8 MB/s
```

**记下这个任务的 JOB ID**，第三步要用。分片路径是
`packed-mxvlkica128/<JOB ID>/packed`。

数据集后续新增场景时，**重跑同一个打包任务即可**：脚本会读 `_manifest.json`，
只处理未打包的场景，不会重复劳动。

### 5.3 第三步：提交训练

```yaml
# .spk-rayjob.yaml
name: my-training
image: harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-packed@sha256:dba620167db021356abc8057104800a093589a9ffaa81644f0ffe108bd3a538d
engine: ray-ddp
dataMode: mount              # 不需要 cache，也不需要 cache-preload
executionMode: ray_train     # 多机多卡；单机多卡用 torchrun
workers: 2
gpusPerWorker: 8
cpuPerWorker: 64
memoryPerWorker: 256Gi
input:
  space: my-runs
  path: packed-mxvlkica128/<第二步的JOB ID>/packed
output:
  path: my-run
entrypoint: >-
  raytrain-bevfusion-prepare python3 tools/westwell_train.py
  configs/westwell_fix_anchor/det/anchorhead/secfpn/lidar/voxelnet_0p075.yaml
  --launcher pytorch --run-dir /workspace/run_dir
  dataset_type=PackedNuScenesDataset
  dataset_root=/mnt/data/input/ eval_dataset_root=/mnt/data/input/
  runner.max_epochs=20 data.samples_per_gpu=8 data.workers_per_gpu=4
```

```
spk-rayjob submit --watch
```

注意 `dataset_type=PackedNuScenesDataset` 是命令行覆盖，**不用改配置文件**。
`ann_file` 不用传（标注已在分片里）。

### 5.4 怎么确认真的生效了

日志里会打印分片信息：

```
PackedNuScenesDataset: 590 shards, 39933 samples from /mnt/data/input
```

然后看 `data_time`：稳定在 **0.01 上下且不抖动**，就说明读取不再是瓶颈。

### 5.5 执行模式怎么选

| 场景 | `executionMode` | `workers` × `gpusPerWorker` |
|---|---|---|
| 单机多卡 | `torchrun` | 1 × 8 |
| 多机多卡 | `ray_train` | 2 × 8 |
| 单卡调试 | `single_gpu` | 1 × 1 |

**不要在 entrypoint 里自己写 torchrun 或 torchpack**，平台会自动加。

---

## 6. 几个必须知道的坑

### 6.1 entrypoint 里不要用 `$变量`

平台渲染任务时用单引号转义了命令，但 Ray CLI 内部会把单引号换成双引号，
导致 `$PLATFORM_OUTPUT_PATH` 这类变量**在 Ray head 上被提前展开成空值**。

```
❌ --run-dir $PLATFORM_OUTPUT_PATH/run_dir     → 变成 /run_dir，权限报错
✅ --run-dir /workspace/run_dir                 → 写死
✅ 在 Python 里读 os.environ["PLATFORM_DATASET_PATH"]   → 正确
```

**正确做法：路径在 Python 代码里读环境变量，不要在 entrypoint 字符串里展开。**

### 6.2 治理存储不支持并发写和追加

TOS 挂载不支持多个进程写同一个文件（8 并发写，6 个报 `Operation not permitted`），
也不支持 append（mmcv 的日志钩子会报 `Permission denied`）。

已在代码里处理：`configs.yaml` 只由 rank 0 写，日志文件名加 rank 后缀。
**自己加输出文件时注意同样的约束。**

### 6.3 多机训练的 run_dir

- `--run-dir /workspace/run_dir`（节点本地）：日志正常，但**多机 eval 收集不到
  其他节点的 `part_*.pkl`** → 需要 `evaluation.interval` 调大或单独跑评估
- `--run-dir /mnt/data/output/...`（共享）：eval 正常，但日志追加会失败

目前建议：训练时用节点本地，评估单独作为一个任务跑。

### 6.4 首次提交可能失败

新账号第一次提交时，源码包会传到一个临时路径，导致任务报找不到归档。
**再提交一次即可**（第二次开始路径就对了）。同一份代码重提会撞
`SOURCE_ARTIFACT_CONFLICT`，随便改一个字符让代码哈希变化即可。

---

## 7. 平台侧待建能力

| # | 能力 | 状态 |
|---|---|---|
| 1 | 增量打包服务（定时/事件触发，diff manifest 只打新增） | 待建 |
| 2 | Portal 展示打包状态（样本数、版本、更新时间），选择时自动指向打包版 | 待建 |
| 3 | `PackedNuScenesDataset` 随镜像分发 | ✅ 已在 `ray-train-bevfusion-packed` 镜像 |
| 4 | 解耦 `ray-data-stage` 与 `ray-train` 引擎（两者本就正交） | 待改 |
| 5 | 预热器 `sorted(rglob())` 改流式 + planning 并行化 | 待改 |
| 6 | 缓存卷按 `(节点, 数据集指纹)` 复用，而非绑 pod 生命周期 | 待改 |

---

## 附：关于"不用缓存要一天 / 用缓存 11 小时"

### lidar-only 的严格对照复现

用**完全相同的参数和样本集**做了受控对比：复制原始 `0825_pkl` 的
39,933 个训练样本（仅把绝对路径改成相对路径，样本集逐条一致），
2 节点 × 8 卡、`samples_per_gpu=8`、`workers_per_gpu=4`、`max_epochs=20`。
日志里的 `1325 iters/epoch` 与原始记录完全吻合，确认是同一配置。

| | 原始运行（8-28） | 复现（8-29，lidar-only） |
|---|---|---|
| **不用缓存** | **31:03:52** · time 4.228 · data_time 0.397 | **11:27:27** · time 1.550 · data_time 0.015 |
| **用缓存** | 11:54:15 · time 1.549 · data_time 0.021 | **11:11:10** · time 1.546 · data_time 0.019 |

lidar-only 场景下复现不出 31 小时——不用缓存也只要 11:27。
另用一个**当天完全未被访问过**的数据集（`gbsuffeluat`，275 GB）做冷数据对照，
不用缓存的 `data_time` 在前 50 步为 0.147，第 100 步起稳定在 **0.012**，
排除了页缓存干扰。

### 但融合配置下差距是真实的

见 §2.3 B 组：同样不用缓存，融合配置（每样本 1 点云 + 5 张图）单步
**0.899–1.000 s**，开缓存后 **0.570 s**，**1.66 倍**。

结论是：**"不用缓存慢一倍多"这个现象真实存在，触发条件是"每样本读多个小文件
且读取并发不足"**。lidar-only 每样本只读 1 个 7.4 MB 的大文件，余量充足，
所以看不出差别；融合训练读的是 ~140 KB 的相机图，文件数 6 倍，就会贴到天花板。

存储侧的对应数据：同一挂载上，点云（7.4 MB）单线程 8.5 文件/s，
相机图（140 KB）单线程 30.5 文件/s——**吞吐几乎与文件大小无关**，
瓶颈是每文件约 135 ms 的固定延迟。所以文件越小，等效带宽越低：
点云 902 MB/s，相机图只有 4.6 MB/s。

### 遇到步时异常怎么排查

看日志里 `time` 和 `data_time` 两个数字：

1. `data_time` 占 `time` **> 20%** → 明确是存储瓶颈
2. `data_time` **平均值低但抖动大**（如 0.008 ↔ 0.058）→ **仍然可能是存储瓶颈**。
   DDP 下每步由最慢的 rank 决定，其他 rank 的等待计入单步耗时而非自身 `data_time`
3. 两者都平稳但单步偏高 → 查计算侧

第 2 种最容易误判，本文档的早期版本就在这里下错过结论。

---

## 附录 A：代码改动

### A.1 新增 `mmdet3d/datasets/packed_nuscenes_dataset.py`

```python
"""NuScenes dataset backed by packed Parquet shards.

Reading one small file per sample is latency bound on the governed object
store, so a dataset that outgrows the node-local cache cannot be pre-staged.
This dataset instead reads shards that carry both the sensor data and its
annotation record, turning random small reads into sequential large ones.
"""
import os
import pickle
from typing import Any, Dict, List

import pyarrow.parquet as pq
from mmdet.datasets import DATASETS

from .nuscenes_dataset import NuScenesDataset, resolve_data_path


def _shard_paths(root: str) -> List[str]:
    paths = []
    for base, _, names in os.walk(root):
        paths += [os.path.join(base, n) for n in names if n.endswith(".parquet")]
    if not paths:
        raise FileNotFoundError("no parquet shards under %s" % root)
    return sorted(paths)


@DATASETS.register_module()
class PackedNuScenesDataset(NuScenesDataset):
    """Drop-in replacement for NuScenesDataset that reads packed shards.

    ``ann_file`` is accepted for interface compatibility but ignored: the
    annotations travel inside the shards, so no separate index is needed.
    """

    def __init__(self, *args, **kwargs):
        self.packed_root = kwargs.pop("packed_root", None) or os.environ.get(
            "PLATFORM_DATASET_PATH", ""
        )
        self._readers = {}
        super().__init__(*args, **kwargs)

    def load_annotations(self, ann_file):
        del ann_file  # annotations live in the shards
        shards = _shard_paths(self.packed_root)
        infos = []
        for shard_index, path in enumerate(shards):
            table = pq.read_table(path, columns=["info"])
            for row_index, blob in enumerate(table.column("info")):
                info = pickle.loads(blob.as_py())
                info["_shard"] = shard_index
                info["_row"] = row_index
                infos.append(info)
        self._shards = shards
        print("PackedNuScenesDataset: %d shards, %d samples from %s"
              % (len(shards), len(infos), self.packed_root), flush=True)
        infos = list(sorted(infos, key=lambda e: e["timestamp"]))
        return infos[:: self.load_interval]

    def _reader(self, shard_index: int) -> pq.ParquetFile:
        # Handles are cheap and reused, so a shuffled epoch pays one file open
        # per shard rather than one per sample.
        reader = self._readers.get(shard_index)
        if reader is None:
            reader = pq.ParquetFile(self._shards[shard_index])
            self._readers[shard_index] = reader
        return reader

    def get_data_info(self, index: int) -> Dict[str, Any]:
        data = super().get_data_info(index)
        info = self.data_infos[index]
        group = self._reader(info["_shard"]).read_row_group(info["_row"])
        data["points_bytes"] = group.column("points")[0].as_py()
        if "images" in group.column_names:
            images = pickle.loads(group.column("images")[0].as_py())
            paths = data.get("image_paths") or []
            if images and paths:
                by_path = {
                    resolve_data_path(cam["data_path"]): images.get(name)
                    for name, cam in info.get("cams", {}).items()
                }
                payloads = [by_path.get(path) for path in paths]
                if all(payload is not None for payload in payloads):
                    data["image_bytes"] = payloads
        return data
```

### A.2 `mmdet3d/datasets/__init__.py` 末尾加一行

```python
from .packed_nuscenes_dataset import PackedNuScenesDataset  # noqa: F401,E402
```

### A.3 `mmdet3d/datasets/nuscenes_dataset.py`

顶部加 `import os`，并在 `from .custom_3d import Custom3DDataset` 之后加：

```python
# Annotation files store sample paths relative to the dataset directory so the
# same pkl works whether the data is mounted from object storage or read from
# packed shards. Absolute paths in legacy annotation files are unchanged.
_DATA_ROOT = (
    os.environ.get("BEVFUSION_DATA_ROOT", "").strip()
    or os.environ.get("PLATFORM_DATASET_PATH", "").strip()
)


def resolve_data_path(path):
    if not path or not _DATA_ROOT or osp.isabs(path):
        return path
    return osp.join(_DATA_ROOT, path)
```

然后把 `get_data_info` 里的两处取路径改成走它：

```python
# 原：lidar_path=info["lidar_path"],
lidar_path=resolve_data_path(info["lidar_path"]),

# 原：data["image_paths"].append(camera_info["data_path"])
data["image_paths"].append(resolve_data_path(camera_info["data_path"]))
```

### A.4 `mmdet3d/datasets/pipelines/loading.py`

顶部加 `import io`。

`LoadPointsFromFile.__call__` 开头：

```python
# 原：
#   lidar_path = results["lidar_path"]
#   points = self._load_points(lidar_path)
payload = results.get("points_bytes")
if payload is not None:
    # Packed shards deliver the raw buffer, so no per-sample file open.
    points = np.frombuffer(payload, dtype=np.float32).copy()
else:
    points = self._load_points(results["lidar_path"])
```

`LoadMultiViewImageFromFiles.__call__` 里读图那段：

```python
# 原：
#   for name in filename:
#       images.append(Image.open(name))
payloads = results.get("image_bytes")
if payloads:
    # Packed shards carry the frames inline, so no per-image file open.
    images = [Image.open(io.BytesIO(payload)) for payload in payloads]
else:
    for name in filename:
        images.append(Image.open(name))
```

### A.5 `tools/westwell_train.py`

三处：

```python
# 1) 文件最开头，第一个 import 之前
import distutils.version  # noqa: F401  (torch.utils.tensorboard expects this submodule)

# 2) 把 cfg.dump 包进 rank 判断
#    治理存储不支持多进程写同一文件，多卡时必然报 Operation not permitted
_rank = int(os.environ.get("RANK", "0"))
if _rank == 0:
    cfg.dump(os.path.join(cfg.run_dir, "configs.yaml"))

# 3) 日志文件名加 rank 后缀，避免同名并发写
log_file = os.path.join(cfg.run_dir, f"{timestamp}_rank{_rank}.log")
```

### A.6 `configs/default.yaml`

把指向历史路径的预训练权重置空（该路径在平台上不存在）：

```yaml
load_from: null
```

---

## 附录 B：打包脚本 `pack_dataset.py`

放在代码库根目录。运行前改 `ANNOTATIONS` 指向自己的 pkl。

```python
"""Pack per-sample sensor data and annotations into large Parquet shards.

The governed object store is latency bound (~135 ms per file open), which caps
a node near 123 files/s regardless of read concurrency, while its bandwidth
reaches ~900 MB/s. Packing turns random small reads into sequential large ones
so training can stream directly from object storage instead of pre-staging the
whole dataset onto node-local NVMe.
"""
import json, os, pickle, time
from concurrent.futures import ThreadPoolExecutor

import pyarrow as pa
import pyarrow.parquet as pq

SOURCE = os.environ.get("PLATFORM_DATASET_PATH", "/mnt/data/input")
ANNOTATIONS = "/mnt/storage/me/pkl/<你的pkl目录>/infos_train.pkl"   # ← 改这里
DEST = os.path.join(os.environ.get("PLATFORM_OUTPUT_PATH", "/mnt/data/output"), "packed")
TARGET_SHARD_BYTES = 512 * 1024 * 1024
READ_CONCURRENCY = 64
MANIFEST = os.path.join(DEST, "_manifest.json")


def load_manifest():
    if os.path.exists(MANIFEST):
        with open(MANIFEST) as handle:
            return json.load(handle)
    return {"scenes": [], "shards": [], "samples": 0}


def read_sample(info):
    relative = info["lidar_path"]
    with open(os.path.join(SOURCE, relative), "rb") as handle:
        payload = handle.read()
    # Camera frames are the small-file case that makes object storage hurt:
    # ~140 KB each, five per sample.
    images = {}
    for name, cam in info.get("cams", {}).items():
        try:
            with open(os.path.join(SOURCE, cam["data_path"]), "rb") as handle:
                images[name] = handle.read()
        except OSError:
            pass
    return relative.split("/")[0], info["token"], payload, pickle.dumps(info, protocol=4), pickle.dumps(images, protocol=4)


def write_shard(rows, index):
    table = pa.table({
        "scene": pa.array([r[0] for r in rows]),
        "token": pa.array([r[1] for r in rows]),
        "points": pa.array([r[2] for r in rows], type=pa.large_binary()),
        "info": pa.array([r[3] for r in rows], type=pa.large_binary()),
        "images": pa.array([r[4] for r in rows], type=pa.large_binary()),
    })
    os.makedirs(DEST, exist_ok=True)
    name = "shard-%05d.parquet" % index
    # One sample per row group: a shuffled read then fetches only that sample,
    # while still paying just one file open per shard instead of one per sample.
    pq.write_table(table, os.path.join(DEST, name), compression="none", row_group_size=1)
    return name, table.nbytes, len(rows)


def main():
    with open(ANNOTATIONS, "rb") as handle:
        infos = pickle.load(handle)["infos"]
    manifest = load_manifest()
    done = set(manifest["scenes"])
    pending = [i for i in infos if i["lidar_path"].split("/")[0] not in done]
    print("total=%d already_packed_scenes=%d pending=%d" % (len(infos), len(done), len(pending)), flush=True)
    if not pending:
        print("PACK DONE nothing to do", flush=True)
        return

    started = time.time()
    index = len(manifest["shards"])
    rows, pending_bytes, total_bytes, total_rows = [], 0, 0, 0
    scenes_seen = set()
    # Bounded batches: ThreadPoolExecutor.map is eager, so mapping the whole
    # dataset at once would materialise every sample in memory before the first
    # shard is written.
    chunk = max(READ_CONCURRENCY * 4, 128)
    with ThreadPoolExecutor(max_workers=READ_CONCURRENCY) as pool:
      for start in range(0, len(pending), chunk):
        for scene, token, payload, blob, imgs in pool.map(read_sample, pending[start:start + chunk]):
            scenes_seen.add(scene)
            rows.append((scene, token, payload, blob, imgs))
            pending_bytes += len(payload) + len(blob) + len(imgs)
            if pending_bytes >= TARGET_SHARD_BYTES:
                name, size, count = write_shard(rows, index)
                manifest["shards"].append(name)
                total_bytes += size; total_rows += count; index += 1
                print("wrote %s %.1f MB %d rows (%.0f s)" % (name, size/1e6, count, time.time()-started), flush=True)
                rows, pending_bytes = [], 0
    if rows:
        name, size, count = write_shard(rows, index)
        manifest["shards"].append(name)
        total_bytes += size; total_rows += count
        print("wrote %s %.1f MB %d rows" % (name, size/1e6, count), flush=True)

    manifest["scenes"] = sorted(done | scenes_seen)
    manifest["samples"] = manifest["samples"] + total_rows
    manifest["updated_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    with open(MANIFEST, "w") as handle:
        json.dump(manifest, handle, indent=2)

    elapsed = time.time() - started
    print("PACK DONE scenes=%d shards=%d samples=%d bytes=%.1f GB elapsed=%.0f s -> %.1f MB/s"
          % (len(scenes_seen), len(manifest["shards"]), total_rows, total_bytes/1e9, elapsed, total_bytes/1e6/elapsed), flush=True)


if __name__ == "__main__":
    main()
```

**前提**：pkl 里的 `lidar_path` 和 `cams[*].data_path` 需要是**相对数据集根**的路径
（形如 `<场景ID>/samples/LIDAR_TOP/xxx.bin`）。如果现有 pkl 存的是绝对路径，
先用一小段脚本去掉公共前缀即可。
