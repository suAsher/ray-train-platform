# RayTrain 入口、认证、调度与多团队治理设计

## 背景与当前约束

RayTrain 正处于前端迁移期：旧独立前端仍由
`raytrain.wellspiking.ai` 提供，新 Portal 在
`spiking-dev.wellspiking.ai/raytrain` 验证，正式入口计划为
`spiking.wellspiking.ai/raytrain`。与此同时，训练提交、Ray 原生命令、上传下载和
MLflow 代理仍依赖稳定的 `raytrain.wellspiking.ai` API。

因此不能把“前端迁移”“浏览器认证迁移”和“API 域名迁移”视为同一件事。本设计要求
每个阶段都保持旧 UI、Portal、CLI 和正在运行的训练任务可用，并且可以单独回滚。

当前集群继续采用 KubeRay 1.6.2、Ray 2.58 和 Kueue 0.19。GPU 节点以 8 卡
RTX 4090 为主，未来会加入 A100、A800、H20 等卡型。当前单一 ClusterQueue、单一
ResourceFlavor 且没有 Topology Aware Scheduling，导致 1 卡和 4 卡任务被 kube-scheduler
分散到多个空闲节点，剩余 GPU 形成 7/4/7 一类碎片，后续 8 卡或 16 卡任务无法准入或落盘。

## 设计目标

1. 旧 UI 和 Portal 在迁移窗口内并存，且调用同一套后端能力。
2. OAuth2 Proxy 负责身份认证，RayTrain 负责成员、团队、角色、配额和资源归属。
3. 调试环境、任务产物和 MLflow 的浏览器代理在有界令牌下工作，不因通用 API
   middleware 重复拦截而失败。
4. 小任务优先在节点内紧凑放置，保留完整 8 卡节点供大任务使用。
5. 不同 GPU 型号绝不在一个分布式任务中混用；团队可借用同卡型空闲配额，但使用行为
   可审计、可限制。
6. 一个身份可加入多个团队，且每个团队拥有独立角色；个人数据不随团队切换漂移。
7. 所有改造均不得重启、重建或修改正在运行的 RayJob、RayCluster 和训练 Pod。

## 不在本期范围

- 不引入 Volcano；现有 Kueue 已覆盖 RayJob 准入、拓扑、队列借用和抢占需求。
- 不将代码重新放入训练镜像。
- 不自动迁移或删除历史个人数据、团队数据、数据集、任务或产物。
- 不在没有 checkpoint/restart 契约的普通训练任务上启用强制抢占。
- 不立即把 `raytrain.wellspiking.ai` 的网页根路径重定向到 Portal。

## 一、入口与兼容边界

### 1.1 稳定入口

| 使用场景 | 迁移期入口 | 最终入口 |
| --- | --- | --- |
| 旧独立 UI | `https://raytrain.wellspiking.ai/` | Portal 验收前保留 |
| Portal UI | `https://spiking-dev.wellspiking.ai/raytrain/` | `https://spiking.wellspiking.ai/raytrain/` |
| REST API / PAT | `https://raytrain.wellspiking.ai/api/...` | 保持不变 |
| Ray 原生提交 | `https://raytrain.wellspiking.ai/ray/...` | 保持不变 |
| CLI 下载 | `https://raytrain.wellspiking.ai/downloads/...` | 保持不变 |
| MLflow 代理 | `https://raytrain.wellspiking.ai/mlflow/...` | API 域名保持不变；Portal 可经前缀代理访问 |

Portal 内部 API 继续使用同源 `/raytrain/api/...`，由 Portal Ingress 精确代理到稳定 API。
只允许以下前缀进入后端：`api/`、`ray/` 和 `mlflow/`。不得使用宽泛的
`/raytrain/(.*)`，以免吞掉 Portal 自己的前端路由和静态资源。

迁移完成前不配置旧域名网页跳转。最终切换时也只重定向 UI 根路由和 `/login`，明确排除
`/api`、`/ray`、`/downloads`、`/healthz`、`/mlflow`。

### 1.2 前端 URL 兼容器

Portal 增加唯一的浏览器导航 URL 解析函数：

- 后端返回 `/api/v1/...` 时，在 Portal 同源环境转换为 `/raytrain/api/v1/...`；
- 后端返回 `/mlflow/...` 时转换为 `/raytrain/mlflow/...`；
- 完整 HTTPS 外链只允许受信 host；
- 禁止 `javascript:`、协议相对地址和任意跨域跳转；
- 旧前端保持根路径行为，不修改后端返回格式。

这样后端契约保持稳定，迁移后的前端仅在浏览器导航边界适配部署前缀。

## 二、认证与权限迁移

### 2.1 过渡态

在旧 UI 尚未由 OAuth2 Proxy 保护并完成登录跳转前，后端使用双栈兼容：

- `OAUTH2_PROXY_AUTH_ENABLED=true`；
- `LOCAL_AUTH_ENABLED=true`；
- OAuth2 Proxy 令牌、已有本地 session 和 PAT 都可通过相应验证器；
- 原始 `X-Auth-Request-User`/`Groups` 不能单独作为可信身份，必须验证转发的 access token；
- 本地登录仅用于旧 UI 过渡，不创建新的日常本地用户。

只有当正式 Portal、OAuth2 Proxy Ingress、CLI/PAT、调试和管理流程全部验收后，才单独发布
`LOCAL_AUTH_ENABLED=false`。关闭前必须验证旧域名不会再把用户引入本地 `/login`。

### 2.2 权限权威

OAuth2 Proxy 只证明“这个人是谁”，不得直接决定 RayTrain 的 SuperAdmin、TenantAdmin、
Engineer 或配额。后端数据库仍是权限与归属的唯一权威：

- 首次 OAuth 登录可 JIT 创建普通成员；
- 默认只授予配置的默认团队中的 `Engineer`；
- 永不从 OAuth group 自动提升 SuperAdmin；
- `guofeng.su` 保留显式 SuperAdmin；
- 禁用或退役身份不得被 JIT 自动复活；
- PAT 必须绑定签发时的团队，不能继承浏览器后来切换的团队。

### 2.3 多团队成员模型

现有 `local_users.tenant_id + roles` 的单团队模型保留为迁移兼容视图，新建不可变身份和成员关系：

```text
Identity
  id, provider, external_subject, username, email, storage_key, disabled_at

TenantMembership
  identity_id, tenant_id, roles[], status, created_at, updated_at

PersonalAccessToken
  identity_id, tenant_id, scopes, expires_at
```

关键规则：

- `storage_key` 属于 Identity，个人目录不会因活跃团队改变；
- 任务、团队数据、配额、队列和团队产物属于请求中的 active tenant；
- 浏览器会话保存 active tenant，用户只能切换到自己具有 ACTIVE membership 的团队；
- 管理员按团队分配角色；SuperAdmin 为全局角色，不复制到每个 membership；
- 历史 `tenant_id` 原样迁移为第一条 membership，不移动任何历史资源；
- 内部 tenant ID `local` 暂不重命名，UI 可单独显示正式团队名称。

所有写请求必须从已验证 principal 得到 active tenant，不能接受请求体覆盖租户。

## 三、浏览器代理与任务详情修复

### 3.1 调试环境

调试访问流程保持两段式：

1. 已认证用户调用 access API，后端检查 owner/管理员权限并签发短期、workspace-scoped token；
2. 浏览器携带该 token 访问 VS Code/Jupyter 代理，代理自行验证 token 并交换为 path-scoped cookie。

当前 workspace proxy 被注册在通用 protected group 内，导致第二步在执行 token 交换前先被 OAuth
middleware 拒绝。修复要求将代理路由移到通用认证 middleware 之外，与 Ray Dashboard 和 MLflow
代理的设计一致；代理自身必须继续验证目标 workspace、有效期、签名和路径，不允许变成匿名代理。

新增集成测试覆盖：合法 token、过期 token、篡改 token、错误 workspace、无 token，以及 Portal
前缀下的导航 URL。

### 3.2 Artifacts

任务详情查询已经通过统一的 `jobForPrincipal` 支持 SuperAdmin 跨团队查看，但 artifact handler
仍使用当前 tenant 的 repository lookup，导致“任务详情 200、artifacts 404”的语义不一致。

后端先使用统一任务可见性查找任务，再单独应用 artifact 授权：

- owner 可列举和下载自己的个人产物；
- 团队共享产物按团队角色授权；
- SuperAdmin 默认可查看产物元数据，但个人 checkpoint 下载仍需显式审计策略；
- 不可见任务返回 404，任务可见但产物无权访问返回 403；
- Portal 对非 owner 且不可下载的个人产物不发起列表请求，显示明确权限说明。

### 3.3 MLflow

迁移后的任务详情补齐“打开 MLflow”操作，但只有后端确认任务具有关联 Run 时显示。流程为：

1. Portal 请求 job-scoped MLflow dashboard access；
2. 后端检查任务可见性并签发短期 token；
3. 浏览器通过安全 URL 解析器打开 `/raytrain/mlflow/...`；
4. Ingress 只把 `mlflow/` 前缀代理到 RayTrain 后端，由后端再代理 MLflow。

平台任务 ID 与 MLflow Run ID 保持显式关联，不强制相同。未接入 MLflow 的训练代码显示“未上报
MLflow”，不得伪造链接。

## 四、GPU 调度与防碎片

### 4.1 保留 Kueue

不引入 Volcano。Kueue 已管理 RayJob 准入，新增调度器会制造双重准入、队列状态分裂和额外运维面。
本期在 Kueue 内使用 ResourceFlavor、ClusterQueue/Cohort、PriorityClass 和 Topology Aware
Scheduling（TAS）。

### 4.2 卡型隔离

每个 GPU 型号建立独立 ResourceFlavor，并用节点不可变标签选择：

```text
gpu-rtx4090 -> accelerator=nvidia-rtx-4090
gpu-a100    -> accelerator=nvidia-a100
gpu-a800    -> accelerator=nvidia-a800
gpu-h20     -> accelerator=nvidia-h20
```

任务必须解析为一个确定卡型；head 继续 CPU-only，worker 的全部 GPU pod 使用同一 flavor。未指定时，
平台可按团队允许的默认卡型解析，但解析结果要固化在任务记录，禁止一个任务跨型号拼卡。

### 4.3 紧凑放置

为 GPU flavor 配置 hostname 级 TAS：

- 单个多 GPU worker 必须在一台节点满足其 GPU 请求；
- 多 worker 分布式任务以所需拓扑域为整体准入；
- 未指定特定节点的 1/2/4 卡任务采用 least-free-capacity/binpack 语义，优先填充已使用节点；
- 8 卡任务优先获得完整空闲节点；16 卡任务需要两个满足同卡型的完整拓扑域；
- Ray head 不申请 GPU，可与 worker 共节点，但不得通过 head 占用 GPU 配额。

上线前先创建 shadow flavor/queue 并用无 GPU dry-run/准入测试验证 rendered workload；切换只影响新提交任务，
不修改已经 admitted/running 的 Workload。

### 4.4 团队配额、借用和抢占

每个团队在每种卡型下拥有独立 ClusterQueue，属于同卡型 Cohort：

- nominal quota 表示保证配额；
- borrowing limit 控制可借用的空闲卡；
- lending limit 保留团队的关键容量；
- 不同卡型不在同一个 Cohort 借用；
- 管理后台分配配额时由后端协调器生成/更新 Kueue 对象，不要求管理员手改 YAML。

定义三档优先级：`production`、`normal`、`opportunistic`。只有用户显式选择、且训练声明可从 checkpoint
恢复的 opportunistic 任务允许被抢占。普通和生产训练默认不可抢占。抢占事件、被抢占任务和恢复点必须
进入审计与任务时间线。

物理节点绑定团队只作为“专属池”可选策略：使用独立 pool label 和 flavor。常规团队隔离采用队列配额与
Cohort 借用，避免空闲专属节点浪费。

## 五、API 与 UI 能力

后端分阶段增加：

- `/api/v1/me/memberships`：当前身份可用团队、各团队角色和 active tenant；
- `/api/v1/me/active-tenant`：切换 active tenant；
- 管理员 membership 增删改接口；
- GPU flavor、团队保证配额、借用上限和 lending limit 管理接口；
- 任务提交的 accelerator class、priority、preemptible、checkpoint capability；
- 调度解释接口：展示准入队列、卡型、拓扑、等待原因和可用/碎片容量。

Portal 后续展示：团队切换器、成员关系、卡型配额、队列/借用、GPU 拓扑和可理解的 Pending 原因。
旧前端在迁移期继续使用单 active tenant 兼容响应，不要求同时实现新管理 UI。

## 六、分阶段发布顺序

### 阶段 A：恢复并修复迁移期访问

1. 保持 OAuth2 Proxy + local session 双栈。
2. 修复 workspace proxy middleware 顺序和 Portal URL 前缀。
3. 统一 artifact 任务可见性与产物授权语义。
4. 补齐 Portal MLflow 链接及精确 Ingress 前缀。
5. 后端先兼容上线，Portal CI 通过后再推 dev。

### 阶段 B：正式 UI 入口

1. 在 `spiking-dev` 完成普通用户、SuperAdmin、跨团队只读、调试、artifact、MLflow、上传下载验收。
2. 创建 `spiking.wellspiking.ai/raytrain` 正式 Ingress。
3. 暂不重定向旧域名；观察期结束后另行审批 UI-only redirect。
4. 最后单独关闭 local auth。

### 阶段 C：Kueue 防碎片和多卡型

1. 采集现有 node label、ResourceFlavor、ClusterQueue、LocalQueue 与 Workload 基线。
2. 增加卡型 flavor、TAS 和 shadow queue，验证渲染与准入。
3. 新任务灰度切换，观察 1/4/8/16 卡排布；运行中任务不迁移。
4. 再启用团队 Cohort 借用和 opportunistic 抢占。

### 阶段 D：多团队成员

1. 先增加 schema 和双读，不改变现有 principal。
2. 将每个现有用户的 tenant/roles 回填为第一条 membership，并核对数量和摘要。
3. 启用 active tenant 与 team-bound PAT。
4. Portal 上线团队切换和成员管理。
5. 稳定后停止写旧 tenant/roles 字段；兼容读保留一个发布周期。

## 七、测试、安全与回滚

每阶段必须在构建机执行单元、集成和相关 E2E 测试；本机不编译、不构建镜像。发布前执行 Helm
server-side dry-run，并保存当前 values、manifest、RayJob、RayCluster、Workload 和队列基线。

安全验收包括：

- 伪造 OAuth headers 不能登录；
- JIT 永不自动获得管理员角色；
- active tenant 不能越权切换；
- workspace/MLflow token 必须短期、资源级、路径级并防篡改；
- artifact 不能因 SuperAdmin 全局任务可见而意外泄露个人 checkpoint；
- 调度参数只能映射到管理员登记的 flavor/priority，不能注入任意 nodeSelector；
- 抢占必须有显式用户声明、平台策略和审计记录。

每阶段独立回滚：

- 阶段 A 可回滚后端/Portal，不撤销数据库向前兼容迁移；
- 阶段 B 可删除新正式 Ingress，API 域名保持不变；
- 阶段 C 可让新任务重新指向旧 LocalQueue，不触碰运行中 Workload；
- 阶段 D 可关闭 active-tenant feature gate，恢复第一 membership 兼容读取。

## 八、完成标准

- 旧 UI 与 Portal 同时可登录和查看其授权任务；旧域名没有未经批准的重定向。
- 调试环境可从 Portal 和旧 UI 打开，非法代理 token 全部拒绝。
- 任务详情不再出现错误的 artifact 404；MLflow Run 存在时可一键打开。
- 1/2/4 卡任务紧凑放置，完整节点尽可能留给 8/16 卡任务；同一任务不混用 GPU 型号。
- 团队配额可由平台直接管理 Kueue，借用、保留和抢占行为可解释、可审计。
- 一个用户可加入多个团队，角色按团队生效，个人数据路径和历史资源归属不变。
- 后端本地、GitHub、内部 GitLab、构建机代码一致；Portal `dev` 分支 CI 和部署均通过。
