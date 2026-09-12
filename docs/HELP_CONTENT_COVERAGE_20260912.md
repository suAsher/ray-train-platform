# Help 内容覆盖矩阵（2026-09-12）

本次修复目标是减少普通用户看到的目录数量，而不是删除旧的常见问题、命令示例和排障细节。公开 `/help/documents` 保持 7 个主主题；旧 seed 和已知 legacy custom 在服务端投影为主主题内的具名章节。管理端 `/admin/help/documents` 和历史版本仍保留原始文档记录。

## 审计发现

此前 7 篇 public guide 只保留了摘要层内容，旧 37 篇中大量可操作内容没有进入普通用户阅读视图。缺失项主要集中在这些部分：

- CLI 安装、SHA-256 校验、PATH 处理、PAT 登录、升级和 Windows PowerShell 示例。
- 单卡、多 Worker、streaming、原生 Ray、PowerShell、状态/日志/取消/续训等完整命令矩阵。
- 自定义训练镜像的 Dockerfile、构建、GPU/CUDA/PyTorch 验证、root/apt 边界和管理员登记前自检。
- mount/cache/ray-data-stage/ray-data/streaming 的选择条件、容量约束、Ray Data 代码片段和 streaming 验收步骤。
- 续训、checkpoint、rank 0 写结果、扩卡吞吐判断、拓扑排队、Worker 连接、调试环境边界。
- MLflow 参数/指标/文件写入示例、Job ID 与 Run ID 边界、原生共享 Tracking URI、分页、ViewType、HTTP 读写和错误处理说明。
- 401/403/413/Pending/无曲线/工具打不开等用户排障路径。

## 公开主主题

| 公开 guide ID | 标题 | 覆盖范围 |
| --- | --- | --- |
| `quickstart` | 快速开始：从登录到第一条训练任务 | 登录、菜单入口、CLI 安装、PAT 登录、第一次单卡任务、配额入口 |
| `account-api` | 账户、团队、令牌与接口边界 | 统一登录、PAT、Git 凭据、平台 API、Ray Jobs、MLflow Native URI、ID 边界 |
| `data` | 代码、镜像与数据准备 | 代码来源、镜像、自定义环境、数据空间、上传、数据模式、缓存、版本化数据集 |
| `training-guide` | 提交训练、分布式、续训与结果 | 提交自检、spk-rayjob、原生 Ray、Ray Train、Ray Data、streaming、续训、扩卡、产物 |
| `debug` | 交互式调试与 Worker 连接 | JupyterLab/VS Code/终端、临时依赖、Worker connect、票据和连接边界 |
| `mlflow` | 实验与 MLflow 接入 | 原生 MLflow SDK/API、训练记录、指标上报、参数/指标/Artifact 示例、程序记录实验 |
| `troubleshooting` | 常见错误与定位路径 | 常见错误、慢训练定位、任务排队、工具打不开、日志曲线不一致 |

## 旧 seed / legacy ID 映射

| 旧 ID | 原标题 | 公开处理 |
| --- | --- | --- |
| `quickstart` | 第一次跑通 | 折入 `quickstart`，保留 train.py、表单项、失败处理 |
| `cli-onboarding-v2` | CLI 安装、登录与升级 | 折入 `quickstart`，保留三端安装、SHA-256、PATH、登录、升级 |
| `quota` | 配额与排队 | 折入 `quickstart`，保留限额/已用/剩余、排队说明 |
| `portal-user-feature-map` | 11 个菜单：按目标找入口 | 折入 `quickstart`，保留真实菜单名和目标入口 |
| `unified-login-and-roles` | 登录、团队与角色 | 折入 `quickstart`，保留统一登录与团队边界 |
| `access` | 令牌与私有仓库凭据 | 折入 `account-api`，保留 PAT/Git 凭据区别 |
| `storage` | 数据从哪里读、写到哪里 | 折入 `data`，保留环境变量、空间权限和输出路径 |
| `code` | 代码怎么进来 | 折入 `data`，保留 Git、工作区快照、ZIP 代码包 |
| `custom-environment` | 缺少环境？自定义训练镜像 | 折入 `data`，保留 Dockerfile、构建验证、镜像登记前自检；生产人工同 ID custom 也折入 `data` 且原文不改 |
| `cache` | 如何使用缓存加速 | 折入 `data`，保留 cache-preload、NVMe A/B 数据、容量/收益判断 |
| `data-mode` | 五种数据模式怎么选 | 折入 `data`，保留 mount/cache/ray-data-stage/ray-data/streaming 选择表 |
| `datasets` | 版本化数据集 | 折入 `data`，保留 READY 版本、场地和版本范围 |
| `uploads` | 大文件上传与恢复 | 折入 `data`，保留分片上传、续传和 hash/idempotency 规则 |
| `submit` | 提交任务与分布式训练 | 折入 `training-guide`，保留单卡/多机/续训参数说明 |
| `preflight` | 提交前自检 | 折入 `training-guide`，保留提交前检查清单 |
| `command-recipes` | 命令提交示例：spk-rayjob 与原生 Ray | 折入 `training-guide`，保留 Bash/zsh、PowerShell、spk-rayjob、原生 Ray、状态/日志/取消/续训示例 |
| `ray-data` | 如何使用 Ray Data | 折入 `training-guide`，保留 ray-data-stage、ray-data 代码片段和 DataIterator 边界 |
| `streaming` | Ray Train 托管 + Ray Data + Parquet + NVMe | 折入 `training-guide`，保留 streaming 模板、场地参数、train_managed.py 适配要求 |
| `streaming-validation` | 验收固定版本的数据训练 | 折入 `training-guide`，保留版本和场地验收步骤 |
| `resume` | 断点续训 | 折入 `training-guide`，保留 checkpoint、镜像/数据/场地不变、ray-ddp 边界 |
| `scaling` | 扩卡效果怎么验收 | 折入 `training-guide`，保留吞吐、步数、loss 和 checkpoint 对比 |
| `scheduling-topology` | 多 Worker、多机与拓扑排队 | 折入 `training-guide`，保留整体可放置性、Worker 固定和排队说明 |
| `artifacts` | 取回训练结果与权重 | 折入 `training-guide`，保留输出目录和产物下载 |
| `debug` | 交互式调试环境 | 折入 `debug`，保留 Jupyter/VS Code、/workspace、/mnt/storage、临时依赖 |
| `worker-connect-and-scheduling-boundary` | 连接自己的训练 Worker | 折入 `debug`，保留 connect 命令和退出/权限边界；生产人工同 ID custom 也折入 `debug` 且原文不改 |
| `mlflow` | 旧 MLflow 记录说明源文档 | 折入 `mlflow`，公开文案改为原生 MLflow 优先，保留 Job/Run 边界和训练指标原则；旧标题仅管理历史保留 |
| `observability` | 在哪看训练状态 | 折入 `mlflow`，保留状态、日志、指标、产物查看路径 |
| `mlflow-framework-metrics` | 让训练指标显示在 MLflow | 折入 `mlflow`，只改过时导航，保留 MMCV Hook、普通 PyTorch、rank 0、start_managed_mlflow_run/finish_managed_mlflow_run 和排障说明 |
| `mlflow-api-with-pat` | API：读取或补充已有训练记录 | 折入 `mlflow`，公开文案改为原生 MLflow 查询等价说明，保留平台 Job 关联边界 |
| `mlflow-external-tracking` | 旧 API / SDK 记录实验源文档 | 折入 `mlflow`，公开文案改为“在自己的程序中记录实验”的原生 MLflow Python/HTTP 示例；旧标题和旧接口细节仅管理历史保留 |
| `diagnose` | 训练慢，怎么定位瓶颈 | 折入 `troubleshooting`，保留 data_time、吞吐、扩卡判断和数值问题 |
| `errors` | 常见错误速查 | 折入 `troubleshooting`，保留错误现象与处理路径 |
| `portal-browser-tools-and-queue` | 打不开工具或任务一直排队 | 折入 `troubleshooting`，保留浏览器工具和排队定位 |
| `telemetry-boundary` | 日志有 Loss，为什么页面没有曲线 | 折入 `troubleshooting`，保留日志/MLflow 曲线边界 |
| `admin-node-onboarding` | 管理员：新增 GPU 节点与缓存验收 | 不进普通说明，管理端/历史保留 |
| `admin-team-retirement` | 超级管理员：团队退役与数据保留 | 不进普通说明，管理端/历史保留 |
| `idc-sync-lifecycle` | 管理员：从 IDC 同步到数据集版本 | 不进普通说明，管理端/历史保留 |

## 自定义文档规则

- 已知 legacy custom ID 折入对应主主题，例如 `custom-environment` → `data`，`worker-connect-and-scheduling-boundary` → `debug`。
- 未知 custom ID 保留独立公开文档，例如 `team-faq`。
- 如果 custom ID 与 7 个主主题精确相同，它作为该主主题的唯一公开文档；同目标 legacy 章节继续追加到它下面，不产生重复主文档。
- 管理员分类的未知 custom 文档不进入普通说明，避免团队运维 Runbook 暴露给普通用户。
- 折叠只影响普通读取视图；管理列表、draft、published 原文和历史版本不改不删。
## 2026-09-12 用户体验修正与真实验收

用户指出“外部实验 / 集成接入 / 高级”的概念混淆，以及上次汇总后操作正文缺失。本轮把实验中心收敛为默认“训练记录”、第二页“MLflow API”和“打开 MLflow”按钮；API 页面直接显示 URI、PAT 创建位置、认证头、可复制的 HTTP/Python 查询与写入示例、Artifact 和模型版本用法、分页字段与错误处理。旧受限接口保留兼容，本轮没有改变任何鉴权或数据归属。

### 版本

- 后端业务 SHA：`4b5b943e32a0c794a5c34f634298d270f49bc8d2`，候选通过验证后同步本地、GitHub、内部 GitLab 与正式构建目录。
- 后端：`release-20260912-08-4b5b943e`，Helm **219**，schema **48**。
- 镜像：`sha256:7f1bb1b84693b56021273effd894d5e2dfc05103b70dbed9d13919f65328a91a`。两个实际 Pod imageID 与此一致，Ready，重启数均为 0。
- Portal dev：`5c83c86c1c5c75d84a43c249d1ccdc8ebccdc285`，[CI 33863](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33863) lint / docker / helm 三项成功，jobs 89802 / 89803 / 89804。
- Portal CI 镜像：`sha256:a05e813669342c9e397552c06f10f4e8f4070ab7f2f622e4b25f48e406a8306a`，CI Helm **1045**；登录浏览器实际加载 `index-BrtVd_RI.js` 和新版页面。使用本机 test-dev kubeconfig 核查 Pod imageID 时，`test-k8s.westwell-research.com:6443` 请求超时，因此实际 Portal Pod 摘要仍未独立核实，不能以 CI 摘要代替。

### 验证

1. 构建机完整 Go 测试通过，设置隔离 PostgreSQL DSN。旧 public projection 下新的全文回归失败；修正后保持七主题、原文、人工补充、旧 ID、重复标题及管理历史测试通过。首轮发现旧文案合同与两个新 marker 不符，按真实源文案修正后全量通过，没有删正文或安全护栏绕过。定向覆盖率：`publicHelpDocuments` 与 `appendLegacyPublicSections` 各 95.7%，其余三个帮助投影函数 100%；这是这些函数的覆盖率，不代表全仓。
2. Portal 构建机完整 Dockerfile.lint、dev build 和 **11/11 Playwright** 测试通过。覆盖配额、同 Job 多 Run 精确比较、原生权限显式选择、旧服务兼容、两页入口、API 示例、七主题全文、章节/搜索定位、旧链接、Markdown 下载、HTML 与 URL 安全。
3. 与生产同版本 MLflow 3.14.0 隔离服务器中，原样执行页面导出的全部 cURL 和 Python 示例：查询实验/Run、指标历史、创建与结束 Run、参数/指标、文件上传下载 SHA-256、真实含 MLmodel 的模型版本及 alias 读回。没有签发生产 PAT，没有向现有训练 Run 写入。
4. 登录浏览器：实验中心实际只有两个 tab，默认训练记录；HTTP/Python 切换正常，可复制配置；本次查询到 25 行训练关联记录。“打开 MLflow”实际到达原生 Home 页面。分布式计算仍有 11 个菜单。任务列表显示 local 总额度 24 卡（本轮未修改），任务详情、现有 JupyterLab/VS Code 页面正常。
5. 帮助真实接口均 200；公开主题保持 7 个。正文长度从 **16,055** 增至 **73,287** 字符。30 篇非 MLflow 用户源正文逐字完整包含；MLflow 指标说明首段后的完整正文保留；另外 3 篇旧接口说明改成原生 API 等价操作。管理端 **37 篇记录 JSON 与发布前完全一致**，包括人工 custom-environment / Worker 说明。3 篇管理员文章不公开。
6. 实际帮助页提供章节目录，训练主题 56 小节；搜索 `413` 返回两处正文命中，点击到对应主题小节并聚焦，目录按钮同样定位，长文无横向溢出。
7. Helm server dry-run **仅一行后端镜像摘要变化**。发布前后的 **4 个活跃 RayJob、4 个 RayCluster、9 个训练 Pod** 的 UID、状态、ready 与重启数逐项相同；这是本次发布时的新快照，不沿用前轮 5/5/11 数量。healthz 正常，schema 48 未变；无训练镜像构建，无调度/配额/个人数据变更。

构建机发布证据保留于受限目录 `/root/raytrain-release-20260912-experiment-help-ux`。临时测试容器/网络/工作树在收尾删除；不处理其他人的测试资源，既有数据库回滚备份保留。

### 未改变的能力边界

本次完成实验中心呈现与用户帮助纠正；独立评估、模型审批发布与生产 Serving 闭环不由这次页面修改完成。团队节点池、TAS/闲时抢占、IDC 实际数据源同步仍以各自后续真实验收为准，没有借本轮变更启用。
