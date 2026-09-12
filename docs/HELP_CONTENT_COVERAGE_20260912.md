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
