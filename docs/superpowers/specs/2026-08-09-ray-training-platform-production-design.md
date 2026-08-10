# Ray 分布式训练平台生产版设计

日期：2026-08-09  
版本：V1 Production Baseline

## 1. 目标与边界

建设一套运行在火山云 VKE 上的 Ray 分布式训练平台，提供 Web Portal、CLI/Python SDK 和管理员入口，支持：

- 训练任务提交、排队、启动、完成、失败、取消、重试和资源回收。
- 基于 Kueue 的 GPU/CPU/内存配额、优先级和全有全无准入。
- 基于 KubeRay RayJob 的批量训练，基于 RayCluster 的交互式调试。
- 任务状态、Pod 拓扑、实时日志、GPU 指标、训练指标和事件审计。
- TOS 数据集/Checkpoint/模型产物、NAS/PVC 工作区、本地 NVMe 缓存。
- SSO/OIDC、租户隔离、RBAC、NetworkPolicy、密钥隔离和私网访问。
- 当前容量为 3 台 RTX 4090×8 训练节点 + 1 台独立调试节点，后续可以增加 GPU 节点或 GPU 类型。

V1 不实现 RayService 模型在线服务、多集群调度和自定义 Training Operator；这些能力预留接口，但不进入第一版生产闭环。

“生产可用”定义为：代码和 Helm 配置具备生产基线，且必须通过目标 VKE 集群的真实提交、故障、NCCL、存储和权限验收后才能标记为生产。

## 2. 设计原则

1. Kubernetes/KubeRay 状态是任务运行事实，PostgreSQL 保存平台元数据、期望状态、状态快照和审计。
2. Kueue 负责资源准入和队列，KubeRay 负责 RayJob/RayCluster 生命周期，平台不重复实现调度器。
3. API 与控制器分离：API 保持无状态，Reconciler 负责最终一致性和重试。
4. 所有外部输入在 API 边界校验；模板、镜像、数据源和资源规格使用版本化 Schema。
5. 成功任务自动释放资源，失败任务短时间保留用于排障，所有保留策略可配置。
6. 训练结果可复现：固定镜像 digest、Git commit、数据版本、依赖锁文件和训练参数。
7. 控制面不保存 TOS 长期密钥；使用 Kubernetes Secret、短期 Token 或火山云工作负载身份。

## 3. 目标架构

### 3.1 组件分层

```text
用户 / CLI / SDK
        |
私网 ALB + TLS + OIDC
        |
Vue Portal ---- Go API Deployment (2 replicas)
                         |
                    PostgreSQL
                         |
              Go Reconciler Deployment
              (leader election, informer/watch)
                         |
                  Kubernetes API
                         |
             Kueue + KubeRay Operator
                         |
        RayJob -> RayCluster -> Head / Worker Pods
          |                         |
   Training GPU Pool             Debug GPU Pool

TOS / NAS-PVC / Local NVMe    Alloy -> Loki
Prometheus + DCGM + Ray Metrics -> Grafana / Alertmanager
```

### 3.2 API Deployment

API 服务只负责请求处理、鉴权、参数校验、数据库事务、查询和向控制器写入期望状态。API 不直接把 Kubernetes 状态当作数据库事实，也不返回模拟数据。

生产部署：

- 2 个或以上副本。
- Readiness、Liveness、Startup Probe。
- PodDisruptionBudget、反亲和性和滚动升级。
- HPA 仅用于 API 请求负载，不用于 Reconciler。
- 所有响应使用统一 Envelope：`success`、`data`、`error`、`request_id`。

### 3.3 Reconciler Deployment

与 API 使用同一 Go 代码库，通过不同启动模式运行。控制器使用 Kubernetes informer/watch 监听：

- RayJob、RayCluster。
- Kueue Workload。
- Head/Worker/Submitter Pod。
- Kubernetes Events。

控制器负责：

- 将数据库中的期望状态转换为 RayJob/RayCluster 资源。
- 将 CR status、Pod 状态和 Events 转换为平台状态。
- 处理 API 重试、controller 重启、resourceVersion 冲突和重复提交。
- 执行取消、超时、重试、失败保留和资源清理。
- 使用 Lease leader election，确保同一时间只有一个 active reconciler。

### 3.4 Portal UI

Vue Portal 是生产交付的必选组成部分，不提供只依赖 kubectl 的替代路径。UI 必须覆盖：

- Keycloak 登录跳转、回调、退出和当前用户/租户展示。
- 训练任务创建向导：代码源、镜像、数据、资源、队列、Checkpoint 和清理策略。
- 任务列表：分页、状态筛选、租户权限过滤、排队时长和资源申请。
- 任务详情：平台/Kueue/RayJob/Pod 多层状态、事件、拓扑、日志、指标、产物和取消/重试。
- Debug Center：工作区启动、停止、Jupyter 访问、空闲倒计时、快照和转训练。
- 管理员页面：租户、队列、配额、用户角色、节点/GPU 健康和审计日志。

生产构建禁止使用网络失败时自动写入的假任务、假日志、假 GPU 或假 Jupyter URL；离线 demo 数据只能由显式 `DEMO_MODE=true` 开启。

### 3.5 Kueue 与 KubeRay

训练任务以 RayJob 为唯一批任务入口：

- 通过 label 绑定 LocalQueue。
- 由 Kueue 控制任务是否 suspend/admit。
- RayJob 创建对应 RayCluster 和 submitter Job。
- 任务结束后按 cleanup policy 删除 RayCluster。
- 模板渲染前通过 CRD discovery 校验已安装 KubeRay 版本和字段，避免 `rayClusterConfig`/`rayClusterSpec` 等版本漂移。
- 不在平台中实现第二套 GPU 调度器。

训练节点和调试节点必须通过 label/taint 分离：

- 训练节点：`workload=training`、`accelerator=nvidia-rtx-4090`。
- 调试节点：`workload=debug`。
- Ray Pod 使用明确的 nodeSelector、toleration、topologySpreadConstraints 和资源 requests/limits。

## 4. 任务模型与状态机

### 4.1 JobSpec

平台 API 接收版本化 JobSpec，不直接接收任意 YAML。下面的 `apiVersion/kind/spec` 是 API 请求 DTO，不表示 V1 必须新增一个 Kubernetes TrainingJob CRD：

```yaml
apiVersion: platform.ray.io/v1alpha1
kind: TrainingJob
metadata:
  name: llama-sft-run-001
spec:
  image: registry.example.com/ray-train@sha256:...
  source:
    type: git
    url: https://git.example.com/team/train.git
    commit: 0123456789abcdef
  entrypoint:
    command: ["python", "train.py"]
    args: ["--config", "configs/sft.yaml"]
  resources:
    workerReplicas: 3
    gpusPerWorker: 8
    cpuPerWorker: 32
    memoryPerWorker: 128Gi
  queue: llm-gpu-queue
  priority: normal
  data:
    datasetUri: tos://bucket/datasets/sft-v1/
    checkpointUri: tos://bucket/checkpoints/base/
  output:
    checkpointUri: tos://bucket/checkpoints/llama-sft-run-001/
  retryPolicy:
    maxRetries: 1
  timeoutSeconds: 86400
  cleanupPolicy:
    successTtlSeconds: 600
    failureTtlSeconds: 3600
```

必须由后端计算和校验：总 GPU、CPU/内存、临时盘、租户队列、优先级上限、镜像 allowlist、代码源 allowlist、TOS URI 权限和任务名称。

### 4.2 状态机

```text
SUBMITTED -> VALIDATING -> QUEUED -> ADMITTED -> PROVISIONING -> RUNNING
                                                                  |-> SUCCEEDED
                                                                  |-> FAILED
                                                                  |-> CANCELED

任何非终态 -> CANCELING -> CANCELED
任何非终态 -> TIMED_OUT
控制器无法确认状态 -> UNKNOWN -> 重新观察后回到实际状态
```

补充状态：`CANCELING`、`TIMED_OUT`、`UNKNOWN`、`DELETING`。

每个状态变化记录：来源组件、时间、原因、用户可读消息、Kubernetes resourceVersion 和关联 Event。前端同时展示平台状态、Kueue 状态、RayJob 状态和 Pod 状态，避免把多个层级混成一个 RUNNING。

### 4.3 幂等和一致性

- 客户端提交必须支持 `Idempotency-Key`。
- 数据库使用唯一约束：租户 + job name + request key。
- API 在同一个数据库事务中保存 Job、期望状态和 outbox event，再由 Reconciler 消费 outbox 创建 CR。
- CR 创建成功后保存 namespace、name、UID、resourceVersion。
- 任何写操作都支持重复执行，不重复创建 RayJob。
- 控制器重启后消费未完成 outbox，并周期性全量扫描数据库/Kubernetes 当前状态重新收敛。

## 5. PostgreSQL 数据模型

最小表集合：

- `tenants`：租户、namespace、LocalQueue、配额和优先级上限。
- `users`：OIDC subject、邮箱、租户和角色映射。
- `training_jobs`：JobSpec、期望状态、观察状态、CR 引用、时间和失败原因。
- `job_events`：状态变化、Kubernetes Event、控制器事件和审计关联。
- `job_artifacts`：Checkpoint、模型、日志导出和数据版本。
- `dev_workspaces`：用户、租户、RayCluster、Jupyter Service、过期时间和快照版本。
- `audit_logs`：登录、提交、取消、权限修改、配额修改和管理员操作。
- `idempotency_keys`：请求键、响应摘要和过期时间。
- `outbox_events`：与 Job 事务同时写入的待处理控制器事件，避免 API 成功但任务没有进入 Reconciler。

数据库要求：

- 外部 PostgreSQL，禁止生产使用容器本地 SQLite。
- 使用版本化 migration，不使用启动时静默 AutoMigrate。
- 连接池、事务、慢查询日志和备份策略可配置。
- Helm 不创建数据库，只通过 Secret 注入连接信息。

## 6. API 设计

核心接口：

```text
POST   /api/v1/jobs
GET    /api/v1/jobs
GET    /api/v1/jobs/{id}
POST   /api/v1/jobs/{id}/cancel
POST   /api/v1/jobs/{id}/retry
GET    /api/v1/jobs/{id}/events
GET    /api/v1/jobs/{id}/logs
GET    /api/v1/jobs/{id}/logs/stream
GET    /api/v1/jobs/{id}/metrics
GET    /api/v1/jobs/{id}/topology

POST   /api/v1/workspaces
GET    /api/v1/workspaces
POST   /api/v1/workspaces/{id}/stop
POST   /api/v1/workspaces/{id}/snapshot

GET    /api/v1/cluster/capacity
GET    /api/v1/queues
GET    /api/v1/tenants
GET    /api/v1/audit-logs
```

删除操作优先实现为 cancel/stop + cleanup，不提供绕过控制器的任意 Kubernetes delete API。

## 7. 日志、指标和调试

### 7.1 日志

Alloy 将 Pod stdout/stderr 写入 Loki，并强制附加：

- `platform_job_id`
- `tenant_id`
- `rayjob_name`
- `raycluster_name`
- `namespace`
- `pod`
- `container`
- `rank`
- `component`

后端根据 Job ID 生成受控 Loki 查询，不允许用户直接提交任意 LogQL。实时流必须有鉴权、断线重连、游标、限速和导出限制。Ray Dashboard/Job API 只作为 Ray 运行时诊断入口，不替代平台任务日志。

### 7.2 指标

- Prometheus + DCGM Exporter：GPU 利用率、显存、温度、功耗、XID、ECC/健康状态。
- Node Exporter：CPU、内存、网络、磁盘和本地 NVMe。
- Ray metrics：Actor、Task、Object Store、Worker 和 Raylet。
- 训练指标：训练脚本通过标准 metrics SDK/MLflow/TensorBoard 上报，不从日志正则解析 Loss。

必须配置告警：GPU 节点不可用、XID 错误、NCCL timeout、OOM、Pod CrashLoop、Checkpoint 长时间未更新、TOS 读写异常、队列等待超时和调试工作区超时。

### 7.3 Debug Workspace

调试环境使用独立 RayCluster：

- 每个用户/租户有最大 workspace 数量和 GPU 配额。
- JupyterLab 使用预构建镜像，不在启动时临时安装依赖。
- 通过私网 Ingress/Service 暴露，使用 OIDC 或短期签名访问凭证。
- 支持启动、停止、重启、空闲 TTL、最大 TTL、快照和删除。
- 代码转换训练任务时保存代码快照、镜像 digest、依赖 lock、数据 URI 和参数，而不是只传 `/workspace/train.py`。

## 8. 存储设计

### TOS

用于 Dataset、Checkpoint、模型和日志归档。所有 URI 必须带项目/租户前缀，Checkpoint 使用临时目录写入、完成标记和 manifest，避免半写文件被恢复任务读取。

### NAS/PVC

用于跨节点共享代码快照和用户工作区。必须使用 VKE 支持的 CSI/PVC 或可验证的 NAS 挂载，不能用 hostPath 伪装共享存储。

### IDC 存储

平台支持挂载现有 IDC 存储，但不在平台中自行创建或假设存储协议。生产配置通过 PVC/StorageClass 接入：

- `storage.idc.enabled`：是否启用 IDC 存储。
- `storage.idc.existingClaim`：已由运维创建并验证的 PVC。
- `storage.idc.storageClassName`：需要动态供给时使用的 StorageClass。
- `storage.idc.mountPath`：容器内统一挂载点，例如 `/mnt/idc`。
- `storage.idc.accessMode`：根据实际存储支持 `ReadWriteMany` 或 `ReadOnlyMany`。
- `storage.idc.readOnly`：数据集挂载默认只读，工作区挂载单独声明读写。

训练 Pod、Debug RayCluster 和 Jupyter 工作区只允许引用平台生成的 PVC 和租户子目录，不接受用户传入 hostPath。若 IDC 存储位于独立网络，必须先验证 VKE 到 IDC 的 VPC/专线、DNS、路由、端口和 MTU；平台启动检查只负责报告 PVC/挂载失败，不绕过网络隔离。

### 本地 NVMe

只用于 Ray object spill、数据缓存和临时文件。必须配置容量监控、任务级目录、TTL GC 和磁盘压力处理；不能作为持久化产物来源。

## 9. 安全与多租户

- 私网 ALB + TLS，生产关闭公共访问。
- 平台通过已有 Keycloak 使用 OIDC/OAuth2 登录；LDAP 由 Keycloak User Federation 管理，平台不直接保存 LDAP 密码或连接 LDAP。
- Keycloak issuer、realm、client ID、redirect URI、JWKS、logout URI 和 group/role claim 名称全部配置化。
- V1 前端使用 Authorization Code + PKCE，access token 只保存在内存；后端校验 issuer、audience、签名、过期时间和 nonce/state。
- 租户、用户和角色由 Keycloak Token 与平台映射表推导，不信任请求体中的 user/tenant 字段。
- `SuperAdmin`、`TenantAdmin`、`Engineer` 三类角色，后端每个接口强制授权。
- 每个租户独立 namespace、ServiceAccount、LocalQueue、Secret 引用和 NetworkPolicy。
- 平台控制器使用最小 RBAC；API 不直接持有跨租户写权限。
- TOS 访问使用短期 Token/Secret，禁止把真实 AccessKey 渲染进 RayJob YAML 或日志。
- Git、镜像和 TOS 来源使用 allowlist；训练代码执行属于受控沙箱边界。
- 取消 `CORS Default()` 和 `CheckOrigin: true`；只允许平台域名和可信 Origin。
- Dashboard/Jupyter/SSH 不直接暴露 Pod IP，必须经过平台鉴权代理。
- 训练任务使用非 root 用户、只读根文件系统（必要目录单独挂载）和资源上限；`SYS_PTRACE` 只允许经过授权的调试环境。

## 10. Helm 与生产部署

Helm 拆分为：

- `ray-platform-api` Deployment/Service。
- `ray-platform-controller` Deployment/Lease/RBAC。
- `ray-platform-migrations` Job。
- Frontend Deployment/Service。
- Private ALB Ingress。
- ServiceAccount、Role/ClusterRole、NetworkPolicy、PodDisruptionBudget。

生产 values 不包含凭证，只引用已有 Secret：

- `DATABASE_URL`。
- OIDC issuer/client secret。
- TOS endpoint/bucket/短期身份配置。
- Loki/Prometheus 地址。
- 镜像仓库 pull secret。

部署必须支持：

- 镜像 digest 固定。
- migration 先执行，API 再滚动更新。
- readiness 失败时阻止流量切换。
- Helm rollback。
- 组件版本、CRD 版本和 Ray 镜像版本在启动日志中打印。
- API、controller、frontend 和依赖服务的备份与恢复手册。

## 11. 测试与验收

### 自动化测试

- Go 单元测试：JobSpec 校验、状态映射、幂等、权限、模板渲染、清理策略。
- Go 集成测试：PostgreSQL migration、Kubernetes fake client、controller reconcile。
- 前端组件测试：提交表单、状态刷新、日志筛选、取消和调试工作区。
- E2E：真实登录、提交任务、排队、启动、日志、指标、取消、完成和产物查看。
- 安全检查：依赖漏洞、RBAC、Secret 扫描、API 输入校验、Origin 和代理路径。

### VKE 验收

1. 1 卡任务能真实运行并完成。
2. 24 卡任务能等待 Kueue 准入并在 3 台节点上启动。
3. GPU 配额不足时任务保持 QUEUED，不创建运行 Pod。
4. 取消任务后 RayJob、RayCluster、Worker 和临时资源最终清理。
5. Worker OOM、节点重启、Head 异常能正确显示 FAILED/UNKNOWN 和原因。
6. NCCL 多节点测试通过，记录带宽、延迟和失败阈值。
7. Checkpoint 能写入 TOS，并能从 Checkpoint 恢复。
8. 调试 workspace 能启动、访问 Jupyter、空闲回收和快照转换。
9. 用户能通过已有 Keycloak 登录，LDAP 用户同步后的 group/role 能正确映射到平台租户和权限。
10. 未授权用户无法读取或操作租户 A/B 的任务、日志、Secret 和工作区。
11. IDC PVC 能在训练 Pod、Debug RayCluster 和 Jupyter 中按只读/读写策略挂载；网络或 PVC 不可用时任务明确失败并产生事件。
12. API/controller 重启后状态能重新收敛，不重复创建任务。
13. GPU、节点、队列、NCCL、OOM 和存储告警能触发。
14. PostgreSQL 备份恢复后平台元数据和任务引用完整。

## 12. 实施阶段

### Phase 0：平台基础

- 新增配置层、统一 API Envelope、错误码和 request ID。
- PostgreSQL migration 和 repository 层。
- Kubernetes dynamic/client-go adapter、CRD discovery 和基础 RBAC。
- JobSpec Schema、输入校验和资源计算。

### Phase 1：真实训练闭环

- RayJob 创建、Kueue 队列、状态 watch、事件同步。
- 取消、重试、TTL、资源清理和幂等提交。
- 训练任务列表、详情、拓扑和事件页面。

### Phase 2：日志和观测

- Loki 查询和实时流。
- Prometheus/DCGM/Ray metrics 接入。
- GPU/节点/队列仪表盘和告警。

### Phase 3：调试工作台

- 真正创建 RayCluster。
- Jupyter 鉴权代理、workspace 生命周期、空闲回收。
- 代码快照和调试到训练转换。

### Phase 4：生产安全与运维

- OIDC、租户 namespace、RBAC、NetworkPolicy、Secret。
- Helm 生产 profile、migration、PDB、备份恢复和回滚。
- 真实 VKE E2E、NCCL、故障注入和安全验收。

## 13. 设计结论

采用“Go API + Go Reconciler + PostgreSQL + Kueue + KubeRay”的 V1 生产路线。当前演示代码中的静态任务、静态 GPU、模拟日志、模拟工作区和 SQLite 只能保留在明确的本地 demo profile，生产 profile 必须完全依赖真实 Kubernetes、Ray、Loki、Prometheus 和 PostgreSQL 状态。
