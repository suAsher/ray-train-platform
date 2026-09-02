# S1H：从拉取代码到 Ray Train + Ray Data 流式训练

本文给算法用户一条可复制的生产流程：从 GitLab 拉取 S1H BEVFusion 代码，选择不可变数据版本，通过 `spk-rayjob` 或原生 `ray job` 提交 2 节点 × 8 GPU 训练，并在 Portal 核对 Ray Data、NVMe 热缓存、Ray spilling、指标与产物。

本文只使用平台公开契约。用户不需要也不应接触 TOS 凭据、PVC、节点目录或 kubeconfig。

## 1. 先理解这条数据链路

生产数据源 `/mnt/storage/public/labeled` 会持续同步，因此它不是可复现的训练输入。数据发布器会从某个稳定时刻创建一个不可变版本：

```text
持续变化的 public/labeled
  → 不可变版本 manifest
  → Parquet 索引（样本、切分、类别、payload 定位和摘要）
  → WebDataset/TAR 大分片（相机、点云等二进制 payload）
  → Ray Data 按需读取索引并流式分发
  → 训练 Worker 只缓存正在使用的 shard
```

这里的“做成 Parquet”不是把十几 TB 图片和点云都塞进 Parquet，也不是把全量数据预热到 NVMe：

- Parquet 保存结构化索引、标注和版本 provenance，便于列裁剪、过滤和并行读取；
- 大量二进制小文件被合并为约 512 MiB～1 GiB 的 TAR shard，减少对象存储请求次数；
- `bounded` NVMe 缓存按 shard digest 保存热点分片，空间满后按 LRU 淘汰；
- Ray object store 承载流水线中的数据 block，内存压力达到阈值时由 Ray spilling 写入本地盘；
- 数据真相始终是不可变版本，NVMe 与 spilling 都不是第二份永久数据。

当前生产 READY 版本如下：

| 字段 | 值 |
| --- | --- |
| 数据集 | `dataset-091df388b464bf2ff76d6167` |
| 版本 | `version-a26436a221f5f4b851863a3bde884d26` |
| schema | `s1h-multimodal-webdataset-v2` |
| train / val | 15,228 / 1,620 |
| 总样本 | 16,848 |
| manifest SHA-256 | `42f3eae047385705129582a086ef261573835962afc275384d6178f5a0f07318` |

只能提交状态为 `READY` 的版本。`DISCOVERING / STABILIZING / VALIDATING / PACKING / FAILED` 都不可训练。

## 2. 拉取一份干净的 S1H 代码

不要复制构建机上的历史 checkout。它们可能包含未提交补丁，不能作为用户流程基线。

```bash
git clone ssh://git@gitlab.qomolo.com/dl/bevfusion.git bevfusion-bev_3dod_s1h
cd bevfusion-bev_3dod_s1h
git checkout platform/raytrain-bev-3dod-s1h
git rev-parse HEAD
```

期望平台适配提交为：

```text
41440886f1d3dc1bac76952873c41e77bf4097e5
```

创建自己的开发分支：

```bash
git switch -c platform/<用户名>-s1h-raydata
```

提交前检查源码会被上传，而且数据、checkpoint、虚拟环境不会被打包：

```bash
python3 -m py_compile \
  tools/westwell_train.py \
  mmdet3d/datasets/platform_paths.py

git check-ignore -v mmdet3d/runner/__init__.py || true
git check-ignore -v mmdet3d/datasets/nuscenes_dataset.py || true
git status --short
```

两条 `git check-ignore` 正常应无输出。若显示被 `run*/` 或 `datasets/` 排除，先修正 `.gitignore` / `.rayignore`，否则任务收到的源码不完整。

## 3. 在 Portal 选择运行契约

在“版本化数据集”中展开 `S1H labeled streaming v2`，确认上述版本为 `READY`。新建任务时选择：

- 引擎：`Ray Train 托管`；
- 数据读取：`Ray Data 流式读取`；
- 数据版本：上述 dataset/version；
- 数据缓存：`bounded`；
- 镜像：`BEVFusion S1H · Ray Train 2.58 + Ray Data · NVMe · empty-target safe`；
- 资源：2 个 Worker，每 Worker 8 GPU、64 CPU、256 GiB；
- 输出：个人训练结果空间。

当前验收镜像的不可变引用是：

```text
harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-ray258@sha256:9acbac27f786822ae51c26cfc210b43cb855b0409df29460ac285b30760029fe
```

不要改用浮动 tag，也不要把旧 `ray-ddp` 任务原地切换引擎；每次提交都会创建新任务，不影响既有任务。

## 4. 方式一：`spk-rayjob` 提交

首次使用时登录平台。密码或访问令牌只在交互提示中输入，不要写进脚本：

```bash
spk-rayjob login --server https://raytrain.wellspiking.ai
```

在 S1H checkout 根目录执行：

```bash
RUN_ID="$(date +%Y%m%d-%H%M%S)"

spk-rayjob submit \
  --name "s1h-raytrain-streaming-${RUN_ID}" \
  --dir . \
  --image 'harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-ray258@sha256:9acbac27f786822ae51c26cfc210b43cb855b0409df29460ac285b30760029fe' \
  --entrypoint 'python tools/westwell_train.py configs/westwell/det/transfusion/secfpn/camera+lidar/resnet50/convfuser.yaml --launcher pytorch --run-dir /mnt/data/output data.samples_per_gpu=1 data.workers_per_gpu=1 optimizer.lr=1.0e-5 model.heads.object.num_classes=10 runner.max_epochs=1 log_config.interval=10 evaluation.interval=1' \
  --engine ray-train \
  --execution-mode ray_train \
  --data-mode streaming \
  --dataset 'dataset-091df388b464bf2ff76d6167:version-a26436a221f5f4b851863a3bde884d26' \
  --dataset-cache-policy bounded \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --max-failures 2 \
  --checkpoint-every-epochs 1 \
  --checkpoint-keep-latest 3 \
  --checkpoint-keep-best 1 \
  --output-path "s1h/raytrain-streaming-${RUN_ID}" \
  --watch
```

注意：

- 托管入口必须是 `python file.py` 或 `python -m package.module`，不要再套 `torchrun`、`raytrain-launch` 或 shell 管道；
- `/mnt/data/output` 是任务独立输出，不能把 checkpoint 写回公共数据；
- `runner.max_epochs=1` 表示完整数据训练 1 epoch，不表示只取一部分样本；
- 更改普通 Python 或 YAML 后重新提交即可，镜像无需重建。

常用查询：

```bash
spk-rayjob jobs
spk-rayjob status <平台任务ID>
spk-rayjob logs -f <平台任务ID>
```

## 5. 方式二：原生 `ray job submit`

原生入口适合已有 Ray Jobs 自动化。客户端使用 Ray 2.58.x，并通过平台 PAT 认证。PAT 不要写进文件或命令历史：

```bash
python3 -m pip install 'ray[default]==2.58.0'
export RAY_ADDRESS='https://raytrain.wellspiking.ai/ray'
read -rsp '平台个人访问令牌: ' RAYTRAIN_PAT
printf '\n'
export RAY_JOB_HEADERS="$(jq -cn --arg token "$RAYTRAIN_PAT" '{Authorization:("Bearer "+$token)}')"
unset RAYTRAIN_PAT
```

构造平台 metadata：

```bash
IMAGE='harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion-ray258@sha256:9acbac27f786822ae51c26cfc210b43cb855b0409df29460ac285b30760029fe'
DATASET_ID='dataset-091df388b464bf2ff76d6167'
VERSION_ID='version-a26436a221f5f4b851863a3bde884d26'
RUN_ID="$(date +%Y%m%d-%H%M%S)"

META="$(jq -cn \
  --arg image "$IMAGE" \
  --arg dataset "$DATASET_ID" \
  --arg version "$VERSION_ID" \
  '{
    "ray-platform.image":$image,
    "ray-platform.worker-replicas":"2",
    "ray-platform.gpus-per-worker":"8",
    "ray-platform.cpu-per-worker":"64",
    "ray-platform.memory-per-worker":"256Gi",
    "ray-platform.queue":"local-gpu",
    "platform.training.engine":"ray-train",
    "platform.dataset.ref":$dataset,
    "platform.dataset.version":$version,
    "platform.dataset.cache-policy":"bounded"
  }')"
```

在 checkout 根目录提交：

```bash
ray job submit \
  --address "$RAY_ADDRESS" \
  --submission-id "s1h-native-${RUN_ID}" \
  --working-dir . \
  --runtime-env-json '{"excludes":[".git/","data/","datasets/","work_dirs/","*.pkl","*.pth","__pycache__/",".venv/"]}' \
  --metadata-json "$META" \
  -- \
  python tools/westwell_train.py \
    configs/westwell/det/transfusion/secfpn/camera+lidar/resnet50/convfuser.yaml \
    --launcher pytorch \
    --run-dir /mnt/data/output \
    data.samples_per_gpu=1 \
    data.workers_per_gpu=1 \
    optimizer.lr=1.0e-5 \
    model.heads.object.num_classes=10 \
    runner.max_epochs=1 \
    log_config.interval=10 \
    evaluation.interval=1
```

任务创建后可删除当前 shell 中的认证头：

```bash
unset RAY_JOB_HEADERS META
```

平台目前会在创建任务时检查团队实时配额。若一个 2×8 任务已经占满 16 GPU，再提交第二个 2×8 会直接返回类似下面的错误，而不是进入 Kueue：

```text
tenant GPU quota exceeded: quota=16 used=16 requested=16
```

这说明提交契约已通过网关校验，但不代表第二个任务已经运行。等待第一个任务结束并释放配额后再提交；不要停止他人的任务来腾卡。

## 6. 如何确认不是“只启动了一个 smoke”

在 Portal 任务详情同时核对以下证据：

1. 任务固定了代码 commit、镜像 digest、dataset ID、version ID 和 manifest digest；
2. `engine=ray-train`、`dataMode=streaming`、`cachePolicy=bounded`；
3. 两个 8-GPU Worker 位于两台节点，Ray Train world size 为 16；
4. Ray Data 读取 15,228 条 train row，并显示 blocks/rows/s；
5. 日志出现持续递增的 `Epoch [1][x/952]`、有限 loss、`data_time` 和 iteration time；
6. 性能页可见 GPU 利用率、Ray Data 吞吐、NVMe hit/miss/eviction 和 spilling；
7. 任务结束后个人结果空间存在配置、checkpoint、指标及完整 manifest。

当前生产验收已经看到 Ray Data 在约 7.05 秒内处理 15,228 条训练索引（约 2.16k rows/s），分为 16 个训练 split，并继续进入真实 BEVFusion forward/backward。该证据验证的是全量数据链路和训练执行；1 epoch 的模型精度仍需算法负责人评审。

### 6.1 启动后很慢是否正常

先看 `data_time`，不要只看一个总 ETA。首轮访问某个 shard 时，Worker 需要从版本存储读取 TAR、校验 digest 并写入本机有界 NVMe；同一 shard 再次被使用时才是缓存命中。Ray Data 读取 Parquet 索引很快，并不等于二进制 payload 已全部位于本地。

一次真实 2×8 全量任务的冷启动观测如下。这些数值用于解释阶段，不是固定 SLA：

| 位置 | iteration time | data_time | 判断 |
| --- | ---: | ---: | --- |
| 10 / 952 | 12.63 s | 5.95 s | 冷 shard 与 CUDA/DDP 预热叠加 |
| 60 / 952 | 8.48 s | 2.91 s | 缓存正在升温 |
| 110 / 952 | 6.31 s | 1.61 s | I/O 等待继续下降 |
| 已完成任务末段 | 约 0.3～1.8 s | 约 0.001～0.58 s | 热点 shard 已命中；不同 batch 计算量仍有波动 |

正常现象是前几十到一百多步逐步下降。出现以下任一情况时，应在任务详情的“训练性能”中排查，而不是继续盲等：

- 连续 100 步以上 `data_time / time` 仍大于 50%，且没有下降趋势；
- GPU 利用率长期偏低，同时 source reads、cache misses 或 prefetch wait 持续增长；
- cache eviction 与 miss 同时快速增长，说明有界缓存过小或 shard 访问局部性差；
- checksum failure 或 fallback 非零；
- Ray object-store spilling 很高，同时 Worker 内存长期接近上限。

`bounded` NVMe 缓存和 Ray spilling 解决的是不同问题：前者缓存远端 WebDataset shard，后者在 Ray object store 内存不足时暂存数据 block。提高 spilling 不能替代 shard 缓存，也不应把 10 TB 全量数据预热到 NVMe。平台应依赖并行预取、热点复用和 LRU 淘汰，让训练边读边跑。

### 6.2 在 MLflow 查看训练

新提交的托管任务会由全局 rank 0 自动创建一个 MLflow Run，Run 名就是平台任务 ID。用户不需要在 S1H 代码中填写 Tracking URI、账号或对象存储凭据。

自动记录的内容包括：

- 参数：optimizer、learning rate、epoch、seed、world size；
- provenance：dataset/version、cache policy、Ray version、cluster attempt；
- 指标：loss、学习率、iteration time、data_time、epoch，以及评估阶段产生的 mAP/NDS；
- 数据读取指标：以 `rank0_worker_dataset_*` 命名，明确表示 rank 0 的代表性 Worker 计数；全局逐 Worker 指标仍以任务性能页的 Prometheus 数据为准；
- 终态：正常结束标记为 `FINISHED`；异常退出由平台终态协调器标记为 `FAILED` 或 `KILLED`。

查看步骤：

1. 打开 Portal 的“实验中心”；
2. 按平台任务 ID 搜索 Run；
3. 在曲线中同时选择 `loss`、`time`、`data_time`，判断慢在计算还是数据；
4. 需要原生 Run 对比时点击“打开 MLflow”；浏览器仍走平台同域鉴权。

若任务已开始但没有 Run：

1. 确认任务使用文档列出的最新不可变镜像 digest；旧镜像中的运行任务不会被追溯补写；
2. 在日志中搜索 `[raytrain][mlflow]`，该行只给出错误类型，不泄露内部地址或凭据；
3. 确认任务确实为 `engine=ray-train`，而不是旧 `ray-ddp`；
4. 将平台任务 ID 提供给管理员，不要在训练代码中手工硬编码 MLflow 地址。

## 7. 不想训练全量数据怎么办

先区分三种需求：

| 需求 | 正确做法 |
| --- | --- |
| 少跑几个 epoch | 改 `runner.max_epochs`；每个 epoch 仍读取该版本完整 train split。 |
| 临时链路/性能验收 | 由管理员从父版本创建固定样本数的不可变验收版本。 |
| 只训练墨西哥、某园区或某时间段 | 发布一个带明确筛选条件的派生数据版本，再训练该 READY 版本。 |

当前 v2 Parquet 索引包含 `token / scene / split / class_ids / timestamp / payloads / info`，但没有经过治理的 `country` 或 `region` 字段；Portal 和提交 API 也不接受用户任意注入 Parquet predicate。因此，现在不能把 `scene` 名猜成国家，也不能在命令里写一个路径就宣称“墨西哥数据集”。

### 7.1 当前可用：固定样本数验收版本

管理员可以从已有 READY 父版本切出确定的 train/val 样本数，用于快速验证吞吐、算法改动或两种提交入口：

```bash
bash ops/datasets/run-s1h-index-build.sh \
  --slice-from-version <父版本ID> \
  --train-samples <数量> \
  --val-samples <数量>
```

这个工具生成新的受信索引；它不是用户侧随机抽样，也不是语义上的“墨西哥筛选”。发布到 `READY` 后，用户用新 dataset/version 替换上面命令中的 `--dataset` 或 metadata，其他训练参数不变。

### 7.2 当前正确的墨西哥流程

1. 数据负责人给出可信的 Mexico scene/token 清单或可审计元数据；
2. 管理员从全量父版本生成只包含这些样本的受信索引；
3. 平台创建派生版本，记录父版本、筛选摘要、样本数与 digest；
4. 发布器复用父版本已有 TAR shard/digest，只有必要时才重打分片；
5. 派生版本变为 `READY` 后，用户在 Portal 选择它并使用同一训练命令。

若当前原始数据没有可信国家字段，必须先补数据治理，不能根据文件名臆测。

### 7.3 平台下一步的通用子集能力

目标 UI 应让数据管理员创建“派生数据版本”，选择治理字段（如 `country / site / weather / date / class`），预览 train/val 数量，然后发布不可变版本。任务仍只引用 READY version；训练用户不能在运行时改变过滤条件。这样既能复用父版本 shard，又能保证同一 version 在不同提交入口上得到完全相同的数据。

## 8. 为什么日志曾出现大量空行和“乱码”

Ray Data 和 Rich 会在真实终端中使用 ANSI 控制序列（例如光标上移）反复刷新一块进度区域。容器 stdout 没有持续终端画布，Loki 会把空行、颜色码和光标控制帧逐条保存；Portal 原样显示时，就表现为大量空行和 `ESC` 乱码。这不是 Parquet 损坏，也不是训练数据编码错误。

平台日志查询层会：

- 删除纯空白和纯终端控制帧；
- 去除颜色/光标转义码；
- 对 `\r` 重绘只保留最终帧；
- 保留真实日志文本、stream 标签、时间戳和原始分页位置。

因此 CLI、Portal 和原生 Ray 日志入口会得到相同的可读结果。若日志仍停滞，按任务状态、Worker 事件和 GPU 指标判断，不要仅凭某一条进度动画下结论。

## 9. 常见问题

| 现象 | 含义与处理 |
| --- | --- |
| 数据版本不可选 | 只有 `READY` 可训练；查看失败分区与发布错误，不要提交 `PACKING`。 |
| `tenant GPU quota exceeded` | 当前团队配额已被占满；等待资源释放。 |
| working directory 上传 413 | 检查排除规则和接入层上传上限；不要把数据或 checkpoint 打包。 |
| 一直没有 Ray Data rows/s | 查看数据版本、manifest 和 Worker 事件；不要重复提交 16 卡任务。 |
| 前 100 步很慢 | 对比 `time` 与 `data_time` 的下降趋势；冷 shard 正常，持续无下降才需要排查。 |
| 实验中心没有 MLflow Run | 确认使用最新托管镜像，搜索 `[raytrain][mlflow]`；旧任务不会追溯补写。 |
| loss/grad 为 NaN | 算法侧检查类别数、学习率、warmup、fp16 loss scale 和梯度裁剪。 |
| 第二个 2×8 没有排队记录 | 当前实时配额门禁在创建前拒绝；这是现行行为。 |
| 修改 Python 后是否重建镜像 | 不需要；当前 checkout 会随任务形成不可变代码包。 |

## 10. 每次验收必须记录

```text
平台任务 ID
提交入口（spk-rayjob / native ray / Portal）
提交用户和团队
算法 Git remote / branch / commit / tree
训练镜像 digest
dataset ID / version ID / manifest digest
资源：Worker × GPU / CPU / memory
Ray Train world size
Ray Data rows、读取时间与吞吐
NVMe hit / miss / eviction、Ray spilling
iteration time / data_time / samples/s / GPU 利用率
训练终态、指标和 checkpoint 路径/摘要
```

缺少其中任一关键证据时，应描述为“链路正在验证”或“提交契约已验证”，不能描述为全量训练已经成功或模型已经收敛。
