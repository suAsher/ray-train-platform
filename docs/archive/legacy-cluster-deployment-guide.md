# Ray Training Platform V1 生产部署手册

> **历史设计资料。** 当前生产架构见 [ARCHITECTURE.md](ARCHITECTURE.md)，当前部署步骤见 [BUILD_AND_DEPLOY.md](BUILD_AND_DEPLOY.md)。本页不再作为新集群的操作入口。

这套部署目标是：VKE + KubeRay + Kueue + PostgreSQL + Keycloak + Loki + Prometheus Operator，在两台 RTX 4090×8 生产节点（16 卡）上提交真实 RayJob；原单卡节点只保留为独立 smoke-test 池。平台不会接受用户提交的 LDAP 密码，LDAP 只作为现有 Keycloak 的 User Federation。

## 0. 当前代码边界

- API、reconciler、Vue Portal、PostgreSQL migration、Keycloak OIDC、Loki 日志查询和 RayCluster 调试入口已经落到仓库。
- 生产任务只允许 sha256 digest 镜像；Git 代码必须提供固定 commit；训练和调试 Pod 使用 PVC，不使用 `hostPath`。
- 任务状态事实来自 Kubernetes/KubeRay，数据库保存任务声明、租户隔离、outbox、审计和状态快照。
- V1 依赖运维预先创建租户 namespace、LocalQueue 和最小权限的 TOS/IDC PVC；平台不会根据用户输入创建任意 namespace、StorageClass、Bucket policy、Secret 或 hostPath。

## 1. VKE 前置条件

### 1.1 节点和 GPU

确认 NVIDIA device plugin 已安装，并给两台生产训练节点打标：

```bash
kubectl label node <worker-a> <worker-b> \
  accelerator=nvidia-rtx-4090 \
  platform.wellspiking.ai/gpu-pool=production --overwrite
kubectl get nodes -L accelerator,platform.wellspiking.ai/gpu-pool
kubectl get nodes -o custom-columns=NAME:.metadata.name,GPU:.status.allocatable.nvidia\\.com/gpu
```

Ray worker Pod 会固定选择 `accelerator=nvidia-rtx-4090,platform.wellspiking.ai/gpu-pool=production`。不要给单卡 smoke-test 节点加生产池标签，避免调度器把 16 卡任务提交到错误节点。

### 1.2 KubeRay 和 Kueue

KubeRay Operator 已部署时，仍要确认 CRD 版本和字段：

```bash
kubectl get crd rayjobs.ray.io rayclusters.ray.io
kubectl api-resources --api-group=ray.io
kubectl get crd rayjobs.ray.io -o json | jq '.spec.versions[] | {name,served,storage}'
```

平台默认使用 `KUBERAY_RAYJOB_CLUSTER_SPEC_FIELD=rayClusterSpec`，适配当前 KubeRay v1 CRD；后端启动时会读取 CRD schema 并拒绝不匹配的配置。旧版现场如果只有 `rayClusterConfig`，再在 Helm values 中显式切换。

确认 Kueue API 和 ClusterQueue：

```bash
kubectl api-resources --api-group=kueue.x-k8s.io
kubectl get clusterqueue cluster-gpu-queue
kubectl get resourceflavor gpu-4090-flavor
```

如果 Kueue 资源还未创建，按现场配额修改 [k8s/base/kueue-resources.yaml](/Users/ashersu/Desktop/西井/ray-train-platform/k8s/base/kueue-resources.yaml) 后应用。每个租户 namespace 需要一个同名的 `LocalQueue`，例如 `team-a-gpu`，并将它绑定到 `cluster-gpu-queue`。

### 1.3 PostgreSQL

使用 IDC/VKE 外部 PostgreSQL，不在平台 Pod 内运行数据库。创建只包含连接串的 Secret：

```bash
kubectl -n ray-train-platform create secret generic ray-platform-postgres \
  --from-literal=DATABASE_URL='postgres://<user>:<password>@<host>:5432/<database>?sslmode=require'
```

连接串不要提交到 values、镜像或日志中。

### 1.3.1 Loki/Alloy 标签

Alloy 必须把 Pod label `platform_job_id` 保留为 Loki label `platform_job_id`，否则 Portal 查询不到任务日志。部署 Alloy 时加入等价的 relabel 规则：

```river
loki.relabel "ray_platform" {
  rule {
    source_labels = ["__meta_kubernetes_pod_label_platform_job_id"]
    target_label  = "platform_job_id"
  }
}
```

平台日志接口只接受平台生成的 job ID，并以 `{platform_job_id="..."}` 查询，避免用户构造任意 LogQL。

### 1.3.2 Prometheus 训练指标约定

任务详情的指标接口只查询平台约定的四个指标名，并按 `platform_job_id` 标签隔离任务：

- `platform_training_loss`
- `platform_training_throughput`
- `platform_learning_rate`
- `platform_training_epoch`

训练镜像或训练侧 exporter 需要将这些指标暴露给现有 Prometheus，并通过 Kubernetes/Prometheus relabel 保留 Ray Pod 的 `platform_job_id` 标签。没有指标样本时 UI 显示“尚未提供指标”，不会从日志正则或固定样例推导 Loss。

### 1.4 Keycloak / LDAP

在已有 Keycloak 中创建 Portal 的 public client：

- Standard Flow 开启，Client authentication 关闭。
- PKCE 使用 `S256`。
- Valid redirect URI：`https://<portal-host>/*`，并精确包含 `/silent-check-sso.html`。
- Web origins 只填 Portal 域名，不填 `*`。
- LDAP 用户由 Keycloak User Federation 同步；平台只验证 OIDC access token。
- Realm role：`SuperAdmin`、`TenantAdmin`、`Engineer`。
- Group：`platform/tenants/<tenant-id>`，用户至少拥有一个租户 group。
- API audience 配成 `ray-training-platform-api`，与 `OIDC_AUDIENCE` 一致。

部署前准备 issuer、realm、client id、audience 和 redirect URI。平台不会保存 Keycloak client secret。

### 1.5 TOS、IDC 与用户数据空间

`shanghai-data-transfer` 是训练用户数据桶；`vke-cluster` 只供 Loki、备份等平台组件使用，绝不挂入训练或调试 Pod。用户在 Portal 只会看到“我的工作区 / 我的文件 / 我的训练结果 / 团队共享 / 公共数据 / IDC 数据”，不会接触桶名、对象前缀、PV/PVC 或 AK/SK。

TOS 采用 **后台挂载适配器**：每个用户、每个租户各有一个只指向单一前缀的静态 PV/PVC；公共数据也为每个租户建立独立的只读 PVC，因为 PVC 不可跨 namespace 复用。Pod 的稳定路径如下：

| 路径 | 用途 | 权限 |
| --- | --- | --- |
| `/workspace`、`/mnt/storage/me` | 个人工作区、文件、训练结果 | 读写 |
| `/mnt/storage/team` | 团队共享 TOS 数据 | 只读 |
| `/mnt/storage/public` | 公共 TOS 数据 | 只读 |
| `/mnt/idc/original`、`/mnt/idc/wellspiking`、`/mnt/idc/shared` | IDC NFS 数据 | 只读 |
| `/mnt/cache` | GPU 节点临时缓存 | 可丢失 |

个人与共享 TOS PV 必须省略 `secretName`；正式鉴权由 csi-fsx **组件级 IRSA** 完成。打开 `dataSpaces.enabled` 前，必须完成真实双前缀验证：

```bash
cd /opt/guofeng/vke-cluster/ray-platform
KUBECONFIG=/etc/kubernetes/admin.conf \
FSX_TOS_SERVER=tos-cn-shanghai.ivolces.com \
FSX_TOS_REGION=cn-shanghai \
FSX_TOS_BUCKET=shanghai-data-transfer \
bash ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh
```

只有输出 `IRSA prefix mount contract verified` 才能启用数据空间；任何失败均保持 `dataSpaces.enabled=false`。该验证清单不接收也不引用 AK、SK 或 Secret。

IDC 三个 NFS 导出分别创建静态只读 PV/PVC，使用 NFS 只读导出、PV 与 `volumeMount.readOnly=true` 三层限制。个人 SSHFS、节点 fstab 目录与 `/root/.ssh` 不进入 RayJob 或调试环境；它们无法保证每个 Worker 一致可见，也会破坏隔离。

```bash
kubectl get csidriver fsx.csi.volcengine.com
kubectl -n tenant-team-a get pvc
kubectl -n tenant-team-a get pvc idc-original-ro idc-wellspiking-ro idc-shared-ro -o wide
```

必须确认 VKE 到 IDC 的专线/VPC 路由、DNS、MTU、只读权限和多节点并发访问。

### 1.5.1 GPU 节点多数据盘：本地缓存（可选）

节点数据盘只能作为**任务级缓存**，不能成为数据资产、Checkpoint 或模型产物的唯一位置。平台默认关闭该能力；启用后，每个 Ray Head / Worker Pod 会获得一个独立的通用临时 PVC，挂载到 `/mnt/cache`，Pod 删除后其 PVC 和缓存也随之回收。Ray 的 session、日志和对象溢写也会放到该路径，不能依赖它保留训练结果。

按以下顺序实施，所有 GPU 节点都通过后才能打开开关：

1. 在每台 GPU 节点挂载并健康检查数据盘；路径、磁盘类型和容量必须一致。
2. 安装并配置 VKE `csi-local`，创建 `ray-cache-local` StorageClass：本地盘、`ReadWriteOnce`、`volumeBindingMode: WaitForFirstConsumer`、回收策略 `Delete`。实际 driver 参数以当前 VKE csi-local 文档和已安装版本为准。
3. 用带 generic ephemeral volume 的探针 Pod 在**每一台** GPU 节点验证：PVC 可绑定、容器以非 root 用户可写、Pod 删除后 PVC 被回收。不能只在一台节点上验证。
4. 设置 `training.localCache.enabled=true`，并以与单个 worker 可用磁盘相匹配的 `size` 启动一个单卡 smoke 训练。

启用后的训练代码可先把选择的 TOS 或 IDC 输入复制到 `$PLATFORM_CACHE_PATH`，从缓存读取；最终 checkpoint、模型和指标始终写回 `$PLATFORM_OUTPUT_PATH`。禁止 `hostPath`、节点 fstab 挂载 TOS、以及个人 SSHFS 用作 RayJob/Dev Workspace 的分布式数据卷。

## 2. 构建不可变镜像

仓库根目录的 [build-image.sh](../build-image.sh) 统一构建测试阶段需要的四个镜像，并默认适配 VKE 的 `linux/amd64`。脚本默认不推送；确认 Docker 已登录阿里云仓库后再显式打开推送：

```bash
docker login registry.cn-shanghai.aliyuncs.com
REGISTRY=registry.cn-shanghai.aliyuncs.com/<namespace> \
IMAGE_TAG=test-20260809 \
PUSH_IMAGE=true \
bash build-image.sh
```

只构建平台控制面：

```bash
BUILD_TARGETS=backend,frontend,source-materializer \
PUSH_IMAGE=true \
bash build-image.sh
```

脚本结束时会打印远端镜像的 `@sha256:<digest>`。构建前可用 `DRY_RUN=true bash build-image.sh` 检查命令，不需要 Docker daemon。

后端和前端镜像都要使用不可变 tag 或 digest，训练镜像还必须在任务请求中使用 `image@sha256:<64 hex>`。例如：

```bash
docker build -t <registry>/ray-train-backend:<git-sha> ./backend
docker build -t <registry>/ray-train-frontend:<git-sha> ./frontend
docker push <registry>/ray-train-backend:<git-sha>
docker push <registry>/ray-train-frontend:<git-sha>
```

另外构建并推送代码源物化镜像；它只负责把固定 Git commit、TOS 压缩包或 IDC 快照放入 `/workspace`，训练镜像仍需预装 Ray/PyTorch/业务运行时：

```bash
docker build -t <registry>/ray-source-materializer:<git-sha> ./images/source-materializer
docker push <registry>/ray-source-materializer:<git-sha>
```

单 GPU 烟囱测试可以额外构建 [test-training](../images/test-training/Dockerfile)，并将 [smoke/train.py](../examples/smoke/train.py) 提交到 `gitAllowlist` 内的 Git 仓库：

```bash
docker build -t <registry>/ray-test:<git-sha> -f images/test-training/Dockerfile images/test-training
docker push <registry>/ray-test:<git-sha>
```

将三个平台镜像和 `ray-test` 镜像都解析成 digest；测试时 `backend.workspaceImage` 可以复用 `ray-test` digest。

将该镜像的不可变 digest 写入 `backend.sourceMaterializerImage`。Git 私有仓库凭据不允许写进 URL；当前 V1 的 Git 物化路径面向 allowlist 内的 HTTPS 仓库，私有仓库如需凭据应扩展为租户 namespace 内的最小权限 Secret。

Ray API 与后端访问 TOS 的凭据 Secret 必须创建在平台控制面 namespace（默认 `ray-train-platform`），并将其名称配置为 Helm 的 `tos.secretName`。该 Secret 的 key 必须精确为 `region`、`access-key`、`secret-key`，并可选 `security-token`；旧式 AWS 风格 key 名不受支持。它只供后端/Ray API 的 TOS 上传与校验使用，不能作为任意租户或 Ray source-materializer 的凭据。若 source-materializer 保留了租户级凭据需求，必须使用该租户 namespace 内单独、最小权限的 Secret，不能复用平台控制面 TOS Secret。

如果镜像仓库是私有仓库，控制面 namespace 和每个租户 namespace 都要预创建同名 `imagePullSecret`，并在 values 中配置 `global.imagePullSecrets`；平台会把该名称传递给后端、前端、Ray head/worker 和调试集群。

训练镜像应预装 Ray、PyTorch、JupyterLab 和业务依赖；调试工作区不在 Pod 启动时执行 `pip install`。训练/调试 Pod 默认不引用平台 namespace 的 ServiceAccount，也不自动挂载 Kubernetes API token；如果训练代码需要 Kubernetes API，必须在对应租户 namespace 预创建最小权限 ServiceAccount，再通过 `backend.rayJobServiceAccount` 配置。

开启 `dataSpaces` 时，部署前的 FSX/IRSA 验证默认使用公共的 `harbor.wellspiking.ai/hub/library/busybox:1.36`，与平台私有工作区镜像及其拉取凭据隔离。隔离网络可通过 `FSX_SMOKE_IMAGE=<可拉取的 BusyBox 镜像>` 覆盖它；该验证不向工作负载注入 AK/SK。

## 3. Helm 部署

先复制 values 并替换所有 `REPLACE_WITH_`、Keycloak、域名、镜像仓库、IDC claim、`gitAllowlist` 和 VKE ALB 注解：

```bash
helm lint ./helm/ray-train-platform
helm upgrade --install ray-platform ./helm/ray-train-platform \
  --namespace ray-train-platform --create-namespace \
  --set global.imageRegistry=<registry> \
  --set backend.image.tag=<backend-git-sha> \
  --set frontend.image.tag=<frontend-git-sha> \
  --set backend.workspaceImage=<registry>/ray-workspace@sha256:<64-hex> \
  --set backend.sourceMaterializerImage=<registry>/ray-source-materializer@sha256:<64-hex> \
  --set tos.secretName=<platform-tos-credential-secret> \
  --set backend.oidc.issuerURL=https://<keycloak>/realms/<realm> \
  --set frontend.runtimeConfig.keycloakURL=https://<keycloak> \
  --set frontend.runtimeConfig.keycloakRealm=<realm> \
  --set ingress.host=<portal-host>
```

### 3.1 单 GPU 测试部署

当前只有一台单 GPU 节点时，不要使用生产的 24 GPU Kueue 清单。先给测试节点打上平台约定的标签，并创建测试租户和单卡队列：

```bash
kubectl label node <single-gpu-node> accelerator=nvidia-rtx-4090 --overwrite
kubectl apply -f k8s/test/tenant-local.yaml
kubectl apply -f k8s/test/kueue-resources.yaml
```

测试 profile 位于 [values-test.yaml.example](../helm/ray-train-platform/values-test.yaml.example)。它只用于隔离环境：关闭 OIDC、关闭 IDC/TOS、关闭 Ingress，使用 `local` 匿名测试身份；数据库仍然必须使用 PostgreSQL，不能把 SQLite 或内存数据当成生产替代品。测试部署前先准备 `ray-platform-postgres` Secret，并将三个镜像替换成固定 digest：

```bash
export TEST_POSTGRES_PASSWORD="$(openssl rand -hex 16)"
export TEST_DATABASE_URL="postgres://platform:${TEST_POSTGRES_PASSWORD}@postgres.ray-train-platform.svc.cluster.local:5432/platform?sslmode=disable"
kubectl create namespace ray-train-platform --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ray-train-platform create secret generic ray-test-postgres-password \
  --from-literal=POSTGRES_PASSWORD="$TEST_POSTGRES_PASSWORD" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f k8s/test/postgres.yaml
kubectl -n ray-train-platform create secret generic ray-platform-postgres \
  --from-literal=DATABASE_URL="$TEST_DATABASE_URL" \
  --dry-run=client -o yaml | kubectl apply -f -
helm lint ./helm/ray-train-platform -f values-test.yaml
helm upgrade --install ray-platform ./helm/ray-train-platform \
  -n ray-train-platform --create-namespace -f values-test.yaml
kubectl -n ray-train-platform rollout status deploy/ray-train-backend --timeout=5m
kubectl -n ray-train-platform rollout status deploy/ray-train-frontend --timeout=5m
kubectl -n ray-train-platform port-forward svc/ray-train-frontend 8088:80
```

然后访问 `http://127.0.0.1:8088`，可以直接查看 UI。测试任务使用单 worker、单 GPU、Git 固定 commit；在 demo profile 下可省略 Keycloak token：

```bash
API_URL=http://127.0.0.1:8088 \
ALLOW_ANONYMOUS=true \
IMAGE='<registry>/ray-test@sha256:<64-hex>' \
GIT_URL='https://<allowed-git-host>/<org>/<repo>.git' \
GIT_COMMIT='<commit-sha>' \
bash scripts/e2e-training.sh
```

这会验证真实 RayJob 的提交、状态变化和终态。Loki/Prometheus 尚未部署时，任务状态仍可验收，但日志和指标页面会显示后端不可用/暂无数据，这是预期的阶段性结果。

Helm hook 会先运行 PostgreSQL migration Job；成功后启动 API。确认：

```bash
kubectl -n ray-train-platform get job ray-train-platform-migrations
kubectl -n ray-train-platform rollout status deploy/ray-train-backend
kubectl -n ray-train-platform rollout status deploy/ray-train-frontend
kubectl -n ray-train-platform get pods,ingress
```

后端多副本共享 API，但只有拿到 `Lease/ray-train-platform-controller` 的副本运行 reconciler；API Pod 重启不会重复创建 RayJob。

## 4. 验收顺序

### 4.1 自动前置检查

```bash
NAMESPACE=ray-train-platform \
KEYCLOAK_ISSUER=https://<keycloak>/realms/<realm> \
IDC_PVC=idc-training-rwx \
HELM_VALUES_FILE=/path/to/values-prod.yaml \
bash scripts/preflight-vke.sh
```

### 4.2 Keycloak 登录、任务和日志

拿到真实 Keycloak access token 后运行：

```bash
API_URL=https://<portal-host> \
ACCESS_TOKEN='<short-lived-token>' \
IMAGE='<registry>/ray-train@sha256:<64-hex>' \
GIT_URL='https://<allowed-git-host>/<org>/<repo>.git' \
GIT_COMMIT='<commit-sha>' \
bash scripts/e2e-training.sh
```

验收必须看到 `SUBMITTED/QUEUED/PROVISIONING/RUNNING` 的真实变化，终态为 `SUCCEEDED`，并能在 Portal 任务详情中读取 Loki 日志。取消任务后应经历删除流程，最终为 `CANCELED`。

### 4.3 调试和 IDC PVC

```bash
API_URL=https://<portal-host> \
ACCESS_TOKEN='<short-lived-token>' \
bash scripts/e2e-storage-auth.sh
```

Portal 的 Jupyter URL 是后端认证代理地址，不是 Pod IP 或 `localhost`。如果工作区为 `FAILED`，先看 API 日志、RayCluster events、PVC mount events 和网络策略。

## 5. 日常运维和回滚

```bash
kubectl -n ray-train-platform logs deploy/ray-train-backend --since=10m
kubectl -n ray-train-platform get lease ray-train-platform-controller -o yaml
kubectl -n ray-train-platform get events --sort-by=.lastTimestamp
helm history ray-platform -n ray-train-platform
helm rollback ray-platform <revision> -n ray-train-platform
```

回滚前确认数据库 migration 向后兼容；不要直接删除 PostgreSQL 表。清理任务必须通过 Portal/API 或 Kubernetes 所有权标签进行，禁止按名称批量删除用户 namespace 中的 RayJob。

## 6. 当前环境说明

本开发环境能完成 Go/Vue 编译、fake-client 单元测试和临时 Helm 模板 lint/render；但本地 kubeconfig 指向的 VKE API 地址被运行环境网络策略阻断。因此不能在此处声称已经完成 VKE、Keycloak、PostgreSQL、Prometheus 或 IDC 的实际连通性验收；请在能访问 IDC/VKE 的运维终端执行第 3、4 节命令，并把现场的 CRD 字段、队列名、指标抓取规则和 PVC 名称写入 values。
