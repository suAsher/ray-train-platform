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
