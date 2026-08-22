# RayTrain 用户使用手册

本手册面向算法工程师。浏览器入口是 [https://raytrain.wellspiking.ai](https://raytrain.wellspiking.ai)；管理员操作看 [管理员手册](ADMIN_GUIDE.md)。运行现有 BEVFusion 分支时使用 [BEVFusion 代码改造与验收](BEVFUSION_CODE_CHANGES.md)；接入其他代码使用 [新训练代码接入手册](NEW_TRAINING_CODE_GUIDE.md)。

## 日常最短路径

代码和训练镜像彼此独立。日常开发不需要反复构建镜像，按下面一条链路循环即可：

```text
改代码
→ spk-rayjob submit --watch
→ spk-rayjob logs -f <任务ID>
→ 平台实验中心看本任务 Loss/指标
→ 可选：打开原生 MLflow 做全局对比或管理
→ 断点续跑/重提
```

第一次接入项目时才需要完成数据路径、分布式入口和镜像兼容性改造；之后每次 `submit` 都会重新打包当前工作目录。

## 先记住这一条主流程

```text
准备数据 → 启动 GPU 调试环境 → 验证代码/环境 → 创建代码快照或 spk-rayjob 打包
→ 选择镜像、GPU、输入、输出 → 提交 RayJob → 看日志/指标 → 查看 checkpoint 与产物
```

平台不要求、也不会向你发放 TOS AK/SK、PVC 名称或 Kubernetes kubeconfig。你只在页面中选择逻辑数据目录，代码只使用平台提供的本地路径与环境变量。

### 第一次使用：15 分钟跑通一条可追溯链路

1. 在 **我的数据** 确认输入数据的具体子目录，并在 **交互式调试** 中确认该目录可读、个人工作区可写。
2. 用同一份镜像和代码提交最小可运行规模；普通 PyTorch 先用 `1 × 1 GPU`，强制初始化 DDP 的旧项目应从 `1 × 2 GPU` 的 `torchrun` 档案开始。输入使用 `PLATFORM_DATASET_PATH`，输出使用 `PLATFORM_OUTPUT_PATH`。
3. 在任务详情查看提交器、Ray head、worker 三类日志；确认 stdout 中有 loss 或首个业务输出，并在“我的运行结果”看到产物。
4. 代码已支持 PyTorch DDP 时，再选择“单机多卡 DDP”；单机通过后，才选择“多机多卡 Ray Train”。

不要从 TOS 根目录猜数据位置，也不要把 TOS URI 写进训练命令。页面选择的子目录和三项环境变量才是训练代码与存储的稳定契约。

## 1. 数据从哪里读、写到哪里

### 数据空间与容器路径

| 页面中的名称 | 调试 / 训练 Pod 中的路径 | Pod 挂载权限 | Portal 发布 / 写入主体 |
| --- | --- | --- | --- |
| 我的工作区 | `/workspace` | 读写 | 本人 |
| 我的文件 | `/mnt/storage/me/files` | 读写 | 本人 |
| 我的运行结果 | `/mnt/storage/me/runs` | 读写 | 本人（训练输出只能写这里） |
| 团队共享数据 | `/mnt/storage/team` | 始终只读 | TenantAdmin 可在页面上传、覆盖和建目录 |
| 公共数据 | `/mnt/storage/public` | 始终只读 | SuperAdmin 可在页面发布 |
| IDC 数据（管理员已登记时） | `/mnt/idc/*` | 只读 | IDC 数据管理员 |

公共数据在调试和训练 Pod 中稳定对应 `/mnt/storage/public`（只读）。用户始终只选“公共数据 + 子目录”，不需要知道底层 bucket 或物理前缀，也不应在代码中写死 TOS URI。

当前公共数据的唯一根目录是 `/mnt/storage/public`。其中 `labeled/` 是持续同步的全量原始数据区；`0429_pkl/`、`0813_pkl/` 等目录保存训练索引；`bevfusion/<版本>/` 保存平台验收用的版本化小样本。**索引文件决定一次训练实际读取哪些样本，看到 `labeled/` 很大不代表任务会自动读取全部目录。**

`/mnt/data/input` 是当前任务所选输入的只读别名。选择公共根时，它与 `/mnt/storage/public` 指向同一个卷和同一个子目录，不会复制数据；选择公共空间中的某个子目录时，它只指向该子目录。调试时可浏览 `/mnt/storage/public`，训练代码应读取 `PLATFORM_DATASET_PATH`。完整关系和读取性能测试见[数据路径与读取基准](DATA_IO_BENCHMARK.md)。

路径换算只有一条规则：代码里的相对路径以 `PLATFORM_DATASET_PATH` 为起点。例如页面选择公共根时，`PLATFORM_DATASET_PATH/0429_pkl/...` 有效；页面选择 `bevfusion/fz-3dod-v1` 时，应写 `PLATFORM_DATASET_PATH/platform-validation/...`，不能再写 `PLATFORM_DATASET_PATH/bevfusion/fz-3dod-v1/...`。

创建任务时你选择的是“空间 + 子目录”，平台还会为本次任务建立三个固定路径：

| 环境变量 | 含义 | 权限 |
| --- | --- | --- |
| `PLATFORM_DATASET_PATH` | 你选择的训练输入子目录 | 只读 |
| `PLATFORM_CHECKPOINT_PATH` | 你选择的基础模型或断点子目录 | 只读 |
| `PLATFORM_OUTPUT_PATH` | 本任务独占的结果目录 | 读写 |

不要在代码中写死 `tos://` 地址。一次任务可在不同节点运行，只有上述环境变量是稳定接口。

### 我如何看到自己的数据

1. 打开 **我的数据**，选择“我的文件”上传文件、建目录或浏览已有文件。网页只允许上传和目录管理，不提供数据下载。
2. 用 **我的数据 → 我的运行结果** 浏览训练产物、checkpoint、日志导出文件和指标文件。
3. 在调试环境中使用 `ls /mnt/storage/me`、`ls /mnt/storage/public` 确认挂载。对象存储目录首次列举可能比本地磁盘慢，这是 FUSE/对象存储按需列举的正常特性；避免在根目录对整个桶执行 `find` 或 `df -h`。

### 共享数据如何写入

团队/公共目录在训练 Pod 中永久只读，避免任意训练脚本污染公共数据。需要发布数据时：

- 团队数据：由 TenantAdmin 在“我的数据”中将已验证的数据发布到团队空间。
- 公共数据：由 SuperAdmin 发布。
- 个人数据：上传到“我的文件”，或将已有数据复制到自己的个人 TOS 空间。

当前生产入口没有开放用户自助 IDC ↔ TOS 双向迁移。个人 IDC 数据可在管理员登记并完成密钥验证后作为受控迁移任务接入；在功能正式开放前，不要把个人 SSH 私钥放进训练镜像、Notebook 或 TOS。

## 2. 如何调试代码

1. 打开 **交互式调试**，选择与最终训练相同的镜像。
2. 启动单卡 GPU Workspace，等状态变为“运行中”。
3. 从页面打开 JupyterLab、VS Code 或 Terminal。它们连接的是带 GPU 的 Ray worker，不是 Ray head。
4. 在 `/workspace` 编辑代码；在 `/mnt/storage/*` 查看数据。工作区、个人数据和工作区中的 `.venv` 都会保留；调试 Pod 本身不是永久实例。
5. 用完点击 **停止工作区**，立即释放 GPU。

最小 GPU 自检：

```bash
nvidia-smi
python - <<'PY'
import torch
print(torch.__version__, torch.cuda.is_available(), torch.cuda.get_device_name(0))
PY
ls -la /mnt/storage/public
```

### pip 与 apt 的边界

- `pip install --user` 或在 `/workspace/.venv` 创建虚拟环境，适合临时调试，重启后仍可使用工作区内的环境。
- 容器以非 root 用户运行，不能 `apt install` 或修改系统 Python。
- 需要 CUDA、系统库、编译器或可复现团队环境时，使用 Dockerfile 构建镜像并推送 Harbor；让 TenantAdmin 将**固定 digest**登记进镜像目录。不要把“运行时 apt 安装”当作生产训练方案。

## 3. 从调试代码提交训练任务

### 页面提交（推荐的第一次使用方式）

1. 在调试环境验证命令与数据。
2. 在 **我的数据 → 我的工作区** 创建不可变代码快照。
3. 打开 **我的训练任务 → 新建训练任务**：
   - 选择已登记的训练镜像；
   - 代码来源选择“调试快照”，或 Git + 固定 commit SHA；
   - 选择 Worker 数与每 Worker GPU 数；
   - 填训练命令；
   - 在“数据与产物”选择输入目录、可选 checkpoint、输出目录。
4. 提交后等待 Kueue 准入。页面每 5 秒同步状态，无需刷新浏览器。

任务状态依次可能是：**排队中 → 启动中 → 运行中 → 已成功 / 失败 / 已取消**。排队中表示等待配额，启动中可能在拉镜像、物化代码或创建 RayCluster，尚未执行你的训练命令。

### 训练命令怎样写

命令从提交的代码根目录执行。读写位置一律使用环境变量，例如：

```bash
python train.py \
  --data-root "$PLATFORM_DATASET_PATH" \
  --work-dir "$PLATFORM_OUTPUT_PATH"
```

输出目录由平台按任务隔离；不要写到容器本地的 `/tmp`、`/root`、`/data1` 或 `/data2`。这些位置不可保证跨重试、跨节点存在。

### 如何选择运行规模

| 页面选择 | 资源 | 实际执行方式 | 适用场景 |
| --- | --- | --- | --- |
| 单卡 | 1 节点 × 1 GPU | 命令在一张 Ray GPU worker 上运行 | 第一次验证代码、数据、日志。 |
| 单机多卡 DDP | 1 节点 × 2/4/8 GPU | worker 内执行 `torchrun --nproc_per_node=N` | 已有 PyTorch/MMCV DDP 代码。 |
| 多机多卡 Ray Train | 2 节点 × N GPU | Ray 严格跨主机放置，再在每个节点执行 `torchrun` | 跨节点训练与 NCCL 验收。 |

平台执行档案生成的 `raytrain-topology.json` 会显示实际主机、node rank、world size、GPU 数和各节点返回码；通用 DDP demo 还会生成 `ddp-rank-*.json`。这比只看申请 GPU 数更能证明分布式训练真正启动。具体提交命令见 [多方式提交手册](SUBMIT_GUIDE.md)。

提交命令仍按普通单进程方式填写，例如 `python train.py ...`；平台会按所选执行档案调用 `torchrun`。因此训练代码本身必须支持 `torch.distributed` / DDP，且不能在脚本中把 world size、rank 或 GPU 编号写死。通用改造见 [新训练代码接入手册](NEW_TRAINING_CODE_GUIDE.md)，BEVFusion 实例见 [BEVFusion 代码改造与验收](BEVFUSION_CODE_CHANGES.md)。

单卡和单机多卡的命令已在平台预留的 GPU worker 中执行。不要在单卡脚本里再用 `@ray.remote(num_gpus=1)` 申请同一张卡，否则外层任务占有 GPU、内层任务等待 GPU，会形成资源自锁。普通 PyTorch/DDP 训练代码不需要自己调用 Ray SDK。

## 4. 日志、GPU 指标和数据分布

### 日志

- 在 **我的训练任务 → 任务详情 → 日志** 查看平台聚合的 Ray submitter、head、worker 日志。
- 终端同样可以执行 `spk-rayjob logs -f <任务ID>`；BEVFusion 会先打印很长的模型结构，查询历史日志时使用 `spk-rayjob logs --limit 3000 <任务ID>` 才能看到后面的 loss。特别大的输出优先使用实时跟随或 Portal 日志流，避免一次拉取整段模型结构。
- 训练脚本应把关键指标写入 stdout，例如 epoch、loss、学习率、吞吐量和 checkpoint 路径。这样 Loki 中可检索，页面也能显示。

一个 2 节点 × 8 GPU 任务通常只有 4 个 Pod 日志流，并不是 16 个：每个 Worker Pod 内部承载 8 个训练 rank。页面现在按用途显示名称：

| 日志流 | 含义 | 出问题时看什么 |
| --- | --- | --- |
| 全部训练日志 | 合并下面所有流，适合搜索 job 全局错误 | 按时间看完整启动与退出顺序 |
| 任务提交器 | KubeRay 提交进程，负责提交入口命令并上报最终状态 | working-dir、入口命令、Ray Job 提交失败 |
| Ray Head | Ray 控制面、GCS、调度器和 Dashboard | Worker 注册、资源调度、Ray/NCCL 控制面异常 |
| 训练 Worker 1/2/... | 真正执行用户训练代码的 GPU Pod；同一流里可能交错多个 local rank | loss、CUDA OOM、数据读取、各 rank traceback |

先看“全部训练日志”定位时间点，再切到对应 Pod。业务训练问题通常看 Worker，集群注册或 Dashboard 问题看 Head，代码包与入口问题看提交器。

### Ray Dashboard

任务被 Kueue 准入并创建 RayCluster 后，任务详情会显示“Ray Dashboard”。它通过平台的短时授权和同源代理访问，只允许任务所有者使用；Ray Head 的 8265 端口保持 ClusterIP，不开放 NodePort。

Dashboard 用于查看运行中的 Ray node、task、actor、object store 和资源分配。成功任务终态后，RayCluster 默认在 60 秒内清理并释放 GPU；失败任务默认保留 600 秒原生排障窗口。RayCluster 清理后 Dashboard 随之消失，历史日志、GPU 指标和产物继续从平台查看。因此不要把 Dashboard 当作日志保留系统。

常见日志信号：

| 现象 | 首先看什么 |
| --- | --- |
| 一直排队 | 团队 GPU 配额、是否有未停止的 Workspace。 |
| 启动失败 | 镜像拉取、Git/代码包物化、挂载状态。 |
| `CUDA out of memory` | batch size、输入分辨率、梯度累积、显存曲线。 |
| NCCL 超时 | 所有 worker 是否已启动、网卡/拓扑、rank 数是否与资源一致。 |
| 数据加载慢 | `PLATFORM_DATASET_PATH` 是否选对、输入路径是否含大量小文件、节点本地缓存是否已启用。 |

### 指标与 loss

任务详情页显示任务状态与运行时 Pod；Grafana 用于 GPU 利用率、显存、CPU、网络与节点健康。推荐打开两个窗口：

1. Portal 日志页：看训练脚本输出的 `loss`、`lr`、`mAP` 等业务指标。
2. Grafana：看 GPU 是否真正忙、显存是否增长、是否发生 OOM 或某个 worker 掉线。

平台不会自动篡改你的学习率、batch size 或模型参数。基于日志做判断，修改 config/命令后创建新任务，旧任务和 checkpoint 保持可追溯。

### MLflow 实验中心与原生管理界面

实验中心是平台筛选视图，用于按当前用户或团队查看任务参数、Loss、学习率、吞吐和 mAP/NDS。它适合日常训练排障，并保留平台任务、提交人、租户和训练指标之间的关联。

实验中心中的按钮 **打开 MLflow 管理界面** 会在新标签页打开同域 `https://raytrain.wellspiking.ai/mlflow/`。原生 MLflow 是登录后可访问的完整管理界面，展示全平台实验。所有平台认证用户都可以创建、修改、删除实验、Run 和模型注册条目，并可上传、下载 MLflow Artifact。原生 MLflow 全功能开放是当前明确策略；这些操作直接改变共享 MLflow 数据，删除或修改前应确认目标对象。

Ray Dashboard 与两种 MLflow 视图的生命周期不同：Ray Dashboard 随任务 RayCluster 回收；实验中心和原生 MLflow 的运行记录长期保留。

当前租户默认训练环境是平台镜像目录中的 `BEVFusion CUDA 11.3 + MLflow`，固定为 `PyTorch 1.10.1 / CUDA 11.3 / Ray 2.10 / MLflow client 2.17.2`。MLflow client 采用 2.17.2 是为了兼容该镜像的 Python 3.8；平台 Tracking Server 为 3.14，标准 Tracking API 已完成兼容验证。自定义镜像若要使用实验中心，也必须预装与自身 Python 版本兼容的 `mlflow-skinny`，不要在每次任务启动时临时安装。

平台会向每个训练任务注入 MLflow 地址、实验名、任务名和由控制面签发的来源标记。训练代码可以读取这些值，但篡改后不会通过实验中心的任务归属校验；它们不应被当作用户不可见的秘密。训练代码只需在 rank 0 使用这些环境变量，不要把服务地址、租户或任务 ID 写死：

```python
import os
import mlflow

if int(os.getenv("RANK", "0")) == 0 and os.getenv("MLFLOW_TRACKING_URI"):
    mlflow.set_tracking_uri(os.environ["MLFLOW_TRACKING_URI"])
    mlflow.set_experiment(os.environ["MLFLOW_EXPERIMENT_NAME"])
    mlflow.start_run(
        run_name=os.environ["MLFLOW_RUN_NAME"],
        tags={
            "platform.job_id": os.environ["RAYTRAIN_JOB_ID"],
            "platform.tenant_id": os.environ["RAYTRAIN_TENANT_ID"],
            "platform.submitter_user_id": os.environ["RAYTRAIN_SUBMITTER_USER_ID"],
            "platform.provenance": os.environ["RAYTRAIN_MLFLOW_PROVENANCE"],
        },
    )
    mlflow.log_params({"batch_size": batch_size, "learning_rate": learning_rate})

# 在训练循环的 rank 0 中调用：
if int(os.getenv("RANK", "0")) == 0 and mlflow.active_run():
    mlflow.log_metrics({"loss": float(loss), "lr": float(current_lr)}, step=global_step)

# 训练与验证结束后：
if int(os.getenv("RANK", "0")) == 0 and mlflow.active_run():
    mlflow.log_metrics({"mAP": float(mAP), "NDS": float(nds)})
    mlflow.end_run(status="FINISHED")
```

训练代码默认只把标量参数和指标写入 MLflow。模型、Checkpoint、配置快照和正式训练结果仍应写入 `PLATFORM_OUTPUT_PATH`，再从“我的运行结果”查看；普通训练 Pod 的 MLflow 写入网关不提供 Artifact 下载能力。

MLflow Artifact 与 `/mnt/storage/public` 治理数据隔离。Artifact 底层存放在 `vke-cluster/ray-train/platform/mlflow-artifacts/` 专用前缀，由 FSX CSI 静态 PV/PVC 只向 MLflow 发布为 `/mlflow-artifacts`。MLflow Pod 只看到 `/mlflow-artifacts` 挂载根，不注入 TOS/AWS AK/SK；FSX CSI 的 `fsx-agent` 使用平台 Secret `ray-train-platform/tos-fsx-credentials`，但凭据不进入 MLflow Pod。

原生界面允许上传、下载 MLflow Artifact，不等于允许下载公共或团队训练数据，也不会把受治理的输入变成可下载对象。Artifact 仅用于明确上传到 MLflow 的实验附件；受治理的训练输入继续通过只读挂载访问。完整可运行示例见仓库 `examples/mlflow/train.py`。

### 数据分布

数据页面和挂载路径可回答“数据在哪里”：

- **数据真相**：个人、团队、公共数据在 TOS；IDC 数据在管理员登记的只读导出。
- **训练读取**：所有 Ray worker 都读取相同的受控输入路径。
- **本地 NVMe**：GPU 节点的 `/data1`、`/data2` 只用于可丢弃缓存。当前集群没有 `ray-cache-local` StorageClass，生产 Profile 中 `training.localCache.enabled=false`，所以本地 NVMe **尚未对训练生效**。启用后，每个 Ray head/worker 获得一个调度到本节点的临时 PVC，挂载为 `/mnt/cache`；Ray session 和 object spilling 写入该目录，Pod 删除后 PVC 一并回收。数据真相仍在 TOS/IDC，checkpoint 和产物仍必须写 `PLATFORM_OUTPUT_PATH`。
- **结果**：每次训练固定写入个人 `my-runs`，在页面与调试环境中都能看到。

## 5. 集群外开发机如何提交

外部开发服务器、个人 8 卡调试机或笔记本只需要访问 `raytrain.wellspiking.ai`，不需要 kubeconfig 和 TOS 凭据。Linux、macOS、Windows 的当前客户端下载与校验命令以 Portal“外部提交”页面为准，也可查看[三种提交方式](SUBMIT_GUIDE.md#31-安装与登录)。

### 安装与登录

```bash
mkdir -p ~/.local/bin ~/.cache/spk-rayjob
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-linux-amd64 \
  -o ~/.cache/spk-rayjob/spk-rayjob-linux-amd64
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS \
  -o ~/.cache/spk-rayjob/SHA256SUMS
(cd ~/.cache/spk-rayjob && grep 'spk-rayjob-linux-amd64$' SHA256SUMS | sha256sum -c -)
install -m 0755 ~/.cache/spk-rayjob/spk-rayjob-linux-amd64 ~/.local/bin/spk-rayjob
export PATH="$HOME/.local/bin:$PATH"

read -rp '平台用户名: ' RAY_PLATFORM_USERNAME
read -rs RAY_PLATFORM_PASSWORD && echo
printf '%s\n' "$RAY_PLATFORM_PASSWORD" | spk-rayjob login \
  --server https://raytrain.wellspiking.ai \
  --username "$RAY_PLATFORM_USERNAME" --password-stdin
unset RAY_PLATFORM_USERNAME RAY_PLATFORM_PASSWORD
```

网页登录和 spk-rayjob 登录使用同一个本地账号；SSO/自动化可使用在“账户与安全”创建的 PAT，并将 `--password-stdin` 替换为 `--token-stdin`。配置文件为仅当前用户可读，密码不会进入 shell 历史。

### 把当前修改提交为不可变代码版本

```bash
cd ~/my-training-project
spk-rayjob submit --dir . --name experiment-001 \
  --image 'harbor.wellspiking.ai/<你的项目>/<镜像>@sha256:<digest>' \
  --entrypoint 'python train.py --data-root "$PLATFORM_DATASET_PATH" --work-dir "$PLATFORM_OUTPUT_PATH"' \
  --workers 1 --gpus-per-worker 1 --cpu-per-worker 8 --memory-per-worker 32Gi \
  --input-space public --input-path '<公共数据的子目录>' \
  --output-path experiment-001
```

默认会根据资源自动选择执行模式；自动化脚本建议明确声明：

```bash
# 一个节点内的 8 卡 DDP
spk-rayjob submit ... --execution-mode torchrun --workers 1 --gpus-per-worker 8

# 两台 8 卡节点
spk-rayjob submit ... --execution-mode ray_train --workers 2 --gpus-per-worker 8
```

`spk-rayjob` 会按 `.gitignore` 与可选的 `.rayignore` 打包当前目录，忽略 `.git`，上传为不可变源码包，再创建与页面相同的 RayJob。每次修改代码后重新执行提交即可；任务会立刻出现在同一账号的“我的训练任务”中。

日常推荐 `spk-rayjob`，因为它提供数据选择、输出隔离、执行档案和断点续训。原生 Ray 2.35 CLI 的协议链路已独立验收，协议版本为 `4`：无 metadata 时走平台默认 1×1，只有传入完整的 `ray-platform.*` metadata 才会按 1×N、N×M 推导执行模式。原生 CLI 没有平台的逻辑数据选择参数，使用者还必须明确传入容器内的受控路径；2026-08-22 的两个 BEVFusion 分支、三种入口共六个 fresh-clone 任务均已通过，完整多卡示例见 [提交指南](SUBMIT_GUIDE.md)。

### 5.1 把提交默认值写进仓库（推荐）

每次提交都重敲镜像 digest、GPU 数和数据路径既费事又容易写错。在代码目录执行一次 `init`，把默认值提交进仓库，之后 `submit` 不再需要任何参数：

```bash
cd ~/my-training-project
spk-rayjob init --name my-training --gpus-per-worker 8
$EDITOR .spk-rayjob.yaml
```

```yaml
name: bevfusion-lidar
image: harbor.wellspiking.ai/<项目>/<镜像>@sha256:<digest>
entrypoint: python tools/westwell_train.py configs/lidar.yaml --launcher pytorch
workers: 1
gpusPerWorker: 8
cpuPerWorker: 64
memoryPerWorker: 256Gi
executionMode: torchrun      # single_gpu | torchrun | ray_train
input:
  space: public
  path: bevfusion/2026-08-0429
output:
  path: bevfusion-lidar
```

日常循环就变成：

```bash
vim tools/westwell_train.py    # 改代码
spk-rayjob submit --watch      # 提交并等待结束
spk-rayjob logs -f <任务ID>    # 跟随日志
```

单次运行想临时改参数，直接加参数覆盖即可，不必改文件：`spk-rayjob submit --gpus-per-worker 1 --name quick-check`。

### 5.2 `entrypoint` 里不要自己写 torchrun

平台会根据 `executionMode` 和 GPU 数自动执行 `torchrun`，并把命令放到真正预留了 GPU 的 worker 上。你只需要写普通的 Python 命令：

| 你写的 | 平台实际执行 |
| --- | --- |
| `python tools/train.py cfg.yaml` （单卡） | `python tools/train.py cfg.yaml` |
| `python tools/train.py cfg.yaml` （单机 8 卡） | `torchrun --standalone --nproc_per_node=8 tools/train.py cfg.yaml` |
| `python tools/train.py cfg.yaml` （2 节点 × 8 卡） | 每节点 `torchrun --nnodes=2 --nproc_per_node=8 --node_rank=N ...` |

自己再写一层 `torchrun`、`torch.distributed.launch` 或 `torchpack dist-run` 会导致重复包装、rendezvous 失败或直接起不来。网页提交表单和 `spk-rayjob` 会显示警告，页面上的「平台实际执行」会显示展开后的完整命令；不要依赖警告替代提交前检查。

### 5.3 常用命令

| 命令 | 用途 |
| --- | --- |
| `spk-rayjob submit --watch` | 提交并阻塞显示 排队 → 运行 → 结束 |
| `spk-rayjob submit --resume-from-job <ID>` | 把上一次运行的结果目录作为只读 checkpoint 续训 |
| `spk-rayjob jobs --state RUNNING` | 按状态列出任务 |
| `spk-rayjob status <ID>` | 查看单个任务的状态、规模与结果目录 |
| `spk-rayjob logs -f <ID>` | 实时跟随日志，任务结束自动退出 |
| `spk-rayjob cancel <ID>` | 停止任务 |
| `<任意命令> --output json` | 输出原始 JSON，供脚本解析（默认是可读文本） |

> 该客户端原名 `rayctl`。旧的下载地址仍然可用，但请改用新命令名 `spk-rayjob`。

## 6. Checkpoint 与断点续跑

训练代码必须把 checkpoint 写入 `PLATFORM_OUTPUT_PATH`，例如：

```python
checkpoint = os.path.join(os.environ["PLATFORM_OUTPUT_PATH"], "checkpoints", "epoch_001.pth")
```

恢复时，在新任务中选择“初始 Checkpoint”，平台将它只读挂载到 `PLATFORM_CHECKPOINT_PATH`。命令显式传入恢复参数：

```bash
python train.py \
  --data-root "$PLATFORM_DATASET_PATH" \
  --resume "$PLATFORM_CHECKPOINT_PATH/latest.pth" \
  --work-dir "$PLATFORM_OUTPUT_PATH"
```

页面上的「提交失败重试」只会重新创建一次 Ray 提交流程（覆盖镜像拉取、节点中断这类提交期故障），会**从头重跑**，**不会自动猜测哪个 checkpoint 可恢复**。只有训练脚本支持 resume 且你显式选择 checkpoint，才是可靠的断点续跑。

续训有三个入口，效果相同：

| 入口 | 操作 |
| --- | --- |
| 任务列表 | 终态任务的「续训」按钮 |
| 任务详情 | 「续训（从此结果继续）」 |
| 命令行 | `spk-rayjob submit --resume-from-job <上一次的任务ID>` |

三者都会把**上一次运行自己的结果目录**（`我的训练结果/<路径>/<任务ID>`）作为只读 checkpoint 挂到新任务的 `PLATFORM_CHECKPOINT_PATH`。这是一个新任务，不会修改原任务。

## 7. 训练前检查清单

- [ ] 镜像是平台已登记的不可变 digest，且在调试环境中已通过 `import`、CUDA 与训练入口自检。
- [ ] 代码来源是固定 Git commit、工作区快照或 spk-rayjob 代码包。
- [ ] 输入空间与子目录正确；训练脚本读取 `PLATFORM_DATASET_PATH`。
- [ ] 训练结果与 checkpoint 写入 `PLATFORM_OUTPUT_PATH`。
- [ ] 先用 1 GPU、最小 batch 跑通一个 step，再扩大到多卡/多节点。
- [ ] 观察 stdout loss 与 Grafana GPU 曲线，确认每张卡都在工作。
- [ ] 调试 Workspace 用完已停止。

## 常见问题

**为什么 `df`、`ls` 在 TOS 目录下感觉慢？**
TOS 是对象存储，经 CSI/FSX 以文件系统语义呈现；目录遍历可能触发对象列举。对具体子目录操作，避免在根目录递归扫描全桶。

**我能在公共数据目录写入吗？**
不能。公共与团队空间在 Pod 中只读；请按发布流程交给有权限的管理员。

**我在调试环境里安装了一个包，训练任务里为什么没有？**
工作区内的虚拟环境可复用，但训练镜像不会自动继承调试 Pod 的系统改动。对可复现训练，应将依赖写入镜像并选择相同 digest。

**任务完成后我在哪里看到文件？**
在“我的数据 → 我的运行结果”，或在新调试环境的 `/mnt/storage/me/runs`。平台不提供数据下载入口。

**修改代码后需要重新构建镜像吗？**
不需要。镜像只在 CUDA、PyTorch、Ray、系统库或 Python 基础依赖变化时重建。普通 Python/config 修改直接用 `spk-rayjob submit --dir .` 或 `ray job submit --working-dir .`。

**“重试”和“断点续训”有什么区别？**
重试从头重跑；断点续训会创建新任务，把旧任务结果只读挂载到 `PLATFORM_CHECKPOINT_PATH`，并要求训练代码显式使用 `--resume`。

**任务一直是“排队中”怎么办？**
检查租户 GPU 配额、当前正在运行的任务和未停止的调试环境。Kueue 只有在同时满足 GPU、CPU 和内存后才准入任务。

**如何确认训练真的用了 GPU 和选中的数据？**
先用 1×1 小任务在 stdout 打印 `torch.cuda.get_device_name(0)` 和一个明确标注文件的 `stat`，再在任务详情核对 GPU 指标与输出目录。不要为了验收而对整个 TOS 根目录执行递归 `find/rglob`。
