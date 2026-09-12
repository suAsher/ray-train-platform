package helpdocs

import "ray-train-platform-backend/domain"

func PublicGuides() []domain.HelpDocument {
	return []domain.HelpDocument{
		{ID: "quickstart", Title: "快速开始：从登录到第一条训练任务", Category: "01 开始使用", SortOrder: 10, Markdown: quickstartPublicGuide},
		{ID: "account-api", Title: "账户、团队、令牌与接口边界", Category: "01 开始使用", SortOrder: 20, Markdown: accountAPIPublicGuide},
		{ID: "data", Title: "代码、镜像与数据准备", Category: "02 准备代码和数据", SortOrder: 110, Markdown: dataPublicGuide},
		{ID: "training-guide", Title: "提交训练、分布式、续训与结果", Category: "03 提交与运行", SortOrder: 210, Markdown: trainingPublicGuide},
		{ID: "debug", Title: "交互式调试与 Worker 连接", Category: "03 提交与运行", SortOrder: 260, Markdown: debugPublicGuide},
		{ID: "mlflow", Title: "实验与 MLflow 接入", Category: "04 结果与MLflow", SortOrder: 310, Markdown: mlflowPublicGuide},
		{ID: "troubleshooting", Title: "常见错误与定位路径", Category: "05 故障排查", SortOrder: 410, Markdown: troubleshootingPublicGuide},
	}
}

const quickstartPublicGuide = `### 从哪里开始

第一次使用按这个顺序走：登录 Portal，确认当前团队，创建个人 PAT，安装 CLI，提交一条 1 卡小任务，再到任务详情查看日志、指标和结果。

| 目标 | 入口 |
| --- | --- |
| 提交和查看训练 | 我的训练任务 |
| 上传、浏览个人/团队/公共数据 | 数据与存储 |
| 选择固定数据版本和场地 | 版本化数据集 |
| 打开 JupyterLab / VS Code | 交互式调试 |
| 查看训练记录和 MLflow | 实验中心 |
| 创建 PAT、管理 Git 凭据 | 账户与安全 |

### 安装 CLI

Linux x86_64：

` + "```bash\n(\n  set -eu\n  spk_tmp=$(mktemp -d)\n  trap 'rm -rf -- \"$spk_tmp\"' EXIT\n  curl -fL 'https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-linux-amd64' -o \"$spk_tmp/spk-rayjob\"\n  curl -fL 'https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS' -o \"$spk_tmp/SHA256SUMS\"\n  spk_want=$(awk '$2 == \"spk-rayjob-linux-amd64\" || $2 == \"*spk-rayjob-linux-amd64\" { print $1 }' \"$spk_tmp/SHA256SUMS\")\n  spk_got=$(sha256sum \"$spk_tmp/spk-rayjob\" | awk '{print $1}')\n  [ \"$spk_want\" = \"$spk_got\" ] || { echo 'SHA256 不一致，停止安装' >&2; exit 1; }\n  mkdir -p \"$HOME/.local/bin\"\n  install -m 0755 \"$spk_tmp/spk-rayjob\" \"$HOME/.local/bin/spk-rayjob\"\n  \"$HOME/.local/bin/spk-rayjob\" version\n)\n```" + `

macOS Apple Silicon：

` + "```bash\n(\n  set -eu\n  spk_tmp=$(mktemp -d)\n  trap 'rm -rf -- \"$spk_tmp\"' EXIT\n  curl -fL 'https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-darwin-arm64' -o \"$spk_tmp/spk-rayjob\"\n  curl -fL 'https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS' -o \"$spk_tmp/SHA256SUMS\"\n  spk_want=$(awk '$2 == \"spk-rayjob-darwin-arm64\" || $2 == \"*spk-rayjob-darwin-arm64\" { print $1 }' \"$spk_tmp/SHA256SUMS\")\n  spk_got=$(shasum -a 256 \"$spk_tmp/spk-rayjob\" | awk '{print $1}')\n  [ \"$spk_want\" = \"$spk_got\" ] || { echo 'SHA256 不一致，停止安装' >&2; exit 1; }\n  mkdir -p \"$HOME/.local/bin\"\n  install -m 0755 \"$spk_tmp/spk-rayjob\" \"$HOME/.local/bin/spk-rayjob\"\n  \"$HOME/.local/bin/spk-rayjob\" version\n)\n```" + `

Windows x64 PowerShell：

` + "```powershell\n& {\n  $ErrorActionPreference = 'Stop'\n  $spkTemp = Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString())\n  New-Item -ItemType Directory -Path $spkTemp | Out-Null\n  try {\n    $spkFile = Join-Path $spkTemp 'spk-rayjob-windows-amd64.exe'\n    $spkSums = Join-Path $spkTemp 'SHA256SUMS'\n    Invoke-WebRequest -Uri 'https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-windows-amd64.exe' -OutFile $spkFile\n    Invoke-WebRequest -Uri 'https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS' -OutFile $spkSums\n    $spkExpected = ((Get-Content $spkSums | Where-Object { $_ -match 'spk-rayjob-windows-amd64\\.exe$' }) -split '\\s+')[0]\n    if ((Get-FileHash $spkFile -Algorithm SHA256).Hash.ToLower() -ne $spkExpected.ToLower()) { throw 'SHA256 不一致，停止安装' }\n    $spkBin = Join-Path $env:USERPROFILE '.spk-rayjob'\n    New-Item -ItemType Directory -Force $spkBin | Out-Null\n    Copy-Item $spkFile (Join-Path $spkBin 'spk-rayjob.exe') -Force\n    & (Join-Path $spkBin 'spk-rayjob.exe') version\n  } finally { Remove-Item -LiteralPath $spkTemp -Recurse -Force }\n}\n```" + `

### 登录和检查

安装命令不会永久修改 shell 配置。继续使用当前终端时，先把安装目录加入本次会话 PATH：

` + "```bash\nexport PATH=\"$HOME/.local/bin:$PATH\"\ncommand -v spk-rayjob\n```" + `

Windows 当前 PowerShell 会话：

` + "```powershell\n$env:PATH = \"$env:USERPROFILE\\.spk-rayjob;$env:PATH\"\nspk-rayjob.exe version\n```" + `

在「账户与安全」创建当前团队的个人 PAT。Linux / macOS：

` + "```bash\nprintf '平台 PAT: '\nread -rs SPK_TOKEN\nprintf '\\n'\nprintf '%s\\n' \"$SPK_TOKEN\" | spk-rayjob login --server 'https://raytrain.wellspiking.ai' --token-stdin\nunset SPK_TOKEN\nspk-rayjob login-check\nspk-rayjob images\nspk-rayjob datasets\n```" + `

Windows PowerShell：

` + "```powershell\n& {\n  $spkSecret = Read-Host '平台 PAT' -AsSecureString\n  $spkPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($spkSecret)\n  try {\n    [Runtime.InteropServices.Marshal]::PtrToStringBSTR($spkPtr) | spk-rayjob login --server 'https://raytrain.wellspiking.ai' --token-stdin\n    if ($LASTEXITCODE -eq 0) { spk-rayjob login-check; spk-rayjob images; spk-rayjob datasets }\n  } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($spkPtr); $spkSecret.Dispose() }\n}\n```" + `

### 第一条任务

「我的训练任务」顶部会显示当前团队的我的 GPU 配额：限额、已用、剩余都会统计训练任务和交互式调试环境。提交前先确认剩余卡数足够。

先用 1 Worker、1 GPU 和已确认可读的小目录跑通：

` + "```bash\nspk-rayjob submit --watch \\\n  --engine ray-ddp --workers 1 --gpus-per-worker 1 \\\n  --cpu-per-worker 8 --memory-per-worker 32Gi \\\n  --image 'REPLACE_REGISTERED_IMAGE' \\\n  --data-mode mount \\\n  --name smoke --entrypoint 'python3 train.py' \\\n  --input-space public --input-path 'REPLACE_WITH_READABLE_SUBDIRECTORY'\n```" + `

如果目录里已有 ` + "`.spk-rayjob.yaml`" + `，命令行没有显式填写的字段会继承项目配置。提交前用预览和配置文件确认镜像、入口、数据路径和资源规模，不要依赖未知默认值。

成功后到「我的训练任务」打开详情，确认状态、日志、GPU 曲线、MLflow 记录和结果目录都符合预期。`

const accountAPIPublicGuide = `### 身份和令牌

Portal 登录会话用于网页；个人 PAT 用于 CLI、原生 Ray、MLflow SDK 和程序接口。不要把 GitLab token、Portal Cookie、MLflow 页面票据或集群凭据当作平台 PAT。

PAT 绑定当前有效团队和显式 scope。旧 token 不会自动获得新增权限；需要原生 MLflow 全局共享读写时重新签发 ` + "`mlflow:full`" + `。` + "`mlflow:full`" + ` 与 MLflow 网页共享范围一致，包含创建、修改、删除、Artifact 和 Registry 操作；平台训练任务、个人目录和调度仍走各自权限。

### 常用地址

| 目标 | 地址 |
| --- | --- |
| 平台 API | ` + "`https://raytrain.wellspiking.ai/api/v1`" + ` |
| 原生 Ray Jobs | ` + "`https://raytrain.wellspiking.ai/ray/`" + ` |
| 原生 MLflow Tracking URI | ` + "`https://raytrain.wellspiking.ai/api/v1/mlflow-native`" + ` |
| MLflow 网页 | ` + "`https://raytrain.wellspiking.ai/mlflow/`" + ` |

### ID 边界

Job ID 标识平台训练任务，MLflow Run ID 标识一次实验记录。一个 Job 可以关联多个 Run，两者不要求相等。原生 MLflow SDK 使用原生 Experiment ID / Run ID；旧的独立实验 REST 和六方法 SDK 才使用平台实验 ID / 平台 Run ID。

接口返回 401 先检查 token 是否过期、撤销或 scope 不够；403 先核对当前团队、角色、数据空间或实验授权。`

const dataPublicGuide = `### 代码和镜像

代码可以来自受控源码包、允许的 Git 来源或原生 Ray ` + "`runtime_env.working_dir`" + `。平台会把分支解析成固定 Commit，校验 ZIP 的 SHA-256 和大小，并把代码只读物化到训练工作区。

训练镜像负责依赖环境，不适合每次业务代码变化都重建。没有合适环境时，基于平台基础镜像派生，确认 CUDA、Python、Ray、PyTorch 和业务依赖匹配，再由管理员登记为团队可用镜像。

### 数据空间

| 空间 | 训练权限 | 用途 |
| --- | --- | --- |
| 个人数据 | 按提交配置读取；输出写入训练结果 | 个人输入、结果、checkpoint |
| 团队数据 | 只读 | 团队共享输入 |
| 公共数据 | 只读 | 平台公共数据 |
| IDC 数据 | 只读 | 已同步的外部数据源 |
| 版本化数据集 | 只读，提交时固定版本 | 可复现实验 |

大文件在「数据与存储」使用分片上传。遇到超时先读上传状态，复用 upload ID、幂等键和文件 hash 续传。

### 数据模式

| 模式 | 什么时候用 | 是否改训练代码 |
| --- | --- | --- |
| mount | 默认，直接读挂载数据 | 不用 |
| cache | 目录放得进 NVMe，且会反复读取 | 不用 |
| ray-data-stage | 海量小文件，先建完整本地视图 | 不用 |
| ray-data | Ray Data 直接把分片交给 Train Worker | 要改 |
| streaming | 固定不可变数据集版本并按需流式读取 | 要改 |

不确定就用 mount 跑通。切换模式后，先核对样本数、split、loss 和 checkpoint，再比较总耗时。

### 版本和场地

在「版本化数据集」选择 READY 版本。按场地训练时填场地代码，不填目录路径；留空代表完整版本。streaming 模式要求镜像和训练入口支持对应 schema。版本和场地范围会写入任务记录，续训沿用原范围；换场地应新建实验。`

const trainingPublicGuide = `### 提交前自检

确认镜像可见、代码来源可解析、数据路径存在、输出目录不会覆盖重要结果、GPU 数和 Worker 数符合团队配额。多 Worker 任务需要每个 Worker 整体能放到节点上，不能只看集群总空闲 GPU。

### 单卡、多机和 streaming 模板

单卡冒烟：

` + "```bash\nspk-rayjob submit --watch \\\n  --engine ray-ddp --workers 1 --gpus-per-worker 1 \\\n  --cpu-per-worker 8 --memory-per-worker 32Gi \\\n  --image 'REPLACE_REGISTERED_IMAGE' \\\n  --data-mode mount \\\n  --name smoke --entrypoint 'python3 train.py' \\\n  --input-space public --input-path 'REPLACE_WITH_READABLE_SUBDIRECTORY'\n```" + `

多 Worker Ray Train。入口必须已接入平台托管协议，不能把普通框架训练脚本直接当作 Ray Train 适配脚本：

` + "```bash\nspk-rayjob submit --watch \\\n  --engine ray-train --workers 2 --gpus-per-worker 2 \\\n  --image 'REPLACE_REGISTERED_IMAGE' \\\n  --entrypoint 'python3 tools/train_managed.py' \\\n  --input-space public --input-path 'REPLACE_WITH_READABLE_SUBDIRECTORY' \\\n  --max-failures 2 --checkpoint-every-epochs 1\n```" + `

固定版本 + 场地 streaming：

` + "```bash\nspk-rayjob submit --watch \\\n  --engine ray-train --data-mode streaming \\\n  --workers 1 --gpus-per-worker 1 \\\n  --image 'REPLACE_REGISTERED_STREAMING_IMAGE' \\\n  --dataset 'REPLACE_DATASET:REPLACE_READY_VERSION' \\\n  --dataset-sites 'REPLACE_SITE_CODE' \\\n  --dataset-cache-policy bounded \\\n  --entrypoint 'python3 tools/train_managed.py'\n```" + `

全量训练没有项目默认场地时删除 ` + "`--dataset-sites`" + `；已有默认值时用 ` + "`--dataset-sites ''`" + ` 清空。提交后 Worker 数固定，新增节点不会自动扩容已有任务。

### 续训

托管 Ray Train 使用历史任务的最新完整 checkpoint：

` + "```bash\nspk-rayjob submit --watch \\\n  --engine ray-train --workers 1 --gpus-per-worker 1 \\\n  --cpu-per-worker 8 --memory-per-worker 32Gi \\\n  --image 'REPLACE_ORIGINAL_REGISTERED_IMAGE' \\\n  --data-mode streaming \\\n  --dataset 'REPLACE_ORIGINAL_DATASET:REPLACE_ORIGINAL_VERSION' \\\n  --dataset-sites 'REPLACE_ORIGINAL_SITE_CODE' \\\n  --dataset-cache-policy bounded \\\n  --entrypoint 'python3 tools/train_managed.py' \\\n  --resume-from-job JOB_ID\n```" + `

续训应保留原镜像、入口、数据版本、场地和资源语义；只更换这些条件时应作为新实验，而不是原实验续训。

普通 ray-ddp 脚本不支持 ` + "`--resume-from-job`" + `，需要训练代码自己读取 checkpoint，并在新任务里明确选择只读 checkpoint 空间/路径。checkpoint 必须写入 ` + "`PLATFORM_OUTPUT_PATH`" + `；本地缓存盘随 Pod 删除。只让 rank 0 写共享 checkpoint。

### 训练代码要点

不要在平台入口外再套 ` + "`torchrun`" + `。Ray Data 已分片时，不要再叠加 DistributedSampler 或按 rank 再切一次。先 1 卡，再单机多卡，最后多机；每一级都核对 loss、样本数和 checkpoint。`

const debugPublicGuide = `### 交互式调试

「交互式调试」提供 JupyterLab / VS Code，通过平台短期票据和 HttpOnly Cookie 访问，不暴露 kubeconfig、节点 SSH 或公共 shell。它适合检查依赖、样本读取、入口脚本和挂载路径。调试环境会占用团队配额，长时间不用请关闭。

### 连接运行中的 Worker

` + "```bash\nspk-rayjob connect JOB_ID\nspk-rayjob connect JOB_ID --worker 0\n```" + `

任务必须正在运行且属于当前用户。输入 ` + "`exit`" + ` 只退出连接，不会停止训练。连接失败时先确认任务状态、Worker 序号、PAT 和 Portal 任务详情。

如果 JupyterLab、VS Code 或 Worker 连接返回 401/404，通常是票据过期、代理路由被通用认证提前拦截，或任务/环境已结束。重新从 Portal 或 CLI 申请入口，不要改用集群凭据。`

const mlflowPublicGuide = `### 页面和记录关系

「实验中心」分为 MLflow 总览、训练记录、API 接入和高级能力。训练任务可以关联一个或多个 MLflow Run；Job ID 不等于 Run ID。页面曲线取决于训练代码是否写入 MLflow metric，日志里有 loss 文本不代表页面一定有曲线。

### 原生 MLflow SDK

新接入优先使用原生全局共享入口；在「实验中心 → API 接入」可以查看当前原生能力、Tracking URI 和示例：

` + "```bash\npip install 'mlflow==3.14.0'\nexport MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'\nexport TOKEN=\"$RAYTRAIN_PAT\"\nexport MLFLOW_TRACKING_TOKEN=\"$TOKEN\"\n```" + `

个人 PAT 需要显式 ` + "`mlflow:full`" + `。原生 SDK 使用原生 Experiment ID / Run ID，可对共享实验、Run、Artifact 和 Registry 做读写修改删除。

列出实验和 Run：

` + "```python\nfrom mlflow import MlflowClient\nfrom mlflow.entities import ViewType\n\nclient = MlflowClient()\npage_token = None\nwhile True:\n    page = client.search_experiments(max_results=100, page_token=page_token, view_type=ViewType.ACTIVE_ONLY)\n    for experiment in page:\n        print(experiment.experiment_id, experiment.name)\n    page_token = page.token\n    if not page_token:\n        break\n\nrun_token = None\nwhile True:\n    runs = client.search_runs(experiment_ids=['1'], max_results=100, page_token=run_token)\n    for run in runs:\n        print(run.info.run_id, run.info.status, run.data.metrics)\n    run_token = runs.token\n    if not run_token:\n        break\n```" + `

默认查询活动实验和 Run；需要包含已删除实验时改用 ` + "`ViewType.ALL`" + `。` + "`max_results`" + ` 是每页大小，不是总量上限；继续使用返回的 ` + "`page.token`" + ` 或 ` + "`runs.token`" + ` 翻页。

写入参数、指标和文件：

` + "```python\nimport tempfile\nimport uuid\nfrom pathlib import Path\nimport mlflow\n\nmlflow.set_experiment('integration-demo-' + uuid.uuid4().hex)\nwith mlflow.start_run(run_name='first-connection') as run:\n    mlflow.log_param('code_version', 'your-git-commit')\n    mlflow.log_metric('validation/accuracy', 0.91, step=1)\n    with tempfile.TemporaryDirectory() as directory:\n        report = Path(directory) / 'report.txt'\n        report.write_text('connection test\\n', encoding='utf-8')\n        mlflow.log_artifact(str(report), artifact_path='reports')\n    print(run.info.run_id)\n```" + `

### 高级：独立实验和受限集成

独立实验指没有 RayTrain Job 的受限平台记录，适合外部评估程序补充指标。旧 ` + "`/api/v1/mlflow`" + ` REST 和 ` + "`/api/v1/mlflow-tracking`" + ` 六方法 SDK 是可选进阶路径，使用平台实验 ID / 平台 Run ID，并受实验 grant、scope 和方法白名单限制。

Serving、独立评估调度、模型审批发布闭环不要当作已完成能力；注册模型版本也不等于已经部署推理服务。`

const troubleshootingPublicGuide = `### 排障顺序

按用户入口到训练内部逐层定位：Portal / CLI → DNS、TLS、Ingress、OAuth2 Proxy → 后端身份和 PAT → 成员、团队、镜像、配额、数据合同 → Kueue 准入 → RayJob / RayCluster / Pod → 节点、GPU、镜像、存储、NVMe → 日志、指标、MLflow、Artifact。

### 常见现象

| 现象 | 先看哪里 |
| --- | --- |
| 401 / INVALID_AUTHENTICATION | PAT 到期、撤销、scope、登录地址 |
| 403 | 当前团队、角色、数据空间、实验授权或 artifact 权限 |
| 413 | 是否走分片上传，单片大小是否符合限制 |
| 长期排队 | 团队剩余配额、Worker 数、Kueue 准入原因、节点整体可放置性 |
| Pod Pending | 事件里的 GPU/CPU/内存不足、taint、PVC 或镜像拉取 |
| 页面没有 loss 曲线 | 训练代码是否写入 MLflow metric，平台是否绑定到对应 Run |
| MLflow 直链打不开 | 浏览器票据和 PAT API 是两种通道，重新从任务详情进入 |

看到 Pending 不要先重启 Pod。先分清提交前预检拒绝、Kueue Suspended、Pod Pending、镜像拉取、挂载失败，还是训练进程自己的错误。

### 性能定位

记录全局 batch、平均单步耗时、样本吞吐、每轮迭代数、整轮耗时和 data_time 占比。扩卡后单步耗时接近单机是可能的；如果每步处理样本翻倍、每轮步数减半，整轮耗时才是关键。grad_norm nan、loss 异常和验证指标 NaN 属于算法或数值稳定性问题，先查学习率、FP16 loss scale、梯度裁剪、warmup、异常数据和 checkpoint 兼容性。`
