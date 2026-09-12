# 外部 MLflow 实验与 SDK 阶段 B 发布记录

2026-09-12，北京时间。用户批准设计并明确授权部署。本轮完成外部实验读写、受控 SDK 子集和 Portal 接入页；不代表文件、模型审批、独立评估或 Serving 已完成。

## 版本与发布证据

| 对象 | 已核实结果 |
| --- | --- |
| Backend 构建时四端 main | `e82614ee0b73a1c7de6b786cad2a097caed77e63`；本地、GitHub、内部 GitLab、正式构建目录一致且干净 |
| Backend 业务验证候选 | `4995b0fd8b32e02787073a2c724b1fa098a2940b`；至构建提交只有 OpenAPI 修订，并单独通过验证 |
| Backend 线上 | `release-20260912-02-e82614ee`；Helm **213**；schema **46** |
| Backend digest | `sha256:d167019aa33164f706cf95a8c8e6029a44ad41ad351ce865ff90e4ee65d33f2f`；两个 Pod imageID 均匹配，2/2 ready、重启 0；healthz 200 |
| Portal dev | `bbeab26c8dcc1c4577521a1ade2091203d4d685a`；独立仓库 dev，本地与远端一致 |
| Portal CI/CD | [#33849](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33849) 成功；lint #89784、build #89785、deploy #89786 均成功，浏览器登录核实 |
| Portal 独立镜像与页面验收 | dev Kubernetes API 超时，未独立取得线上 Pod imageID；后续浏览器 AX 卡在菜单、截图不可用，新增页面完整生产交互尚未验收 |
| MLflow | 原 3.14.0 镜像 `sha256:03c206d175084ee3f654a1353ab62ccd2e59048c54f9488f08cc4d8f5de0037d`、2/2 ready；未重建 |

业务候选先在构建机 detached worktree 验证，再重新查询两个远端仍为 `9105a47`，才推送并快进正式目录。只构建 backend。Helm `--reuse-values` server dry-run 与当前 manifest 只有 backend image 一行不同；随后 `--atomic --wait` 发布成功。文档尾提交另行同步，不重建业务镜像。

## 数据与运行训练保护

迁移 0046 只新增 `mlflow_tracking_experiments`、`mlflow_tracking_runs` 及其索引/约束。没有修改历史 Job、用户、配额、个人数据归属，也未修改调度、节点或训练镜像。

发布前使用 `pg_dump -Fc --no-owner --no-acl` 将 schema 45 数据库备份到构建机受限目录 `/root/raytrain-release-20260912-phaseb/platform-schema45.dump`，文件校验和另存 `backup.sha256`。在隔离 PostgreSQL 新数据库执行 `pg_restore --exit-on-error` 成功，查询 schema 为 45，随后删除该临时恢复库。恢复时应先停止受影响写入、在独立数据库恢复并验证，再按维护方案切换连接；不能直接覆盖运行数据库。`--atomic` 不回滚已提交的数据库迁移，旧后端兼容新增表。

升级前后快照完全相同：5 个非终态 RayJob、6 个 RayCluster、11 个训练 Pod；逐项比较 namespace/name/UID、状态和容器重启数，diff 为空。发布时三项 RUNNING Job：

| Job | UID |
| --- | --- |
| tenant-algorithm / job-d72db38e32d63b292d017f8b | `a90fad88-9cc4-4090-acc1-3860fb58174f` |
| tenant-local / job-3bf795eb68edc5fc850c7ed3 | `24e3a3b2-0ab1-427b-916c-2a39dfbc8a8b` |
| tenant-local / job-94440c922ca159a059aa5430 | `7a9456e2-4ab4-47b0-8296-ccf11189d86d` |

另两项为待运行记录 `job-9fa8863fbe8b012adf40fd1e`、`job-1e5827c8678b1b292c17ee6b`，均保留相同 UID 和状态。此前交接或浏览器快照中的不同任务集合没有被用作发布基线。升级后两张新表均为 0 条：没有创建真实 PAT、生产演示实验、Run、评估或推理服务，也没有向现有训练写入演示指标。

## 完成的行为

- 实验中心新增“外部实验”和“API 接入”；当前身份可查询自己的实验、分页 Run 和详情。能力接口失败或未启用时明确说明原因。现有 11 个一级菜单不增加，配额仍在训练列表顶部。
- 外部 REST 支持创建实验/Run、有界分页、读取、log-batch、finish。当前有效团队与本人所有权都必须匹配，管理员没有代写权限。旧 `jobs:read`、`mlflow:write` 和默认 PAT scope 保持原行为。
- 外部 PAT 可选 `experiments:read`、`experiments:write`。创建使用显式幂等键、持久化意图和上游证明；不确定状态保留 PENDING。数据库写租约协调 log/finish，终态不重开，finish 冲突会核对真实上游终态。
- SDK 固定测试版本 3.14.0，仅允许 `get_run/log_batch/log_metric/log_param/set_tag/set_terminated`。先用 REST 创建资源，再以**平台 Run ID**调用 SDK。上游 `mlflowRunId` 仅用于原生界面核对。支持 SDK 同时发送相等的 `run_id/run_uuid`，拒绝冲突值。
- SDK 使用 `/api/v1/mlflow-tracking` 前缀，返回原生协议；不是全量 MLflow 代理。Artifact URI 为不可上传的 `raytrain-disabled:`，不暴露内部存储路径。
- 新写入口有权限、参数、请求体、限流、超时、写前/写后审计控制。批量写仍可能部分生效，不宣称跨系统原子性；示例客户端不盲目自动重试。

接口交付入口：[交付单](MLFLOW_PARTNER_HANDOFF.md)、[外部实验合同](MLFLOW_EXTERNAL_TRACKING_API.md)、[OpenAPI](api/mlflow-external-tracking.openapi.json)、[Python 示例](../examples/mlflow_integration/README.md)。对接方需适当身份及短期 PAT、API 地址、权限范围、平台 ID、创建幂等键和指标约定。未颁发或发送真实凭据。

## 验证结果

实际编译、测试、lint、构建均在构建机；本地用于编辑、diff 审阅和候选提交。

| 检查 | 结果 |
| --- | --- |
| 全部 Go 包 | `go test -timeout=20m -p 4 ./...` 通过 |
| 新增 Go 文件覆盖率 | **900/1059 = 85.0%**；REST 83.9%、SDK 87.4%、service 88.5%、adapter 83.0%、存储 80.0%/81.0%；选定四个历史包总体 72.3%，不声称全仓达到 80% |
| 真实 PostgreSQL | 新装、重复迁移、schema 45 旧数据升级与配额/归属保留通过；8 个独立连接竞争创建/租约通过；修复四项旧 artifact 测试的当前成员归属 fixture，未放宽生产约束 |
| 真实 MLflow | 固定线上同版镜像、隔离内部 Docker 网络、临时数据库；创建、查找、log、read、finish、重复 finish 通过 |
| 官方 SDK | 实际 MLflow 3.14.0 Python SDK 经适配层读 Run、写参数/指标/标签、log_batch、set_terminated、读回终态通过；测试鉴权 fixture，不是生产 PAT 验收 |
| Python 示例 | 27/27 通过；client 97.49%、external_tracking 96.20%、合计 96.92% |
| OpenAPI | validator 通过；85 个本地引用可解析；21 个合法和 74 个非法 SDK 请求 schema 用例通过 |
| Portal | 完整 Dockerfile.lint 门禁、生产编译通过；5 个 Playwright 场景通过（20.5 秒），包含能力门控、外部实验分页/详情、平台与上游 ID 分离；接口为隔离 mock |
| 审阅 | 后端/前端与安全独立复审，无剩余 HIGH/CRITICAL |

测试中发现并修正：SDK 的 legacy `run_uuid`、上游终态冲突对账、显式结束时间保留、PG 0045 归属外键 fixture，以及 OpenAPI 悬空引用。SDK 工作流与真实数据库均在修正后复验，不将首次失败或 skip 算作通过。

构建机主要证据：

- `/tmp/rtp-mlflow-phaseb-release-tests.log`
- `/tmp/rtp-mlflow-phaseb-release-integration.log`
- `/tmp/rtp-mlflow-phaseb-sdk-release.log`
- `/tmp/rtp-mlflow-phaseb-out/release.cover`
- `/tmp/rtp-mlflow-phaseb-python-final.log`、`/tmp/rtp-mlflow-phaseb-python-final-coverage.json`
- `/tmp/rtp-mlflow-phaseb-openapi-docfix.log`、`/tmp/rtp-mlflow-phaseb-sdk-schema-final.log`
- `/tmp/portal-mlflow-phaseb-lint.log`、`/tmp/portal-mlflow-phaseb-build.log`、`/tmp/portal-mlflow-phaseb-e2e.log`
- `/root/raytrain-release-20260912-phaseb/`：备份与恢复证据、values、overlay、build/upgrade 日志、训练资源前后快照。

## 未完成的验收与能力

1. 登录浏览器实际确认分布式计算 11 个菜单及 Portal CI 三阶段成功；随后自动化卡在菜单，无法取得新版外部实验/API 接入完整页面及登录业务 API 响应。dev Kubernetes API 同时超时，Portal Pod imageID 独立核对未完成。健康 200、CI、mock E2E 与隔离 SDK 测试不能替代这些项目。
2. 对接方实际 PAT 与生产专用测试实验的首次端到端读写尚未进行。普通 PAT 是当前用户/团队权限，不是单实验 grant，也不是 service principal；不能用共享管理员 PAT 代替集成身份。
3. 当前读回最多 20 个指标键、每键 500 点，不是完整历史导出。创建异常通过同键请求对账，没有后台 outbox 自动恢复 worker。
4. 文件上传/下载 API、不可变模型候选、独立评估、模型审批/版本发布、Serving 和集成身份资源授权仍待后续阶段。原生 MLflow 管理界面仍是既有共享入口，不具备本外部 API 的 owner 隔离，也不能作为审批强制生效的证据。
5. 团队专属节点、TAS/闲时抢占、IDC 连接器不在本次变更内。未借本次发布修改这些功能、24 卡配额或个人数据。
