# GPU 配额与 MLflow 候选验证记录

日期：2026-09-12。状态：本地候选已开发验证，尚未推送或部署。本文之后若仅追加文档提交，业务代码的已验证提交仍以下表为准。

## 版本与真实状态

| 对象 | 本轮核对结果 |
| --- | --- |
| Backend 已验证代码 | `36388441026525ebf199e3e6f36396da801bffc0`；本地 main 上的候选 |
| Backend GitHub main | `e25e983e17aa4b7dfcc56fefa41a816563d906d7` |
| Backend 内部 GitLab main | `e25e983e17aa4b7dfcc56fefa41a816563d906d7` |
| 构建机正式 main | `e25e983e17aa4b7dfcc56fefa41a816563d906d7`，工作区干净 |
| Portal 本地 dev | `8bf2e476dfb7f3e8e19b21a4dbff5fa0696a6173`；独立仓库 `/private/tmp/raytrain-portal-20260912` |
| Portal 远端 dev | `9bca129581a36070b5e1c301c4b9b3a6680a1b91` |
| 线上 Backend | `release-20260911-12`；Helm revision 211；2/2 ready；healthz 200 |
| 线上 Backend digest | `sha256:93bc46b59db697175971d86e85791f1b0199c1e986532bbc5cbbb71c1a1043c7` |
| schema / MLflow | schema 45；MLflow 3.14.0、2/2 ready |

远端使用 `ls-remote` 实时复核，没有用旧 tracking ref 代替。四端目前有明确差异，不能称作同步完成。候选未产生生产镜像、Helm revision 或 Portal CI pipeline；Portal dev 推送本身会触发自动构建部署。

本轮开始和收尾时以下 RayJob 均为 RUNNING，UID 相同：

| Namespace / Job | UID |
| --- | --- |
| tenant-algorithm / job-1278acfc0cb9cee918cb9b00 | `0b0cf1c3-c7ca-4a74-b8dc-06d1f0241bd9` |
| tenant-local / job-3bf795eb68edc5fc850c7ed3 | `24e3a3b2-0ab1-427b-916c-2a39dfbc8a8b` |
| tenant-local / job-94440c922ca159a059aa5430 | `7a9456e2-4ab4-47b0-8296-ccf11189d86d` |

没有部署、提交训练、修改配额/调度、迁移/删除个人数据。本轮不涉及数据库迁移、CLI、旧 frontend 或训练镜像。上述只读核对不能替代未来部署前后的 RayCluster、Pod UID 和重启数对比。

## 完成的行为

- 新 Portal 任务列表展示“我的 GPU 配额”：当前 RayTrain 团队总额、占用、剩余，明确是共享额度；未知和请求失败不会显示成 0。现有 local 24 卡配置未改。
- 实验中心增加已加载记录搜索、状态过滤、2–4 Run 对比，显示和复制 Job/Run ID，保留原生 MLflow 与训练产物入口。按原始指标键对齐最新标量及参数，缺失为“—”；本轮未实现曲线叠加。
- 新增指定 Run 读取与 log-batch 写入 API。写入只允许同时具有 `jobs:read`、`mlflow:write` 的 PAT，且是当前团队本人任务的已有 RUNNING Run；管理员不能代写。校验平台 Job、实验、归属标签与 HMAC provenance。
- 保留旧前端/CLI 的默认 PAT scope 和旧任务最新实验接口；旧 Run 缺少历史 tenant/owner 标签仍可在其他关联校验通过时读取，不能通过新接口写入。
- 严格 JSON、字段边界、限流、超时、写前/写后审计和安全错误映射；单字段 batch 不向 MLflow 发送 null 数组。
- 账户与安全页可选择训练、实验只读、实验读写令牌用途；六项 MLflow 能力展示真实入口及未完成边界。

接口交付见 [MLflow 外部对接说明](MLFLOW_INTEGRATION_API.md)；产品生命周期见 [设计](superpowers/specs/2026-09-12-quota-mlflow-design.md)。未创建或发送真实令牌。

## 验证证据

全部测试、格式化、lint、编译均在构建机运行。本机只编辑、审阅与 `git diff --check`。

| 验证 | 结果与范围 |
| --- | --- |
| 后端全量测试 | 固定 release Go 1.25 Alpine digest，安装 bash/git/build-base/jq，整个 detached worktree 只读挂载；`go test -timeout=20m -p 4 -coverprofile=… ./...` 全部通过 |
| 新增接口覆盖率 | 四个新增生产文件 `api/mlflow_integration{,_decode}.go`、`observability/mlflow_integration{,_batch}.go` 合计 **252/270 = 93.33%**；全仓历史基线约 72%，未宣称全仓达到 80% |
| 真实 MLflow | 使用线上相同 3.14.0 镜像 digest 的独立 Docker 服务、内部网络、临时 SQLite/Artifact；最终 smoke 通过（0.245s）。验证参数/指标/标签单独写入并回读、指定旧 Run、旧标签 Run 拒写、FINISHED 拒写。没有访问生产 MLflow 数据 |
| Portal 完整 CI 门禁 | `docker build --pull -f docker/Dockerfile.lint .` 通过；lint 0 errors、1450 warnings（既有仓库警告）；EP、store、路由、日志导出、访问、新增 experience 与 token-purpose 合同均通过 |
| Portal 编译 | `pnpm build:prod` 通过；隔离容器 8 GiB、Node heap 6 GiB。首次默认堆上限导致 OOM，调整测试容器限制后通过；没有改业务代码规避编译 |
| Chromium 页面交互 | 最终 **4 passed (13.6s)**：24/8/16 配额及刷新、加载/503 错误、同 Job 不同 Run 精确比较与缺失值、读写令牌 scope 请求。API 全部 mock，禁止未模拟业务请求经代理访问生产 |
| 独立审阅 | 后端权限/安全与 Portal 契约独立复审，未发现残留 HIGH/CRITICAL；补充审计失败、错误脱敏、数值边界测试 |

Portal lint 门禁对应 `5d381e6a3864907b424e6784ea8f314e7dfe0b5c`；prod build 对应 `3307257f7b619e3b1630b2f3e3cae8bcc486775b`。两者与最终 `8bf2e476` 的产品源码、配置、依赖完全相同，后续仅修改 E2E 文件的可见控件选择器和截图动画选项；最终 E2E 已重跑。没有把后续测试文件修改误报为产品变更。

RED 证据包括首次缺少实现、大小写字段曾被错误接受、单字段 batch 曾输出 null；修正后通过。Chromium 首轮两项失败源于测试点击 Element Plus 隐藏/被遮挡 input，改点可见 label/wrapper 后通过，未使用 force click。截图取动画结束状态。

构建机日志：

- `/tmp/rtp-mlflow-full-test-final-20260912.log`
- `/tmp/rtp-mlflow-real-smoke-final-20260912.log`
- `/tmp/rtp-mlflow-results-20260912/coverage-final.out`
- `/tmp/raytrain-portal-lint-verified-20260912.log`
- `/tmp/raytrain-portal-build-final-20260912.log`
- `/tmp/raytrain-portal-browser-evidence-20260912.log`

截图与精简证据也保存在本机 `/private/tmp/raytrain-evidence-20260912`；截图使用 mock 数据，仅用于候选视觉审阅。

## 未完成与下一步

1. 尚未授权执行并完成本候选的远端推送/生产发布；需要再次复核远端、同步 backend 四端、仅构建 backend，再按 release skill 完成最小 Helm dry-run 和运行训练保护；Portal 推 dev 会自动部署。
2. Portal dev Kubernetes API 本轮连接超时，未核实线上 Portal 镜像，未完成真实登录、原生 MLflow/编辑器票据和生产 API 联调。mock 浏览器通过不等于这些验收通过。
3. 外部读写仅覆盖已有 Run 的参数/指标/标签。官方 SDK 全协议、外部创建 Run、Artifact API、Registry API、服务账号委托仍待建设；历史缺少完整归属标签的 Run 不做自动回填。
4. 独立评估、模型审批发布和 Serving 尚未实现。设计为 Job → Run → 显式候选模型包 → 固定数据/代码评估 → 审批 → Registry 版本/别名 → 推理部署或外部发布；不能将 checkpoint 自动当成合规 MLflow Model。
5. 团队专属节点绑定仍待落地；本轮核实 TAS/闲时抢占仍关闭；IDC 同步已启用但连接器和同步记录均为 0。这些不属于本次改动。

所有首次真实写入联调都应使用另外获准的测试 Job/Run，不向现有用户训练灌入演示数据。
