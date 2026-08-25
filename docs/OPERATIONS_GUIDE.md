# RayTrain 生产运维手册

本文是平台值班、发布、巡检和故障恢复的统一入口。它回答四个问题：平台部署了什么、组件之间如何依赖、日常如何维护、故障时按什么顺序恢复。

生产部署以仓库中的清单和脚本为准，不以聊天记录、临时命令或某台机器上的未提交文件为准：

- 平台 Profile：`deploy/profiles/vke-cpu-ha.yaml`
- 平台 Helm Chart：`helm/ray-train-platform/`
- 平台发布脚本：`ops/platform/`
- MLflow：`ops/mlflow/`
- Prometheus Operator：`ops/observability/prometheus-operator/`
- Loki：`ops/observability/loki/`
- Alloy：`ops/observability/alloy/`
- FSX/TOS：`ops/storage/shanghai-data-transfer/`
- DNS：`ops/dns/`

> 安全边界：本文不会记录密码、PAT、AK/SK、Harbor 密码或 kubeconfig 内容。任何恢复操作都不得把长期 TOS 凭据注入训练 Pod，也不得用静态 AK/SK 绕过 IRSA 故障。

## 1. 当前生产基线

截至 2026-08-23，生产集群的逻辑资源池如下：

| 资源池 | 数量 | 标签 | 主要用途 |
| --- | ---: | --- | --- |
| CPU 控制面节点 | 3 | `platform.wellspiking.ai/pool=control-plane` | Portal、API、PostgreSQL、Kueue/KubeRay、Prometheus、Grafana、Alloy、Loki 网关等。 |
| GPU 训练节点 | 2 | `accelerator=nvidia-rtx-4090`、`platform.wellspiking.ai/gpu-pool=production` | 训练 Worker、调试 Worker、MLflow 服务以及 GPU/FSX 节点探针。 |
| GPU | 16 | 每节点 8 卡 | Kueue 当前集群级准入容量为 16 卡；后续节点加入后由标签和平台自动配额逻辑重新测量。 |

平台只有一个用户入口：

```text
https://raytrain.wellspiking.ai
```

入口链路为：

```text
用户 / spk-rayjob
  → IDC Nginx Ingress
  → VKE 私网 ALB
  → ray-train-frontend ClusterIP
  → ray-train-backend ClusterIP
  → PostgreSQL / KubeRay / Kueue / Loki / Prometheus / MLflow
```

平台服务不使用用户可访问的 NodePort。MLflow 原生界面通过平台同域代理的 `/mlflow/` 访问；Ray Dashboard 只在任务运行期间通过平台代理访问。

## 2. 部署了哪些组件

### 2.1 命名空间和责任边界

| 命名空间 | 组件 | 资源形态 | 持久化 | 部署入口 |
| --- | --- | --- | --- | --- |
| `ray-train-platform` | Frontend、Backend、`spk-rayjob` 发布服务 | Deployment，均为 2 副本 | 无状态 | `ops/platform/deploy.sh` |
| `ray-train-platform` | 平台 PostgreSQL | StatefulSet，1 副本 | 20Gi EBS | 平台 Helm Chart |
| `tenant-local` | RayJob、任务专属 RayCluster、调试 RayCluster、LocalQueue | 动态创建 | TOS/FSX 数据卷；Pod 本身可回收 | 平台 Backend、KubeRay、Kueue |
| `kuberay-system` | KubeRay Operator | Deployment，2 副本 | 无 | 独立 Helm release `kuberay` |
| `kueue-system` | Kueue Controller | Deployment，2 副本 | Kubernetes API 对象 | 独立 Helm release `kueue` |
| `kube-system` | NVIDIA device plugin、DCGM exporter | DaemonSet，仅 GPU 节点 | 无 | VKE 插件 |
| `kube-system` | CSI-FSX Controller、`csi-fsx-node`、FSX Agent | Deployment + DaemonSet | 节点挂载状态 | VKE 插件 `csi-fsx` |
| `monitoring` | Prometheus Operator、Prometheus、Alertmanager、Grafana | Deployment/StatefulSet | Prometheus 每副本 50Gi EBS | `ops/observability/prometheus-operator/deploy.sh` |
| `monitoring` | Alloy | StatefulSet，2 副本 | 无 | `ops/observability/alloy/deploy-ha.sh` |
| `loki` | Loki active release `loki-cpu` | 3 副本 StatefulSet + 2 副本网关 | 每副本 50Gi EBS；历史日志在 TOS | `ops/observability/loki/deploy-ha.sh` |
| `loki` | 旧 release `loki` | 已缩容到 0 | 旧 PVC 保留 | 仅作迁移回滚，不承载流量 |
| `mlflow-system` | MLflow、写入网关 | Deployment，各 2 副本 | Artifact 使用 FSX RWX | `ops/mlflow/deploy.sh` |
| `mlflow-system` | MLflow PostgreSQL | StatefulSet，1 副本 | 20Gi EBS | `ops/mlflow/deploy.sh` |
| `mlflow-system` | FSX 挂载探针、DNS 探针 | DaemonSet，各覆盖 2 个生产 GPU 节点 | 使用 MLflow Artifact PVC | `ops/mlflow/35-fsx-health-probe.yaml` |

### 2.2 平台控制面

`ray-platform` Helm release 当前负责：

- Frontend ×2：Web UI、同域 API 路由、MLflow/Ray Dashboard 入口。
- Backend ×2：认证、租户、用户、配额、数据目录、训练任务、调试环境、日志/指标代理和 Kubernetes Reconciler。
- `spk-rayjob-release` ×2：CLI 安装包发布，不是任务执行器。
- PostgreSQL ×1：用户、租户、镜像目录、任务历史、审计和平台元数据。
- HPA：Frontend/Backend 最少 2、副本上限 6，CPU 目标 70%。
- PDB：滚动升级时至少保留 1 个 Frontend、Backend 和 CLI 发布 Pod。
- Kueue 资源：`cluster-gpu-queue`、`gpu-4090-flavor` 和租户 LocalQueue。
- VKE ALB Ingress：只把私网 ALB 接到 ClusterIP 服务。

Backend 多副本不会重复创建任务：Reconciler 使用 Kubernetes Lease 选主。已经创建的 RayJob 由 Kubernetes、KubeRay 和 Ray 自主运行，Frontend/Backend 滚动升级不会停止训练。

当前平台 PostgreSQL 是控制面单点。Pod 可重建，EBS 数据仍在；但数据库不可用期间不能登录、创建任务或读取平台元数据。严格高可用需要迁移到外部 HA PostgreSQL。

### 2.3 训练与调试运行时

每个训练任务自动创建：

```text
RayJob
├── submitter Pod：上传 working-dir 并调用 Ray Jobs API
└── RayCluster
    ├── head Pod：Ray GCS、Dashboard、任务控制
    └── worker Pod(s)：CPU、内存、GPU、训练进程
```

任务结束后，RayCluster 按保留窗口回收。任务状态、历史日志、MLflow 指标和 TOS 结果仍可查看。提交器、head、worker 是不同职责，因此多 Pod 是预期行为。

调试环境也是 RayCluster：head 不申请 GPU，JupyterLab、VS Code 和 Terminal 代理到 GPU worker。Pod 可重建；`/workspace`、个人数据和结果应放在持久化目录，不能把重要文件只放在容器根文件系统或 `emptyDir`。

### 2.4 调度与配额

Kueue 的职责是“准入”，不是启动训练进程：

- `ResourceFlavor gpu-4090-flavor` 只选择生产 4090 节点。
- `ClusterQueue cluster-gpu-queue` 表示集群物理资源上限。
- `LocalQueue local-gpu` 位于租户 namespace。
- 平台团队配额决定某团队最多能使用多少 GPU；集群配额决定所有团队总共能准入多少。
- Ray Worker 的 nodeSelector 再次限制到生产 GPU 池。

任务 Pending 时必须先区分：等待 Kueue 配额、等待空闲 GPU、CPU/内存请求过大、拓扑无法满足，还是卷挂载失败。

### 2.5 数据与存储

公共数据的唯一对象存储根是：

```text
tos://shanghai-data-transfer/ray-train/public/
```

训练和调试容器只使用稳定逻辑路径：

| 容器路径 | 权限 | 底层来源 | 用途 |
| --- | --- | --- | --- |
| `/workspace` | 读写 | 工作区快照/持久工作区 | 代码、配置、`.venv`、编辑器状态。 |
| `/mnt/storage/me` | 读写 | 用户 TOS 前缀 | 个人数据、Checkpoint、结果。 |
| `/mnt/storage/team` | 训练 Pod 只读 | 团队 TOS 前缀 | 团队管理员发布的数据。 |
| `/mnt/storage/public` | 只读 | `ray-train/public/` | 全平台公共数据。 |
| `/mnt/data/input` | 只读 | 本任务选中的数据子目录 | `PLATFORM_DATASET_PATH`。 |
| `/mnt/data/output` | 读写 | 本任务独立结果目录 | `PLATFORM_OUTPUT_PATH`。 |
| `/mnt/data/checkpoints` | 只读 | 续训所选历史任务目录 | `PLATFORM_CHECKPOINT_PATH`。 |
| `/mnt/idc/*` | 按登记策略只读 | IDC NFS | 可选 IDC 数据源；当前生产 Profile 未启用。 |

TOS 通过 `fsx.csi.volcengine.com` 静态 PV/PVC 挂载。身份在 `kube-system/fsx-agent` 上通过 IRSA 获取，训练 Pod 看不到 AK/SK。平台目前关闭 GPU NVMe 缓存，因此 `/data1`、`/data2` 还不是用户数据路径，不能作为数据真相。

EBS 用于 PostgreSQL、Prometheus 和 Loki 的本地状态；TOS 用于训练数据、用户结果、Checkpoint、日志对象和 MLflow Artifact。两者用途不能互换。

### 2.6 日志、指标和实验

| 数据 | 采集/存储 | 用户入口 | 保留 |
| --- | --- | --- | --- |
| Pod stdout/stderr | Alloy → Loki | 平台任务详情 | Loki 当前配置 30 天。 |
| Node/Pod/Kubernetes 指标 | Prometheus Operator | Grafana/平台指标 | 15 天或每副本 45GiB。 |
| GPU 利用率、显存、温度、功耗 | DCGM exporter → Prometheus | 后端任务级接口 → Portal；管理员全局显卡物理矩阵 | 同 Prometheus。 |
| Loss、参数、评估指标 | MLflow ingest → MLflow | 实验中心和 `/mlflow/` | 由 MLflow DB 和 Artifact 存储保持。 |
| Ray 运行时拓扑 | Ray Dashboard | 任务运行期 Dashboard | RayCluster 回收后不可用。 |

Alloy 使用 Kubernetes API 拉取 Pod 日志，不要求每个 GPU 节点运行一个日志 DaemonSet。Node exporter、DCGM 和 FSX 探针才是 DaemonSet。

## 3. 运维机器、仓库和权限

### 3.1 工作目录

建议形成单向发布链：

```text
内部 Git release commit/tag
  → 构建机受控 checkout
  → 构建并推送 Harbor digest
  → 更新生产 Profile 中的 digest
  → preflight / deploy / verify
```

当前构建机标准目录：

```text
/opt/guofeng/vke-cluster/ray-platform
```

本地开发目录可用于编码和评审，但线上发布必须对应一个可追溯 commit/tag。不要长期双向 rsync 本地和构建机，也不要以构建机未提交工作区作为唯一源码。

若构建机仓库因目录属主不同出现 `detected dubious ownership`，先核对目录确实是本项目，再使用单次只读参数检查：

```bash
git -c safe.directory=/opt/guofeng/vke-cluster/ray-platform status --short
git -c safe.directory=/opt/guofeng/vke-cluster/ray-platform rev-parse HEAD
```

长期应统一构建目录属主和发布用户。不要配置 `safe.directory=*`，也不要在没有确认来源的目录上全局豁免 Git 安全检查。

### 3.2 客户端依赖

运维机至少需要：

```text
kubectl
helm
jq
openssl
bash
git
docker + buildx（构建镜像时）
```

每次操作前确认当前集群：

```bash
kubectl config current-context
kubectl cluster-info
kubectl get nodes
helm list -A
```

不要依赖 shell 默认 namespace；所有命令显式写 `-n`。

### 3.3 Secret 管理

平台基础 Secret 由 `ops/platform/bootstrap-secrets.sh` 创建：

- `ray-platform-postgres`
- `ray-platform-pat`
- `ray-platform-bootstrap-admin`
- `harbor-registry`
- 环境需要时的 `tos-credentials`

Secret 源文件必须为 `0600`，不能提交 Git：

```bash
chmod 600 /secure/path/secrets.env
bash ops/platform/bootstrap-secrets.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --env-file /secure/path/secrets.env
```

FSX/IRSA 不使用这些静态 TOS Secret。Loki 当前 S3 兼容写入仍使用 `loki/loki-tos`，这是独立的运维凭据边界。

## 4. 日常巡检

### 4.1 每日快速检查

以下命令全部只读：

```bash
kubectl get nodes -L platform.wellspiking.ai/pool,platform.wellspiking.ai/gpu-pool,accelerator
kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded -o wide
kubectl get events -A --sort-by=.lastTimestamp | tail -n 100
helm list -A

kubectl -n ray-train-platform get deploy,sts,pods,pvc,hpa,pdb
kubectl -n mlflow-system get deploy,sts,ds,pods,pvc
kubectl -n monitoring get deploy,sts,ds,pods,pvc
kubectl -n loki get deploy,sts,pods,pvc

kubectl get clusterqueues,resourceflavors
kubectl -n tenant-local get localqueues,workloads
kubectl -n kube-system get ds csi-fsx-node nvidia-device-plugin dcgm-exporter
```

健康标准：

- 所有 Node 为 Ready。
- GPU 节点能看到期望数量的 `nvidia.com/gpu`。
- Frontend/Backend/CLI 发布服务均至少 2 个 Ready。
- 两个 PostgreSQL StatefulSet 各 1 个 Ready。
- KubeRay、Kueue 均 2 个 Ready。
- NVIDIA、DCGM、CSI-FSX 在目标节点全部 Ready。
- Prometheus、Alertmanager、Grafana、Alloy 满足设计副本数。
- MLflow、ingest 和两个 FSX 探针满足设计副本数。
- Loki active release 应为 3/3；网关应为 2/2。
- PVC 全部 Bound。

### 4.2 每周功能检查

```bash
bash ops/platform/preflight.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --verify-fsx-irsa

bash ops/platform/verify.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml

bash ops/mlflow/verify.sh
bash ops/observability/prometheus-operator/verify.sh
bash ops/observability/loki/30-verify-loki.sh
bash ops/gpu/verify-production-pool.sh
bash ops/dns/deploy-coredns-split-dns.sh --check
```

`preflight --verify-fsx-irsa` 会创建并清理隔离的临时探针，不是完全只读；应在资源充足时执行。

### 4.3 容量检查

```bash
kubectl top nodes
kubectl top pods -A --sort-by=memory
kubectl get pvc -A
kubectl get clusterqueue cluster-gpu-queue -o yaml
kubectl -n tenant-local get workloads
```

重点关注：

- CPU 控制面节点是否还能满足硬反亲和和滚动升级空间。
- Loki/Prometheus EBS PVC 是否接近容量上限。
- Kueue nominalQuota 是否与 Ready 的生产 GPU 总量一致。
- 团队配额总和可以超过物理容量，但同时准入不能超过 ClusterQueue。
- TOS “配额”是平台逻辑护栏，不是文件系统 block quota；必须结合桶侧容量和审计监控。

## 5. 标准部署、升级和回滚

### 5.1 发布前检查

1. 代码已经进入内部 Git，并有 release commit/tag。
2. `git status --short` 为空。
3. 镜像已推送 Harbor，并获得不可变 digest。
4. Profile 只修改预期的 tag/digest/配置，不使用临时 `helm --set`。
5. 备份平台状态。
6. 确认没有数据库迁移与旧版本不兼容。
7. 确认集群有滚动升级资源余量。

发布前还要比较线上 Helm values 与受评审 Profile。至少核对镜像 digest 和关键集成地址：

```bash
helm -n ray-train-platform get values ray-platform -o json \
  | jq '{backend: .backend.image, frontend: .frontend.image,
         release: .spkRayjobRelease.image,
         mlflow: .backend.mlflow,
         dataSpaces: .dataSpaces,
         training: .training,
         kueue: .kueue}'

git diff -- deploy/profiles/vke-cpu-ha.yaml
```

发现漂移时先确认线上值是不是已经验收的新基线。若是，应先把值回写 Profile、评审并提交；若不是，再使用受评审 Profile 原子部署。不能直接运行旧 Profile“消除漂移”，因为它可能回退已上线功能。

本地交付检查见 [构建与部署](BUILD_AND_DEPLOY.md)。

### 5.2 平台备份

```bash
backup_dir="/secure/backups/ray-platform-$(date +%Y%m%d-%H%M%S)"
bash ops/platform/backup-state.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --output-dir "$backup_dir" \
  --secret harbor-registry
chmod -R go-rwx "$backup_dir"
```

备份包含平台 PostgreSQL 和指定 Secret 清单，不包含 TOS、IDC、Loki 或训练 Pod 数据。备份目录必须离开仓库并加密保存。

### 5.3 平台原子升级

```bash
cd /opt/guofeng/vke-cluster/ray-platform

bash ops/platform/preflight.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --verify-fsx-irsa

bash ops/platform/deploy.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --verify-fsx-irsa \
  --timeout 15m

bash ops/platform/verify.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml
```

`deploy.sh` 固定使用 `helm upgrade --install --atomic --wait`。失败会自动回到原 release，不应同时启动第二个发布进程。

首次部署或专用 ALB 被重建后，先执行：

```bash
bash ops/platform/bootstrap-alb.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --timeout 15m
```

ALBInstance 和 IngressClass 有意放在平台 Helm release 之外，避免 Helm 在 ALB 未 Ready 时先创建路由。

### 5.4 平台回滚

```bash
helm -n ray-train-platform history ray-platform
helm -n ray-train-platform rollback ray-platform <revision> --wait --timeout 15m
bash ops/platform/verify.sh --profile deploy/profiles/vke-cpu-ha.yaml
```

Helm 回滚不会回退数据库 schema。若新版本执行了不兼容迁移，应按变更方案恢复数据库备份，而不是只回滚镜像。

### 5.5 平台元数据恢复

仅用于新建或已确认可覆盖的 standalone PostgreSQL。恢复脚本会临时缩容 Frontend/Backend：

```bash
chmod 600 /secure/backups/metadata.sql
bash ops/platform/restore-metadata.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --input /secure/backups/metadata.sql
```

完整重建时先恢复 Secret，再部署，再恢复元数据：

```bash
bash ops/platform/restore-secrets.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --state-dir /secure/backups/<snapshot>
```

### 5.6 MLflow 发布

MLflow 使用独立 namespace、独立 PostgreSQL 和独立 Artifact PVC：

```bash
cd /opt/guofeng/vke-cluster/ray-platform
bash ops/mlflow/deploy.sh
bash ops/mlflow/verify.sh
```

`deploy.sh` 使用 `mlflow-system/mlflow-deploy` Lease 串行化部署、数据库升级、Artifact 验收和回滚。另一个部署进程看到非空 holder 时必须退出，不能强制抢锁。

MLflow 数据备份至少包含：

```bash
umask 077
kubectl -n mlflow-system exec statefulset/mlflow-postgres -- \
  sh -ec 'exec pg_dump --clean --if-exists -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
  > /secure/backups/mlflow-$(date +%Y%m%d-%H%M%S).sql
```

Artifact 位于 TOS 专用前缀，不随 PostgreSQL dump 一起备份。恢复 MLflow 时必须保证数据库记录与 Artifact 前缀来自一致的时间点。

### 5.7 Prometheus/Grafana 发布

```bash
bash ops/observability/prometheus-operator/deploy.sh
bash ops/observability/prometheus-operator/verify.sh
```

脚本会安装固定版本 Chart、复制 Harbor pull Secret、原子升级并实际查询 DCGM GPU 指标。Grafana 配置通过 ConfigMap sidecar 恢复，当前没有 Grafana PVC；手工在 UI 中创建但未进入 ConfigMap 的 Dashboard 不属于可恢复配置。

### 5.8 Loki/Alloy 发布

```bash
bash ops/observability/loki/deploy-ha.sh --render-only
bash ops/observability/loki/deploy-ha.sh --install
bash ops/observability/loki/30-verify-loki.sh

bash ops/observability/alloy/deploy-ha.sh --render-only
bash ops/observability/alloy/deploy-ha.sh --install
```

不要删除已缩容的旧 `loki` release 或旧 PVC，除非迁移验收、保留期和回滚窗口都已结束，并单独审批清理。

### 5.9 CoreDNS 分流配置

```bash
bash ops/dns/reconcile-coredns-placement.sh --apply
bash ops/dns/reconcile-coredns-placement.sh --check
bash ops/dns/deploy-coredns-split-dns.sh --apply
bash ops/dns/deploy-coredns-split-dns.sh --check
```

调度脚本使 CoreDNS 优先运行在 CPU 控制面节点，并允许生产 GPU 节点兜底，但不允许虚拟节点。每副本请求 `250m` CPU / `256Mi` 内存，限额为 `2` CPU / `1Gi`，不占用 GPU 卡。

分流脚本把火山服务域名转发到 VKE VPC DNS `100.96.0.2/100.96.0.3`，并把 CoreDNS 默认根转发器显式设置为 IDC DNS `192.168.110.61/192.168.111.63`。不要使用 `/etc/resolv.conf` 作为默认转发器，否则 `gitlab.qomolo.com` 等 IDC 域名可能被解析到不可达的公网地址。脚本在 rollout 失败时自动恢复旧 Corefile。

hostNetwork 的 FSX Agent 不一定使用 CoreDNS；新节点还要执行节点侧分流：

```bash
sudo bash ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh --apply
sudo bash ops/storage/shanghai-data-transfer/50-configure-node-split-dns.sh --check
```

## 6. GPU 节点扩容和下线

### 6.1 新节点上线

按顺序验收：

1. Node Ready，containerd、CNI 正常。
2. NVIDIA 驱动、device plugin、DCGM exporter 正常。
3. 每节点暴露 8 个 `nvidia.com/gpu`。
4. FSX Agent 与 `csi-fsx-node` Ready。
5. 节点 DNS 分流已安装并通过检查。
6. TOS/FSX 前缀 mount smoke 通过。
7. `/data1`、`/data2` 只登记为未来缓存盘，不直接挂给用户。
8. 最后才增加生产标签，使其进入训练资源池。

```bash
kubectl get node <node> -o wide
kubectl describe node <node>
kubectl get pods -n kube-system -o wide --field-selector spec.nodeName=<node>

kubectl label node <node> accelerator=nvidia-rtx-4090 --overwrite
kubectl label node <node> platform.wellspiking.ai/gpu-pool=production --overwrite
```

标签完成后更新 Profile 中的任务形状上限，执行平台 `preflight → deploy → verify`，再提交 1 卡和多机 smoke。不要直接手工修改 ClusterQueue 后跳过 Profile，否则下一次 Helm 发布会产生漂移。

### 6.2 节点维护或下线

先确认没有运行中的 RayJob、调试环境和 MLflow 唯一副本，再进行维护：

```bash
kubectl cordon <node>
kubectl get pods -A -o wide --field-selector spec.nodeName=<node>
```

GPU 节点上存在 Ray Worker 时，不要直接 drain。先在平台停止/等待任务完成；调试工作区确认持久化后再停止。MLflow 至少保留另一个 Ready 副本，FSX 探针恢复后才能解除维护。

维护完成：

```bash
kubectl uncordon <node>
bash ops/mlflow/verify.sh
bash ops/gpu/verify-production-pool.sh
```

## 7. 故障排查总流程

先确定层次，再操作。不要看到 `Error` 或 `Pending` 就重启节点。

```text
入口不可用？
  → DNS / IDC Ingress / 私网 ALB / ClusterIP Endpoint

任务未创建？
  → Frontend / Backend / PostgreSQL / Kueue

Pod Pending？
  → 配额 / GPU / CPU / 内存 / 拓扑 / PVC 事件

Pod ContainerCreating？
  → 镜像拉取 / CNI / CSI 挂载

Pod Running 但任务失败？
  → entrypoint / Python / CUDA / NCCL / 数据路径 / 用户代码

日志或指标缺失？
  → Alloy/Loki 或 Prometheus/DCGM

实验缺失？
  → MLflow ingest / MLflow DB / Artifact mount
```

第一组证据：

```bash
kubectl -n <namespace> get pod <pod> -o wide
kubectl -n <namespace> describe pod <pod>
kubectl -n <namespace> logs <pod> --all-containers --tail=500
kubectl -n <namespace> get events --field-selector involvedObject.name=<pod> --sort-by=.lastTimestamp
```

只有 Event 中出现 `FailedMount`、`MountVolume`、`input/output error`、`transport endpoint`、`context deadline exceeded` 等挂载证据，才能初步归类为存储故障。用户脚本退出码 1、Python traceback、403、文件内容不符合断言都不是挂载失败。

## 8. 常见故障与恢复

### 8.1 访问域名返回 503

检查顺序：

```bash
kubectl -n ray-train-platform get ingress
kubectl -n ray-train-platform describe ingress ray-platform-rayctl-https-ingress
kubectl -n ray-train-platform get svc,endpoints,endpointslice
kubectl -n ray-train-platform get deploy,pods -o wide
kubectl -n ray-train-platform logs deploy/ray-train-frontend --tail=200
```

判断：

- ALB 直接 503：通常是 ALB 后端没有 Ready Endpoint、IngressClass/监听器错误。
- IDC Nginx 503：检查 IDC 代理 Service/Endpoints 是否仍指向私网 ALB，upstream Host/SNI 是否为 `raytrain.wellspiking.ai`。
- 前端正常、API 失败：检查 Backend Endpoint 和数据库。
- 不要为了绕过 503 把服务改成 NodePort。

### 8.2 Frontend 或 Backend 不 Ready

```bash
kubectl -n ray-train-platform rollout status deploy/ray-train-frontend --timeout=5m
kubectl -n ray-train-platform rollout status deploy/ray-train-backend --timeout=5m
kubectl -n ray-train-platform describe pod -l app=ray-train-backend
kubectl -n ray-train-platform logs deploy/ray-train-backend --tail=500
kubectl -n ray-train-platform get secret ray-platform-postgres ray-platform-pat ray-platform-bootstrap-admin
```

常见原因：镜像 digest 不存在、Harbor Secret 缺失、数据库连接失败、Profile 与 schema 不兼容、CPU 节点资源不足导致硬反亲和无法满足。

恢复优先使用 Helm 回滚；不要直接在线编辑 Deployment，因为下一次发布会覆盖且无法追溯。

### 8.3 PostgreSQL 故障

```bash
kubectl -n ray-train-platform get sts,pod,pvc
kubectl -n ray-train-platform describe pod postgres-0
kubectl -n ray-train-platform logs postgres-0 --tail=500
kubectl -n ray-train-platform exec postgres-0 -- pg_isready
```

- PVC Pending：检查 `ebs-ssd`、可用区和 EBS 配额。
- Pod CrashLoop：优先查看日志，不要删除 PVC。
- 数据逻辑损坏：停止 Frontend/Backend 写入后，从受保护的 SQL 备份恢复。
- EBS 故障或节点故障：StatefulSet 可在兼容可用区重新调度并重新挂载 EBS。

当前单实例没有自动数据库故障切换，这是已知风险。

### 8.4 任务一直 Pending

```bash
kubectl -n tenant-local get rayjobs,rayclusters,workloads,localqueues
kubectl get clusterqueue cluster-gpu-queue
kubectl -n tenant-local describe workload <workload>
kubectl -n tenant-local describe pod <pending-pod>
kubectl get nodes -l 'platform.wellspiking.ai/gpu-pool=production' \
  -o custom-columns=NAME:.metadata.name,READY:.status.conditions[-1].status,GPU:.status.allocatable.nvidia\.com/gpu
```

`describe pod` 的调度事件是权威依据：

- `Insufficient nvidia.com/gpu`：等待任务结束或减少 GPU。
- `Insufficient cpu/memory`：请求超过节点剩余资源；CPU/内存是每个 Worker Pod 的值。
- `didn't match Pod's node affinity/selector`：节点标签或 ResourceFlavor 错误。
- Kueue 未 admitted：团队/集群配额不足或上一任务 TTL 尚未释放。
- `FailedMount`：进入 FSX 排障，不属于调度故障。

### 8.5 任务 Pod Error，但挂载正常

先看 submitter 和 worker 日志：

```bash
kubectl -n tenant-local get pods -l platform_job_id=<job-id> -o wide
for pod in $(kubectl -n tenant-local get pod -l platform_job_id=<job-id> -o name); do
  echo "===== $pod ====="
  kubectl -n tenant-local logs "$pod" --all-containers --tail=500
done
```

如果日志已经显示成功读取 `/mnt/data/input`、`/mnt/data/checkpoints`，且 Ray 任务真正启动，最后为 Python/命令退出码 1，则应修复训练代码或验收脚本。不要重启 FSX。

### 8.6 FSX/TOS 挂载失败

#### 判定

```bash
kubectl -n <namespace> describe pod <pod> | sed -n '/Events:/,$p'
kubectl -n <namespace> get pvc
kubectl get pv <pv> -o yaml
kubectl -n kube-system get deploy csi-fsx-controller
kubectl -n kube-system get ds csi-fsx-node
kubectl -n kube-system get pods -o wide | grep -E 'csi-fsx|fsx-agent'
```

PVC `Bound` 只表示控制面绑定成功，不代表节点 FUSE 会话健康。CSI/Agent Pod Running 也不代表已有 FUSE bind mount 可读写。

#### 节点级探测

```bash
kubectl -n mlflow-system get ds mlflow-fsx-probe mlflow-fsx-dns-probe
kubectl -n mlflow-system get pods -o wide
kubectl -n mlflow-system logs -l app.kubernetes.io/name=mlflow-fsx-probe --tail=100 --prefix
kubectl -n mlflow-system logs -l app.kubernetes.io/name=mlflow-fsx-dns-probe --tail=100 --prefix
```

探针全 Ready 且真实 `stat/读写/删除` 成功时，不应仅凭 FSX 脚本中带 `ERROR` 字样的“status has finished successfully”判断故障。火山 FSX 工具的日志级别可能误导，要以 RPC、Event 和有界 I/O 结果为准。

#### 恢复顺序

以下是有状态恢复，必须先停止该节点承接新任务并确认没有正在运行的用户训练：

1. 恢复 DNS；DNS 未恢复时不要反复重启 FSX。
2. 找到故障节点上的 `fsx-agent`，只重建该 Pod，等待 Agent Ready，并确认 `fsx-health-check` 通过。
3. 再只重建该节点上的 `csi-fsx-node` Pod，等待全部容器 Ready。
4. 最后重建受影响的业务 Pod。已经失效的 FUSE bind mount 不会因 CSI 恢复自动重连。
5. 等待两个 FSX 探针全 Ready，并执行 `ops/mlflow/verify.sh` 与平台 FSX 前缀验收。

不要配置“挂载失败就自动重启所有业务 Pod”的 liveness 策略；它会造成重启风暴，却不修复节点 FUSE 会话。

同一节点反复故障时，收集 `/var/log/fsx` 和 `/opt/fsx/tools/sysinfo_collector.sh` 诊断包，升级 VKE CSI-FSX/FSX Agent，提交火山引擎支持。

### 8.7 DNS 不稳定

Pod 最多接受 3 个 nameserver。把 4 个 DNS 同时写进 Pod `dnsConfig.nameservers` 会产生 `DNSConfigForming`，最后一个会被丢弃。

当前策略：

- 火山 TOS/STS/API 域名通过 CoreDNS 分流到 `100.96.0.2`、`100.96.0.3`。
- IDC 域名走原 IDC DNS 链。
- hostNetwork FSX Agent 由节点 `systemd-resolved` 路由分流。

检查：

```bash
bash ops/dns/deploy-coredns-split-dns.sh --check
kubectl -n kube-system get configmap coredns -o yaml
kubectl -n kube-system logs deploy/coredns --tail=200
kubectl -n mlflow-system logs -l app.kubernetes.io/name=mlflow-fsx-dns-probe --tail=100 --prefix
```

若 IDC DNS 超时但 VKE DNS 可解析 TOS，FSX/TOS 可继续工作，IDC Git/NFS/内部域名可能受影响。不要把所有域名永久转到 VKE DNS，因为它不能替代 IDC 私有域解析。

### 8.8 MLflow 不可用或 Artifact 失败

```bash
kubectl -n mlflow-system get deploy,sts,ds,pod,pvc -o wide
kubectl -n mlflow-system logs deploy/mlflow --tail=300
kubectl -n mlflow-system logs deploy/mlflow-ingest --tail=300
kubectl get --raw '/api/v1/namespaces/mlflow-system/services/http:mlflow:5000/proxy/mlflow/health'
bash ops/mlflow/verify.sh
```

- `/healthz` 200、`/health` 403：写入网关策略可能是正常的，不代表 MLflow Down。训练只应访问被允许的 ingest 路径。
- MLflow Ready 但 Artifact I/O 卡住：进入 FSX 节点恢复流程。
- MLflow DB 不 Ready：检查 `mlflow-postgres-0` 和 20Gi EBS PVC。
- 部署提示 Lease locked：先确认没有部署进程、升级 Job 和 Pod，再严格按 `ops/mlflow/README.md` 的 resourceVersion 安全解锁；不得删除 Lease 或强制覆盖。

### 8.9 Loki 日志缺失

```bash
kubectl -n monitoring get sts,pod -l app.kubernetes.io/name=alloy
kubectl -n monitoring logs sts/alloy --tail=300
kubectl -n loki get sts,deploy,pod,pvc -o wide
kubectl -n loki logs sts/loki-cpu --all-containers --tail=300
bash ops/observability/loki/30-verify-loki.sh
```

排查链路：

```text
训练 Pod 有 stdout
  → Alloy discovery 能发现 platform_job_id
  → Alloy push 到 loki-cpu-gateway
  → Gateway 后端 Endpoint Ready
  → Loki 写入 TOS、查询返回
```

Loki 3 副本使用硬反亲和和 `DoNotSchedule`。当一个 CPU 节点资源不足时，Pod 会 Pending，而不是挤到已有 Loki 节点。先执行 `bash ops/dns/reconcile-coredns-placement.sh --apply`，修正 CoreDNS 的超额预留和调度偏好；如仍无足够资源再扩容 CPU 节点。不要取消反亲和把 3 个副本堆在同一节点伪装高可用。

若 CoreDNS 刚完成调度变更后 Loki Gateway 日志出现 `could not be resolved` 和 502，先确认 Service DNS 已恢复，再滚动刷新网关的 Nginx resolver 状态：

```bash
kubectl -n loki exec deploy/loki-cpu-gateway -- getent hosts loki-cpu.loki.svc.cluster.local
kubectl -n loki rollout restart deployment/loki-cpu-gateway
kubectl -n loki rollout status deployment/loki-cpu-gateway --timeout=5m
bash ops/observability/loki/30-verify-loki.sh
```

### 8.10 Prometheus/Grafana 或 GPU 指标缺失

```bash
kubectl -n <namespace> get rayjobs,rayclusters,pods -o wide
kubectl -n monitoring get deploy,sts,ds,pod,pvc
kubectl -n monitoring get servicemonitor,prometheusrule
kubectl -n kube-system get ds dcgm-exporter -o wide
kubectl -n kube-system logs ds/dcgm-exporter --tail=200
bash ops/observability/prometheus-operator/verify.sh
```

任务级 GPU 曲线的完整查询链路是 `DCGM exporter → Prometheus → 后端任务级接口 → Portal`；ServiceMonitor 负责让 Prometheus 发现 DCGM target，但浏览器不直接查询 Prometheus。后端只使用任务记录中已持久化的 `KubernetesNS` 和 `RayClusterName` 构造选择器，并将后者限制为对应的 Worker Pod 前缀。浏览器只能选择白名单时间窗口，不能提供 PromQL、namespace 或 Pod 正则表达式。

Portal 每 30 秒轮询一次，并合并相同请求键（任务 ID + 时间窗口）的并发请求。Prometheus 短暂报错时，页面保留上一次成功结果并提示重试，不把缺失样本显示成零。

任务训练正常但曲线为空时，严格按以下顺序排查：

1. 平台任务记录是否已经观察并持久化对应 RayCluster。
2. 该 RayCluster 的训练 Worker Pod 是否已经 Running；排队中或 Worker 未启动时无 GPU 样本是正常现象。
3. Prometheus 中对应 DCGM target 是否为 Up，exporter 是否持续暴露该 Worker GPU 的指标。
4. 后端查询 Prometheus 是否成功，以及所选时间范围是否仍在 Prometheus 保留期内。

这是只读监控链路。曲线缺失不会改变训练状态、参数、数据、通信或结果；不要通过重启 RayJob、Worker、GPU 驱动或 FSX Agent 修复展示问题，这些操作不能修复查询链路，反而可能中断正常训练。

任务级接口只返回已授权任务的 Worker GPU。管理员的“显卡物理矩阵”仍是独立的全局资源视图，用于查看物理节点和全卡状态；不要把它与任务详情曲线合并，也不要向普通任务提交者开放全局矩阵接口。

### 8.11 镜像拉取失败

```bash
kubectl -n <namespace> describe pod <pod> | sed -n '/Events:/,$p'
kubectl -n <namespace> get secret harbor-registry
kubectl -n <namespace> get serviceaccount default -o yaml
```

检查 digest 是否存在、目标 namespace 是否有 pull Secret、镜像仓库网络是否可达。生产 Profile 要求 digest；不要临时改成 `latest`。

### 8.12 GPU、CUDA 或 NCCL 故障

```bash
kubectl get nodes -l 'platform.wellspiking.ai/gpu-pool=production' \
  -o custom-columns=NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu
kubectl -n kube-system get ds nvidia-device-plugin dcgm-exporter -o wide
kubectl -n tenant-local logs <worker-pod> --tail=1000 | grep -Ei 'NCCL|CUDA|OOM|NaN|timeout'
```

- `nvidia-smi` 不存在：镜像工具问题，不等于节点没有 GPU。
- `CUDA out of memory`：调整 batch、显存策略或每进程 GPU 使用，不是调度故障。
- 多机 NCCL 超时：检查两个 Worker 是否在不同节点、网络端口、rank/world size、启动器参数和同版本环境。
- Kueue 已分配 GPU 但利用率为 0：训练进程可能卡在数据加载、初始化或 barrier。

## 9. 清理、重装和资源保护

平台 reset 默认只预览：

```bash
bash ops/platform/reset.sh --profile deploy/profiles/vke-cpu-ha.yaml
```

它只识别本项目标签和目标 namespace，永远排除 TOS 对象、共享 KubeRay、Kueue Controller、CSI、Loki、Alloy、Ingress 和身份系统。真正执行必须显式确认，并且只能在完成备份和人工评审预览后进行：

```bash
bash ops/platform/reset.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --execute \
  --confirm-reset-ray-platform
```

不要使用通配符批量删除 `tenant-local` 中所有 Job/Pod。历史失败 Pod、RayJob、平台数据库记录和 Loki 日志属于不同对象；清理前要确认平台历史是否仍需展示。

## 10. 备份与恢复矩阵

| 数据 | 数据真相 | 备份方式 | 恢复方式 | 当前风险 |
| --- | --- | --- | --- | --- |
| 平台元数据 | 平台 PostgreSQL | `backup-state.sh` / `pg_dump` | `restore-metadata.sh` | 单实例。 |
| 平台 Secret | Kubernetes Secret | `backup-state.sh`，离库加密 | `restore-secrets.sh` | 泄露后必须轮换。 |
| 用户/公共/团队数据 | TOS | 桶版本、生命周期、跨桶/离线备份策略 | 桶侧恢复 | 平台 reset 不触碰。 |
| Checkpoint/任务结果 | TOS 个人前缀 | 同 TOS | 同 TOS | 用户删除/覆盖策略需审计。 |
| Loki 日志 | TOS + Loki EBS 工作目录 | 桶侧策略；EBS 不是唯一真相 | 重建 Loki 并读取对象 | 当前 active 副本应保持 3。 |
| Prometheus 指标 | 两个独立 EBS PVC | 可选 VolumeSnapshot | PVC/Snapshot 恢复 | 15 天窗口，可重抓但历史不可重建。 |
| Grafana 配置 | ConfigMap/Helm values | Git | 重新部署 sidecar 配置 | UI 临时修改不保证保存。 |
| MLflow 元数据 | MLflow PostgreSQL | 独立 `pg_dump` | 独立恢复 | 单实例。 |
| MLflow Artifact | TOS 专用前缀 | 桶侧策略 | 同一前缀重新挂载 | DB 与 Artifact 时间点需一致。 |
| Ray Dashboard | RayCluster 内存状态 | 不备份 | 无 | 仅运行期存在。 |

每季度至少做一次恢复演练，不能只验证备份文件存在。

## 11. 告警和维护建议

建议至少建立：

- Node NotReady、GPU allocatable 数量变化。
- Kueue Pending Workload 持续时间和配额占用。
- Frontend/Backend Ready 副本少于 2。
- PostgreSQL/MLflow PostgreSQL 不可用、PVC 使用率。
- CSI-FSX/FSX Agent DaemonSet 不完整。
- FSX DNS 探针退化、FSX I/O 探针失败或无法调度。
- Loki 少于 3 副本、Alloy push 错误、日志查询失败。
- Prometheus/Grafana/Alertmanager 副本不足、Prometheus PVC 接近上限。
- DCGM scrape 失败、GPU 温度/功耗/显存异常。
- 私网 ALB 5xx 和 Backend API 5xx。
- TOS API 错误率、容量和生命周期异常。

建议维护节奏：

| 周期 | 操作 |
| --- | --- |
| 每日 | 节点、Pod、Event、Kueue、FSX 探针、Loki/MLflow 快速检查。 |
| 每周 | 平台、MLflow、Prometheus、Loki verify；1 卡 smoke；备份结果检查。 |
| 每月 | 受控滚动重启演练、镜像漏洞/证书检查、PVC 和 TOS 容量评审。 |
| 每季度 | 平台 DB 与 MLflow DB 恢复演练；节点替换和多机训练验收。 |

## 12. 当前已知问题和生产风险

本节必须随生产状态更新：

1. 平台 PostgreSQL 和 MLflow PostgreSQL 都是单实例，数据库层没有自动高可用。
2. 平台当前 `APP_ENV=development`，本地认证开启，OIDC 未强制；正式安全交付应切换生产校验并接入 Keycloak。
3. 平台 NetworkPolicy 当前未启用；MLflow 有独立 NetworkPolicy。
4. IDC DNS 最近从 GPU 节点出现间歇性超时。VKE DNS 分流保证 TOS/STS 优先走 VPC DNS，但 IDC Git/NFS/私有域仍依赖 IDC DNS。
5. FSX 探针目前是常驻 DaemonSet。它提供节点级预警，但长期方案应改为轻量 DNS exporter 加有界周期 FSX canary，替代告警上线后再删除当前探针。
6. GPU NVMe 缓存未启用；训练仍直接读取 FSX/TOS，不应宣称已有本地缓存加速。
7. IDC 与 TOS 的用户自助审批式双向同步未开放。
8. 原生 MLflow 当前对已认证用户开放完整管理能力；共享实验和 Artifact 的删除/修改会影响全平台。
9. 2026-08-23 巡检发现线上 Helm revision 85 的 Backend 镜像和 MLflow tracking 配置与 `deploy/profiles/vke-cpu-ha.yaml` 不一致。下一次平台发布前必须确定正确基线并回写 Profile。
10. 构建机仓库当前存在未提交文件，且构建目录属主与 root 发布用户不一致；它不能直接作为正式 release 证据。应以本地已提交版本为基础整理内部 Git release，再让构建机使用干净 checkout。

## 13. 故障升级需要收集什么

不要只发截图或一句“挂载失败”。至少保存以下信息：

```text
发生时间和时区
用户、租户、平台 job-id
RayJob、Pod、Node 名称
kubectl describe pod 完整 Events
submitter/head/worker 对应容器日志
PVC/PV 名称和状态
Kueue Workload 状态
同节点 csi-fsx-node 与 fsx-agent 日志
FSX DNS/I/O 探针日志
平台 Helm revision 和镜像 digest
是否可稳定复现、是否只发生在一个节点
```

采集模板：

```bash
incident_dir="/secure/incidents/<timestamp>-<job-id>"
mkdir -p "$incident_dir"
chmod 700 "$incident_dir"

kubectl -n tenant-local get rayjob <job-id> -o yaml > "$incident_dir/rayjob.yaml"
kubectl -n tenant-local get pod -l platform_job_id=<job-id> -o yaml > "$incident_dir/pods.yaml"
kubectl -n tenant-local get events --sort-by=.lastTimestamp > "$incident_dir/events.txt"
helm -n ray-train-platform status ray-platform > "$incident_dir/platform-helm.txt"
```

日志、YAML 和 Helm status 仍可能包含用户路径、镜像地址和内部主机名，发给外部支持前必须脱敏。Secret、token 和环境变量值不得进入诊断包。

## 14. 运维命令速查

```bash
# 平台
bash ops/platform/preflight.sh --profile deploy/profiles/vke-cpu-ha.yaml --verify-fsx-irsa
bash ops/platform/deploy.sh --profile deploy/profiles/vke-cpu-ha.yaml --verify-fsx-irsa --timeout 15m
bash ops/platform/verify.sh --profile deploy/profiles/vke-cpu-ha.yaml

# MLflow
bash ops/mlflow/deploy.sh
bash ops/mlflow/verify.sh

# Prometheus/Grafana
bash ops/observability/prometheus-operator/deploy.sh
bash ops/observability/prometheus-operator/verify.sh

# Loki/Alloy
bash ops/observability/loki/deploy-ha.sh --install
bash ops/observability/loki/30-verify-loki.sh
bash ops/observability/alloy/deploy-ha.sh --install

# DNS
bash ops/dns/deploy-coredns-split-dns.sh --check

# GPU 和任务
bash ops/gpu/verify-production-pool.sh
kubectl -n tenant-local get rayjobs,rayclusters,workloads,pods -o wide

# Helm
helm list -A
helm -n ray-train-platform history ray-platform
```

更详细的专项说明：

- [生产架构](ARCHITECTURE.md)
- [构建与部署](BUILD_AND_DEPLOY.md)
- [管理员手册](ADMIN_GUIDE.md)
- [MLflow 运维说明](../ops/mlflow/README.md)
- [Prometheus Operator 运维说明](../ops/observability/prometheus-operator/README.md)
- [DNS 分流说明](../ops/dns/README.md)
- [数据路径与 FSX 排障](DATA_IO_BENCHMARK.md)
