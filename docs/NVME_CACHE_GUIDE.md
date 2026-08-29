# 双 NVMe 训练数据加速使用指南

本文说明训练平台的节点本地 NVMe 缓存：用户如何用参数开启、平台实际做了什么、代码如何读取、如何估算容量，以及遇到问题如何排查。

## 1. 最短结论

提交任务时指定四项即可：

1. 选择一个具体的训练输入子目录；
2. `cache.mode=runtime`；
3. 选择缓存总容量；
4. `cache.preload=input`。

```bash
spk-rayjob submit \
  --cache-mode runtime \
  --cache-size 5Ti \
  --cache-preload input \
  --input-space public \
  --input-path labeled/<数据集版本> \
  --watch
```

平台会在每个 GPU Worker 启动训练前，把所选输入复制到该节点的两块 NVMe。模型代码不负责复制，训练镜像也不因缓存而变化。

## 2. 两种 runtime 缓存用法

| 配置 | 实际效果 | 数据代码是否要处理缓存 |
| --- | --- | --- |
| `mode: runtime`，不配置 `preload` | Ray session、object spilling 和用户主动写入的临时文件使用 NVMe；训练输入仍从 TOS/FSX 读取 | 不需要 |
| `mode: runtime`，`preload: input` | 平台自动把所选输入预热到双 NVMe，并把 `PLATFORM_DATASET_PATH` 切到缓存视图 | 不需要复制代码；程序应遵守平台数据路径契约 |

缓存默认关闭。未开启缓存的旧任务、正在运行的任务和现有训练镜像保持原行为。

## 3. 平台实际做了什么

生产 GPU 节点的两块本地盘分别提供 `/data1/ray-cache`、`/data2/ray-cache`。用户和训练容器不会直接看到节点路径。平台为每个 Worker 创建两个一次性本地卷：

```text
/mnt/cache   -> 节点 /data1/ray-cache 下的任务隔离目录
/mnt/cache2  -> 节点 /data2/ray-cache 下的任务隔离目录
```

自动预热流程：

```text
任务通过 Kueue 获得资源
  -> 每个 Worker 绑定本节点的两块临时 NVMe 卷
  -> 平台预热容器枚举所选输入并做容量预检
  -> 32 路并发复制，文件稳定分散到两块盘
  -> 原子发布 /mnt/cache/dataset-view
  -> Ray Worker 和 GPU 训练进程启动
```

Head 不复制训练数据；Submitter 也不挂载 NVMe。多机训练中的每个 Worker 都有自己的本地副本，因此不会跨节点共享缓存。

## 4. 训练容器中的路径

自动预热后，平台注入：

```text
PLATFORM_DATASET_SOURCE_PATH=/mnt/data/input
PLATFORM_DATASET_PATH=/mnt/cache/dataset-view
PLATFORM_CACHE_PATH=/mnt/cache
PLATFORM_CACHE_PATHS=/mnt/cache:/mnt/cache2
PLATFORM_CACHE_PRELOAD=input
```

- `PLATFORM_DATASET_SOURCE_PATH`：TOS/FSX 的持久只读源，用于诊断；
- `PLATFORM_DATASET_PATH`：本次训练应读取的活跃数据根，开启预热后指向 NVMe；
- `PLATFORM_OUTPUT_PATH`：持久训练结果目录，不会切到 NVMe；
- `PLATFORM_CHECKPOINT_PATH`：持久只读续训输入，不会切到 NVMe。

平台兼容代码应读取 `PLATFORM_DATASET_PATH`。如果训练程序本来支持 `--data-root`，提交命令直接传环境变量即可，无需修改模型逻辑：

```bash
python3 train.py --data-root "$PLATFORM_DATASET_PATH"
```

现有平台版 BEVFusion 的路径解析器已经遵守这个契约。若旧代码把 `/mnt/storage/public` 写死在 Python 中，它会绕过缓存；这类代码需要做一次通用数据根适配，之后每次任务只改提交参数，不再改缓存代码或重建镜像。

## 5. 网页使用

1. 在“训练输入”中选择数据空间，例如“公共数据”。
2. 必须继续选择一个具体子目录，例如 `labeled/fz-3dod-v1`，不能停在整个 `public` 根。
3. 在“运行规模”中选择“运行时缓存”和总容量。
4. 开启“自动预热所选输入到双 NVMe”。
5. 检查提交预览中的缓存容量和自动预热标记后提交。

页面会在没有具体输入目录、容量不受支持或缓存关闭却配置预热时阻止提交。

## 6. spk-rayjob 使用

### 6.1 单次命令

```bash
spk-rayjob submit \
  --name bevfusion-cache-$(date +%Y%m%d-%H%M%S) \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --cache-mode runtime \
  --cache-size 5Ti \
  --cache-preload input \
  --input-space public \
  --input-path labeled/<本次数据集版本> \
  --entrypoint 'python3 tools/westwell_train.py <配置文件> --launcher pytorch' \
  --watch
```

### 6.2 项目默认值

```yaml
cache:
  mode: runtime
  size: 5Ti
  preload: input

input:
  space: public
  path: labeled/<本次数据集版本>
```

然后执行 `spk-rayjob submit --watch`。临时关闭使用：

```bash
spk-rayjob submit --cache-mode off --watch
```

显式关闭时，平台同时清除项目文件里继承的容量和预热设置。

## 7. 原生 ray job submit

原生 Ray API 也使用逻辑数据空间，不接受 TOS AK/SK、PVC 或节点路径：

```bash
META="$(jq -cn \
  --arg image 'harbor.wellspiking.ai/<项目>/<镜像>:<tag>' \
  '{
    "ray-platform.image": $image,
    "ray-platform.worker-replicas": "2",
    "ray-platform.gpus-per-worker": "8",
    "ray-platform.cpu-per-worker": "64",
    "ray-platform.memory-per-worker": "256Gi",
    "ray-platform.queue": "local-gpu",
    "platform.data.input-space": "public",
    "platform.data.input-path": "labeled/<本次数据集版本>",
    "platform.cache.mode": "runtime",
    "platform.cache.size": "5Ti",
    "platform.cache.preload": "input"
  }')"

ray job submit \
  --address "$RAY_ADDRESS" \
  --submission-id "bevfusion-cache-$(date +%Y%m%d-%H%M%S)" \
  --working-dir . \
  --metadata-json "$META" \
  -- python3 train.py
```

未知的 `platform.cache.*`、`platform.data.*` 字段会被拒绝，避免拼写错误悄悄退化为直读存储。

## 8. 容量怎么选

界面中的容量是“每个 Worker 的双盘总容量”，平台平均拆到两块盘：

| 用户选择 | `/data1` 对应卷 | `/data2` 对应卷 |
| --- | ---: | ---: |
| `200Gi` | `100Gi` | `100Gi` |
| `1Ti` | `512Gi` | `512Gi` |
| `5Ti` | `2.5Ti` | `2.5Ti` |

文件按相对路径哈希分盘，不保证字节数绝对各占一半。平台会分别检查每块盘的计划字节数和实时可用空间；任一块不足都会在训练启动前失败。

容量至少应满足：

```text
所选输入实际容量 + Ray 临时文件 + object spilling + 10%~20% 余量
```

不要选 `public` 根去预热约 10 TB 全量数据。应把任务实际使用的数据组织/登记为一个数据集版本目录，再只选择该目录。

## 9. 冷启动、训练收益与适用场景

每个新任务都会先从 TOS/FSX 复制一次，任务结束后缓存释放，不跨任务保留。

预热发生在已经绑定目标 GPU 节点的 Worker Pod 内，因此预热期间该任务的 GPU 配额和显卡已经被保留，但训练进程尚未启动。这样才能保证数据写入训练即将运行的同一台机器；评估总收益时必须把这段冷启动时间算进去。

在 2026-08-26 对 `bev_3dod_s1h`、约 300 GiB/24 万小文件的实测中：

| 方式 | 稳态单步时间 | `data_time` | 备注 |
| --- | ---: | ---: | --- |
| 直接读 TOS/FSX | 约 `3.4~3.7 s` | 常见 `0.02 s`，但有元数据长尾 | 16 卡扩展效率差 |
| 双 NVMe | 约 `1.5~1.6 s` | 约 `0.02 s` | 单机、多机都明显改善 |

每节点预热约 15~17 分钟。该结果是当前数据形态和硬件的验收基线，不是所有数据集的性能保证。

适合自动预热：大量小文件、随机访问、多个 epoch 重复读取、长训练，以及增加 GPU 后共享存储没有获得相同比例加速的任务。

不适合：短冒烟任务、少量大文件顺序读取、输入大于 NVMe，或预热时间大于训练节省时间的单轮任务。

```text
有收益 = 预热时间 + NVMe 训练时间 < 直接存储训练时间
```

## 10. 日志和状态怎么看

预热期间任务处于资源准备阶段，GPU 训练日志尚未出现。平台预热日志包含：

```text
RAYTRAIN_DATASET_CACHE_PROGRESS files=<已复制>/<总数> bytes=<字节数>
RAYTRAIN_DATASET_CACHE={...最终文件数、字节数、耗时、每盘分布...}
```

预热成功后才出现 Ray Worker、NCCL 和训练 epoch 日志。

| 提示 | 原因 | 处理 |
| --- | --- | --- |
| `requires a non-empty input path` | 选了整个数据空间根 | 选择具体数据集版本目录 |
| `selected dataset exceeds cache capacity` | 某块盘计划数据超过额度或可用空间 | 增大容量，或缩小输入目录 |
| `source dataset contains a symbolic link` | 输入含可能逃逸授权目录的符号链接 | 发布为真实文件目录后重提 |
| `dataset cache warmup timed out` | 目录过大、存储/DNS 或节点 I/O 异常 | 查看进度并检查存储，必要时缩小目录 |
| 训练仍读 `/mnt/storage/public` | 代码写死旧路径 | 使用 `PLATFORM_DATASET_PATH` 或已有 `--data-root` 参数 |

预热失败会在 GPU 训练开始前明确失败，不会静默切回慢速路径。立即回退可用同一代码重提并指定 `--cache-mode off`。

## 11. 任务结束是否自动清理

会。缓存使用 Kubernetes generic ephemeral PVC：

```text
Ray Worker 删除 -> 临时 PVC 删除 -> 本地 PV 删除 -> provisioner 删除该任务隔离目录
```

训练结果、Checkpoint、MLflow 记录和原始 TOS/FSX 数据不在回收链路中。成功任务较快释放缓存；失败任务为保留诊断窗口，可能延迟数分钟释放。任何必须保留的文件都要写入 `PLATFORM_OUTPUT_PATH`。
