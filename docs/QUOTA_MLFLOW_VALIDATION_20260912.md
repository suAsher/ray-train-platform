# GPU 配额与 MLflow 发布验证记录

日期：2026-09-12（北京时间）。状态：用户明确授权后，Backend 与 Portal dev 均已部署；部分生产浏览器验收尚未完成，详见下表与未完成项。后续文档尾提交不改变已验证业务代码，也不重建镜像。

## 版本与真实状态

| 对象 | 本轮核对结果 |
| --- | --- |
| Backend 已验证业务代码 | `36388441026525ebf199e3e6f36396da801bffc0`；后续至构建提交仅文档变化 |
| Backend 构建时四端 main | `3800b753d3fa23c9a30888af21f8594e6ff05ffe`，本地与构建机工作区干净；发布记录尾提交另行同步 |
| Portal 本地 / 远端 dev | `3c4fb473ab64b59feb0a1e67d3b7ca6e84211b35`；独立仓库 `/private/tmp/raytrain-portal-20260912` |
| 线上 Backend | `release-20260912-01-3800b753`；Helm revision **212**；2/2 ready；healthz 200 |
| 线上 Backend digest | `sha256:3eaa6da5ad3508451fcd2f578c107ccb7f3f3df90f9941e297f7f184507d2713`；两个新 Pod imageID 均匹配，重启数 0 |
| schema / MLflow | schema **45** 未变；MLflow 保持原 3.14.0 镜像，本轮未重建 |
| Portal CI/CD | [pipeline #33846](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33846) 成功；lint #89779、build #89780、[deploy #89781](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/jobs/89781) |
| Portal 部署证据 | deploy 日志记录 checkout `3c4fb473`、release `yuanzhu-he-wellspiking-frontend-master`、namespace `yuanzhu-he`、Helm revision **1040**、新 Pod 与 `Helm deploy succeeded` |
| Portal 镜像独立核实 | dev Kubernetes 管理 API 连接超时，尚未直接取得线上 Pod imageID；CI 成功不替代此项 |

远端使用 `ls-remote` 实时复核，没有覆盖其他人的更新。Backend 先验证候选再推双远端并快进构建机，构建时四端一致。只构建 backend，`--reuse-values` 最小 overlay；server dry-run 与现网 manifest **只有 backend image 一行差异**，随后 `--atomic --wait` 升级成功。未构建 CLI、旧 frontend 或训练镜像，未改 schema、配额、调度、节点或个人数据。

本次部署前后以下 RayJob 均为 RUNNING、UID 相同；5 个 RayCluster、11 个训练 Pod 的 UID、状态和重启数记录 diff 均为空：

| Namespace / Job | UID |
| --- | --- |
| tenant-algorithm / job-33dfc9bf45dd65181bbd05e3 | `d7d65832-7101-4832-bcec-174d3f9e8585` |
| tenant-local / job-3bf795eb68edc5fc850c7ed3 | `24e3a3b2-0ab1-427b-916c-2a39dfbc8a8b` |
| tenant-local / job-94440c922ca159a059aa5430 | `7a9456e2-4ab4-47b0-8296-ccf11189d86d` |

早先候选验证快照中的 `job-1278acfc0cb9cee918cb9b00` 在此次构建/部署前已变为 FAILED，本次使用重新采集的发布前基线，不将两个时点的不同训练集合混为一谈。未提交、删除或重启训练。

## 真实浏览器核验

- 公司 VPN 下 Chrome 能登录 Portal 和内部 GitLab。当前 `guofeng.su` 管理员可见“分布式计算”全部 **11** 个菜单，与用户截图一致：我的训练任务、实验中心、交互式调试、版本化数据集、外部提交、GPU 资源池、GPU 占用明细、平台管理、账户与安全、使用说明、数据与存储。没有据此声称每个角色都拥有相同菜单。
- 浏览器管理页显示 local 团队配额 24、占用 24；本次未修改配额。“我的 GPU 配额”加入任务列表顶部，明确当前团队共享，不新增第 12 个菜单。
- 动态菜单实际账户路径为 `/raytrain/RayTrainAccountSecurity`。上线前发现新实验中心的硬编码路径与其不符，已改为命名路由 `RayTrainAccountSecurity`，增加真实菜单结构合同并完成 RED → GREEN。
- 新后端上线后，使用当前登录身份 GET `/raytrain/api/v1/jobs/job-94440c922ca159a059aa5430/mlflow/runs/8fb45fa8a7e442ae8b5603d00ce5036e` 返回 `success: true`，Run ID 精确一致、状态 RUNNING，并返回已有训练指标。这是业务成功响应，不是未认证 401 探测。
- 线上实验记录中同一个 `job-167d76a115d1aaa8b1a82e65` 存在多个不同 Run，证明应保持可信的一对多关联，而非强制两个 ID 相等。
- Portal 部署后浏览器自动化辅助功能视图卡在菜单，截图不可用；重新选择应用、取消菜单及重置会话未恢复。未据此宣称新版配额、比较和账户跳转已完成生产交互验收。构建机隔离浏览器回归通过是另一项证据。

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
| Chromium 页面交互 | 最终 **4 passed (14.8s)**：24/8/16 配额及刷新、加载/503 错误、同 Job 不同 Run 精确比较与缺失值、账户动态菜单跳转、读写令牌 scope 请求。API 全部 mock，禁止未模拟业务请求经代理访问生产 |
| 独立审阅 | 后端权限/安全与 Portal 契约独立复审，未发现残留 HIGH/CRITICAL；补充审计失败、错误脱敏、数值边界测试 |

首轮候选 `8bf2e476` 的业务验证通过后，实际浏览器发现账户菜单路径差异，最终 `3c4fb473` 修正两处命名路由及 E2E 菜单 fixture。最终候选重新完成 Dockerfile.lint 全部门禁、prod build 与四项 Playwright 回归，**4 passed (14.8s)**；独立复审未发现 HIGH。先有错误 href 的 RED，再验证新 href 与账户页导航成功。

RED 证据包括首次缺少实现、大小写字段曾被错误接受、单字段 batch 曾输出 null；修正后通过。Chromium 首轮两项失败源于测试点击 Element Plus 隐藏/被遮挡 input，改点可见 label/wrapper 后通过，未使用 force click。截图取动画结束状态。

构建机日志：

- `/tmp/rtp-mlflow-full-test-final-20260912.log`
- `/tmp/rtp-mlflow-real-smoke-final-20260912.log`
- `/tmp/rtp-mlflow-results-20260912/coverage-final.out`
- `/tmp/raytrain-portal-lint-verified-20260912.log`
- `/tmp/raytrain-portal-build-final-20260912.log`
- `/tmp/raytrain-portal-browser-evidence-20260912.log`
- `/tmp/raytrain-menu-route-red-20260912.log`
- `/tmp/raytrain-portal-lint-menu-20260912.log`
- `/tmp/raytrain-portal-build-menu-20260912.log`
- `/tmp/raytrain-portal-browser-menu-20260912.log`
- `/root/raytrain-release-20260912-3800b753/`：受限目录内保存 values 备份、backend overlay、build/upgrade 日志及 jobs/clusters/pods 发布前后记录；临时 manifest 与 dry-run 输出在收尾清理

截图与精简证据也保存在本机 `/private/tmp/raytrain-evidence-20260912`；截图使用 mock 数据，仅用于候选视觉审阅。

## 未完成与下一步

1. Backend 已部署并完成镜像、健康、schema、训练资源保护和登录精确 Run API 验证；Portal 已推送并通过自动部署。发布记录文档随后同步四端，无需重建镜像。
2. 仍需补齐 Portal Pod imageID 独立核对，以及新版配额/搜索比较、任务详情、逐行原生 MLflow 深链、使用说明、Jupyter/VS Code 一次性票据和 403/404 业务消息的完整生产浏览器验收。VPN 网页可达，但 dev Kubernetes 管理 API 超时、浏览器自动化后续失效；不能以 CI 或 mock 测试替代。
3. 外部读写仅覆盖已有 Run 的参数/指标/标签。官方 SDK 全协议、外部创建 Run、Artifact API、Registry API、服务账号委托仍待建设；历史缺少完整归属标签的 Run 不做自动回填。
4. 独立评估、模型审批发布和 Serving 尚未实现。设计为 Job → Run → 显式候选模型包 → 固定数据/代码评估 → 审批 → Registry 版本/别名 → 推理部署或外部发布；不能将 checkpoint 自动当成合规 MLflow Model。
5. 团队专属节点绑定仍待落地；本轮核实 TAS/闲时抢占仍关闭；IDC 同步已启用但连接器和同步记录均为 0。这些不属于本次改动。

所有首次真实写入联调都应使用另外获准的测试 Job/Run，不向现有用户训练灌入演示数据。


## 2026-09-14 实验指标摘要兼容修复

以下为本次修复的实际状态；上文 9 月 12 日的未完成项是历史记录，完整模型生命周期进展另见模型发布验收文档。

- 后端业务候选 `d4242d4083750a2e85e342a3ee82a9a9c9d44b34`：按关键指标优先、其余键稳定排序选取最多 20 项；仍最多查询 20 条指标历史、每条 500 点。没有修改 MLflow Run、训练代码、训练镜像、归属校验或数据库 schema（仍为 52）。
- 后端线上 `release-20260914-02-d4242d4`，Helm revision **230**；实际 digest `sha256:52288f1862275e84ca168b7666b94a3680f95b8591b51f47a185588418636956`。本轮另一次构建索引 `sha256:09eecb5ab231ec427621353d6e074487c008e9eff409fa6cacc85e0da99e1ae9` 与线上共享相同 amd64 manifest `sha256:0e37c876f251070215bef7c272ccce52f46e705c1bfd504c02eec9a1d308260b`，仅 attestation 不同，因此未重复部署。
- Portal dev `c051c01c587a385642b1f0ebde8b039a7a377f52`；[CI 33917](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/33917) lint/build/deploy 全成功。实际 Pod imageID `sha256:889ba429a5f34584dcfc5128c88cd03bd784d1096bce05805a2c39ba4df30b18`，Ready、重启 0；页面资产 `index-OZ38PLr0.js`。
- 构建机 RED 复现超过 20 项后丢关键指标和顺序不稳定；GREEN 完整 `go test ./...` 通过。帮助说明最初插入旧正文触发保留原文测试，改为追加说明后完整门禁通过，未删除保留原文断言。Portal 完整 Dockerfile.lint 与 11 项浏览器回归通过。
- 生产浏览器确认 fef3923、47074168、b990ef4 三条训练显示 Loss/学习率/mAP/NDS；Loss 分别 0.0991、0.1306、0.1155。只含数据集统计的旧 Run 显示“已记录其他指标 · 在 MLflow 查看”，不再误报“尚未上报”。
- 三条任务 experiment API 返回 200，训练 Loss 历史点分别 492、500、156；首条“MLflow 详情”按钮打开正确 experiment 1 / run 708c9c9bc9be41fe9efb5f7cb2b024c6。
- 平台使用说明 `mlflow-framework-metrics` 已包含“有指标却未显示时如何判断”，保留原训练接入内容。后端两副本 Ready、重启 0、healthz 200；revision 229→230 manifest 仅后端镜像变化，记录的训练 UID/重启数未变。
- 边界：SPT Run 10d289d44d544e2aa881debd9ce3fde1 已有指标但没有平台关联标签，本次未擅自改写历史 Run。功能仓自动上传仍处于接口合同确认阶段，本次没有向功能仓上传用户权重。

构建机证据目录：`/root/raytrain-release-20260914-metrics`。本地未跟踪的用户验收 ZIP 保留，未提交。

## 2026-09-16 当前团队训练记录与 GPU 资源池

用户明确范围是普通成员所在的 RayTrain 团队，不是所有团队。平台成员表的当前 tenant 仍是权限依据，不以 Portal 顶部团队名称或 MLflow 可修改标签替代。

### 已上线行为

- 普通成员可选择“当前团队 / 仅我的”任务，Portal 默认团队；实验中心显示当前团队成员的训练 Run、提交人和指标，可打开对应 MLflow 详情。SuperAdmin 保留跨团队能力，页面明确显示“所有团队 / 平台范围”。旧 CLI 未指定 scope 时仍默认个人任务。
- GPU 资源池保留独立菜单；所有交互登录成员可读实时与历史 GPU 指标。普通成员看不到其他团队及无法确认归属的工作负载标识；没有给 PAT 隐式增加资源池权限，也没有开放资源管理操作。
- 同团队可读不等于可操作队友任务。取消、Worker/Ray Dashboard、恢复、模型登记和功能仓同步沿用各自所有者/管理员边界；MLflow 指定 Run 写入仍校验所有者。既有共享原生 MLflow、`mlflow:full` 和 Registry 权限未收紧或扩张。
- 任务及实验展示提交人名称，批量查询仅包含当前返回结果中的用户 ID；缺失时保留稳定 ID。未开放用户目录、迁移历史归属或向旧 Run 补写标签。
- 平台“使用说明”既有登录权限、任务和实验相关文章已更新，无需用户查看仓库文档。没有增加文档分类或管理员操作手册。

### 版本与证据

| 组件 | 发布事实 |
| --- | --- |
| 后端业务提交 | `5633f313507de0f0701ec1b863137ea41cfe0b50` |
| 后端组合构建 | `8168c7c409dae3b793667ea04455f7c64b048d7d`，保留并行任务的 BEVFusion 调试镜像源码提交；本次仅构建 backend |
| 后端镜像 | `release-20260916-01-8168c7c`，digest `sha256:9a4138decdcd27f627221d65db0299136053d9c3532385220458f71c2afa9bc5` |
| 生产状态 | Helm **242**，schema **54**；两副本 Ready、重启 0，`/healthz` 200 |
| Portal dev | `b3601dcf6662fc5c939a11b2b33cc449b09122cf`；[CI 34082](https://gitlab.wellspiking.ai/wellspiking/frontend/wellspiking-frontend/-/pipelines/34082) 的 lint 90339、docker-dev 90340、helm-deploy-dev 90341 全成功 |

构建机先验证候选，后推送和同步正式目录：权限 RED 测试复现原先的同团队 403/404；完整 Go 回归及权限重点 race 测试在业务候选与组合构建上均通过。Portal 完整 lint、RayTrain 合同测试及开发构建通过；lint 为 0 error，保留既有 warnings。普通成员同团队读取、跨团队拒绝、伪造 Run 归属、旧 PAT 和变更操作权限均有隔离测试。审阅未发现阻塞权限问题。

真实浏览器使用用户指定的 `guofeng.su` SuperAdmin 会话：11 个原有菜单保留，GPU 资源池加载 48 张卡和历史曲线；任务与实验显示 `yihan.she` 提交人。`job-a1937fae0256779603d7b10e` 详情正常，实验行“MLflow 详情”打开 experiment `1` / Run `94c4c9a55f924611af77af99c90790dd`，指标页面正常。已登录 team jobs、experiments、GPU 实时及历史 API 均 200；线上使用说明展示新的团队权限说明。

用户选择“先用当前账号，普通角色用隔离测试验证”。因此普通角色 API/前端合同已验证，但没有真实普通账号的 SSO 动态菜单验收证据，不能用管理员浏览器冒充。后端权限是最终边界；本次未修改 Portal 动态菜单服务配置。

Helm dry-run 仅一处后端镜像变化；发布前后记录的 RayJob、RayCluster、训练 Pod UID、状态与重启数无差异。本次无数据库迁移，未创建验收身份或 PAT、提交训练、修改配额/调度/存储。独立旧前端、CLI 与训练镜像未构建或发布。后续仅验收文档提交，无需再次部署。

受限构建机证据目录：`/root/raytrain-release-20260916-team-read`，包含 RED/GREEN、`full-test.log`、`combined-test.log`、Portal 测试与构建日志、镜像构建、Helm diff、前后资源快照、最终两副本及 schema 核验。用户原有未跟踪 ZIP 保留。
