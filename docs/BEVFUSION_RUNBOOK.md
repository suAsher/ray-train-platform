# BEVFusion 分布式训练交付手册

本文记录两份 BEVFusion 代码在 RayTrain 上的**实际改造、提交命令和 2 节点 × 8 GPU 验收结果**。用户代码随任务上传，镜像只提供固定的 Python、CUDA、PyTorch、Ray 与已编译扩展；修改代码后不需要重新构建镜像。

> 2026-08-20 接入报告复核：`bev-3dod-smoke6` 的 RayJob 与 RayCluster 均已正常创建，失败点是 entrypoint 未使用 `raytrain-bevfusion-prepare`，导致 working-dir 中的新版 `mmdet3d` 覆盖镜像包后缺少 `mmdet3d.ops` 编译扩展。另一个判断也已更正：`.gitignore` 的 `run*/` 按 Git 自身语义就会匹配 `mmdet3d/runner/`，不是 `spk-rayjob` 扩大了匹配范围。

## 1. 当前可交付基线

| 项目 | 生产基线 |
| --- | --- |
| 平台入口 | `https://raytrain.wellspiking.ai` |
| 分支一 | `bev_3dod`，基线提交 `0c1dc9d` |
| 分支二 | `bev_3dod_s1h`，基线提交 `7931cee` |
| 兼容运行镜像 | `harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553` |
| smoke 数据选择 | `public / bevfusion/fz-3dod-v1` |
| 全量 FZ 数据选择 | `public`，相对路径留空，即公共根目录；不要填写 `.` |
| Pod 中公共根目录 | `/mnt/storage/public`（只读） |
| smoke 标注 | `platform-validation/annotations/fz-0429-platform-smoke-128` |
| 用户结果根目录 | `/mnt/storage/me/runs`（个人读写） |
| 提交客户端 | `spk-rayjob prod-20260819-cli-r7` |

资源口径固定为：生产 2×8 使用每个 Worker 64 CPU / 256GiB；32 CPU / 128GiB 仅用于 smoke。表格和命令中的 CPU、内存均是单个 Worker Pod 的申请量，不是任务总量。

对象存储中的物理 bucket 和前缀由平台管理员维护。用户不接触 TOS URI 或 AK/SK，只选择 `public / <数据集>/<版本>`；平台把登记的数据目录以只读方式挂载给每个训练 Worker，并把每次任务结果写入用户自己的结果空间。

## 2. 2026-08-21 实际验收结果

以下六条均从两个 GitLab 分支的全新 clone 出发，以 `guofeng.su` 身份串行提交到真实 KubeRay + Kueue 集群，不是本机模拟：

| 入口与分支 | 平台任务 | 资源 | 结果 | 产物 |
| --- | --- | --- | --- | --- |
| `spk-rayjob`，`bev_3dod` | `job-0d368c1c99570c6cfd30a704` | 2 Worker × 8 GPU | `SUCCEEDED`，6/6 iteration | checkpoint、验证、拓扑、MLflow |
| 原生 Ray CLI，`bev_3dod` | `job-5cd8dd759fa2a481b5a0178e` | 2 Worker × 8 GPU | `SUCCEEDED`，6/6 iteration | 日志、状态、MLflow |
| Portal 快照，`bev_3dod` | `job-12a730d45f0f83e4b4ae37c5` | 2 Worker × 8 GPU | `SUCCEEDED`，6/6 iteration | checkpoint、验证、拓扑、MLflow |
| `spk-rayjob`，`bev_3dod_s1h` | `job-ec9a64a66c953efd346fefe8` | 2 Worker × 8 GPU | `SUCCEEDED`，smoke-128，6/6 iteration | checkpoint、验证、拓扑、MLflow |
| 原生 Ray CLI，`bev_3dod_s1h` | `job-42b55a5447b7e93afe29bb87` | 2 Worker × 8 GPU | `SUCCEEDED`，smoke-128，6/6 iteration | 日志、状态、MLflow |
| Portal 快照，`bev_3dod_s1h` | `job-69993074d2ff5de4f2d30b17` | 2 Worker × 8 GPU | `SUCCEEDED`，smoke-128，6/6 iteration | checkpoint、验证、拓扑、MLflow |

拓扑文件已经确认：

```json
{
  "mode": "ray_train",
  "workers": 2,
  "gpusPerWorker": 8,
  "worldSize": 16,
  "placementStrategy": "STRICT_SPREAD",
  "nodes": [
    {"nodeRank": 0},
    {"nodeRank": 1}
  ],
  "returnCodes": [0, 0]
}
```

两个 Worker 分别运行在两台不同的 4090 节点，每个 Pod 的 GPU limit 为 8；日志显示 NCCL 初始化，rank 0 完成 6 条 loss 输出。`epoch_1.pth` 大小为 33,332,845 字节，checkpoint 元数据为 `epoch=1, iter=6`。

最终 r9 运行镜像还完成代表性 `2×8` 回归任务 `job-56fbc0b27e9a5b5bc4491023`，终态 `SUCCEEDED`。其清单同样为两个 8-GPU Worker 并分布在两台节点；日志包含 NCCL 2.10.3、6/6 iteration、checkpoint 与验证。checkpoint 位于用户 Pod 内 `/mnt/storage/me/runs/acceptance/20260821/bev-3dod/r9-final/job-56fbc0b27e9a5b5bc4491023/epoch_1.pth`，大小 33,332,845 字节。

这证明了以下链路：

```text
外部代码目录 → 不可变 working-dir 包 → 平台任务记录 → Kueue 准入
→ KubeRay 集群 → 两节点 STRICT_SPREAD → 每节点 torchrun 8 进程
→ NCCL/反向传播 → stdout/Loki → 个人 TOS checkpoint
```

### 2.1 全量 FZ 长时 2×8 验收

在 smoke 基线之外，2026-08-21 使用公共根目录中的 15,228 条训练样本和 1,620 条验证样本完成了一次完整长任务：

| 项目 | 验收结果 |
| --- | --- |
| 平台任务 | `job-6e7c7bb0e7282c61b68ca876` |
| 状态 | `SUCCEEDED` |
| 资源拓扑 | 2 Worker × 8 GPU，`worldSize=16`，`STRICT_SPREAD`，两个 Worker 位于不同 GPU 节点 |
| 训练 | 1 epoch，3,585/3,585 iteration；loss 与 grad norm 全程保持有限值 |
| 验证 | 完整处理 1,620 条验证样本；`mAP=0.5854`，`NDS=0.4364` |
| Checkpoint | `epoch_1.pth`，99,798,985 字节，位于该任务的个人结果目录 |
| 分布式返回码 | `returnCodes=[0, 0]` |
| 平台时间 | 2026-08-21 06:55 创建，07:08 结束（平台显示时间） |

本次使用下文模板中的 `optimizer.lr=1.0e-5`。它验证的是全量数据读取、长时间跨节点 DDP/NCCL、训练、验证、日志和持久化结果链路；一次 epoch 的指标不能作为模型已经收敛或达到业务精度的结论。

随后按端到端文档重新拉取和改造代码，再完成一次独立复跑：数据预检任务 `job-62b349d0af267061b7d14eac` 检查 9,216 条路径、`missing=0`；全量任务 `job-20c0affe64d0a638f1f348c8` 使用 2 Worker × 8 GPU，于 2026-08-21 10:22:23 UTC 开始、10:37:12 UTC 结束，终态 `SUCCEEDED`，验证结果为 `mAP=0.6090`、`NDS=0.4909`，产物接口返回 99,798,985 字节的 `epoch_1.pth`。两次结果共同证明文档流程可复现；指标差异不替代算法侧多 epoch 评审。

### 验收边界

本次使用 128 条 smoke 样本、1 个 epoch、6 个 iteration，已经执行真实 BEVFusion forward/backward 和 checkpoint，不只是 NCCL hello-world。它验证的是**平台、代码兼容性和分布式执行**，不代表完整数据集 20 epoch 的收敛质量已经验收。

当前 smoke 日志中 `grad_norm` 为 `nan`，loss 为有限值。这属于旧 FP16 配置与小样本的算法数值问题；生产训练前仍应由算法负责人用正式数据检查 loss 趋势、mAP、梯度和 checkpoint 可恢复性。

## 3. 代码改造

两个 checkout 都要包含路径重定位、DDP/rank-0 写入和忽略规则修复；S1H 还需删除写死 checkpoint 并修正检测范围。用户不需要访问或复制平台维护机器上的文件。[BEVFusion 代码改造说明](BEVFUSION_CODE_CHANGES.md) 给出了可直接写入算法仓库的完整 Python 和逐文件 diff；修改后应提交到自己的 Git 分支。

提交前至少运行算法仓库自身的单元测试、`python3 -m py_compile`、`git diff --check`，并确认 `git check-ignore -v mmdet3d/runner/__init__.py` 没有输出。

不要把用户仓库打进兼容镜像。镜像 digest 固定环境，当前 checkout 每次由 `spk-rayjob` 或 `ray job submit --working-dir .` 上传。

## 4. 数据目录契约

页面或 `spk-rayjob` 选择 `public / bevfusion/fz-3dod-v1` 后：

```text
PLATFORM_DATASET_PATH=/mnt/storage/public/bevfusion/fz-3dod-v1
PLATFORM_OUTPUT_PATH=<本任务的个人结果目录>
```

当前 smoke 数据使用：

```text
$PLATFORM_DATASET_PATH/
├── raw/...
└── platform-validation/annotations/fz-0429-platform-smoke-128/
    ├── final_merged_nuscenes_infos_train.pkl
    └── final_merged_nuscenes_infos_val.pkl
```

历史 pkl 内含生成机器的绝对路径。`DatasetPathResolver` 会保留可命中的最长路径后缀，并重定位到 `$PLATFORM_DATASET_PATH`。提交前的抽样检查已通过：128/128 点云、640/640 相机文件存在。

正式数据集发布时仍建议把索引、原始文件和 `manifest.json` 放在同一个不可变版本目录，并记录样本数、校验摘要、train/val/test 切分和生成代码提交。路径解析器是兼容层，不替代数据版本治理。

### 4.1 当前公共数据实测

2026-08-21 通过与训练任务相同的公共 PVC、普通用户身份和 BEVFusion 运行镜像检查了 `/mnt/storage/public`。公共根目录包含：

```text
/mnt/storage/public/
├── 0429_pkl/
├── 0813_pkl/
├── bevfusion/
├── cnfzhjyg/
├── mxvlkica/
└── run_dir/
```

FZ 合并数据的实际结果为：

| 索引 | 样本数 | 抽查的数据文件 | 缺失 |
| --- | ---: | ---: | ---: |
| `0429_pkl/fz/merged_nuscenes_infos_train.pkl` | 15,228 | 1,024 | 0 |
| `0429_pkl/fz/merged_nuscenes_infos_val.pkl` | 1,620 | 512 | 0 |

这些 pkl 保存的是旧机器路径 `/temp_data/fz/<scene>/...`，发布后的文件位于 `/mnt/storage/public/cnfzhjyg/<scene>/...`。代码改造中的 `DatasetPathResolver` 会自动发现这一级命名空间变化；不要批量重写 pkl，也不要在代码里写死 `fz → cnfzhjyg`。

把仓库中的 [`examples/bevfusion/platform_data_preflight.py`](../examples/bevfusion/platform_data_preflight.py) 作为 `tools/platform_data_preflight.py` 提交到算法分支后，可以在正式训练前读取 train/val 索引并检查 lidar、sweep 与 camera 路径。脚本源码随公开仓库交付，不依赖平台维护机器：

```bash
python3 tools/platform_data_preflight.py \
  --train 0429_pkl/fz/merged_nuscenes_infos_train.pkl \
  --val 0429_pkl/fz/merged_nuscenes_infos_val.pkl \
  --max-train 1024 \
  --max-val 512
```

在平台任务中运行时，选择公共根目录并让脚本从 `PLATFORM_DATASET_PATH` 读取；不要向 Pod 注入 TOS AK/SK。

### 4.2 全量 FZ 的长时 2×8 训练模板

下面模板使用 15,228 条训练样本和 1,620 条验证样本，预计迭代数、运行时间和资源消耗都显著高于 128 样本 smoke。它用于验证长时间跨节点训练、日志、指标和 checkpoint 链路，不等同于算法收敛验收。

在 `bev_3dod` checkout 根目录保存为 `.spk-rayjob.yaml`：

```yaml
name: bevfusion-fz-merged-2x8-long
image: harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553
entrypoint: >-
  raytrain-bevfusion-prepare python3 tools/westwell_train.py
  configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml
  --launcher pytorch
  --run-dir "$PLATFORM_OUTPUT_PATH"
  dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/"
  eval_dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/"
  data.train.dataset.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_train.pkl"
  data.val.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl"
  data.test.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl"
  data.samples_per_gpu=1
  data.workers_per_gpu=1
  optimizer.lr=1.0e-5
  runner.max_epochs=1
  log_config.interval=10
  evaluation.interval=1
workers: 2
gpusPerWorker: 8
cpuPerWorker: 64
memoryPerWorker: 256Gi
executionMode: ray_train
input:
  space: public
output:
  path: acceptance/bevfusion-fz-merged-2x8-long
```

然后提交当前 checkout：

```bash
spk-rayjob submit --name "bevfusion-fz-merged-2x8-long-$(date +%Y%m%d-%H%M%S)" --watch
```

不使用 `.spk-rayjob.yaml` 时，下面是一条等价的完整命令。它必须在已经按本文完成补丁的 `bev_3dod` checkout 根目录执行；代码和配置会随本次任务上传，镜像不会因代码修改而重建：

```bash
RUN_ID="$(date +%Y%m%d-%H%M%S)"

spk-rayjob submit \
  --name "bevfusion-fz-merged-2x8-${RUN_ID}" \
  --image 'harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553' \
  --entrypoint 'raytrain-bevfusion-prepare python3 tools/westwell_train.py configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml --launcher pytorch --run-dir "$PLATFORM_OUTPUT_PATH" dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/" eval_dataset_root="$PLATFORM_DATASET_PATH/0429_pkl/fz/" data.train.dataset.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_train.pkl" data.val.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl" data.test.ann_file="$PLATFORM_DATASET_PATH/0429_pkl/fz/merged_nuscenes_infos_val.pkl" data.samples_per_gpu=1 data.workers_per_gpu=1 optimizer.lr=1.0e-5 runner.max_epochs=1 log_config.interval=20 evaluation.interval=1' \
  --input-space public \
  --output-path "acceptance/manual/bevfusion-fz-merged-2x8-${RUN_ID}" \
  --workers 2 \
  --gpus-per-worker 8 \
  --cpu-per-worker 64 \
  --memory-per-worker 256Gi \
  --execution-mode ray_train \
  --watch
```

入口参数整体使用单引号非常重要：`PLATFORM_DATASET_PATH` 和 `PLATFORM_OUTPUT_PATH` 应在训练 Worker 内展开，不能提前在提交机器上展开。

这里最容易写错的是训练索引层级：`data.train` 是 `CBGSDataset` 包装器，真正的 NuScenes 配置在 `data.train.dataset`，所以必须覆盖 `data.train.dataset.ann_file`。写成 `data.train.ann_file` 不会替换内层默认值，任务会继续读取旧路径并报 `FileNotFoundError`。

公共根目录的 `input.path` 应留空；不要填写 `.`，平台会把它视为不安全路径段。第一轮全量试跑使用原始 cyclic LR，在 1,675/3,585 步时因 Hungarian cost 出现 NaN/Inf 失败。上述验收模板把基础学习率从 `1e-4` 降到 `1e-5`（cyclic 峰值相应从约 `1e-3` 降到约 `1e-4`）；这是数值稳定性参数，不是平台数据路径修复。正式超参数仍由算法负责人根据 loss、mAP 与收敛结果确认。

使用该模板的第二轮任务 `job-6e7c7bb0e7282c61b68ca876` 已完整跑通 3,585 个训练 iteration、验证与 Checkpoint，并以 `returnCodes=[0,0]` 结束。用户复跑时仍应使用唯一任务名，并在改动数据、配置或镜像后重新执行预检。

## 5. 方式一：`spk-rayjob` 提交（当前推荐）

### 5.1 `bev_3dod`

在自己的 `bev_3dod` checkout 根目录按 [多方式提交手册](SUBMIT_GUIDE.md#35-bevfusion-的正确提交) 创建 `.spk-rayjob.yaml`，然后执行 `spk-rayjob submit --watch`。

### 5.2 `bev_3dod_s1h`

`bev_3dod_s1h` 已验证范围是 smoke-128。可以在自己的 checkout 根目录使用 smoke 模板，并增加 S1H 的 `data.val.ann_file`、`data.test.ann_file` 覆盖后执行 `spk-rayjob submit --watch`。

S1H 全量训练不能直接复用 `bev_3dod` 的全量模板：全量数据包含 IGV 第 10 类，而历史 S1H Head 只有 9 类；修正类别数后还观察到 fp16/Hungarian cost/loss 数值不稳定。正式全量前必须由算法负责人确认 `num_classes`、checkpoint 兼容性、学习率和 fp16 策略，并单独完成收敛验收。

每次修改代码后再次执行 `spk-rayjob submit --watch`。客户端重新打包当前目录，任务详情中保留不可变源码包摘要；无需重建镜像。

常用检查：

```bash
spk-rayjob jobs
spk-rayjob status <平台任务ID>
spk-rayjob logs -f <平台任务ID>

# BEVFusion 首次打印模型结构较长；历史任务建议扩大查询行数
spk-rayjob logs --limit 3000 <平台任务ID>
```

任务结束后在“我的数据 → 我的运行结果”查看：

```text
configs.yaml
epoch_1.pth
latest.pth                 # 某些存储浏览器把它显示为链接
raytrain-topology.json
```

## 6. 方式二：原生 `ray job submit`

原生 Ray 2.35 CLI 已完成 2×8 BEVFusion 和 1×1 公共数据读写验收。当前生产入口返回 Jobs API 协议版本 `4`，网关会根据资源形状自动填写 `single_gpu / torchrun / ray_train`。IDC Nginx Ingress 必须配置 `nginx.ingress.kubernetes.io/proxy-body-size: "2g"`；未配置时普通页面与 `/healthz` 正常，但较大的 `--working-dir` 会返回 413。

下面命令用于需要官方 Ray CLI 的自动化。PAT 必须从环境变量传入，不能写进脚本或 shell history：

```bash
export RAY_ADDRESS=https://raytrain.wellspiking.ai/ray
read -rsp '个人访问令牌: ' RAYTRAIN_PAT
printf '\n'
export RAY_JOB_HEADERS="$(jq -cn --arg token "$RAYTRAIN_PAT" '{Authorization:("Bearer "+$token)}')"
unset RAYTRAIN_PAT

IMAGE='harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553'
META=$(jq -cn --arg image "$IMAGE" '{
  "ray-platform.image":$image,
  "ray-platform.worker-replicas":"2",
  "ray-platform.gpus-per-worker":"8",
  "ray-platform.cpu-per-worker":"64",
  "ray-platform.memory-per-worker":"256Gi",
  "ray-platform.queue":"local-gpu"
}')

ray job submit \
  --submission-id bev3dod-native-001 \
  --working-dir . \
  --runtime-env-json '{"excludes":[".git/","assets/","auto_report_system/","*.pkl","*.pth","tools/points.pcd","tools/points_intensity.txt","official_metrics.txt"]}' \
  --metadata-json "$META" \
  -- \
  env PLATFORM_DATASET_PATH=/mnt/storage/public/bevfusion/fz-3dod-v1 \
      PLATFORM_OUTPUT_PATH=/mnt/storage/me/runs/bev3dod-native-001 \
  raytrain-bevfusion-prepare python3 tools/westwell_train.py \
  configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml \
  --launcher pytorch \
  --run-dir /mnt/storage/me/runs/bev3dod-native-001 \
  dataset_root=/mnt/storage/public/bevfusion/fz-3dod-v1/platform-validation/annotations/fz-0429-platform-smoke-128/ \
  eval_dataset_root=/mnt/storage/public/bevfusion/fz-3dod-v1/platform-validation/annotations/fz-0429-platform-smoke-128/ \
  data.samples_per_gpu=1 data.workers_per_gpu=1 runner.max_epochs=1 \
  log_config.interval=1 evaluation.interval=1
```

元数据中的 `2 × 8` 会被网关解析为 `ray_train`，平台自动增加跨节点 `raytrain-launch`。不要在原生 CLI 的 entrypoint 里再写一层 `raytrain-launch` 或 `torchrun`。

原生提交会出现在平台任务列表，`submissionOrigin=ray-cli`，提交人是 PAT 对应的平台用户。`ray job logs` 的网关查询上限可能被长模型结构填满，末尾 loss 请从平台日志页或 `spk-rayjob logs --limit 3000 <平台任务ID>` 查看。

## 7. 方式三：网页提交

网页中使用相同模板参数：

1. 代码来源选择当前调试工作区快照，或固定 Git commit。
2. 镜像选择上述 BEVFusion 不可变 digest。
3. 执行方式选择“多机多卡 Ray Train”，Worker `2`，每 Worker GPU `8`，CPU `64`，内存 `256Gi`。
4. 输入选择“公共数据 / `bevfusion/fz-3dod-v1`”。
5. 命令填写模板中的普通训练命令，不要自己写 `torchrun`。
6. 输出选择个人运行结果目录。

网页、`spk-rayjob` 和原生 Ray CLI 最终都会创建同一类平台任务与 RayJob；区别只是参数录入和工作目录上传方式。

## 8. checkpoint 与断点续训

BEVFusion 将 checkpoint 写入 `$PLATFORM_OUTPUT_PATH`。续训必须创建新任务并显式读取旧结果：

```bash
spk-rayjob submit \
  --resume-from-job <上一次的平台任务ID> \
  --entrypoint 'raytrain-bevfusion-prepare python3 tools/westwell_train.py ... resume_from="$PLATFORM_CHECKPOINT_PATH/epoch_1.pth" --run-dir "$PLATFORM_OUTPUT_PATH"' \
  --watch
```

平台把旧任务结果以只读 `PLATFORM_CHECKPOINT_PATH` 挂载，新任务写入新的 `PLATFORM_OUTPUT_PATH`。页面“重试”只是重新执行，并不自动选择 checkpoint；只有训练命令显式传入 `resume_from` 才是断点续训。

## 9. 其他训练代码如何接入

新项目至少满足以下契约：

- 数据根目录从 `PLATFORM_DATASET_PATH` 读取。
- checkpoint、模型和业务结果写入 `PLATFORM_OUTPUT_PATH`。
- 不写死 rank、world size、GPU 编号和主机地址。
- 接受 `torchrun` 注入的 `LOCAL_RANK`、`RANK`、`WORLD_SIZE`。
- 只让 rank 0 写共享文件；各 rank 独立文件必须带 rank 后缀。
- 日志写 stdout，避免在对象存储上追加同一个日志文件。
- 先 1×1，再 1×8，最后 2×8；每一档都检查 loss、GPU 利用率和 checkpoint。

如果代码已经支持标准 PyTorch DDP，通常无需引入 Ray SDK；平台的 `raytrain-launch` 会负责 Ray placement group、跨节点 rendezvous 和每节点 `torchrun`。

## 10. 发布前剩余事项

平台分布式链路与两份 BEVFusion smoke 已通过。对外宣称“正式模型训练可交付”前还要完成：

- 将两个 checkout 的平台补丁提交到内部 Git 分支，不能只保留未提交工作树。
- 用正式全量 train/val 数据跑预定 epoch，验收 loss/mAP、梯度、吞吐与 resume。
- 将兼容镜像升级为在 RTX 4090/CUDA 11.8+ 环境重新编译扩展的生产镜像。
- 原生 Ray API 协议、执行模式推导和 IDC Ingress 上传大小已修复；日常训练仍推荐使用数据和断点治理更完整的 `spk-rayjob`。
- 所有上线镜像、代码和 Helm revision 关联到 Git commit/tag，保证可回滚。
