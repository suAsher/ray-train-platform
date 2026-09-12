package helpdocs

import "ray-train-platform-backend/domain"

func PublicGuides() []domain.HelpDocument {
	return []domain.HelpDocument{
		{ID: "quickstart", Title: "快速开始：从登录到第一条训练任务", Category: "01 开始使用", SortOrder: 10, Markdown: quickstartPublicGuide},
		{ID: "account-api", Title: "账户、团队、令牌与接口边界", Category: "01 开始使用", SortOrder: 20, Markdown: accountAPIPublicGuide},
		{ID: "data", Title: "代码、镜像与数据准备", Category: "02 准备代码和数据", SortOrder: 110, Markdown: dataPublicGuide},
		{ID: "training-guide", Title: "提交训练、分布式、续训与结果", Category: "03 提交与运行", SortOrder: 210, Markdown: trainingPublicGuide},
		{ID: "debug", Title: "交互式调试与 Worker 连接", Category: "03 提交与运行", SortOrder: 260, Markdown: debugPublicGuide},
		{ID: "mlflow", Title: "实验与 MLflow 接入", Category: "04 结果与MLflow", SortOrder: 310, Markdown: mlflowPublicGuide + "\n\n" + sharedModelsPublicSection},
		{ID: "troubleshooting", Title: "常见错误与定位路径", Category: "05 故障排查", SortOrder: 410, Markdown: troubleshootingPublicGuide},
	}
}

func PublicSectionForSeedDocument(document domain.HelpDocument) domain.HelpDocument {
	switch document.ID {
	case "mlflow":
		document.Title = "查看训练实验与结果"
		document.Markdown = mlflowSeedPublicSection
	case "mlflow-api-with-pat":
		document.Title = "查询实验、Run 和历史指标"
		document.Markdown = mlflowAPISeedPublicSection
	case "mlflow-external-tracking":
		document.Title = "在自己的程序中记录实验"
		document.Markdown = mlflowExternalSeedPublicSection
	case "mlflow-framework-metrics":
		document.Title = "让训练指标显示在 MLflow"
		document.Markdown = mlflowMetricsSeedPublicSection
	}
	return document
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

Job ID 标识平台训练任务，MLflow Run ID 标识一次实验记录。一个 Job 可以关联多个 Run，两者不要求相等。原生 MLflow SDK、HTTP API 和页面都使用 MLflow 自己返回的 Experiment ID / Run ID。

接口返回 401 先检查 token 是否过期或撤销；403 先核对 PAT 是否包含 ` + "`mlflow:full`" + `，以及当前团队、角色和数据空间权限。`

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

在「版本化数据集」选择 READY 版本。按场地训练时填场地代码，不填目录路径；留空代表完整版本。streaming 模式要求镜像和训练入口支持对应 schema。版本和场地范围会写入任务记录，续训沿用原范围；换场地应新建实验。

训练集 train 用于学习模型参数，验证集 val 用于开发过程中的效果检查，测试集 test 用于独立测试。页面分别显示样本数；数量为 0 表示该版本没有对应划分，不会自动使用另一个划分。没有 READY 版本时，需等待有权限的管理员完成发布，上传目录不等于已发布数据版本。

训练数据和评估数据分别固定和记录：训练时选择的版本保存在任务来源中；[独立评估](#model-evaluation-start)还需另选具体版本与 val/test 划分，不能用修改模型说明来替换历史来源。评估不使用 latest；普通用户可以选择有权访问的版本，数据发布权限保持原样。`

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

「实验中心」提供训练记录、模型和 MLflow API。训练记录用于查看 RayTrain Job 与 MLflow Run 的关联；模型用于查看共享模型和权重版本；MLflow API 用于复制原生 Tracking URI、Python 示例和 HTTP 调用方式。需要进入原生页面时点击“打开 MLflow”。

训练任务可以关联一个或多个 MLflow Run；Job ID 不等于 Run ID。页面曲线取决于训练代码是否写入 MLflow metric，日志里有 loss 文本不代表页面一定有曲线。

### 原生 MLflow SDK

本地电脑、外部服务和独立 Notebook 接入共享 MLflow 时使用下面的原生入口。平台内训练已注入连接时不要覆盖它，接法见[如何向 MLflow 记录训练参数和指标？](#mlflow-framework-metrics)。在「实验中心 → MLflow API」可以查看当前原生能力、Tracking URI 和示例：

` + "```bash\npip install 'mlflow==3.14.0'\nexport MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'\nexport MLFLOW_TRACKING_TOKEN=\"$RAYTRAIN_PAT\"\n```" + `

个人 PAT 需要显式 ` + "`mlflow:full`" + `。原生 SDK 使用原生 Experiment ID / Run ID，可对共享实验、Run、Artifact 和 Registry 做读写修改删除。

列出实验和 Run：

` + "```python\nfrom mlflow import MlflowClient\nfrom mlflow.entities import ViewType\n\nclient = MlflowClient()\nselected_experiment_id = None\npage_token = None\nwhile True:\n    page = client.search_experiments(max_results=100, page_token=page_token, view_type=ViewType.ACTIVE_ONLY)\n    for experiment in page:\n        print(experiment.experiment_id, experiment.name)\n        if experiment.name == 'REPLACE_EXPERIMENT_NAME':\n            selected_experiment_id = experiment.experiment_id\n    page_token = page.token\n    if not page_token:\n        break\n\nif selected_experiment_id is None:\n    raise SystemExit('没有找到目标实验，请从上面输出选择已有 Experiment ID')\n\nrun_token = None\nwhile True:\n    runs = client.search_runs(experiment_ids=[selected_experiment_id], max_results=100, page_token=run_token)\n    for run in runs:\n        print(run.info.run_id, run.info.status, run.data.metrics)\n    run_token = runs.token\n    if not run_token:\n        break\n```" + `

默认查询活动实验和 Run；需要包含已删除实验时改用 ` + "`ViewType.ALL`" + `。` + "`max_results`" + ` 是每页大小，不是总量上限；继续使用返回的 ` + "`page.token`" + ` 或 ` + "`runs.token`" + ` 翻页。

写入参数、指标和文件：

` + "```python\nimport tempfile\nimport uuid\nfrom pathlib import Path\nimport mlflow\n\nmlflow.set_experiment('integration-demo-' + uuid.uuid4().hex)\nwith mlflow.start_run(run_name='first-connection') as run:\n    mlflow.log_param('code_version', 'your-git-commit')\n    mlflow.log_metric('validation/accuracy', 0.91, step=1)\n    with tempfile.TemporaryDirectory() as directory:\n        report = Path(directory) / 'report.txt'\n        report.write_text('connection test\\n', encoding='utf-8')\n        mlflow.log_artifact(str(report), artifact_path='reports')\n    print(run.info.run_id)\n```" + `

### 打开 MLflow 页面

“打开 MLflow”进入共享 MLflow 页面，适合浏览实验、Run、Artifact 和 Registry。程序接入仍使用上面的 Tracking URI 和 PAT，不使用浏览器 Cookie 或页面跳转地址。

注册模型版本不等于已经部署推理服务；不要把训练成功、文件上传或 Registry 条目写成 Serving 已完成。`

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

const mlflowSeedPublicSection = `先区分平台训练任务和 MLflow Run。Job ID 标识 RayTrain 训练任务，用于调度、日志、产物和权限；MLflow Run ID 标识一次实验记录。一个 Job 可以关联多个 Run，二者不要求相等。训练任务详情和「实验中心 → 训练记录」会展示平台 Job 与 Run 的关联；程序访问原生 MLflow 时使用 MLflow 自己返回的 Experiment ID / Run ID。

### 推荐入口

| 目的 | 去哪里 | 当前能做什么 |
| --- | --- | --- |
| 查看训练任务关联的指标 | 实验中心 → 训练记录；任务详情 | 查询本人有权查看的 RayTrain 任务、日志、指标和关联 Run |
| 用程序读写共享实验 | 实验中心 → MLflow API | 使用 ` + "`https://raytrain.wellspiking.ai/api/v1/mlflow-native`" + ` 和 ` + "`mlflow==3.14.0`" + ` |
| 浏览共享 MLflow 页面 | 打开 MLflow | 通过浏览器会话查看共享实验、Run、Artifact 和 Registry |
| 下载训练权重和结果 | 任务详情 → 训练产物 | 取回写入 ` + "`PLATFORM_OUTPUT_PATH`" + ` 的文件 |

外部程序默认使用原生 MLflow 入口；平台内训练沿用已注入的连接。外部调用的个人 PAT 需要显式 ` + "`mlflow:full`" + `，旧 token 不会自动升级。该 scope 与共享 MLflow 页面范围一致，包含实验、Run、Metric、Param、Tag、Artifact、删除和 Model Registry 操作。平台训练任务、个人目录、数据空间和调度权限仍按 RayTrain 自身规则控制。

### 训练代码如何产出指标

平台会为训练注入 Tracking URI、实验名、Run 名和来源信息；镜像需安装兼容 MLflow 客户端，代码仍需主动创建或复用 Run 并记录指标。环境变量不会自动生成 loss，也不会把 stdout 转成曲线。

只由 global rank 0 写 MLflow。已有框架集成或托管适配器时复用其 Run，不再启动第二条独立 Run。参数、代码版本、数据范围与带 step 的 loss/lr 应一起记录，比较结果时才可追溯。

注册模型版本不等于已经部署推理服务。需要独立评估时，从共享模型版本[发起评估](#model-evaluation-start)，固定数据和方案并等待有效报告；审批与 Serving 尚未上线。`

const mlflowNativeConnectionGuide = `该地址是外部程序访问共享 MLflow 的入口前缀，当前验证的客户端为 mlflow==3.14.0，范围包括 Tracking、Artifacts 和 Model Registry；其他版本或产品协议需另外验证。

- Python SDK 的 tracking_uri 只填 ` + "`https://raytrain.wellspiking.ai/api/v1/mlflow-native`" + `，可设置 MLFLOW_TRACKING_URI，或调用 ` + "`MlflowClient(tracking_uri=\"https://raytrain.wellspiking.ai/api/v1/mlflow-native\")`" + `。SDK 会自行拼接后续原生 API 路径，不把 /api/2.0/mlflow 加到 tracking_uri。
- 直接用 HTTP 查询或记录实验时，在前缀后接 /api/2.0/mlflow/...，例如 ` + "`https://raytrain.wellspiking.ai/api/v1/mlflow-native/api/2.0/mlflow/runs/search`" + `。文件操作优先使用 SDK，避免把所有文件协议当作同一种 REST 路径。
- 两种外部调用都需要有效个人 PAT，且显式包含 mlflow:full。SDK 从 MLFLOW_TRACKING_TOKEN 读取它；HTTP 用 Authorization: Bearer 请求头。由运行环境安全注入令牌，不写进源码、URL 或日志。

平台内训练已注入 MLFLOW_TRACKING_URI 时，不要覆盖为这个外部 PAT 网关，也不需要把个人 PAT 填进训练脚本。沿用运行时和框架适配器的连接，见[如何向 MLflow 记录训练参数和指标？](#mlflow-framework-metrics)。`

const mlflowAPISeedPublicSection = `查询训练实验时，先列出自己能看到的实验，再从搜索结果里选择 Experiment ID 查询 Run。用户只需要 MLflow 自己返回的 Experiment ID / Run ID；Job ID 只用于回到 RayTrain 任务详情查日志、队列、产物和训练状态。

` + mlflowNativeConnectionGuide + `

### Python：读取实验、Run、指标历史和文件

` + "```bash\npip install 'mlflow==3.14.0'\nexport MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'\nexport MLFLOW_TRACKING_TOKEN=\"$RAYTRAIN_PAT\"\n```" + `

` + "```python\nfrom mlflow import MlflowClient\nfrom mlflow.entities import ViewType\n\nclient = MlflowClient()\nselected_experiment_id = None\nexperiment_token = None\nwhile True:\n    experiments = client.search_experiments(\n        max_results=100,\n        page_token=experiment_token,\n        view_type=ViewType.ACTIVE_ONLY,\n    )\n    for experiment in experiments:\n        print(experiment.experiment_id, experiment.name)\n        if experiment.name == 'REPLACE_EXPERIMENT_NAME':\n            selected_experiment_id = experiment.experiment_id\n    experiment_token = experiments.token\n    if not experiment_token:\n        break\n\n# 需要已删除实验时，把 view_type 改成 ViewType.ALL。\nif selected_experiment_id is None:\n    raise SystemExit('没有找到目标实验，请从上面输出选择已有 Experiment ID')\n\nrun_token = None\nwhile True:\n    runs = client.search_runs(\n        experiment_ids=[selected_experiment_id],\n        filter_string=\"attributes.status = 'FINISHED'\",\n        max_results=100,\n        page_token=run_token,\n    )\n    for run in runs:\n        print(run.info.run_id, run.info.status, run.data.params, run.data.metrics)\n        print(client.get_metric_history(run.info.run_id, 'validation/accuracy'))\n        print(client.list_artifacts(run.info.run_id, 'reports'))\n    run_token = runs.token\n    if not run_token:\n        break\n```" + `

默认查询活动实验和 Run；max_results 是每页大小，不是总量上限。继续使用返回的 page.token 或 runs.token 翻页。

### HTTP：读取和分页

` + "```bash\nexport MLFLOW_API='https://raytrain.wellspiking.ai/api/v1/mlflow-native/api/2.0/mlflow'\n\ncurl -sS --fail-with-body \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"max_results\":100,\"view_type\":\"ACTIVE_ONLY\"}' \\\n  \"${MLFLOW_API}/experiments/search\"\n\ncurl -sS --fail-with-body \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"experiment_ids\":[\"REPLACE_EXPERIMENT_ID_FROM_SEARCH\"],\"max_results\":100}' \\\n  \"${MLFLOW_API}/runs/search\"\n\ncurl -sS --fail-with-body \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  \"${MLFLOW_API}/metrics/get-history?run_id=REPLACE_RUN_ID&metric_key=validation%2Faccuracy\"\n\ncurl -sS --fail-with-body \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  \"${MLFLOW_API}/artifacts/list?run_id=REPLACE_RUN_ID&path=reports\"\n```" + `

search 响应里如果有 next_page_token 字段，把它原样放进下一次请求正文的 page_token。HTTP 错误按 MLflow 原生响应处理；401 先查 PAT 是否有效、过期或撤销；403 查 PAT 是否包含 mlflow:full；404 查 Experiment ID / Run ID；429 按 Retry-After 等待；502/503 或超时先读回确认结果再重试。不要把 PAT 写进 URL、日志或截图。`

const mlflowExternalSeedPublicSection = `外部训练脚本、评估脚本或 Notebook 想把结果记到 RayTrain 的共享 MLflow 时，直接使用原生 Tracking URI。程序会创建普通 MLflow Experiment 和 Run，不需要先创建平台训练 Job。

` + mlflowNativeConnectionGuide + `

### Python：创建实验、写参数/指标/文件

` + "```bash\npip install 'mlflow==3.14.0'\nexport MLFLOW_TRACKING_URI='https://raytrain.wellspiking.ai/api/v1/mlflow-native'\nexport MLFLOW_TRACKING_TOKEN=\"$RAYTRAIN_PAT\"\n```" + `

` + "```python\nimport tempfile\nimport uuid\nfrom pathlib import Path\nimport mlflow\nfrom mlflow import MlflowClient\n\nmlflow.set_experiment('program-demo-' + uuid.uuid4().hex)\nwith mlflow.start_run(run_name='first-connection') as run:\n    mlflow.log_param('code_version', 'your-git-commit')\n    mlflow.log_param('dataset_version', 'your-dataset-version')\n    for step, score in enumerate([0.82, 0.87, 0.91], start=1):\n        mlflow.log_metric('validation/accuracy', score, step=step)\n    with tempfile.TemporaryDirectory() as directory:\n        report = Path(directory) / 'report.txt'\n        report.write_text('connection test\\n', encoding='utf-8')\n        mlflow.log_artifact(str(report), artifact_path='reports')\n    run_id = run.info.run_id\n\nclient = MlflowClient()\nprint(client.get_run(run_id).data.metrics)\nprint(client.get_metric_history(run_id, 'validation/accuracy'))\nprint(client.list_artifacts(run_id, 'reports'))\n```" + `

返回的是原生 MLflow Run ID。后续 get_run、get_metric_history、log_metric、log_artifact、delete_run、restore_run 和 Registry 操作都使用这个 ID。

示例中的 code_version、dataset_version 和指标值必须替换为本次运行真实信息；不知道时省略，不能用占位值冒充训练来源。自定义参数或标签只记录你的声明，不会自动建立 RayTrain Job 关联，也不会回填任务或共享模型的数据来源。不要手工伪造 platform.* 来源标签。

### HTTP：创建、写入和读回

` + "```bash\nexport MLFLOW_API='https://raytrain.wellspiking.ai/api/v1/mlflow-native/api/2.0/mlflow'\n\ncurl -sS --fail-with-body -X POST \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"name\":\"program-demo-http\"}' \\\n  \"${MLFLOW_API}/experiments/create\"\n\ncurl -sS --fail-with-body -X POST \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"experiment_id\":\"REPLACE_EXPERIMENT_ID\",\"run_name\":\"first-http-run\"}' \\\n  \"${MLFLOW_API}/runs/create\"\n\ncurl -sS --fail-with-body -X POST \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"run_id\":\"REPLACE_RUN_ID\",\"params\":[{\"key\":\"code_version\",\"value\":\"your-git-commit\"}],\"metrics\":[{\"key\":\"validation/accuracy\",\"value\":0.91,\"timestamp\":1789171200000,\"step\":1}],\"tags\":[{\"key\":\"source\",\"value\":\"program-demo\"}]}' \\\n  \"${MLFLOW_API}/runs/log-batch\"\n\ncurl -sS --fail-with-body -X POST \\\n  -H \"Authorization: Bearer ${RAYTRAIN_PAT}\" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"run_id\":\"REPLACE_RUN_ID\",\"status\":\"FINISHED\"}' \\\n  \"${MLFLOW_API}/runs/update\"\n```" + `

HTTP 适合服务端集成和批量写指标；文件上传建议使用 Python SDK 的 log_artifact，避免手写 artifact 存储路由。读取文件列表可用 artifacts/list，下载文件优先用 MlflowClient.download_artifacts。

先在专用测试实验中联调，不向正在训练的 Run 写演示数据。注册模型版本、上传文件或结束 Run 不代表已经完成评估审批，也不代表已经部署 Serving。`

const mlflowMetricsSeedPublicSection = `适用于在 RayTrain 上运行的训练代码。自己的脚本不创建 RayTrain Job、只想记录实验时，使用[如何用自己的程序向 MLflow 写入数据和文件？](#mlflow-external-tracking)的原生 MLflow 示例。

### 平台内训练使用哪个地址

启用训练 MLflow 接入后，平台向训练环境注入 MLFLOW_TRACKING_URI、实验名、Run 名和可信任务来源。运行时适配器读取这些配置；平台不会注入个人 PAT。不要覆盖已注入的 MLFLOW_TRACKING_URI 为外部 /api/v1/mlflow-native 地址，不要照搬外部示例设置 MLFLOW_TRACKING_TOKEN 或重新选择另一个实验。

普通自定义训练仍需安装兼容客户端并接入已有适配器。发现变量缺失、没有 Run 或框架 Hook 未初始化时，先核对镜像和训练入口；换成外部地址不会自动修复任务关联。

平台提供连接和可信关联信息；训练代码仍需主动创建 Run、记录参数和指标，不会从 stdout 猜测 Loss。

### 训练代码需要做什么

1. 由 global rank 0 创建 Run，沿用平台注入的连接与归属信息，不覆盖 ` + "`platform.*`" + ` 等保留标签。
2. 用单调递增的训练 step 记录标量；参数用于不随时间改变的配置，变化值用 metric。
3. 记录可复现信息，例如代码版本、数据集版本、配置、随机种子。不要记录凭据。
4. Checkpoint、模型和报告写到 ` + "`PLATFORM_OUTPUT_PATH`" + `，见[训练产物](#artifacts)。

| 来源 | 建议指标 | 页面含义 |
| --- | --- | --- |
| 普通 PyTorch | ` + "`loss`" + `、` + "`learning_rate`" + `、` + "`epoch`" + `、` + "`throughput`" + ` | 按代码显式上报 |
| MMCV MlflowLoggerHook | ` + "`train/loss`" + `、` + "`train/stats/...`" + `、` + "`learning_rate`" + ` | ` + "`loss`" + ` 与 ` + "`train/loss`" + ` 显示为 Training Loss |
| 验证过程 | ` + "`val/loss`" + ` 或双方约定的评估键 | 不覆盖训练 Loss |

` + "`ray.train.report()`" + ` 管理 Ray Train 的结果与 Checkpoint；` + "`mlflow.log_metrics()`" + ` 记录实验指标，两者不能互相替代。

### 在已有 Run 中补充标量

已使用平台托管 Hook 或框架 MlflowLoggerHook 的入口，先确认 Hook 已创建并关联 Run，再在训练循环调用下面的函数；不要额外调用 ` + "`start_run()`" + ` 创建第二条记录。` + "`global_rank`" + ` 应从训练框架获取全局 rank，不能用每台机器各有一个 0 的 local rank。

` + "```python\nimport mlflow\n\n# 嵌入已有训练循环；loss、step 和 global_rank 由训练框架提供。\ndef log_training_metric(loss, step, global_rank):\n    if global_rank != 0:\n        return\n    if mlflow.active_run() is None:\n        print(\"MLflow Run 尚未初始化，请检查平台或框架 Hook\", flush=True)\n        return\n    try:\n        mlflow.log_metric(\"train/loss\", float(loss), step=int(step))\n    except Exception as exc:\n        # 辅助观测故障不应中断 GPU 训练；不输出可能含凭据的完整异常。\n        print(f\"MLflow 指标写入失败：{type(exc).__name__}\", flush=True)\n```" + `

### 新训练入口还没有 Run 时

先选兼容的已登记运行时并接入平台训练适配器。平台运行时的 ` + "`start_managed_mlflow_run(training_parameters, rank=global_rank, world_size=world_size)`" + ` 会使用注入的实验、Job/团队/用户及来源信息；结束时用配套 ` + "`finish_managed_mlflow_run(client, owned=owned, status=...)`" + `，只结束本入口创建的 Run。两者位于 ` + "`raytrain_runtime.reporting`" + `，需在镜像内确认该模块和对应版本可用。

普通自定义镜像未安装这个适配器时，仅 ` + "`pip install mlflow`" + ` 或调用无标签的 ` + "`start_run()`" + ` 不会自动建立平台可信关联。应先让管理员或模型维护者按训练入口接入，并用单卡任务验证；不要手工猜造 ` + "`platform.*`" + ` 标签、复制其他任务的来源信息，或将平台来源环境变量打印出来。

### 如何确认接入成功

运行 ` + "`spk-rayjob status JOB_ID`" + ` 和 ` + "`spk-rayjob logs -f JOB_ID`" + `，确认已进入训练 step；再到任务详情核对关联的 Job、Run、参数与带 step 的曲线。

**Job ID 与 MLflow run_id 不要求相等。** Run 名称可以包含任务 ID，但名称也不是归属凭据；一次任务可能关联多个 Run，核对时使用页面返回的明确关联。

没有 Run：检查创建逻辑与平台关联。没有曲线：检查 global rank 0、指标键和 step。只有普通日志：补充主动上报。仍有问题时提供 Job ID、Run ID 和指标键，参见[工具排查](#portal-browser-tools-and-queue)。

Artifact、Models 或 Traces 为空不能用来判定训练失败；当前训练接入不等于完整模型注册、审批或服务发布。

### 记录代码、数据版本与训练参数

已接入的适配器会记录可取得的优化器、学习率、epoch、随机种子和分布式规模。固定数据版本存在时，会从真实运行环境带入 dataset_id、dataset_version_id 等参数以及 platform.dataset_version_id 等来源标签。它不会从目录名猜数据版本，也不会保证每种训练入口都自动记录 Git commit。

在已有 Run 中，由 global rank 0 用 ` + "`mlflow.log_params(...)`" + ` 补充实际训练配置；每个 step 用 ` + "`mlflow.log_metric(...)`" + ` 记录 loss、学习率等变化值。确知代码 commit 时，可用 code_commit 参数记录，或使用自定义 research.code_commit 标签；不要覆盖 platform.* 标签。参数和标签只有你明确记录后才存在，不知道的值保持缺失。

要让数据进入可追溯流程，先在[版本化数据集](#datasets)选 READY 版本，并在提交训练时固定该版本和场地范围。任务、MLflow Run 和共享模型是不同记录：训练任务保存实际数据来源，适配器在 Run 中记录可用来源；任务结束后将所选权重[登记为模型版本](#shared-model-registration)，沿用任务已有的数据版本。

历史任务未固定数据版本时，模型显示“未知 / 未登记”；登记权重时可自选可访问的 READY 版本并标为“用户补充”。它不改写历史训练记录；修改 MLflow 参数或标签也不会自动同步到模型版本，不能把用户声明当作已核实的训练数据来源。

模型这里记录的是训练数据来源。用于衡量模型表现的[独立评估](#model-evaluation-start)需要另行固定评估数据和方案，提交任务并成功生成有效报告。审批与 Serving 尚未上线，不能把填写数据版本或记录一次验证 metric 当作已完成这些流程。`
