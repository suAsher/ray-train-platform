# Help 内容覆盖矩阵（2026-09-12）

当前方案是 **六个分类、34 篇独立问题文章**：目录按分类折叠，点击一个问题只阅读该问题的完整正文。此前七主题汇总恢复了操作内容，但单篇过长；它的发布记录和旧映射在本文后半部分保留为历史，不代表新版 Portal 的目录结构。

本轮依据用户已确认的 [按问题查找、按单篇阅读设计](superpowers/specs/2026-09-12-question-based-help-design.md) 实施。新版读取 `/api/v1/help/articles`；旧 `/api/v1/help/documents` 仍提供七主题兼容视图。管理端 `/api/v1/admin/help/documents`、已发布原文、草稿和历史版本继续保留。这里的“34”是已知用户源文章数，新增已发布自定义文章不受此数字限制。

## 当前：六分类与独立问题覆盖

| 分类 ID | 分类 | 已知问题数 | 稳定文章 ID |
| --- | --- | ---: | --- |
| `start` | 开始使用与账号 | 6 | `quickstart`、`cli-onboarding-v2`、`quota`、`access`、`portal-user-feature-map`、`unified-login-and-roles` |
| `data` | 代码、环境与数据 | 7 | `code`、`storage`、`uploads`、`datasets`、`custom-environment`、`data-mode`、`cache` |
| `training` | 提交、分布式与续训 | 9 | `preflight`、`submit`、`resume`、`scheduling-topology`、`command-recipes`、`ray-data`、`streaming`、`scaling`、`streaming-validation` |
| `debug` | 调试与训练结果 | 4 | `debug`、`worker-connect-and-scheduling-boundary`、`observability`、`artifacts` |
| `mlflow` | MLflow 与 API | 4 | `mlflow`、`mlflow-framework-metrics`、`mlflow-api-with-pat`、`mlflow-external-tracking` |
| `troubleshooting` | 常见故障 | 4 | `errors`、`diagnose`、`portal-browser-tools-and-queue`、`telemetry-boundary` |

每篇提供问题标题、一句摘要、关键词、完整正文和相关问题。分类总览只列问题与摘要，不再复制文章正文。首页保留新手入口和常见问题；阅读页有当前文章目录、复制本文链接和下载本文，整套下载置于首页。文档管理入口移至平台管理，普通使用说明不显示管理按钮。

### 近期七主题补充内容的精确去向

七主题新增的 **23 个有效小节** 按“guide ID + 小节标题”逐一映射到独立文章。每个源小节必须恰有一个目标，且正文完整包含在目标中；不能只恢复旧 seed 而丢失近期补充。

| 原 guide ID | 原小节标题 | 独立文章 ID |
| --- | --- | --- |
| `quickstart` | 从哪里开始 | `portal-user-feature-map` |
| `quickstart` | 安装 CLI | `cli-onboarding-v2` |
| `quickstart` | 登录和检查 | `cli-onboarding-v2` |
| `quickstart` | 第一条任务 | `quickstart` |
| `account-api` | 身份和令牌 | `access` |
| `account-api` | 常用地址 | `access` |
| `account-api` | ID 边界 | `mlflow` |
| `data` | 代码和镜像 | `code` |
| `data` | 数据空间 | `storage` |
| `data` | 数据模式 | `data-mode` |
| `data` | 版本和场地 | `datasets` |
| `training-guide` | 提交前自检 | `preflight` |
| `training-guide` | 单卡、多机和 streaming 模板 | `submit` |
| `training-guide` | 续训 | `resume` |
| `training-guide` | 训练代码要点 | `submit` |
| `debug` | 交互式调试 | `debug` |
| `debug` | 连接运行中的 Worker | `worker-connect-and-scheduling-boundary` |
| `mlflow` | 页面和记录关系 | `mlflow` |
| `mlflow` | 原生 MLflow SDK | `mlflow-api-with-pat` |
| `mlflow` | 打开 MLflow 页面 | `mlflow` |
| `troubleshooting` | 排障顺序 | `errors` |
| `troubleshooting` | 常见现象 | `errors` |
| `troubleshooting` | 性能定位 | `diagnose` |

### 原文、人工编辑与链接兼容规则

- 37 篇源记录中，34 篇用户源有独立文章映射；`admin-node-onboarding`、`admin-team-retirement`、`idc-sync-lifecycle` 三篇管理员文档不进入普通目录、搜索、正文或导出，管理记录与历史仍保留。
- 原文在读取视图中投影，不回填覆盖数据库。已知人工编辑文章保留其 Markdown 正文，仍可附加匹配的近期补充；不把人工正文替换为内置摘要。未知自定义文章沿用原发布边界及正文，管理员分类的自定义文档继续排除。
- `portal-browser-tools-and-queue` 中“任务处于 SUBMITTED / Suspended”小节，仅当来源和目标 `scheduling-topology` 都是当前可读的 `platform-seed` 记录且能精确识别源小节时，才在公开文章视图移至排队文章，并在原处保留指向目标的说明。来源或目标经人工修改、目标缺失、源标题边界不匹配时，保留原处全文，不移动或丢弃。
- 旧独立文章 ID 继续定位文章；旧七主题链接按约定解析到分类总览，其中 `quickstart`、`debug`、`mlflow` 优先定位同名文章。旧 `topic:section` 链接保留明确的文章及小节映射，排队小节迁移也保留旧锚点。不存在或无法识别的链接显示明确状态，不默默打开另一篇。
- 本轮只调整帮助内容的组织与阅读入口，不修改 MLflow 鉴权、训练、调度、配额、运行时或个人文件。

### 当前验证状态

已完成构建机验证、后端发布和 Portal 登录浏览器验收：

- 后端业务 SHA：`3da896073093610eb06e11a9f944e36b91863899`；完整 Go 测试通过，配置真实隔离 PostgreSQL；`helpdocs` 定向覆盖率 **93.5%**，gofmt 差异为 0。覆盖率仅指该测试包，不代表全仓。
- Portal dev 候选 SHA：`90d105dda54e37fd4a54de0344b772e857b41072`；完整 lint、dev build 和 **23/23 Playwright** 通过。
- 内容合同覆盖六分类及 34 篇用户源、23 小节唯一映射、人工正文保留、三篇管理员排除、旧链接和排队小节的移动条件与失败保留。

- 后端发布 `release-20260912-09-3da89607`，Helm **220**，schema **48**。Harbor 摘要与两副本实际 Pod imageID 均为 `sha256:b1c3066caf10660cf047e48b1800e569a0d7ba447477fdb55467fe37f867ca88`，Ready、重启数 0，healthz 200。发布时业务提交已四端同步。
- server dry-run 仅一行后端镜像摘要变化；新快照中 4 个活跃 RayJob、4 个 RayCluster、9 个训练 Pod 的 UID、状态及 Pod ready/重启数逐项不变。未构建训练镜像，未变更配额、调度或 schema。
- 登录浏览器同源读取三个帮助接口均为 200。旧七主题 API 的 data JSON 与发布前完全一致，管理端 37 篇源记录 data JSON 与发布前完全一致；新版返回 34 篇、六分类，正文合计 72,737 字符。29 篇用户正文逐字保留；指标说明保留首段后的全文；工具文档剩余全文与迁移到排队文档的完整小节共同覆盖源正文；三篇原生 MLflow 正文与上一版已发布正文逐字一致。三篇管理员文章不进入新接口。
- Portal 依赖审计尝试返回内部 Nexus `ERR_PNPM_AUDIT_BAD_RESPONSE`（HTTP 400），本轮没有依赖变更；不将此项记录为通过。完整既有 lint/合同/构建与 E2E 门禁已通过。

- Portal dev 已推送 `90d105dda54e37fd4a54de0344b772e857b41072`；[CI 33869](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33869) 的 lint / docker-dev / helm-deploy-dev 全部成功（jobs 89811 / 89812 / 89813），CI Helm **1046**，镜像摘要 `sha256:89fa295e7b69a2b7596e75f0833e9e1c95d1f27bb40c1a2a3371e98d6e5fc1e9`。登录浏览器加载新资源 `index-BJTPmTK3.js`。本机 test-dev Kubernetes API 请求超时，实际 Portal Pod imageID 未独立核实；此限制与 CI/浏览器通过证据分别记录。
- 浏览器首页显示六个默认收起的分类，问题数为 6/7/9/4/4/4。搜索 `413`、`续训`、`run_id`、`缺少依赖`、`没有曲线` 均首先返回对应独立问题，进入后仅有一篇文章，返回搜索保留输入。旧 `troubleshooting:section-任务处于-submitted-suspended` 书签定位到排队文章并聚焦同名小节。
- 复制本文链接与复制代码分别显示成功提示；实际下载排队文章（1,392 字符）包含迁移小节，整套 Markdown（78,479 字符）包含六分类、CLI/MLflow/镜像说明且无管理员文章。390px 移动端提供“本篇目录”，帮助容器 158px、文章 132px，二者 scrollWidth 均不超过 clientWidth；桌面同样无横向溢出。
- 普通 Help 无管理按钮；平台管理的“管理使用说明”可打开既有编辑器，仅查看、未保存。分布式计算仍有 11 个菜单。训练列表配额总额仍为 24 卡，详情/日志正常；实验中心保留“训练记录 / MLflow API”两页，25 行记录，MLflow 详情按钮打开准确的原生 Run 页面。JupyterLab 与 VS Code 现有页面正常；不存在任务的真实 404 文案显示 `training job was not found`，未退化为无信息的系统错误。

发布证据保留于构建机受限目录 `/root/raytrain-release-20260912-help-articles`。本轮两个隔离工作树、三个 Portal 候选目录、测试 PostgreSQL（含其临时卷）、测试网络和候选 bundle/归档已清理，既有数据库备份保留。本次不签发 PAT、不提交训练、不改现有 Run 或用户数据。独立评估、审批发布和 Serving 等后续能力不由本轮 Help 改造完成。

## 历史：七主题内容恢复与发布记录

以下保留此前七主题汇总方案、覆盖映射及其真实验收事实。它们用于追溯内容来源和旧接口兼容；新版 Portal 实现采用上面的六分类与独立问题方案，不能以本节的七主题数量或旧版本作为本轮验收结果。

当时的修复目标是减少普通用户看到的目录数量，而不是删除旧的常见问题、命令示例和排障细节。公开 `/help/documents` 保持 7 个主主题；旧 seed 和已知 legacy custom 在服务端投影为主主题内的具名章节。管理端 `/admin/help/documents` 和历史版本仍保留原始文档记录。

### 历史审计发现

此前 7 篇 public guide 只保留了摘要层内容，旧 37 篇中大量可操作内容没有进入普通用户阅读视图。缺失项主要集中在这些部分：

- CLI 安装、SHA-256 校验、PATH 处理、PAT 登录、升级和 Windows PowerShell 示例。
- 单卡、多 Worker、streaming、原生 Ray、PowerShell、状态/日志/取消/续训等完整命令矩阵。
- 自定义训练镜像的 Dockerfile、构建、GPU/CUDA/PyTorch 验证、root/apt 边界和管理员登记前自检。
- mount/cache/ray-data-stage/ray-data/streaming 的选择条件、容量约束、Ray Data 代码片段和 streaming 验收步骤。
- 续训、checkpoint、rank 0 写结果、扩卡吞吐判断、拓扑排队、Worker 连接、调试环境边界。
- MLflow 参数/指标/文件写入示例、Job ID 与 Run ID 边界、原生共享 Tracking URI、分页、ViewType、HTTP 读写和错误处理说明。
- 401/403/413/Pending/无曲线/工具打不开等用户排障路径。

### 历史公开主主题

| 公开 guide ID | 标题 | 覆盖范围 |
| --- | --- | --- |
| `quickstart` | 快速开始：从登录到第一条训练任务 | 登录、菜单入口、CLI 安装、PAT 登录、第一次单卡任务、配额入口 |
| `account-api` | 账户、团队、令牌与接口边界 | 统一登录、PAT、Git 凭据、平台 API、Ray Jobs、MLflow Native URI、ID 边界 |
| `data` | 代码、镜像与数据准备 | 代码来源、镜像、自定义环境、数据空间、上传、数据模式、缓存、版本化数据集 |
| `training-guide` | 提交训练、分布式、续训与结果 | 提交自检、spk-rayjob、原生 Ray、Ray Train、Ray Data、streaming、续训、扩卡、产物 |
| `debug` | 交互式调试与 Worker 连接 | JupyterLab/VS Code/终端、临时依赖、Worker connect、票据和连接边界 |
| `mlflow` | 实验与 MLflow 接入 | 原生 MLflow SDK/API、训练记录、指标上报、参数/指标/Artifact 示例、程序记录实验 |
| `troubleshooting` | 常见错误与定位路径 | 常见错误、慢训练定位、任务排队、工具打不开、日志曲线不一致 |

### 历史七主题的旧 seed / legacy ID 映射

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

### 历史七主题自定义文档规则

- 已知 legacy custom ID 折入对应主主题，例如 `custom-environment` → `data`，`worker-connect-and-scheduling-boundary` → `debug`。
- 未知 custom ID 保留独立公开文档，例如 `team-faq`。
- 如果 custom ID 与 7 个主主题精确相同，它作为该主主题的唯一公开文档；同目标 legacy 章节继续追加到它下面，不产生重复主文档。
- 管理员分类的未知 custom 文档不进入普通说明，避免团队运维 Runbook 暴露给普通用户。
- 折叠只影响普通读取视图；管理列表、draft、published 原文和历史版本不改不删。

### 历史：2026-09-12 七主题用户体验修正与真实验收

用户指出“外部实验 / 集成接入 / 高级”的概念混淆，以及上次汇总后操作正文缺失。本轮把实验中心收敛为默认“训练记录”、第二页“MLflow API”和“打开 MLflow”按钮；API 页面直接显示 URI、PAT 创建位置、认证头、可复制的 HTTP/Python 查询与写入示例、Artifact 和模型版本用法、分页字段与错误处理。旧受限接口保留兼容，本轮没有改变任何鉴权或数据归属。

#### 历史版本

- 后端业务 SHA：`4b5b943e32a0c794a5c34f634298d270f49bc8d2`，候选通过验证后同步本地、GitHub、内部 GitLab 与正式构建目录。
- 后端：`release-20260912-08-4b5b943e`，Helm **219**，schema **48**。
- 镜像：`sha256:7f1bb1b84693b56021273effd894d5e2dfc05103b70dbed9d13919f65328a91a`。两个实际 Pod imageID 与此一致，Ready，重启数均为 0。
- Portal dev：`5c83c86c1c5c75d84a43c249d1ccdc8ebccdc285`，[CI 33863](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33863) lint / docker / helm 三项成功，jobs 89802 / 89803 / 89804。
- Portal CI 镜像：`sha256:a05e813669342c9e397552c06f10f4e8f4070ab7f2f622e4b25f48e406a8306a`，CI Helm **1045**；登录浏览器实际加载 `index-BrtVd_RI.js` 和新版页面。使用本机 test-dev kubeconfig 核查 Pod imageID 时，`test-k8s.westwell-research.com:6443` 请求超时，因此实际 Portal Pod 摘要仍未独立核实，不能以 CI 摘要代替。

#### 历史验证

1. 构建机完整 Go 测试通过，设置隔离 PostgreSQL DSN。旧 public projection 下新的全文回归失败；修正后保持七主题、原文、人工补充、旧 ID、重复标题及管理历史测试通过。首轮发现旧文案合同与两个新 marker 不符，按真实源文案修正后全量通过，没有删正文或安全护栏绕过。定向覆盖率：`publicHelpDocuments` 与 `appendLegacyPublicSections` 各 95.7%，其余三个帮助投影函数 100%；这是这些函数的覆盖率，不代表全仓。
2. Portal 构建机完整 Dockerfile.lint、dev build 和 **11/11 Playwright** 测试通过。覆盖配额、同 Job 多 Run 精确比较、原生权限显式选择、旧服务兼容、两页入口、API 示例、七主题全文、章节/搜索定位、旧链接、Markdown 下载、HTML 与 URL 安全。
3. 与生产同版本 MLflow 3.14.0 隔离服务器中，原样执行页面导出的全部 cURL 和 Python 示例：查询实验/Run、指标历史、创建与结束 Run、参数/指标、文件上传下载 SHA-256、真实含 MLmodel 的模型版本及 alias 读回。没有签发生产 PAT，没有向现有训练 Run 写入。
4. 登录浏览器：实验中心实际只有两个 tab，默认训练记录；HTTP/Python 切换正常，可复制配置；本次查询到 25 行训练关联记录。“打开 MLflow”实际到达原生 Home 页面。分布式计算仍有 11 个菜单。任务列表显示 local 总额度 24 卡（本轮未修改），任务详情、现有 JupyterLab/VS Code 页面正常。
5. 帮助真实接口均 200；公开主题保持 7 个。正文长度从 **16,055** 增至 **73,287** 字符。30 篇非 MLflow 用户源正文逐字完整包含；MLflow 指标说明首段后的完整正文保留；另外 3 篇旧接口说明改成原生 API 等价操作。管理端 **37 篇记录 JSON 与发布前完全一致**，包括人工 custom-environment / Worker 说明。3 篇管理员文章不公开。
6. 实际帮助页提供章节目录，训练主题 56 小节；搜索 `413` 返回两处正文命中，点击到对应主题小节并聚焦，目录按钮同样定位，长文无横向溢出。
7. Helm server dry-run **仅一行后端镜像摘要变化**。发布前后的 **4 个活跃 RayJob、4 个 RayCluster、9 个训练 Pod** 的 UID、状态、ready 与重启数逐项相同；这是本次发布时的新快照，不沿用前轮 5/5/11 数量。healthz 正常，schema 48 未变；无训练镜像构建，无调度/配额/个人数据变更。

构建机发布证据保留于受限目录 `/root/raytrain-release-20260912-experiment-help-ux`。临时测试容器/网络/工作树在收尾删除；不处理其他人的测试资源，既有数据库回滚备份保留。

#### 历史发布未改变的能力边界

本次完成实验中心呈现与用户帮助纠正；独立评估、模型审批发布与生产 Serving 闭环不由这次页面修改完成。团队节点池、TAS/闲时抢占、IDC 实际数据源同步仍以各自后续真实验收为准，没有借本轮变更启用。
