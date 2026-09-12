# 原生共享 MLflow 与用户帮助汇总

2026-09-12。用户明确要求程序使用全部共享 MLflow，并把普通使用说明分类汇总、移除管理员文档；随后明确授权原生令牌拥有与现有网页一致的创建/修改/删除能力，以及一枚一天专用验收 PAT，验收后撤销。

## 变更范围

- 原生 SDK Tracking URI：`https://raytrain.wellspiking.ai/api/v1/mlflow-native`，个人 PAT 显式选择 `mlflow:full`。共享实验、Run、Artifact、模型注册表使用 MLflow 原生协议和原生 ID；已有令牌不自动升级。
- 保留原生共享浏览器入口；平台训练 Job API、独立实验受限 SDK/REST、集成 grant 和受控产物接口继续兼容。
- Portal 实验中心以全部共享 MLflow 为主入口，独立实验与受限集成放入高级能力。
- 用户帮助按操作场景汇总，运维文档从普通阅读、搜索和导出中移出；历史和管理员管理数据保留。
- 不修改调度、GPU 配额、训练运行时或个人数据，不提交训练任务。

## 发布前证据

旧后端 `80dd1642a274b664e532a9c559dee16a8f93d927`，Portal dev `8113e99d8e1f0754bd830d027624a95cdfae1686`，远端实时核对一致。

原生共享网页 POST `/mlflow/api/2.0/mlflow/experiments/search` 返回 200，活动实验 5 个，证明此前网页确实是共享视图；程序入口尚无同等能力。构建机新增真实注册路由 RED 测试复现 native 路由 404 与 `mlflow:full` 未支持，日志 `/tmp/rtp-native-red.log`。

隔离原生服务器使用与生产一致的 MLflow 3.14.0 镜像，独立 SQLite 与临时文件目录；不会接触生产 PostgreSQL 或现有 Artifact。生产部署继续只更新后端镜像和独立 Portal dev。

## 构建机验证

- 最终业务候选 `6fbd2b0938ae22babcae45aafaad78ba9afca977`：完整 `go test -timeout=20m -p 4 ./...` 通过，配置真实隔离 PostgreSQL，数据库测试没有因缺少 DSN 跳过。日志 `rtp-help-fold-green2.log`。
- 帮助 RED 在旧公开投影中复现 legacy 自定义文档独立出现；修复覆盖完整原文、管理列表/历史、未知自定义文档、同 ID 覆盖、无 seed 和输入不变性。日志 `rtp-help-fold-red.log`。第一次 GREEN 命令误写 `-p4`，Go 报未知参数；修正命令后全量通过，未修改产品代码绕过测试。
- 原生网关 race 测试通过。新增代理文件语句覆盖率 92/112（82.1%）；这是该文件的覆盖率，不是整个 API 包或全仓覆盖率。
- 与生产相同的 MLflow 3.14.0 镜像中，真实 Python SDK 验证：完整实验分页、创建 Run、参数/标签/指标历史、结束时间、Artifact 上传下载 SHA-256、Registry 模型版本与 alias 创建/读取/删除。使用专用容器 SQLite 和文件目录，不访问生产训练。
- Portal `610298a4e702a5ea9f23ab11dcf8bd1c0f606fc0` 在构建机通过 Dockerfile.lint 全部门禁、dev 编译及 9/9 Playwright 用例。归档不包含 `.env*`；CI 使用自身既有环境配置。
- 独立只读审阅未发现本次公开帮助投影的丢文档、重复 ID、管理员源修改或权限扩大问题。

## 最终版本

| 项目 | 实际观测 |
| --- | --- |
| 后端业务提交 | `6fbd2b0938ae22babcae45aafaad78ba9afca977`；候选通过后同步本地、GitHub、内部 GitLab、正式构建目录 |
| 后端镜像 | `release-20260912-07-6fbd2b09` |
| 后端 digest | `sha256:6503fb57b877718a3679c6c4f14af404af201b7451946e7da7db54b60a381dfe` |
| Helm / schema | `ray-platform` revision **218**，deployed；数据库查询为 **48**，本批无迁移 |
| 后端运行 | 两个 Pod Ready、重启 0，实际 imageID 均等于上述 digest |
| Portal dev | `610298a4e702a5ea9f23ab11dcf8bd1c0f606fc0` |
| Portal CI | [33859](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33859)，lint / docker-dev / helm-deploy-dev 全成功 |
| Portal 发布记录 | CI 输出 release `yuanzhu-he-wellspiking-frontend-master`、namespace `yuanzhu-he`、Helm **1044**；构建推送 digest `sha256:7687e7c8152758b436344730bf3b460015c482185ea07a2ae975823250425d0f` |
| Portal 浏览器 | 线上 asset `index-BosGRuTp.js`，新总览、API 指南、主动选择全量 PAT 和七主题帮助均实际显示 |

原生程序入口先于帮助收尾在 `c4b16ddf` / `release-20260912-06-c4b16ddf` / Helm 217 上线；218 只合并历史自定义帮助。两次 server-side manifest diff 均只有预期后端镜像变化。最终文档同步提交不重建镜像，与表内业务提交分别记录。

## 生产原生 API 验收及令牌撤销

使用用户明确授权的 guofeng.su / local 一枚 1 天 `mlflow:full` PAT，通过已登录 Portal 同域代理验证实际生产后端。令牌仅保存在当前页面内存，不向第三方发送，不在日志或文档中记录明文。

| 验收 | 结果 |
| --- | --- |
| capabilities | 200，nativeAvailable=true；仅 full scope 的受限接口 read=false |
| 全部共享实验 | 每页 2 条，连续三页返回原有活动实验 `8, 7, 6, 1, 0`，没有按平台 Job 或用户过滤 |
| 生产 Registry 查询 | 200，当前 0 条注册模型；没有创建生产模型或推理服务 |
| 专用测试资源 | 新实验 `9`，Run `cc2152bb8dce435b8ef71e15e2650a93` |
| 写入与读回 | 参数、标签和两个 step 的指标均正常；metric history 为 step 0/value 1、step 1/value 2 |
| Artifact | PUT/GET 均 200，64 字节合成内容，SHA-256 `5c25b5e3af793e00d6ea3ad3259516490aad10267066f1d50ed5def00a5fd15a`，上传下载一致 |
| 结束 Run | FINISHED，end_time 与提交的毫秒值相同；未修改现有训练 Run |
| 旧接口边界 | 仅 full scope 调受限 `/api/v1/mlflow/experiments` 返回 403；旧 scope 不扩权 |
| 即刻撤销 | 验收 PAT `pat-438385e8930b67dc0ac375fc` DELETE 返回 200，随后原生调用 401；页面内存令牌已清除，账户页显示已撤销 |

保留专用实验与合成文件供审计。生产读写是原生 REST，经 Portal 到同一后端；完整 Python SDK/Registry CRUD 运行在隔离服务器。没有把令牌跨浏览器域搬运到生产 canonical 页面，也没有在真实对接方机器上执行 SDK。

## 线上用户路径

- 分布式计算仍为 11 个菜单；「我的训练任务」显示“我的 GPU 配额”，local 总额度/已用/剩余为 24/24/0，含团队共享和队列解释。
- 实验中心默认“MLflow 总览”，另有“训练记录”“API 接入”“高级”。原生全量接入为主流程，独立实验和受限集成收进高级能力。
- 账户与安全默认仍是训练 CLI；主动选择“MLflow 全局读写”后仅显示 `mlflow:full`，明确包含修改和删除。验收只打开并取消表单，没有签发第二枚令牌。
- 训练记录同时显示原生 Run ID 和 Job ID；真实点击 `job-078243ab019985c842f9f74b` 的详情打开 Run `dd5228699de24096b72145f542c9eb2a`，原生页面含正确 `platform.job_id`、指标和参数。两类 ID 有可信关联，无需取成同一个值。
- 公开使用说明实际返回和显示 **7 个主题**：快速开始；账户与接口；代码/镜像/数据；训练/分布式/续训；交互调试；MLflow；故障排查。管理员文章不在公开列表、搜索或导出输入中。
- 生产已有两篇人工改过的 legacy 文档：`custom-environment`（4036 字符）和 `worker-connect-and-scheduling-boundary`（196 字符），分别完整折入 data/debug。完整原文包含检查通过，管理记录 JSON 和全部历史数据前后比对一致；未写入或删除原记录。
- 浏览器旧链接 `#custom-environment` 正常选择“代码、镜像与数据准备”，显示完整环境指南，页面显示 `7 / 7 个主题`。
- Jupyter/VS Code 一次性票据、Run 比较与真实错误提示的前序生产证据保留在[使用说明与 SDK 补验记录](HELP_SDK_ACCEPTANCE_20260912.md)；本次 Portal 自动化也覆盖对应未改变的错误、比较和帮助合同。

## 训练保护、记录与未完成项

两次后端发布前后逐项比较 **5 个活跃 RayJob、5 个 RayCluster、11 个训练 Pod**，UID、状态、容器重启数均一致。`/healthz` 返回 `{"status":"ok"}`，未发现 panic/fatal/migration failed。没有更改配额、调度、训练镜像、个人数据归属，未提交或停止训练。

构建机受限记录：`/root/raytrain-release-20260912-native`、`/root/raytrain-release-20260912-help-fold`；保留 values、overlay、manifest diff、镜像、测试日志和前后资源快照。前批获准数据库备份仍保留于原受限目录，不因本次文档合并而删除。

本次隔离 MLflow、隔离 PostgreSQL 容器与测试数据库匿名卷、内部测试网络、候选 worktree 和 Portal 测试目录已清理；没有删除其他任务的 worktree 或容器。

尚未完成：

1. Portal Pod imageID 独立核查：`test-dev.conf` 指向的 Kubernetes 管理 API 超时，未读取 Pod imageID。CI 构建/部署结果和线上新页面已确认，不能据此替代 Pod 证据。
2. 实际对接方的账号/长期凭据安全交付，以及其运行机器的 DNS/TLS/443 与 SDK 联调；本次验收凭据已经撤销，不可作为交付凭据。
3. 平台不可变模型候选、独立评估、可选审批、Serving；团队节点池/TAS/抢占、IDC 连接器与持续数据库恢复能力。它们不是本次原生 MLflow 接口上线的完成项，下一批见[平台计划](PLATFORM_NEXT_PLAN_20260912.md)。
