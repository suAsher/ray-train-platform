# 训练任务提交指南：网页、spk-rayjob、原生 Ray CLI

> 适用平台：`https://raytrain.wellspiking.ai`
> 文档版本：2026-08-22
> 用户只需平台账号、本文和自己的代码目录。

本文从一份可以在本机运行的原始训练代码开始，说明如何完成代码改造、选择数据和镜像、提交任务、查看 RayCluster、日志、Dashboard 与产物。`--engine ray-ddp` 是现有 PyTorch/DDP 兼容引擎，`--engine ray-train` 是受门禁保护的 Ray Train 托管引擎；后者的 Python 入口、MMCV、checkpoint/resume 和性能契约见 [Ray Train 托管指南](RAY_TRAIN_MANAGED_GUIDE.md)。

## 1. 先选择一种入口

| 入口 | 最适合谁 | 代码如何进入集群 | 数据如何选择 |
| --- | --- | --- | --- |
| 网页 | 第一次使用、需要可视化选数据的人 | 固定 Git commit 或调试工作区快照 | 页面目录树 |
| `spk-rayjob` | 频繁修改本地代码的算法工程师，**日常推荐** | 每次自动打包当前工作目录 | `--input-space` + `--input-path` |
| 原生 `ray job submit` | 已有 Ray 自动化脚本的用户 | Ray `--working-dir .` | 命令中使用平台挂载路径 |

三种入口只在“如何收集参数和上传代码”上不同，进入平台后是同一条链路：

```text
用户提交 → 平台任务记录 → Kueue 排队/准入 → KubeRay RayJob
         → 自动创建本任务 RayCluster → Head Pod + Worker Pod(s)
         → 运行训练 → Loki/Prometheus/产物 → 按 TTL 清理 RayCluster
```

是的，**每个提交成功的平台任务都会自动拉起自己的 RayCluster**，用户不创建 RayCluster YAML，也不接触 kubeconfig。单 Worker 任务通常看到三个 Pod：RayJob submitter、Ray head、Ray worker；这是预期结构，不是重复启动了三份训练。

## 2. 原始训练代码必须满足的契约

### 2.1 数据与结果不要写死路径

训练代码只依赖三个环境变量：

| 变量 | 含义 | 权限 |
| --- | --- | --- |
| `PLATFORM_DATASET_PATH` | 本任务选择的输入目录 | 只读 |
| `PLATFORM_CHECKPOINT_PATH` | 续训时选择的旧结果目录，可为空 | 只读 |
| `PLATFORM_OUTPUT_PATH` | 本任务独占的输出目录 | 读写 |

Python 最小改造：

```python
import os
from pathlib import Path

data_root = Path(os.environ["PLATFORM_DATASET_PATH"])
output_root = Path(os.environ["PLATFORM_OUTPUT_PATH"])
checkpoint_root = os.environ.get("PLATFORM_CHECKPOINT_PATH", "")
output_root.mkdir(parents=True, exist_ok=True)

# 读取 data_root；模型、checkpoint、评估结果只写 output_root。
```

环境变量在某次任务中可能解析成 `/mnt/data/input`，也可能解析成 `/mnt/storage/public/...`。这是平台实现细节；**代码必须读取环境变量，不能判断或写死它的实际值**。调试环境中可用 `/mnt/storage/me`、`/mnt/storage/public` 浏览数据，但最终训练命令仍应使用环境变量。

`input.path` 与代码路径必须成对理解：

| 提交时选择 | `PLATFORM_DATASET_PATH` 指向 | 代码中的索引路径 |
| --- | --- | --- |
| `public` + 空路径 | 公共根 | `$PLATFORM_DATASET_PATH/0429_pkl/...` |
| `public` + `labeled` | 全量原始数据目录 | `$PLATFORM_DATASET_PATH/<站点>/...` |
| `public` + `bevfusion/fz-3dod-v1` | 版本化验收数据目录 | `$PLATFORM_DATASET_PATH/platform-validation/...` |

不要把所选子目录重复拼到环境变量后面。`labeled/` 存放原始文件，训练仍由 pkl/manifest 等索引决定样本集合。

### 2.2 分布式代码约束

- `ray-ddp` 命令写普通入口，例如 `python3 train.py`，不要写 `torchrun`；`ray-train` 只支持托管指南列出的 `python` 入口。
- 接受平台注入的 `LOCAL_RANK`、`RANK`、`WORLD_SIZE`。
- 不写死 GPU 编号、主机地址、rank 或 world size。
- 只让 rank 0 写共享 checkpoint；每个 rank 独立输出必须带 rank 后缀。
- 日志写 stdout，平台才能在 Loki 中聚合。

平台根据资源档案包装命令：

下面的 `ray_train` 是兼容 `ray-ddp` 引擎中历史保留的多节点 execution mode 名，不是 `--engine ray-train`。训练引擎与执行档案必须分别理解。

| 档案 | 资源 | 平台执行方式 |
| --- | --- | --- |
| `single_gpu` | 1 Worker × 1 GPU | 直接执行命令 |
| `torchrun` | 1 Worker × 2/4/8 GPU | 单节点 `torchrun` |
| `ray_train` | 2+ Worker × N GPU | Ray 跨节点放置，每节点 `torchrun` |

并非所有老代码都能用 `single_gpu`。如果代码在启动时强制 `init_dist(launcher="pytorch")`，单卡直接执行时没有 `RANK/WORLD_SIZE`。正确处理是让代码在单卡时走非分布式分支，或从最小多卡 `torchrun` 档案开始；不要伪造一组分布式环境变量掩盖入口问题。

`single_gpu` 启动器已经把命令放进占有 1 张 GPU 的 Ray task。脚本中应直接使用 PyTorch/CUDA，不要再调用 `@ray.remote(num_gpus=1)` 申请同一张卡，否则外层 task 占卡、内层 task 等卡，会形成资源自锁。需要自行编排 Ray task 的高级项目应选择专用执行模板，而不是套用 `single_gpu`。

### 2.3 镜像与代码的边界

```text
镜像：CUDA、Python、PyTorch、Ray、系统库、已编译 CUDA/C++ 扩展
代码：当前仓库的 Python、配置和脚本，每次任务重新上传
```

修改普通 Python/config 后不构建镜像。只有以下变化需要新镜像：CUDA/PyTorch ABI、系统包、编译器、CUDA/C++ 扩展或新增的大型固定依赖。

如果仓库本身含 `mmdet3d` 这类带 `.so` 的包，上传源码会优先于镜像中的同名包。不能简单排除整个源码包，也不能假设 Python 会把两个普通包自动合并。BEVFusion 已提供 `raytrain-bevfusion-prepare`：它在启动时只把镜像中缺失的 `.so`/兼容文件补进本次 working-dir，不覆盖用户源码。

### 2.4 提交前检查打包内容

`spk-rayjob` 同时读取 `.gitignore` 和 `.rayignore`。先检查关键源码有没有被排除：

```bash
git check-ignore -v --no-index mmdet3d/runner/__init__.py || true
git check-ignore -v --no-index mmdet3d/ops/__init__.py || true
```

例如 `.gitignore` 的 `run*/` **会被 Git 自身匹配到 `mmdet3d/runner/`**。应改成只匹配仓库根输出目录的 `/run/`、`/run_dir/`，不要用过宽的目录模式。

## 3. 方式一：spk-rayjob（推荐）

### 3.0 `.spk-rayjob.yaml` 是可选的

`spk-rayjob` 不依赖 `.spk-rayjob.yaml`。下面两种写法等价：

```bash
# 不使用模板：所有参数都在命令行指定
spk-rayjob submit --name my-smoke --entrypoint 'python3 train.py' \
  --input-space public --input-path my-dataset/v1 \
  --output-path my-project/my-smoke \
  --workers 1 --gpus-per-worker 1 --execution-mode single_gpu --watch

# 使用模板：适合同一仓库频繁重复提交
spk-rayjob init
vim .spk-rayjob.yaml
spk-rayjob submit --watch
```

`name` 是可重复使用的任务显示名称，不是 Kubernetes 资源主键。每次提交都会生成新的平台任务 ID，并以该 ID 创建独立 RayJob；因此修改代码后可以继续使用同一个项目模板，不需要手工添加时间戳。平台还会在 `output.path` 后追加本次任务 ID，所以重复提交不会覆盖上一次结果。

`.spk-rayjob.yaml` 只是项目默认参数，不是必需的平台文件，也不会固化代码。每次提交都会重新打包当前目录；命令行参数优先于模板。原生 `ray job submit` 和网页提交都不使用该文件。

### 3.1 安装与登录

Portal“外部提交”页面始终展示与当前平台版本匹配的 Linux、macOS 和 Windows 下载命令；版本升级后优先复制该页面中的命令。下面是相同流程的手工写法。

Linux AMD64：

```bash
mkdir -p ~/.local/bin ~/.cache/spk-rayjob
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-linux-amd64 \
  -o ~/.cache/spk-rayjob/spk-rayjob-linux-amd64
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS \
  -o ~/.cache/spk-rayjob/SHA256SUMS
(cd ~/.cache/spk-rayjob && grep 'spk-rayjob-linux-amd64$' SHA256SUMS | sha256sum -c -)
install -m 0755 ~/.cache/spk-rayjob/spk-rayjob-linux-amd64 ~/.local/bin/spk-rayjob
export PATH="$HOME/.local/bin:$PATH"

spk-rayjob login --server https://raytrain.wellspiking.ai
spk-rayjob jobs
```

macOS Apple Silicon：

```bash
mkdir -p ~/.local/bin ~/.cache/spk-rayjob
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-darwin-arm64 \
  -o ~/.cache/spk-rayjob/spk-rayjob-darwin-arm64
curl -fL https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS \
  -o ~/.cache/spk-rayjob/SHA256SUMS
(cd ~/.cache/spk-rayjob && grep 'spk-rayjob-darwin-arm64$' SHA256SUMS | shasum -a 256 -c -)
install -m 0755 ~/.cache/spk-rayjob/spk-rayjob-darwin-arm64 ~/.local/bin/spk-rayjob
export PATH="$HOME/.local/bin:$PATH"
spk-rayjob login --server https://raytrain.wellspiking.ai
```

Windows AMD64（PowerShell）：

```powershell
$dir = "$env:USERPROFILE\.spk-rayjob"
New-Item -ItemType Directory -Force $dir | Out-Null
Invoke-WebRequest https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-windows-amd64.exe -OutFile "$dir\spk-rayjob.exe"
Invoke-WebRequest https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS -OutFile "$dir\SHA256SUMS"
$expected = ((Select-String 'spk-rayjob-windows-amd64.exe$' "$dir\SHA256SUMS").Line -split '\s+')[0].ToLower()
$actual = (Get-FileHash -Algorithm SHA256 "$dir\spk-rayjob.exe").Hash.ToLower()
if ($actual -ne $expected) { throw 'spk-rayjob checksum mismatch' }
& "$dir\spk-rayjob.exe" login --server https://raytrain.wellspiking.ai
```

登录使用平台账号；`spk-rayjob jobs` 能列出自己的任务即说明会话有效。客户端不会获得 kubeconfig、TOS AK/SK。

### 3.2 最小提交

```bash
cd ~/my-training-project

spk-rayjob submit \
  --engine ray-ddp \
  --name my-smoke \
  --entrypoint 'python3 train.py' \
  --input-space public \
  --input-path my-dataset/v1 \
  --output-path my-project/my-smoke \
  --workers 1 --gpus-per-worker 1 \
  --execution-mode single_gpu \
  --watch
```

每次执行都会重新打包当前目录，因此修改代码后重复这条命令就是提交最新版。`--watch` 只等待状态，不改变任务行为。

### 3.3 多机多卡提交

```bash
spk-rayjob submit \
  --engine ray-ddp \
  --name my-train-2x8 \
  --image 'harbor.wellspiking.ai/<个人项目>/<镜像>@sha256:<digest>' \
  --entrypoint 'python3 train.py --config configs/train.yaml' \
  --input-space public --input-path my-dataset/v1 \
  --output-path my-project/train-2x8 \
  --workers 2 --gpus-per-worker 8 \
  --cpu-per-worker 64 --memory-per-worker 256Gi \
  --execution-mode ray_train \
  --watch
```

命令中仍然不要手写 `torchrun`。平台会生成跨节点 rendezvous 与每节点 8 进程。生产 2×8 默认是每个 Worker 64 CPU / 256GiB；32 CPU / 128GiB 仅用于 smoke，不要把 smoke 资源复制到正式长任务。

### 3.4 用模板固定项目参数

```bash
cd ~/my-training-project
spk-rayjob init                    # 生成 .spk-rayjob.yaml
vim .spk-rayjob.yaml
spk-rayjob submit --watch
```

模板只是项目默认值，不是另一种执行引擎。不同项目入口、镜像、数据和资源不同，所以模板也不同；同一项目可以长期复用一个模板，命令行参数会覆盖模板值。

### 3.5 BEVFusion 的正确提交

不要直接使用报告中的 `python3 tools/westwell_train.py ...`。该 working-dir 含新版 `mmdet3d` Python 源码，而编译扩展在镜像内，必须使用准备器：

`bev_3dod` 在自己的 checkout 根目录创建：

下面模板使用 128 样本，只是 2×8 链路 smoke，因此采用每 Worker 32 CPU / 128GiB；正式长任务改为每 Worker 64 CPU / 256GiB。

```bash
cat > .spk-rayjob.yaml <<'YAML'
name: bevfusion-3dod-2x8
engine: ray-ddp
image: harbor.wellspiking.ai/guofeng.su/ray-train-bevfusion@sha256:66b906d062870131121b07e4455783dc5f2913e285b29fdbb2cf1decc100f553
entrypoint: >-
  raytrain-bevfusion-prepare python3 tools/westwell_train.py
  configs/westwell/det/transfusion/secfpn/lidar/voxelnet_0p075.yaml
  --launcher pytorch
  --run-dir "$PLATFORM_OUTPUT_PATH"
  dataset_root="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/"
  eval_dataset_root="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/"
  data.samples_per_gpu=1 data.workers_per_gpu=1
  runner.max_epochs=1 log_config.interval=1 evaluation.interval=1
workers: 2
gpusPerWorker: 8
cpuPerWorker: 32
memoryPerWorker: 128Gi
executionMode: ray_train
input:
  space: public
  path: bevfusion/fz-3dod-v1
output:
  path: bevfusion/bev_3dod-2x8
YAML

if git check-ignore -v mmdet3d/runner/__init__.py; then
  echo 'mmdet3d/runner 被忽略，请先修复 .gitignore' >&2
  exit 1
fi
spk-rayjob submit --watch
```

`bev_3dod_s1h` 的 **smoke-128 链路验收**使用相同资源与镜像，但需在 entrypoint 的 `eval_dataset_root=...` 之后再加两个参数，并改名称/输出目录：

```yaml
name: bevfusion-3dod-s1h-2x8
# entrypoint 中增加：
data.val.ann_file="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_val.pkl"
data.test.ann_file="$PLATFORM_DATASET_PATH/platform-validation/annotations/fz-0429-platform-smoke-128/final_merged_nuscenes_infos_val.pkl"
# output.path 改为：
output:
  path: bevfusion/bev_3dod_s1h-2x8
```

S1H 的全量训练不能直接套用 `bev_3dod` 的全量模板。当前全量数据包含 IGV 第 10 类，而历史 S1H Head 只有 9 类；扩成 10 类后又不能继续加载旧 9 类 checkpoint，并且历史复跑还出现过 fp16/Hungarian cost/loss NaN。正式全量训练必须由算法负责人先核对类别映射、Head、checkpoint、学习率与 fp16 策略，再单独完成收敛验收。

模板只保存项目默认参数；不想创建该文件时，把字段改成同名命令行参数即可。完整源码补丁见 [BEVFusion 代码改造与验收](BEVFUSION_CODE_CHANGES.md)。

### 3.6 状态和日志

```bash
spk-rayjob jobs --state RUNNING
spk-rayjob status <平台任务ID>
spk-rayjob logs -f <平台任务ID>
spk-rayjob logs --limit 3000 <平台任务ID>
spk-rayjob cancel <平台任务ID>
```

注意 `--limit` 在任务 ID 之前。`spk-rayjob logs <ID> --limit 3000` 不是当前 CLI 的合法顺序。

## 4. 方式二：原生 ray job submit

### 4.1 准备 PAT 与 Ray CLI

```bash
python3 -m pip install 'ray[default]==2.56.1'
command -v jq >/dev/null || {
  echo '请先通过操作系统包管理器安装 jq' >&2
  exit 1
}

# 在平台“账户与安全 → 个人访问令牌”创建 PAT。
export RAY_ADDRESS='https://raytrain.wellspiking.ai/ray'
export RAY_JOB_HEADERS='{"Authorization":"Bearer <PAT>"}'
ray job submit --help >/dev/null
```

不要把 PAT 写入仓库、镜像或共享 shell 脚本。

PAT 只用于获准的任务与源码包 API。工作区文件上传、创建快照、用户管理和存储管理属于交互操作，必须使用浏览器登录或本地账号 / OIDC 产生的交互会话；普通 PAT 调用这些接口会返回 `INTERACTIVE_LOGIN_REQUIRED`。这条边界用于避免外部自动化令牌获得个人工作区和管理面的宽权限。

### 4.2 带当前代码和 2×8 资源提交

```bash
cd ~/my-training-project

IMAGE='harbor.wellspiking.ai/<个人项目>/<镜像>@sha256:<digest>'
META="$(jq -cn --arg image "$IMAGE" '{
  "ray-platform.image":$image,
  "ray-platform.worker-replicas":"2",
  "ray-platform.gpus-per-worker":"8",
  "ray-platform.cpu-per-worker":"64",
  "ray-platform.memory-per-worker":"256Gi",
  "ray-platform.queue":"local-gpu",
  "platform.training.engine":"ray-ddp"
}')"

ray job submit \
  --submission-id my-native-2x8-001 \
  --working-dir . \
  --runtime-env-json '{"excludes":[".git/","assets/","auto_report_system/","*.pkl","*.pth","tools/points.pcd","tools/points_intensity.txt","official_metrics.txt"]}' \
  --metadata-json "$META" \
  -- \
  env PLATFORM_DATASET_PATH=/mnt/storage/public/my-dataset/v1 \
      PLATFORM_OUTPUT_PATH=/mnt/storage/me/runs/my-native-2x8-001 \
  python3 train.py --config configs/train.yaml
```

`--working-dir .` 是关键：Ray CLI 会上传当前代码。`excludes` 只排除 Git 历史、本地数据、checkpoint 和报告，不影响挂载在 `/mnt/storage/public` 的训练数据。资源 metadata 必须全部提供，值必须是字符串，镜像必须固定到 digest。平台根据 `2 × 8` 自动选择 `ray_train`，不要在 entrypoint 再包一层 `raytrain-launch` 或 `torchrun`。

如果上传阶段返回 `HTTP 413`，不是训练代码错误，而是入口代理限制了请求体。平台维护者需确认 IDC Nginx Ingress 含 `nginx.ingress.kubernetes.io/proxy-body-size: "2g"`；用户不应绕过平台域名或改用 NodePort。代码仓库也不应长期保存数据集、checkpoint、`.git` 对象和大报告。

原生入口目前没有网页目录选择参数，因此只使用平台公开的逻辑挂载：`/mnt/storage/public/...` 只读、`/mnt/storage/me/runs/...` 读写。需要平台校验目录、自动隔离结果和一键续训时，优先用 `spk-rayjob` 或网页。

原生入口手工指定的输出目录不会自动绑定到平台“训练产物”浏览根；任务、日志、状态和 MLflow 仍会进入 Portal，但若希望页面自动展示 checkpoint，请使用 `spk-rayjob` 或网页入口，由平台生成 `PLATFORM_OUTPUT_PATH`。

### 4.3 如何在平台找到原生任务

原生 Ray CLI 返回的是 `submission-id`；平台同时创建自己的任务 ID。Portal 使用交互会话并记录 `submissionOrigin=portal`；`spk-rayjob` 与原生 Ray API 都记录 `submissionOrigin=ray-cli`。原生入口还记录 `externalSubmissionId=my-native-2x8-001`，平台通过该字段和实际接入方式区分两条 CLI/API 路径，不接受客户端伪造 origin。提交人始终是认证主体。之后可在网页看，也可用：

```bash
ray job status my-native-2x8-001
ray job logs my-native-2x8-001
ray job stop my-native-2x8-001

# 找平台任务 ID 后使用聚合日志
spk-rayjob jobs
spk-rayjob logs --limit 3000 <平台任务ID>
```

BEVFusion 原生入口同样必须写：

```text
raytrain-bevfusion-prepare python3 tools/westwell_train.py ...
```

## 5. 方式三：网页提交

网页提交使用当前浏览器的本地账号或 OIDC 交互会话，不使用平台 PAT。工作区上传和快照是本人范围内的交互操作；外部脚本若确实需要做 Portal 链路验收，也必须先用同一账号的交互登录建立独立、仅属主可读的会话配置，不能拿共享 `admin` 或任务 PAT 代替。

1. 登录 `https://raytrain.wellspiking.ai`。
2. 先在“我的数据”确认公共数据目录，在“交互式调试”用最终镜像验证命令。
3. 把代码变成不可变版本：
   - 私有 Git：先在“Git 凭据”添加并测试凭据，再选择仓库和固定 commit；或
   - 调试工作区：在“我的工作区”选择代码目录，点击发布/创建快照。
4. 打开“训练任务 → 新建任务”：
   - 选择镜像 digest；
   - 选择代码版本；
   - 选择单卡、单机多卡或多机多卡；
   - 命令填写普通 `python3 ...`；BEVFusion 填 `raytrain-bevfusion-prepare python3 ...`；
   - 从目录树选择输入、可选 checkpoint 和个人输出子目录。
5. 确认页核对代码摘要、镜像 digest、GPU 总数、数据和实际执行档案后提交。
6. 进入任务详情查看状态、Pod 拓扑、日志、GPU 指标、Ray Dashboard 和产物。

网页不会把一个正在编辑的可变目录直接拿去训练；Git commit 或工作区快照保证任务可追溯。继续改代码后创建新快照并提交新任务。

## 6. RayCluster、Dashboard 与保留时间

任务被 Kueue 准入后，KubeRay 自动创建专属 RayCluster。任务详情出现 `rayClusterName` 后会显示“Ray Dashboard”按钮：

- Dashboard 通过平台认证代理访问，Head Service 和 8265 始终是 ClusterIP，不开放 NodePort。
- 用户只能打开自己任务的 Dashboard。
- Dashboard 适合看运行中的 Ray task、actor、node、object store 和资源分配。
- 成功任务默认在终态后 60 秒内清理 RayCluster并释放 GPU；失败任务默认保留 600 秒原生排障窗口。RayCluster 清理后原生 Dashboard 随之消失，历史状态、Loki 日志、Prometheus 指标和个人产物继续在平台查看。

因此 Ray Dashboard 是**运行期诊断入口**，不是历史日志系统。

### 6.1 四类日志怎么看

| 页面标签 | 含义 | 主要看什么 |
| --- | --- | --- |
| 全部训练日志 | 合并同一任务的所有 Pod stdout/stderr | 按时间还原启动、训练和退出过程 |
| 任务提交器 | KubeRay 提交容器 | 代码上传、入口命令、提交成功/失败和最终退出码 |
| Ray Head | Ray 控制面 | GCS、调度、Dashboard、Worker 注册和资源管理 |
| 训练 Worker N | 真正运行用户训练代码的 GPU Pod | Loss、NCCL、OOM、数据加载、Checkpoint 和 Python traceback |

日志流按 Pod 聚合，不按 GPU 拆分。例如 `2 Worker × 8 GPU` 通常为 4 个日志流（提交器、Head、2 个 Worker），每个 Worker 日志内再包含 8 个 rank 的输出。用户代码应在行内输出 `rank`/`local_rank`，便于筛选。

## 7. Checkpoint 与断点续训

训练脚本必须把 checkpoint 写入 `$PLATFORM_OUTPUT_PATH`。新任务读取旧任务时：

```bash
spk-rayjob submit \
  --resume-from-job <旧平台任务ID> \
  --entrypoint 'python3 train.py --resume "$PLATFORM_CHECKPOINT_PATH/epoch_10.pth" --output "$PLATFORM_OUTPUT_PATH"' \
  --watch
```

旧目录只读挂载为 `PLATFORM_CHECKPOINT_PATH`，新任务写自己的 `PLATFORM_OUTPUT_PATH`。普通“重试”是重新执行，不等于断点续训；训练入口必须显式使用 checkpoint 参数。

## 8. 历史失败案例：working-dir 覆盖镜像扩展

历史任务 `bev-3dod-smoke6` 已成功创建 RayJob 和 RayCluster，日志中 Ray 已连接到 Head，但在训练代码导入阶段失败：

```text
ModuleNotFoundError: No module named 'mmdet3d.ops'
```

原因是 working-dir 中的 `mmdet3d` 覆盖镜像同名包，但源码仓库没有编译 `.so`；最终命令又没有使用已经交付的 `raytrain-bevfusion-prepare`。正确做法不是每次改 Python 都重建镜像，而是：

1. 镜像固定 ABI 和已编译扩展；
2. 当前 Python/config 继续随任务上传；
3. entrypoint 以 `raytrain-bevfusion-prepare` 开头，运行时只补齐缺失扩展；
4. 修正 `.gitignore` 的 `run*/`，因为 Git 本身会排除 `mmdet3d/runner`；
5. 使用文档中的 2×8 模板，不给 `single_gpu` 伪造 `RANK`。

补丁差异、完整命令和 2026-08-22 六个 fresh-clone 任务的结果见 [BEVFusion 代码改造与验收](BEVFUSION_CODE_CHANGES.md)。

## 9. 常见错误速查

| 错误 | 含义 | 处理 |
| --- | --- | --- |
| `python: not found` | 镜像只有 `python3` | 命令统一使用 `python3` |
| `KeyError: RANK` | 强制分布式入口却选了单卡直接执行 | 单卡走非分布式分支，或选择 `torchrun/ray_train` |
| `No module named mmdet3d.runner` | 被 `.gitignore` 的 `run*/` 排除 | 改成根目录定向规则并用 `git check-ignore` 验证 |
| `No module named mmdet3d.ops` | 上传源码覆盖镜像编译包 | BEVFusion 使用 `raytrain-bevfusion-prepare` |
| 一直排队 | Kueue 配额或 GPU 不足 | 看任务状态原因、团队配额和未停止 Workspace |
| Dashboard 不可用 | Head 尚未就绪或 RayCluster 已清理 | 运行期稍后重试；结束后看 Loki/Prometheus |
| 日志看不到末尾 loss | 模型结构日志过长 | `spk-rayjob logs --limit 3000 <ID>`；特别大的输出使用 `logs -f` 或 Portal 日志流 |
| 单卡任务一直 RUNNING，日志停在 `Connected to Ray cluster` | `single_gpu` 外层已占 1 卡，代码又用 `ray.remote(num_gpus=1)` 二次申请 | 删除内层 Ray GPU task，直接运行 PyTorch/CUDA；或改用专用 Ray task 模板 |
| 旧版本返回 `JOB_CREATE_FAILED` 且后端提示任务名重复 | 旧版本把显示名称错误地作为租户内永久唯一键 | 升级平台；当前版本允许同一项目名称重复提交，实际任务由平台 ID 唯一标识 |
| `spk-rayjob login --token-stdin` 返回 `INVALID_AUTHENTICATION` | PAT 已过期、被撤销、复制不完整，或误把 GitLab Token 当成平台 PAT | 在 Portal“账户与安全 → 个人访问令牌”重新创建；临时可用同一平台账号的 `--username ... --password-stdin` 登录 |
| 原生 `--working-dir` 返回 `413` | IDC Nginx 请求体上限过小，或代码包混入 `.git`/数据/checkpoint | 使用上面的 `excludes`，并让平台管理员配置 `proxy-body-size: 2g` |
