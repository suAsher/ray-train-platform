# GPU 配额入口与 MLflow 集成设计

日期：2026-09-12。状态：候选开发；尚未推送或部署。用户本轮要求先核实，再说明方案与影响范围并开发验证；对接方已明确需要读取和写入。

## 核实结果

- 后端本地、GitHub、内部 GitLab、构建机 main 均为 `e25e983e17aa4b7dfcc56fefa41a816563d906d7`，本地与构建机干净。
- 生产 Helm revision 211，backend 2/2，摘要 `sha256:93bc46b59db697175971d86e85791f1b0199c1e986532bbc5cbbb71c1a1043c7`；健康接口 200。
- Portal 权威 dev 为 `9bca129581a36070b5e1c301c4b9b3a6680a1b91`。本轮独立 checkout 位于 `/private/tmp/raytrain-portal-20260912`，未修改旁边 master。
- Portal 创建任务页与管理员控制台已有配额，任务列表缺少可见入口。后端 `GET /api/v1/quota` 提供当前团队的权威额度，含训练和活跃调试环境占用。
- MLflow 3.14.0 deployment 2/2。已有实验列表和默认任务实验查询；默认查询取最新 Run，不能用于指定历史 Run 的精确比较。
- 原生 Dashboard 是共享管理界面，通过一次性票据和 Cookie 访问；训练网关只开放受限 Tracking 路由。两者均不是可直接发给第三方的 PAT API。
- 当前 3 个 RUNNING 训练；TAS/preemption 关闭，IDC sync 开启。Portal dev Kubernetes API 本轮连接超时，线上 Portal 镜像及浏览器验收仍待核实。

## 选择的方案

采用平台实验中心、原生 MLflow 详情和任务绑定 API 三层：平台负责身份/归属及用户工作流，MLflow 保存实验记录，训练存储仍保存原始产物。只增加原生链接无法解决配额与 ID 关联；重写完整 MLflow UI 会重复其维护成本；全量对外代理则会把原生共享权限扩大到程序调用，因此本轮不采用这些方案。

### 我的 GPU 配额

放在训练任务列表的统计区之前，标题为“我的 GPU 配额”，副标题明确“当前团队共享”。从 `/quota` 读取 `tenantId/gpuLimit/gpuUsed/gpuAvailable`，展示分配额度、已占用与剩余额度。剩余额度不保证立即调度，也不是机器实际空闲卡数。请求失败或未加载显示未知，不显示虚假的零。团队变化立即清除旧状态；重叠请求只采纳最新响应。保留创建页和管理员入口，不引入配额修改操作。

### 实验中心

保留组件名、路由、原生票据跳转与共享视图提示。增加当前已加载记录的搜索（实验/Run/Job 名称、ID），明确查询窗口，支持选择 2–4 个 Run 对比。对比用精确 Run API 读取参数和指标；原始指标键逐项对齐，缺失显示“—”，不把验证 loss 当训练 loss，也不把不同数据集的结果称为可直接比较。列表同时显示和复制 Job ID/Run ID，并链接原任务与产物入口。

一个 Job 可以有多个 Run（重试、重复埋点、不同尝试），一个模型版本又是独立实体。不要重命名 run_id 为 job_id，也不要批量改写历史归属。新接口按平台 job 记录与可信标签核对指定 Run。

### 对外读写第一阶段

- 继续使用 `GET /api/v1/experiments` 获取当前身份可见的 Job/Run 映射（默认最近 50、上限 100，非完整分页导出）。
- 新增 `GET /api/v1/jobs/:id/mlflow/runs/:runId`：PAT `jobs:read`；保持既有任务所有者/团队管理员读取规则；返回 `JobExperiment`（experimentName、run、series）。
- 新增 `POST /api/v1/jobs/:id/mlflow/runs/:runId/log-batch`：仅 PAT，新增独立 `mlflow:write` scope，仅当前团队本人任务的 RUNNING Run；写 params、metrics、自定义 tags。旧 PAT 不自动获得此权限，CLI 默认令牌范围不变。
- 每次先查平台 DB，再核验目标实验、Run ID、Job/tenant/submitter 标签及 provenance。客户端不能指定或覆盖 `platform.*`、`mlflow.*` 系统字段。
- 严格校验 JSON、大小、数量、有限数值、整数时间戳与非负 step；限制请求频率与总超时；写前审计失败必须阻止写入，审计不记录令牌/参数值；上游失败只暴露安全业务错误。
- 不创建新 Run，不更改 Run/Job 状态，不上传文件，不开放任意代理路径。这样外部集成不会改变旧详情页选择的最新 Run，也不会中断训练。
- 这是平台 REST 接口，不是完整官方 MLflow SDK Tracking URI。交付 base URL、PAT 申请/撤销、scope、团队、Job/Run 示例、请求/响应、错误与重试语义。业务重试必须固定 metric 的 key/step/timestamp；不承诺 exactly-once。

### 六项能力的产品组织

| 能力 | 第一阶段展示/可用入口 | 后续完整工作流 |
| --- | --- | --- |
| Experiment Tracking | Run 列表、参数/指标、精确比较和任务关联；训练仍需主动埋点 | 受控外部 Run 创建与幂等关联、代码/数据版本规范 |
| Artifact Management | 平台训练产物入口；原生 MLflow 小附件是独立存储 | 从本人已完成产物选择文件，校验摘要/大小，显式复制发布、审计；不移动个人数据 |
| Model Registry | 原生管理已有合规模型版本；不自动扫描 checkpoint | 候选模型包含 MLmodel、signature、依赖、代码/数据版本、来源 Job/Run；团队命名空间 |
| Model Evaluation | 对比已上报的原始指标；不等于独立评测任务 | 固定评测数据版本、评测代码与指标合同，独立 CPU/GPU 作业，产出不可变报告 |
| Model Serving | 明确尚无平台一键部署 | 仅审批模型创建独立推理 Deployment/资源额度、健康探针、鉴权和灰度；显式授权才启动 |
| UI | 平台归属视图与原生共享详情各保留入口 | 审批/发布记录、生产别名受控提升、失败重试与回滚 |

目标生命周期：训练 Job → 一个或多个 Run → 显式候选模型包 → 固定评测版本上的独立评估 → 审批 → Registry 版本/别名 → 推理部署或外部发布。失败可追溯、发布可撤回、原始训练产物不被迁移或删除。审批与 Serving 是后续建设，不作为本轮上线成果。

## 影响、验证与发布边界

本轮组件为 Portal RayTrain 与 backend 控制面；不需数据库迁移，不构建 CLI/训练环境/旧 frontend，不调整队列、节点、配额、存储或已有任务。先在构建机验证测试失败和实现后的通过，跑完整后端测试及 Portal Dockerfile.lint 门禁、新增合同、前端编译。新增权限和写接口须经独立安全审阅。

发布以本轮明确授权为准。候选完成后重新查询所有远端防并发覆盖；backend bundle 隔离验证后才能推双远端并快进构建机；Portal dev 推送会自动部署，必须单独记录 push/CI/线上镜像/登录验收。若未获推送/部署授权，交付可审阅候选及验证证据，不把候选版本称为线上版本。
