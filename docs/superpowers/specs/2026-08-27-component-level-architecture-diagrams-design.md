# Ray 训练平台组件级架构图设计

## 目标

在现有生产架构图基础上，交付一套既能用于汇报、又能支持研发和运维交接的组件级架构图。图中展示当前已经上线的关键组件、组件归属、请求流、控制流、数据流和观测流，同时把尚未完成的演进能力与生产现状明确分离。

本次只调整架构表达，不改变平台代码、部署资源或运行中训练任务。

## 交付物

生成四套图，每套同时提供 SVG 源文件和 3840×2160 PNG：

1. 生产总架构图。
2. 控制面与多租户子图。
3. 训练任务生命周期子图。
4. 存储与可观测性子图。

根 README 只展示生产总架构图；`docs/ARCHITECTURE.md` 依次展示总图和三张子图，并用正文解释边界和例外情况。

## 总体表达原则

- 不展示 IP、服务器名称、节点数量、Pod 副本数和具体硬件规格。
- 已上线组件使用实线边框；规划能力只放在图外的虚线“演进方向”区域。
- 不把应用多副本等同于数据库高可用，也不把 NVMe 缓存画成持久数据真相。
- 用户侧只展示稳定的产品入口和逻辑数据路径，不暴露 Kubernetes、TOS AK/SK 或节点路径。
- 运维组件使用真实产品名称，避免“日志系统”“数据库”这类无法定位责任的模糊框。
- 箭头必须有语义，区分请求、控制、数据和观测四种流量。

## 视觉系统

延续现有蓝白生产架构风格，以浅色背景、圆角容器和泳道组织组件：

| 颜色 | 含义 |
| --- | --- |
| 蓝色 | Portal、Backend、任务控制器、Kubernetes/KubeRay/Kueue 等控制面。 |
| 绿色 | Ray Worker、训练、数据空间、Checkpoint 和持久存储。 |
| 橙色 | 日志、指标、告警与诊断。 |
| 紫色 | 认证、权限、MLflow 和开发调试入口。 |
| 灰色虚线 | 尚未完成的演进能力。 |

箭头图例：

- 蓝色实线：用户请求或 API 调用。
- 深蓝实线：控制器、调度器和 Kubernetes 控制流。
- 绿色粗线：训练数据、Checkpoint 和 Artifact 数据流。
- 橙色虚线：日志、指标和实验记录观测流。

## 图一：生产总架构

### 布局

使用从上到下的领域分层布局：

1. 用户与入口。
2. 访问与认证。
3. 平台控制面。
4. 集群控制与调度。
5. Ray 运行时。
6. 数据与存储。
7. 可观测性与实验。
8. 图外演进方向。

### 组件

#### 用户与入口

- Web Portal。
- `spk-rayjob`。
- 原生 Ray Jobs API。
- 交互式 GPU 调试入口。
- 管理员控制台。

#### 访问与认证

- 企业 DNS。
- IDC NGINX Ingress 代理。
- 私网 ALB。
- Kubernetes Ingress。
- Frontend。
- Backend API。
- 本地账号。
- Keycloak OIDC 接入点。

#### 平台控制面

- 用户、团队和角色服务。
- 镜像目录。
- Git 凭据。
- 数据目录与发布治理。
- 任务 API。
- Reconciler。
- 审计。
- 平台 PostgreSQL。

#### 集群控制与调度

- Kubernetes API。
- Kueue Controller。
- ClusterQueue。
- LocalQueue。
- ResourceFlavor。
- KubeRay Operator。

#### Ray 运行时

- 调试 RayCluster。
- Submitter Pod。
- Ray Head Pod。
- Ray Worker Pods。
- Ray Dashboard。
- PyTorch DDP。
- NCCL。

#### 数据与存储

- 个人空间。
- 团队空间。
- 公共数据。
- Checkpoint 与训练产物。
- TOS。
- FSX CSI。
- FSX Agent / IRSA。
- 可选 IDC 数据源。
- GPU 节点双 NVMe 临时缓存。

#### 可观测性与实验

- Alloy。
- Loki。
- Prometheus Operator。
- Prometheus。
- DCGM Exporter。
- Node Exporter。
- kube-state-metrics。
- Grafana。
- MLflow Tracking Server。
- MLflow PostgreSQL。
- MLflow Artifact 空间。

### 演进方向

只在虚线区域展示：

- 外部 HA PostgreSQL。
- 强制 Keycloak OIDC。
- IDC 与 TOS 审批式双向迁移。
- 多团队成员关系与团队切换。

## 图二：控制面与多租户

### 目的

回答“请求如何进入平台、身份和团队如何确定、谁能管理什么、任务如何进入对应 namespace 和队列”。

### 组件与关系

- 企业入口依次连接 Frontend、Backend API 和认证模块。
- 本地会话、OIDC 会话、PAT 和 Git 凭据是不同凭据类型，不能合并成一个通用令牌框。
- Backend 展开为身份服务、租户服务、任务服务、镜像服务、数据治理服务、审计服务和 Reconciler。
- SuperAdmin 管理全平台团队、用户、物理容量和镜像范围。
- TenantAdmin 管理本团队成员、团队 GPU 配额使用和团队数据发布。
- Engineer 只管理自己的调试环境、训练任务、个人数据和结果。
- 一个团队映射到一个 `tenant-<team>` namespace 和对应 Kueue LocalQueue。
- LocalQueue 进入共享 ClusterQueue，再由 ResourceFlavor 限定生产 GPU 节点。
- PostgreSQL 保存平台元数据，不保存训练数据、日志、指标或模型文件。

## 图三：训练任务生命周期

### 目的

回答“用户修改代码后如何提交、什么时候开始占卡、RayCluster 如何创建、任务结束后哪些内容保留”。

### 主流程

1. Portal 工作区快照、`spk-rayjob` working directory 或原生 Ray working directory 形成代码包。
2. Backend 执行鉴权和提交前检查，包括镜像、数据目录、输出目录、资源形状、团队配额和 MLflow 可达性。
3. 平台写入任务记录并创建 Kueue Workload。
4. Kueue 排队并在资源满足后准入。
5. KubeRay 创建 RayJob 和任务专属 RayCluster。
6. Submitter 将入口命令提交到 Ray Head。
7. Ray Head 负责 Driver、GCS 和 Dashboard。
8. Ray Worker 使用 PyTorch DDP/NCCL 执行单机多卡或多机多卡训练。
9. stdout、资源指标和 MLflow 指标进入各自持久链路。
10. Checkpoint 和结果写入个人持久空间。
11. Backend 回写任务终态；RayCluster 按 TTL 回收。
12. 任务历史、Loki 日志、Prometheus 指标、MLflow Run 和持久产物按各自保留策略继续存在。

### 失败分支

- 提交前检查失败：不创建 RayCluster，也不占用 GPU。
- Kueue 未准入：显示排队原因，不创建训练 Worker。
- 镜像或挂载失败：任务进入可诊断失败状态。
- 用户代码、CUDA、NCCL 或 NaN：保留聚合日志和错误分类。
- 用户取消：只有任务属主、授权的团队管理员或平台管理员可以执行。

## 图四：存储与可观测性

### 目的

回答“数据从哪里读、结果写到哪里、NVMe 是否持久、日志指标和 MLflow 分别保存什么”。

### 稳定数据路径

- `/workspace`：用户代码、配置、虚拟环境和调试状态。
- `/mnt/storage/me`：个人读写空间。
- `/mnt/storage/team`：训练 Pod 内团队只读空间。
- `/mnt/storage/public`：全平台公共只读数据。
- `PLATFORM_DATASET_PATH`：本任务选中的输入目录。
- `PLATFORM_OUTPUT_PATH`：本任务持久结果目录。
- `PLATFORM_CHECKPOINT_PATH`：续训时的历史只读目录。
- `PLATFORM_CACHE_PATH`：可选本地临时缓存。

### TOS 与 FSX 链路

TOS 前缀通过 FSX CSI 发布为 PV/PVC，再挂载到 Ray Worker。对象存储身份只存在于 `kube-system` 中的 FSX Agent，通过 IRSA 获取；用户 Pod 不包含 AK/SK。

### 双 NVMe 链路

- `off`：训练直接读取持久输入。
- `runtime`：Ray 临时目录和 object spilling 使用双 NVMe。
- `preload: input`：每个 Worker 启动前把所选输入预热到本节点双盘缓存，并让 `PLATFORM_DATASET_PATH` 指向缓存视图。
- 任务结束后临时 PVC 与缓存目录回收。
- Checkpoint、模型和正式结果始终写 `PLATFORM_OUTPUT_PATH`。

### 日志、指标与实验链路

- Pod stdout/stderr → Alloy → Loki → Backend 查询接口 → Portal。
- GPU 指标 → DCGM Exporter → Prometheus → Grafana / Portal。
- CPU、内存、磁盘、网络和 Kubernetes 状态 → Node Exporter / kube-state-metrics → Prometheus。
- 参数、Loss、mAP、NDS 和实验 Artifact → MLflow；MLflow 使用独立 PostgreSQL 和 Artifact 空间。
- Ray Dashboard 是运行期入口，任务集群回收后不承担历史查询。

## 文档集成

- 根 `README.md` 展示新总图。
- `docs/README.md` 链接总图和三张子图。
- `docs/ARCHITECTURE.md` 在对应章节嵌入子图。
- `docs/HANDOVER_GUIDE.md` 使用总图，并链接到三张子图进行排障。
- 原有 v3 图在确认新版可读后移入 `docs/archive/architecture/`，避免两个“最新架构图”并存。

## 验收标准

- 四张图均同时生成 SVG 和 3840×2160 PNG。
- 文字在 100% 与文档缩放宽度下均可辨识，没有裁切或重叠。
- 箭头不穿过标题、组件名称或图例。
- 总图包含已批准的全部组件，三张子图与总图命名一致。
- 不包含 IP、主机名、节点数量、Pod 副本数、具体硬件型号或凭据。
- 已上线与规划能力的线型不同，读者不会误把规划能力当作当前生产状态。
- 组件与 `deploy/profiles/vke-cpu-ha.yaml`、Helm Chart、`ops/` 清单和现行文档一致。
- PNG 经视觉检查，SVG 可由无外部字体依赖的本地浏览器稳定重渲染。
- `python3 scripts/test_docs.py`、Markdown 链接检查和 `git diff --check` 通过。

## 非目标

- 不修改平台功能、Kubernetes 资源或正在运行的训练任务。
- 不在图中表达实时节点容量或任务状态。
- 不替代详细用户手册、部署手册和运维 runbook。
- 不把规划中的数据迁移、外部数据库或强制 OIDC 描述成已上线。
