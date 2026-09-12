# 使用说明整理与 SDK 读回验收

2026-09-12。接续外部 MLflow 阶段 B，按本次部署授权发布；本记录补充而非覆盖上一份发布记录的历史证据。

## 已发布与已验证

| 对象 | 证据 |
| --- | --- |
| Backend 构建提交 | `9d0999e8cb5dcf0413cbaa37507359c78cd5c9db`；构建前本地、GitHub、内部 GitLab、正式构建目录四端一致且干净 |
| Backend release | `release-20260912-03-9d0999e8`，Helm **214**，schema **46**；无新数据库迁移 |
| Backend digest | `sha256:ae312d72806652dcb4595bee3b99e1111602d4d119e67ac2eb98e1b25f1caf56`；两个 Pod imageID 匹配、ready、重启 0，healthz 200 |
| Portal 候选与远端 dev | `08fe5fdd01aabcf802f2a082183404bf5c9f16d1`；独立 Portal 仓库，本地与实时远端一致 |
| Portal CI / 部署 | pipeline [33853](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33853) 成功；lint **89789**、docker-dev **89790**、helm-deploy-dev **89791** 全部成功，提交 `08fe5fdd` |
| Portal 部署目标 | CI 部署记录为 `infer / yuanzhu-he`，release `yuanzhu-he-wellspiking-frontend-master`，Helm **1042**；2026-09-12 03:48:43 UTC 发布。已完成登录页面复验，但仍未直接取得运行中 Pod imageID |

后端仅构建 backend，服务端 Helm dry-run 与当前 manifest 只有后端 image 一行变化。没有修改训练镜像、调度、配额、个人数据或既有训练资源。发布前后 **5 个活跃 RayJob、5 个 RayCluster、11 个训练 Pod** 的 namespace/name/UID、状态和容器重启数逐项 diff 为空。后端文档尾提交不另行重建镜像。

## 使用说明变化

完整审阅并整理为 **37 篇**，按开始使用、准备代码和数据、提交与运行、结果与 MLflow、故障排查、进阶与管理员六类排列。快速开始提供浏览器首个任务路径；CLI 安装、常用命令和管理员操作拆开，避免新用户先读长篇部署命令。

核对并明确：团队 GPU 配额位置与共享含义、代码与数据分离、checkpoint 恢复前提、Job/Run 两种 ID 合同、外部 REST/SDK 权限与限制、尚未验收的评估/审批/Serving/调度能力。补充可用的指标上报示例，移除过时的特定任务性能结论。

帮助页新增首次使用路径；目录搜索不隐藏正在阅读的跨主题文档。正文和 Markdown 下载使用 Portal 已注册路由，保留代码示例、引用链接和自定义文档。未修改全局路由或旧任务详情兼容路径，一级菜单仍为 11 个。

线上数据库实查 **37 篇已发布文档**：35 篇平台维护内容与发布源码一致；原 34 个已发布 ID 全部保留。`custom-environment` 与 `worker-connect-and-scheduling-boundary` 两篇管理员内容、草稿、发布内容和版本 3 与发布前完全一致。预置同步没有覆盖管理员编辑。

## SDK 读回修复

验收发现 SDK `get_run` 原来丢弃已写入的自定义标签，并将指标 timestamp/step 填成 0。现在从上游 `runs/get` 保留真实最新指标 tuple，通过 additive `latestMetrics` 和 `tags` 透传；旧 `latest/params/series` 兼容保留。缺失或非法元数据的指标不伪造 SDK tuple。

安全自定义标签按允许名称过滤并排序，最多 100 项、值最多 5000 UTF-8 字节，排除平台、MLflow、内部及凭据类名称。原有归属验证与写权限未扩大。SDK 仍是固定 3.14.0 的六方法子集，先 REST 创建平台 Run；不是原生全量 MLflow 代理。

## 验收结果

所有编译、测试、lint、格式化和镜像构建都在构建机进行。

| 检查 | 结果 |
| --- | --- |
| RED 回归 | 旧实现的标签与 timestamp/step 读回断言如预期失败 |
| 后端全部 Go 包 | `go test -timeout=20m -p 4 ./...` 通过；格式化后源码与精确候选逐文件一致 |
| 真实 MLflow 与 SDK | 使用线上相同 3.14.0 固定镜像、内部 Docker 网络、临时数据库；六方法通过，读回两个标签、loss=0.25、timestamp=2000、step=2 和终态均核实 |
| OpenAPI | 最新外部实验合同通过 openapi-spec-validator |
| Portal | 完整 Dockerfile.lint、生产编译通过；最终候选 6/6 Playwright E2E 通过（17 秒），接口为隔离 mock |
| 下载 | 精确中文文件名、成功下载、全部主题与自定义内容、绝对链接及代码块原样保留均断言通过；首次失败源于测试容器 C locale，修正 UTF-8 测试环境，未修改生产下载 helper |
| SDK 独立审阅 | 无阻塞发现；Portal 由根代理完成 diff 审阅，独立 Portal 审阅未取得最终结论 |

本轮发布前登录浏览器真实核实：11 个菜单；外部实验空列表/API 接入页；账户与安全跳转；能力接口成功；已有任务详情可达，并通过“打开 MLflow”进入对应 Run `3f3c7e6945d14c2594acd6b70eac4404`，页面展示任务名及 168 项指标。关联 Job 为 `job-3bf795eb68edc5fc850c7ed3`。Job ID 与 Run ID 关联正确，不要求字符串相等。

首轮收尾时浏览器与 GitLab 查询曾受阻；用户恢复登录浏览器后，使用现有获准会话补验如下。没有提取浏览器 Cookie 或复用现有 PAT。

### 登录浏览器补验

- 分布式计算实际为 **11 个菜单**，与用户截图一致。「我的训练任务」顶部显示 local 团队 GPU 总额 **24**、已用 **24**、剩余 **0**，并说明由团队共享。
- 新版使用说明真实显示 **37 / 37** 主题、六类目录和六步首次使用路径；截图核对布局、目录、正文、表格及代码块可读。搜索 `CLI 安装` 后显示 5 / 37，点击正文中的「配额与排队」能切到 `#quota`，保留搜索并提示当前主题不在搜索结果中。任务交叉链接通过注册路由到达列表。
- 实际下载 `raytrain-使用说明.md`（121914 字节），读取核实六类主题、末尾管理员文档、绝对帮助链接和代码块；搜索状态没有截断下载内容。用户下载文件保留。
- 任务详情 `job-a17cde329393c0cac7159fc9` 加载成功；访问不存在的 `job-000000000000000000000000`，真实 API 返回 404 / `JOB_NOT_FOUND`，Portal 显示后端 `training job was not found`，没有退化成通用错误。
- 实验列表勾选 Run `9b11cc91fa014b6a87562de181f0de28` 和 `dd5228699de24096b72145f542c9eb2a`，比较弹窗展示真实原始指标、参数和缺失值。后者点击「MLflow 详情」到达同一 Run，原生页面显示 5 项指标、5 项参数及 `platform.job_id=job-078243ab019985c842f9f74b`。
- 外部实验页真实显示本轮专用验收实验与两条已完成 Run；平台 ID 与上游 MLflow ID 分列展示，未伪造 Job ID。
- 既有运行中工作区 `ws-12763518143f232a2fab94e7` 的两次访问票据请求均 200；JupyterLab 到达 Launcher，VS Code/code-server 到达工作区并选择不信任作者、保持受限模式。未启动 kernel/终端、创建或重启工作区。

Portal 运行中 Pod 摘要仍不能直接核对：本机 `test-dev.conf` 对应 API `192.168.120.3:6443` 请求超时，其余已核对的可达配置没有该 Portal 对象。CI 的成功部署日志不能代替 `.status.containerStatuses[].imageID`，也不据此改写集群或凭据。

## 生产 MLflow 首次联调

2026-09-12 12:16–12:17（Asia/Shanghai），在构建机通过生产 HTTPS API `https://raytrain.wellspiking.ai` 完成专用外部实验联调。使用当前 `guofeng.su` / `local` 身份的短期读写 PAT 与同身份只读 PAT；服务端 `/api/v1/me` 核实 subject 与团队，两种凭据均未记录到报告或传给对接方。只创建本次验收专用外部实验和两个 Run，没有向既有训练 Run 写入，也未申请 GPU。

| 资源 | 平台 ID | 上游 MLflow Run ID |
| --- | --- | --- |
| 实验 `acceptance-20260912-b7ed182a0d41` | `7029bb81342851bc53e792e0fddf32d3` | 不适用 |
| 专用 REST Run | `0801d98081e09e69acf1c72ee12bc4b2` | `97c47db067ed44da9fe8772df9ed8d73` |
| 专用 SDK Run | `09cea5b12d58a7d3f335b7891a42c64a` | `754428a96d6248ac8af7374d85ef362a` |

| 验收项 | 生产结果 |
| --- | --- |
| 创建与幂等 | 实验与两个 Run 分别使用独立 Idempotency-Key；相同请求重复创建均返回原平台 ID |
| REST 批量写入与读回 | 参数 `external.acceptance_version=1`、标签 `external.purpose=acceptance-20260912` 核实；指标 tuple 为 `external/acceptance_score=0.75, timestampMs=1789186598447, step=7` |
| MLflow 3.14.0 SDK 六方法 | `get_run`、`log_param`、`log_metric`、`set_tag`、`log_batch`、`set_terminated` 均通过生产接口；参数 `external.sdk_version=3.14.0`、`external.sdk_batch=1` 和两个自定义标签读回通过，未返回平台或 MLflow 保留标签 |
| SDK 最新指标元数据 | 原生 SDK get 响应精确核实 `external/sdk_score=0.9, timestamp=1789186598448, step=11`；并非只核对浮点值 |
| 同身份只读权限 | 只读 PAT 可以读取专用 Run，写入返回 **403 INSUFFICIENT_SCOPE**；request_id `8a8a0fd34dbed7f34631a72c25c9685c` |
| 结束与终态拒写 | 两个专用 Run 均结束为 `FINISHED`；REST 追加返回 **409 MLFLOW_TRACKING_CONFLICT**，SDK 追加返回 **409 INVALID_STATE** |
| 凭据撤销 | 联调后立即撤销读写、只读两枚 PAT；两次 GET capabilities 均返回 **401 INVALID_AUTHENTICATION**，request_id 分别为 `4bb10753468be55c20ed934d00243f76`、`b3bb22b00778bdf5e462bfd4e505b9fa` |

本地与构建机专用凭据环境文件均已清理，保留已结束的验收记录和不含凭据的收据。构建机证据为 `/root/raytrain-release-20260912-help/production-mlflow-acceptance.json`、`production-mlflow-revoked-rw.json`、`production-mlflow-revoked-ro.json`。

真实跨用户/跨团队负向验收仍为 **NOT_RUN**：当前未提供获准的第二真实身份，已向用户确认；不能用伪造身份 Header、同用户只读 PAT 或隔离测试代替。以上结果证明当前身份的生产读写与凭据生命周期，不代表已为实际对接应用交付身份、令牌或资源授权。

## 尚未销项

1. Portal 线上 Pod imageID 的直接核对：CI 成功和新版帮助搜索/跨主题/下载、页面样式的登录复验已销项，仅目标 infer 管理面连接仍受阻。
2. 专用生产外部实验的真实 REST/SDK 读写、同身份只读拒写、终态拒写及两枚验收 PAT 撤销已完成，见上节。待获准第二真实身份后补齐跨用户/跨团队负向验收；实际对接应用的专用身份与凭据交付仍需确定负责人和权限范围。未向正在运行的训练 Run 写演示数据，未向对接方发送真实 PAT。
3. 调试环境 Jupyter/VS Code 一次性票据、真实业务 404 提示已销项；本轮没有创建或重启调试环境。
4. Artifact API、不可变模型候选、独立评估、模型审批发布、Serving、集成身份/resource grant 和后台创建对账仍按后续批次实施。原生 MLflow 仍为既有共享入口，不能作为严格所有权隔离/审批生效证据。
5. 团队专属节点、TAS/闲时抢占、IDC 连接器未在本轮改变；不把设计或开关存在当作真实验收。

给对接方使用[接口交付单](MLFLOW_PARTNER_HANDOFF.md)。下一步按[平台计划](PLATFORM_NEXT_PLAN_20260912.md)分批执行；计划不是调度、凭据或资源创建授权。

## 构建机证据位置

- `/tmp/rtp-sdk-readback-red.log`、`/tmp/rtp-help-green-tests.log`
- `/tmp/rtp-help-sdk-smoke.log`、`/tmp/rtp-help-openapi.log`
- `/tmp/portal-help-final-lint.log`、`/tmp/portal-help-final-build.log`、`/tmp/portal-help-release-e2e.log`
- `/root/raytrain-release-20260912-help/`：values 备份、最小 overlay、manifest.diff、构建/升级日志、训练前后快照、help-after.jsonl、help-verification.log。
- `/tmp/rtp-help-before-20260912.jsonl`：受限权限的帮助记录发布前备份；恢复需通过文档版本/发布流程处理，不能覆盖其他管理员后续更新。
