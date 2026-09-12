# 使用说明整理与 SDK 读回验收

2026-09-12。接续外部 MLflow 阶段 B，按本次部署授权发布；本记录补充而非覆盖上一份发布记录的历史证据。

## 已发布与已验证

| 对象 | 证据 |
| --- | --- |
| Backend 构建提交 | `9d0999e8cb5dcf0413cbaa37507359c78cd5c9db`；构建前本地、GitHub、内部 GitLab、正式构建目录四端一致且干净 |
| Backend release | `release-20260912-03-9d0999e8`，Helm **214**，schema **46**；无新数据库迁移 |
| Backend digest | `sha256:ae312d72806652dcb4595bee3b99e1111602d4d119e67ac2eb98e1b25f1caf56`；两个 Pod imageID 匹配、ready、重启 0，healthz 200 |
| Portal 候选与远端 dev | `08fe5fdd01aabcf802f2a082183404bf5c9f16d1`；独立 Portal 仓库，本地与实时远端一致 |
| Portal 发布证据边界 | 已验证候选 lint/build/E2E，已核对远端提交；本轮未取得 CI 最终状态和线上 Pod imageID，不能据此宣称前端部署及登录 UI 已全部验收 |

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

随后浏览器 AX 只返回菜单或空内容，截图不可用，扩展标签枚举连续超时；另一隔离浏览器停在登录页。未提取或复用浏览器凭据。GitLab 匿名 API 返回 404，不能替代获准身份的 CI 查询。dev Kubernetes API 此前超时；构建机两个可达 kubeconfig 经核对均不是 Portal dev 部署目标。

## 尚未销项

1. Portal 本轮 CI 最终成功、线上 Pod imageID、新版帮助搜索/跨主题/下载及页面样式的完整登录复验。
2. 对接方专用身份/PAT 与专用生产外部实验的真实首次读写、撤销和负向权限验收；隔离 SDK 与 mock E2E 不代替生产联调。未创建或发送任何真实 PAT，也未向正在运行的训练 Run 写演示数据。
3. 本轮未补齐调试环境 Jupyter/VS Code 一次性票据及业务 403/404 提示的真实浏览器验收；没有为此创建或重启调试环境。
4. Artifact API、不可变模型候选、独立评估、模型审批发布、Serving、集成身份/resource grant 和后台创建对账仍按后续批次实施。原生 MLflow 仍为既有共享入口，不能作为严格所有权隔离/审批生效证据。
5. 团队专属节点、TAS/闲时抢占、IDC 连接器未在本轮改变；不把设计或开关存在当作真实验收。

给对接方使用[接口交付单](MLFLOW_PARTNER_HANDOFF.md)。下一步按[平台计划](PLATFORM_NEXT_PLAN_20260912.md)分批执行；计划不是调度、凭据或资源创建授权。

## 构建机证据位置

- `/tmp/rtp-sdk-readback-red.log`、`/tmp/rtp-help-green-tests.log`
- `/tmp/rtp-help-sdk-smoke.log`、`/tmp/rtp-help-openapi.log`
- `/tmp/portal-help-final-lint.log`、`/tmp/portal-help-final-build.log`、`/tmp/portal-help-release-e2e.log`
- `/root/raytrain-release-20260912-help/`：values 备份、最小 overlay、manifest.diff、构建/升级日志、训练前后快照、help-after.jsonl、help-verification.log。
- `/tmp/rtp-help-before-20260912.jsonl`：受限权限的帮助记录发布前备份；恢复需通过文档版本/发布流程处理，不能覆盖其他管理员后续更新。
