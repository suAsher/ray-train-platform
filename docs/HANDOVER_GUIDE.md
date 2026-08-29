# Ray 分布式训练平台交接与入手手册

本文是新维护者接手平台的第一份文档。它回答五个问题：源码以哪里为准、集群里部署了什么、如何安全发布、每天如何维护、故障时从哪里开始排查。

具体参数与完整命令仍由专项手册维护：构建发布见[构建与部署](BUILD_AND_DEPLOY.md)，生产故障见[运维手册](OPERATIONS_GUIDE.md)，设计边界见[生产架构](ARCHITECTURE.md)。本手册负责把这些内容串成一条可执行的交接路径。

![Ray 分布式训练平台组件级生产架构](architecture/ray-training-platform-production-architecture-v4.svg)

专项排障可直接打开三张组件图：Pending、租户配额和授权问题看[控制面与多租户](architecture/ray-training-platform-control-plane-tenancy-v1.svg)；任务失败、取消和 TTL 看[训练任务生命周期](architecture/ray-training-platform-job-lifecycle-v1.svg)；FSX、缓存、日志、指标和 MLflow 看[存储与可观测性](architecture/ray-training-platform-storage-observability-v1.svg)。

## 1. 先记住的结论

1. **Git 是唯一源码真相。** 本地用于开发，构建机用于从已推送 commit 构建；不能把构建机未提交目录或双向 `rsync` 当作发布基线。
2. **训练任务不属于平台 Deployment。** 更新 Frontend、Backend 或 CLI 发布服务不会删除已经创建的 RayJob/RayCluster；但发布前后仍要验证 API、Reconciler 与正在运行的任务。
3. **数据真相不在节点 NVMe。** 公共、团队、个人、Checkpoint 和产物在 TOS/FSX；GPU 节点双 NVMe 只是按任务创建、可回收的缓存副本。
4. **平台数据库与 MLflow 数据库相互独立。** 两者当前都是单实例 PostgreSQL，各自备份和恢复。
5. **Ray Dashboard 只在任务运行期间存在。** 历史日志看 Loki，指标看 Prometheus/Grafana，Loss、参数与实验看 MLflow，结果和 Checkpoint 看个人数据空间。
6. **所有平台服务保持 ClusterIP。** 对外入口经过私网 ALB；IDC 侧可使用 NGINX Ingress 反向代理到该 ALB，不应开放 NodePort。
7. **任何删除、恢复、数据库迁移、Secret 修改和节点下线都不是日常巡检动作。** 先备份、确认目标、评估正在运行的任务，再执行对应 runbook。

## 2. 源码与发布基线

### 2.1 三端职责

| 位置 | 用途 | 允许的状态 |
| --- | --- | --- |
| 维护者本地目录 | 开发、测试、评审、文档 | 可以有受控未提交改动；发布前必须提交并推送。 |
| GitHub / 后续内部 Git | 唯一共享源码与审计历史 | 受保护的 `main`、发布 tag、可追溯 commit。 |
| 构建机 `/opt/guofeng/vke-cluster/ray-platform` | 构建镜像、执行 Helm 发布和集群验收 | 发布时必须是目标 commit，且 `git status --short` 为空。 |

2026-08-27 的只读核对结果：

| 位置 | 分支 | commit | 工作区 |
| --- | --- | --- | --- |
| 本地 | `main` | `de71c073176d59a602ff68e3aa79a6a2d299e480` | 仅 `docs/NVME_CACHE_GUIDE.md` 有待纳入本次文档整理的修改。 |
| 构建机 | `main` | 同上 | 干净。 |
| GitHub `main` | `main` | 同上 | 远端引用一致。 |

该表是交接时点的快照，不是永远固定的版本号。以后使用下面的命令重新核对，不要凭文档猜测：

```bash
git branch --show-current
git rev-parse HEAD
git status --short
git remote -v
git ls-remote origin refs/heads/main
```

只有 `HEAD`、远端 `main` 和计划发布的 commit 三者一致，且构建机工作区干净，才能开始生产构建。

### 2.2 标准源码流

```text
本地修改和测试
  → commit
  → 推送受保护分支 / 合并 main
  → 构建机 fetch + checkout 目标 commit
  → 镜像构建与推送
  → 将不可变 digest 写入生产 Profile
  → commit + review
  → preflight → deploy → verify
  → 创建 annotated release tag
```

不要把 Git 凭据、平台 PAT、TOS AK/SK、数据库密码、kubeconfig 或恢复备份提交到仓库。聊天中曾出现过的令牌不应继续作为长期交付凭据，应由凭据所有者在对应系统中轮换。

## 3. 平台部署了什么

### 3.1 命名空间与责任边界

| namespace | 主要组件 | 持久状态 | 发布入口 |
| --- | --- | --- | --- |
| `ray-train-platform` | Frontend、Backend、CLI 发布服务、平台 PostgreSQL、Ingress、Kueue 资源 | 平台 PostgreSQL 20Gi EBS | `ops/platform/deploy.sh` |
| `kuberay-system` | KubeRay Operator | 无业务数据 | 独立 Helm release |
| `kueue-system` | Kueue Controller | Kubernetes API 中的队列与准入对象 | 独立 Helm release |
| `tenant-<team>` | 该团队的 RayJob、RayCluster、调试环境、数据 PVC | TOS/FSX 及任务对象 | 平台自动管理 |
| `mlflow-system` | MLflow、写入网关、独立 PostgreSQL、Artifact PVC、FSX 探针 | PostgreSQL 20Gi EBS；Artifact TOS/FSX | `ops/mlflow/deploy.sh` |
| `monitoring` | Prometheus Operator、Prometheus、Grafana、DCGM 采集 | Prometheus EBS | `ops/observability/prometheus-operator/deploy.sh` |
| `loki` | Loki、Gateway、日志存储组件 | Loki EBS/对象存储 | `ops/observability/loki/deploy-ha.sh` |
| `monitoring` | Alloy 日志发现与采集 StatefulSet（2 副本） | 无业务持久状态 | `ops/observability/alloy/deploy-ha.sh` |
| `ray-cache-local` | `/data1`、`/data2` 两套本地卷供应器与监控 | GPU 节点可回收缓存 | `ops/storage/nvme-cache/install.sh` |
| `kube-system` | CoreDNS、FSX CSI node/agent、GPU 插件等 | 集群基础设施 | VKE 与专项运维脚本 |

### 3.2 当前高可用边界

| 组件 | 当前方式 | 结论 |
| --- | --- | --- |
| Frontend | 2 副本、PDB、HPA | 单 Pod 故障可用。 |
| Backend / Reconciler | 2 副本、Lease 选主、PDB、HPA | 单 Pod 故障可用，避免重复创建任务。 |
| CLI 发布服务 | 2 副本 | 单 Pod 故障可用。 |
| KubeRay / Kueue | 各 2 副本 | 控制器层具备副本冗余。 |
| Loki | 3 个数据副本、2 个 Gateway | 日志服务有副本冗余。 |
| Prometheus | 2 副本 | 指标采集有副本冗余；本地 TSDB 不是跨副本强一致数据库。 |
| Grafana | 2 副本 | UI 单 Pod 故障可用。 |
| MLflow | 2 副本 | Tracking 服务有副本冗余。 |
| 平台 PostgreSQL | 1 个 StatefulSet Pod | 当前控制面单点。 |
| MLflow PostgreSQL | 1 个 StatefulSet Pod | 当前实验元数据单点。 |

“应用双副本”不等于整个平台没有单点。严格生产模式还需要外部 HA PostgreSQL、强制 OIDC、平台 NetworkPolicy 和经过演练的恢复流程。

## 4. 请求、任务和数据如何流动

### 4.1 一条训练任务

```text
用户登录 Portal 或 spk-rayjob
  → Backend 鉴权并确定当前团队
  → 校验镜像、数据、输出、GPU/CPU/内存和团队配额
  → 创建平台任务记录与 Kueue Workload
  → Kueue 准入
  → KubeRay 创建任务专属 RayCluster
  → Head 启动 Driver，Worker 使用 GPU 执行 DDP/NCCL
  → Alloy/Loki 收集日志，Prometheus/DCGM 收集资源指标
  → MLflow 记录参数、Loss、指标和 Artifact
  → Checkpoint/结果写入个人持久空间
  → 任务进入终态，RayCluster 按 TTL 回收
```

平台记录、Loki 日志、Prometheus 指标、MLflow Run 和持久产物的保留周期彼此独立。不要通过保留 Ray Pod 来实现历史查询。

### 4.2 稳定数据路径

| 容器路径 | 默认权限 | 数据真相 |
| --- | --- | --- |
| `/workspace` | 读写 | 用户持久工作区。 |
| `/mnt/storage/me` | 读写 | 个人 TOS/FSX 前缀。 |
| `/mnt/storage/team` | 训练 Pod 只读 | 团队发布前缀；TenantAdmin 通过治理入口发布。 |
| `/mnt/storage/public` | 训练 Pod 只读 | `tos://shanghai-data-transfer/ray-train/public/`。 |
| `/mnt/idc/*` | 按登记策略，通常只读 | 可选 IDC 数据源；当前生产 Profile 默认未启用。 |
| `/mnt/cache`、`/mnt/cache2` | 临时读写 | GPU 节点 `/data1`、`/data2`，任务删除后可回收。 |

训练代码优先使用：

```text
PLATFORM_DATASET_PATH
PLATFORM_OUTPUT_PATH
PLATFORM_CHECKPOINT_PATH
PLATFORM_CACHE_PATH
```

启用输入预热时，平台把选中的输入复制到每个 Worker 的本地 NVMe，并让 `PLATFORM_DATASET_PATH` 指向缓存视图；用户不需要改训练镜像，也不需要自己写复制脚本。Checkpoint 与结果始终写持久路径，不能只写 NVMe。

## 5. 新维护者第一小时

以下操作全部是只读检查。先确认当前 kubeconfig 指向正确集群，再执行：

```bash
kubectl config current-context
kubectl get nodes
helm list -A
kubectl get pods -A
kubectl get storageclass
kubectl get pv
kubectl get clusterqueue,resourceflavor
```

然后只看平台和关键依赖：

```bash
kubectl -n ray-train-platform get deploy,sts,pod,svc,ingress,pdb,hpa
kubectl -n kuberay-system get deploy,pod
kubectl -n kueue-system get deploy,pod
kubectl -n mlflow-system get deploy,sts,pod,pvc
kubectl -n monitoring get deploy,sts,daemonset,pod,pvc
kubectl -n loki get deploy,sts,pod,pvc
kubectl -n ray-cache-local get deploy,daemonset,pod
kubectl -n kube-system get daemonset csi-fsx-node
```

通过以下命令建立“当前版本快照”：

```bash
git rev-parse HEAD
helm -n ray-train-platform history ray-platform
helm -n mlflow-system history mlflow
kubectl -n ray-train-platform get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .spec.template.spec.containers[*]}{.image}{" "}{end}{"\n"}{end}'
kubectl -n mlflow-system get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .spec.template.spec.containers[*]}{.image}{" "}{end}{"\n"}{end}'
```

不要在初次接手时执行 reset、delete、rollout restart、数据库恢复或节点 drain。先完成只读盘点并保存输出。

## 6. 日常维护节奏

### 每日

- 检查所有节点 Ready、GPU 可分配数量和磁盘压力。
- 检查平台、KubeRay、Kueue、FSX CSI、Loki、Alloy、Prometheus、Grafana、MLflow Ready。
- 检查 Pending/Failed 任务，先区分“排队”“资源不足”“挂载失败”“镜像失败”“用户代码失败”。
- 检查平台入口 `/healthz`、日志查询、GPU 指标和 MLflow `/healthz`。
- 检查平台与 MLflow PostgreSQL、Prometheus、Loki PVC 使用率。
- 检查 GPU 节点 `/data1`、`/data2` 使用率和缓存回收是否正常。

### 每周

- 用普通测试用户执行 1 GPU 短任务，覆盖登录、提交、日志、产物和终态。
- 验证一次 Ray Dashboard 运行期访问，任务结束后不要求 Dashboard 保留。
- 验证个人目录写入、公共目录只读和团队隔离。
- 检查 Kueue 团队配额总和、物理 GPU 动态容量和超配情况。
- 检查 Loki、Prometheus、MLflow 和两个 PostgreSQL 的容量增长。
- 检查 FSX Agent 版本一致性、FailedMount 事件和 DNS 超时告警。

### 每月或发布前

- 生成平台元数据和 Secret 的受保护备份；备份必须离开仓库并加密保存。
- 对平台 PostgreSQL 和 MLflow PostgreSQL 分别执行恢复演练。
- 检查 GitHub/internal Git、构建机和 Profile 中的 commit/digest 对应关系。
- 清理构建机 Docker/BuildKit 可回收缓存前先查看占用，不删除仍在使用的发布镜像。
- 复核过期账号、PAT、Git 凭据、镜像目录和团队配额。
- 复核告警规则、PDB、HPA、证书有效期和 ALB 后端健康。

完整巡检命令见[生产运维手册第 4 节](OPERATIONS_GUIDE.md#4-日常巡检)。

## 7. 安全发布流程

### 7.1 发布前

1. 确认没有把正在运行任务的删除或 CRD 变更混入发布。
2. 在本地完成后端、前端、Helm 和文档测试。
3. 提交并推送 Git；记录计划发布 commit。
4. 在构建机 `git fetch` 后 checkout 该 commit，确认工作区干净。
5. 构建并推送新镜像；记录 registry 返回的 digest。
6. 把 Backend、Frontend 和 CLI 发布服务的 digest 写入 `deploy/profiles/vke-cpu-ha.yaml`。
7. 再次提交 Profile 变更并评审。
8. 备份平台元数据和环境 Secret。

### 7.2 发布

构建机仓库中执行：

```bash
bash ops/platform/preflight.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --verify-fsx-irsa

bash ops/platform/deploy.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --timeout 15m \
  --verify-fsx-irsa

bash ops/platform/verify.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml
```

需要真实 1 GPU smoke 时，按[构建与部署](BUILD_AND_DEPLOY.md)提供受保护的环境文件、API 地址和已登记镜像 digest，不要把密码放在命令行历史中。

### 7.3 发布后

- 对比 Frontend、Backend、CLI Deployment 的镜像 digest。
- 确认 rollout、PDB、HPA、Service EndpointSlice 和 Ingress 后端健康。
- 确认发布前已经运行的训练任务仍在运行，日志仍可查询。
- 新提交一条短任务验证完整控制链。
- 记录 commit、tag、镜像 digest、Helm revision、验证任务 ID 和回滚 revision。

## 8. 独立组件发布顺序

平台 Chart 不拥有所有基础设施。新集群从零交付时按以下顺序执行，已有集群升级只操作发生变化的组件：

1. Kubernetes/VKE、节点标签与 GPU 驱动/Device Plugin。
2. KubeRay Operator 与 Kueue。
3. FSX CSI、IRSA、CoreDNS 分流和公共/租户静态存储。
4. 两套 NVMe 本地缓存供应器。
5. Prometheus Operator、DCGM、Grafana。
6. Loki 与 Alloy。
7. MLflow、独立 PostgreSQL 和 Artifact PVC。
8. 平台 Secret、私网 ALB/IngressClass、平台 Chart。
9. IDC 侧 NGINX Ingress 代理和企业 DNS。
10. 普通用户端到端验收。

专项入口：

```bash
bash ops/storage/nvme-cache/install.sh
bash ops/observability/prometheus-operator/deploy.sh
bash ops/observability/loki/deploy-ha.sh --install
bash ops/observability/alloy/deploy-ha.sh --install
bash ops/mlflow/deploy.sh
```

这些脚本会修改独立 release；执行前必须阅读相应目录 README、确认 values 与目标集群，并安排维护窗口。不要因为平台发版而无条件重复部署所有依赖。

## 9. 故障排查决策树

### 9.1 入口 503 或页面打不开

```text
域名是否解析到企业统一入口？
  否 → 修企业 DNS
  是 → IDC NGINX Ingress 是否有后端 Endpoint？
        否 → 修代理 Service/Endpoints
        是 → 私网 ALB 后端是否健康？
              否 → 查 VKE Ingress、Service、EndpointSlice、Pod readiness
              是 → 查 Frontend 到 Backend 路由与证书/SNI
```

先请求 `/healthz`，再看 Ingress describe 和 Backend EndpointSlice。不要第一步重启全部 Pod。

### 9.2 任务 Pending

按顺序判断：

1. 平台状态是“排队”还是 Kubernetes Pod Pending。
2. Kueue 是否准入，团队配额是否足够。
3. Ready GPU、单节点 GPU 数量、CPU、内存是否满足每 Worker 请求。
4. 节点选择、污点容忍和拓扑策略是否可满足。
5. PVC 是否 Bound、FSX 是否能挂载。
6. 镜像是否可拉取。

多机任务请求 `2 × 8 GPU` 时，需要两台各有 8 张空闲 GPU 且满足 CPU/内存请求的节点；“集群总共还有 16 卡”不代表一定可调度。

### 9.3 任务失败

先看平台任务详情和聚合日志，不要直接改平台：

- `ImagePullBackOff`：镜像、Harbor 权限或 digest。
- `FailedMount`：FSX CSI、IRSA、DNS、PV/PVC。
- Python traceback：训练代码、配置或依赖。
- CUDA OOM：Batch、模型、显存。
- NCCL timeout：进程数、网卡、节点连通性或某个 rank 先失败。
- `NaN`：算法数值稳定性，不等同于平台故障。
- `data_time` 高：小文件/远端存储/DataLoader；按性能文档做同参数 A/B，再决定 NVMe 预热。

### 9.4 突然“无日志流”

沿链路检查：任务 Pod 是否仍存在并产生日志 → Alloy 是否在该节点运行 → Loki Gateway/数据副本是否 Ready → 查询时间范围和任务标签是否正确 → Backend 聚合接口是否健康。

任务 Pod 回收后仍应从 Loki 查询历史日志；如果只有 `kubectl logs` 能看到而平台看不到，问题在 Alloy/Loki/查询链，不在训练进程。

### 9.5 FSX/TOS 挂载或 DNS 问题

先区分宿主机 DNS 与 `fsx-agent` Pod 内 DNS。企业 IDC DNS 解析企业域名；火山 TOS、STS 和 VKE OIDC 域名通过 CoreDNS 分流到火山 DNS。检查：

- CoreDNS ConfigMap 与 Pod 是否一致、是否 Ready。
- `csi-fsx-node` DaemonSet 是否在所有目标节点 Ready。
- `fsx-agent` 是否使用 IRSA，角色信任的 `aud`、`iss`、`sub` 是否准确。
- Agent 容器内 TOS、STS、OIDC 域名是否能解析并访问。
- 事件中是否有 `FailedMount`、超时或客户端 revision 不一致。

不要用长期 AK/SK Secret 绕过 IRSA，也不要在有活跃挂载时批量强杀 FSX 容器。

### 9.6 MLflow 异常

依次检查 MLflow Deployment、独立 PostgreSQL、Artifact PVC、FSX 健康探针和写入网关。实验长时间显示 RUNNING 时，要区分训练仍在执行、终态回写失败和历史 Run 未补偿；不能仅按 Pod 是否存在判断。

### 9.7 GPU 指标与节点 `nvidia-smi` 不一致

检查 DCGM Exporter 是否覆盖所有 GPU 节点、Prometheus Target 是否 Up、指标标签能否关联 Pod/任务、查询窗口和聚合函数是否把瞬时值稀释。训练用户应看每卡利用率、显存、功率、温度及时间曲线，同时结合 CPU、内存、网络和 `data_time` 判断瓶颈。

### 9.8 NVMe 缓存异常

检查两套 StorageClass、两个供应器 Deployment、节点 `/data1`/`/data2`、PVC 绑定、预热 initContainer 和任务选择的输入目录。缓存失败必须能够回报明确错误；不能静默改读其他用户路径。缓存空间可回收，输出仍应写 `PLATFORM_OUTPUT_PATH`。

更完整命令和恢复边界见[生产运维手册第 7、8 节](OPERATIONS_GUIDE.md#7-故障排查总流程)。

## 10. 备份、恢复与回滚

### 10.1 平台备份

```bash
umask 077
bash ops/platform/backup-state.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --output-dir <repository-outside-encrypted-directory>
```

该备份包含平台 PostgreSQL 元数据和指定 Secret 清单，不包含 TOS/IDC 数据、Loki 日志、MLflow 数据或训练 Pod。MLflow PostgreSQL 和 Artifact 必须按 MLflow runbook 独立保护。

### 10.2 应用回滚

应用发布失败时优先回滚 Helm revision，并确认 Profile 与目标镜像仍可获得：

```bash
helm -n ray-train-platform history ray-platform
helm -n ray-train-platform rollback ray-platform <known-good-revision> \
  --wait --timeout 15m
```

回滚应用不会自动回滚数据库破坏性迁移。任何 schema 变更都必须具备向前兼容窗口和单独恢复方案。

### 10.3 数据库恢复

恢复平台 PostgreSQL 会短时影响登录、任务创建和平台查询，必须在维护窗口按 `ops/platform/restore-metadata.sh --help` 操作。正在运行的 Ray 进程可能继续计算，但控制面在数据库恢复期间不能视为可用。

## 11. GPU 节点扩容和下线

新增节点至少验证：

1. 节点 Ready，GPU 驱动、Device Plugin 和 `nvidia-smi` 正常。
2. 生产 GPU 标签、污点和容忍策略一致。
3. FSX CSI、Alloy、DCGM Exporter 等 DaemonSet 已覆盖。
4. `/data1`、`/data2` 存在、权限正确、没有残留测试数据。
5. 两套 NVMe 缓存 StorageClass 均可在该节点创建、写入和回收卷。
6. Kueue 动态容量和平台算力页面反映新 GPU。
7. 完成 1 GPU smoke，再完成多节点拓扑验证。

下线节点前先禁止新任务、等待或迁移现有工作负载、确认没有活跃本地缓存和 FSX 挂载，再 drain。强制删除 GPU Worker 会中断训练；RayJob 不会凭空恢复未持久化的进程内状态。

## 12. 当前已知风险与待办

| 风险 | 当前状态 | 建议目标 |
| --- | --- | --- |
| 平台 PostgreSQL 单实例 | Pod 可重建，数据库层无自动 HA | 外部/托管 HA PostgreSQL，定期恢复演练。 |
| MLflow PostgreSQL 单实例 | Tracking 服务双副本，元数据层单点 | 独立 HA PostgreSQL 与一致性备份。 |
| `APP_ENV=development` | 为兼容本地账号和内置数据库保留 | 完成 OIDC、外部数据库后切 `production` 校验。 |
| OIDC 未强制 | 本地账号仍可登录 | Keycloak/LDAP 联邦，保留受控 break-glass 本地账号。 |
| 平台 NetworkPolicy 关闭 | 主要依赖 namespace/RBAC/入口控制 | 完成训练、存储、观测流量清单后逐步启用。 |
| 原生 MLflow 全功能共享 | 当前是明确策略 | 若需要多租户治理，增加实验/Artifact 授权和审计。 |
| 数据自助迁移未开放 | IDC 与 TOS 双向审批流待设计 | 目录选择、审批、进度、校验、断点续传和审计。 |
| 发布 tag 缺失 | 交接时 GitHub 没有 release tag | 下一次完整验收后创建 annotated tag。 |
| 旧 Loki release 保留 | 旧 release 已缩容，历史 PVC 保留 | 确认保留期和回滚边界后再清理。 |
| NVMe preflight 节点清单固化在脚本 | 当前脚本只覆盖交付时的 GPU 节点 | 新增节点前先参数化节点发现或更新受控清单并评审。 |

这些风险不能通过“多起几个 Pod”全部解决，需要分别治理数据层、身份、安全策略和恢复能力。

## 13. 交接验收清单

交接双方共同确认：

- [ ] 能解释 Git、本地和构建机各自职责，并重新核对三端 commit。
- [ ] 能列出所有 Helm release、关键 namespace、持久卷和 Secret 所有者。
- [ ] 能在不使用 Kubernetes 管理权限的情况下，以普通用户提交、看日志、看 MLflow、查看结果并续训。
- [ ] 能执行只读日常巡检并识别 Pending、FailedMount、ImagePull、用户代码失败。
- [ ] 能完成一次平台备份，知道备份不包含什么。
- [ ] 能按 commit、镜像 digest 和 Helm revision 执行发布与回滚。
- [ ] 能说明平台 PostgreSQL、MLflow PostgreSQL、TOS/FSX、Loki、Prometheus 和 NVMe 各保存什么。
- [ ] 能安全扩容 GPU 节点，并验证 CSI、日志、指标和双 NVMe。
- [ ] 知道哪些操作必须维护窗口和额外授权。
- [ ] 已记录当前 release commit、镜像 digest、Helm revision、证书到期时间和联系人。

## 14. 文档阅读路径

### 新维护者

1. 本手册。
2. [生产架构](ARCHITECTURE.md)。
3. [构建与部署](BUILD_AND_DEPLOY.md)。
4. [生产运维](OPERATIONS_GUIDE.md)。
5. [管理员手册](ADMIN_GUIDE.md)。

### 用户支持

1. [用户文档入口](user/README.md)。
2. [用户手册](USER_GUIDE.md)。
3. [多方式提交](SUBMIT_GUIDE.md)。
4. [训练性能诊断](TRAINING_PERFORMANCE_DIAGNOSIS.md)。
5. [NVMe 缓存](NVME_CACHE_GUIDE.md)。

### 研发

1. [开发指南](DEVELOPMENT.md)。
2. [新训练代码接入](NEW_TRAINING_CODE_GUIDE.md)。
3. 相关 `ops/*/README.md` 与测试。

`archive/` 和 `superpowers/` 是历史设计与实施记录，不是生产操作入口。发生冲突时，先以代码、生产 Profile 和集群只读状态核实，再修正文档，不能让临时命令成为新的隐性真相。
