# 从零部署与受保护重部署

> **历史兼容资料。** 新集群部署、日常升级与回滚请以 [构建与部署](BUILD_AND_DEPLOY.md) 为唯一操作入口；本页仅保留受保护重置和旧 Profile 的细节。

这是唯一的标准部署流程，适用于当前 VKE 单卡验收和后续 4090×8 集群。它不依赖
历史 Helm revision、固定节点 IP、NodePort 或临时 `--set` 参数。

平台 Chart 只管理 Portal、API、测试 PostgreSQL、平台专属 Kueue 资源和入口。
KubeRay、Kueue Controller、CSI、ALB、Loki、Alloy、Keycloak/LDAP 都是共享前置
组件：本项目只检查和复用，绝不会安装、升级或卸载它们。

训练或调试第一次使用某个租户时，API 会幂等创建该租户的 namespace 与 LocalQueue，
并仅将 Profile 指定的镜像拉取 Secret 复制到该 namespace。这样历史账号恢复后不用
手工创建 `tenant-*` 资源，新加入 GPU 节点也能拉取私有训练镜像；它不会复制任何其他
平台 Secret 或 TOS 凭据。

## 1. Profile 与环境边界

| Profile | 用途 | 数据库 | 控制面 |
| --- | --- | --- | --- |
| `deploy/profiles/vke-test.yaml` | 当前 VKE 单卡验收 | Chart 管理单实例 PostgreSQL | 单副本、`NodePort` 仅供 `internal-alb` 后端使用 |
| `deploy/profiles/test.yaml` | 任意合规集群的最小测试基线 | Chart 管理单实例 PostgreSQL | 单副本、无 Ingress |
| `deploy/profiles/production.yaml.example` | 生产配置模板 | 外部/托管 HA PostgreSQL | API/Portal ≥2 副本、HPA、PDB、Ingress |

Profile 只能存非敏感配置。数据库 URL、PAT pepper、首个管理员密码、TOS 控制面
凭据、镜像仓库密码都只能放在本机受保护的 env 文件和 Kubernetes Secret 中。

`dataSpaces.enabled` 默认关闭。只有完成 FSX CSI 组件级 IRSA 的真实前缀隔离
验收后，才能在部署时传 `--verify-fsx-irsa` 打开它；训练和调试 Pod 永不获得
TOS AK/SK。

`idcDataSpaces.enabled` 同样默认关闭，但它与 TOS 独立：填入 `original`、
`wellspiking`、`shared` 三个 NFS 导出并确认所有 GPU 节点均能只读挂载后才开启。
平台会为每个租户创建受控的静态 `ReadOnlyMany` PV/PVC；不使用节点 fstab、SSHFS
或旧的通用 `storage.existingClaim`。

## 2. 前置条件

```bash
kubectl get crd rayjobs.ray.io rayclusters.ray.io \
  clusterqueues.kueue.x-k8s.io localqueues.kueue.x-k8s.io
kubectl get nodes -L accelerator
kubectl get ingressclass
```

- 至少有一台物理 GPU 节点，其 `nvidia.com/gpu` 为正数，且标签与
  Profile 的 `training.nodeSelector` 一致。
- 已部署 KubeRay、Kueue Controller 和 NVIDIA device plugin。
- `vke-test` 还需要 `internal-alb` IngressClass 和能拉取 Profile 镜像的
  `aliyun-registry` Secret。
- 生产环境需先提供 HA PostgreSQL，并准备只包含 `DATABASE_URL` 的连接信息。
- Loki/Alloy、IDC NFS、FSX CSI 是可选但独立的共享能力；没有 Loki 不阻止平台
  启动，只是历史日志不可查询。

## 3. 构建镜像

在构建机的项目目录执行。发布标签必须是新的、不可变的版本号；不要覆盖已部署
标签。

```bash
cd /opt/guofeng/vke-cluster/ray-platform
IMAGE_TAG=dev-$(date -u +%Y%m%d-%H%M%S) \
BUILD_TARGETS=backend,frontend,source-materializer,test-training,workspace \
PUSH_IMAGE=true USE_BUILDX=false \
bash build-image.sh
```

将新标签写入待部署 Profile 的 `backend.image.tag` 与 `frontend.image.tag`。
`workspaceImage` 和 `sourceMaterializerImage` 必须保持构建输出的 `@sha256`
digest。生产 Profile 同时应填写 API/Portal 的 `image.digest`，并设置
`release.requireImageDigests=true`。

## 4. 准备 Secret 输入文件

复制模板到构建机的受保护目录，填写真实值并限制权限。文件不进入 Git，也不上传
到任意聊天或工单。

```bash
cp deploy/secrets.env.example /secure/ray-platform/vke-test.env
chmod 600 /secure/ray-platform/vke-test.env
vi /secure/ray-platform/vke-test.env
```

测试 Profile 必填：`DATABASE_URL`、`PAT_PEPPER`、`BOOTSTRAP_ADMIN_PASSWORD`、
`POSTGRES_USER`、`POSTGRES_PASSWORD`。`DATABASE_URL` 应指向
Chart 创建的 `postgres.ray-train-platform.svc.cluster.local`。

生产 Profile 只需提供外部 PostgreSQL 的 `DATABASE_URL`，不填写 `POSTGRES_*`。
如需使用 TOS 控制面目录浏览或代码包上传，再一并填写 `TOS_ACCESS_KEY` 与
`TOS_SECRET_KEY`；它们不会进入 Ray Pod。

## 5. 保留平台账号与元数据（可选但推荐）

若要保留本地账号、密码哈希、租户、镜像目录、任务历史、TOS 控制面 Secret 和
镜像拉取 Secret，先创建一个只允许管理员访问的恢复目录：

```bash
install -d -m 700 /secure/ray-platform/state-$(date +%Y%m%d-%H%M%S)

bash ops/platform/backup-state.sh \
  --profile deploy/profiles/vke-test.yaml \
  --output-dir /secure/ray-platform/state-<时间戳> \
  --secret aliyun-registry
```

该命令只导出 PostgreSQL 的平台元数据和指定 Secret 清单。它**不会**读取、复制或
删除 `shanghai-data-transfer`、`vke-cluster`、IDC 数据、Loki 数据或任意训练
工作负载数据。恢复目录含数据库与 Secret 的敏感内容，必须保留在构建机受保护路径
且不要提交到 Git。

## 6. 受保护重置（可选）

先预览，绝不跳过这一步：

```bash
bash ops/platform/reset.sh \
  --profile deploy/profiles/vke-test.yaml \
  --include-legacy-platform-resources
```

预览只应出现：`ray-train-platform`、平台创建的 `tenant-*` namespace、其
RayJob/RayCluster/LocalQueue、平台 PV/PVC 和平台专属 Kueue Queue。它永远不会
列出或删除 KubeRay、Kueue Controller、CSI、Loki、Alloy、ALB、Keycloak，或任何
TOS 桶/对象。

确认输出正确后才执行：

```bash
bash ops/platform/reset.sh \
  --profile deploy/profiles/vke-test.yaml \
  --include-legacy-platform-resources \
  --execute --confirm-reset-ray-platform
```

这会删除平台数据库 PVC、测试租户的静态 PV/PVC 和 Kueue 测试队列；它不影响
`shanghai-data-transfer/ray-train/` 或 `vke-cluster` 中的任何对象。若要保留账号
和元数据，必须先完成上一节备份。

## 7. 恢复 Secret 或创建新 Secret、部署与验收

**保留原账号时**，先恢复上一节的 Secret，再部署：

```bash
bash ops/platform/restore-secrets.sh \
  --profile deploy/profiles/vke-test.yaml \
  --state-dir /secure/ray-platform/state-<时间戳>
```

**全新开始时**，从 env 文件创建新的 Secret：

```bash
bash ops/platform/bootstrap-secrets.sh \
  --profile deploy/profiles/vke-test.yaml \
  --env-file /secure/ray-platform/vke-test.env

bash ops/platform/preflight.sh --profile deploy/profiles/vke-test.yaml

bash ops/platform/deploy.sh \
  --profile deploy/profiles/vke-test.yaml \
  --timeout 12m

# 仅在保留状态时执行；它会短暂停止 Portal/API 并恢复原本地账号、租户和目录。
bash ops/platform/restore-metadata.sh \
  --profile deploy/profiles/vke-test.yaml \
  --input /secure/ray-platform/state-<时间戳>/metadata.sql

bash ops/platform/verify.sh --profile deploy/profiles/vke-test.yaml
```

部署使用 `helm upgrade --install --atomic --wait`。API 启动时在 PostgreSQL
advisory lock 下执行只增迁移；不使用会早于 Chart 内置 PostgreSQL 运行的 Helm
pre-install hook。

当前 VKE Profile 的入口为 `http://train.xx.com`。由于 VKE ALB 的非透传模式
要求后端 Service 为 NodePort，Chart 会让 Kubernetes 自动分配一个 NodePort，
但它只是 ALB 后端：不写死、不作为用户访问地址，也不应在安全组中向办公网开放。
浏览器所在网络只需能解析该内网域名。

## 8. 一卡完整冒烟

验证控制面通过后，使用已登记且包含 Ray/CUDA 的训练镜像运行一次真实 RayJob：

```bash
bash ops/platform/verify.sh \
  --profile deploy/profiles/vke-test.yaml \
  --smoke \
  --api-url http://train.xx.com \
  --smoke-image harbor.wellspiking.ai/guofeng.su/ray-test@sha256:<digest> \
  --env-file /secure/ray-platform/vke-test.env
```

脚本会登录本地 `admin`、提交 1 Worker × 1 GPU 的 RayJob、等待 `SUCCEEDED` 并
校验日志 API。完成后 Portal 可查看任务状态和日志；RayJob 的终态保留时间由任务
策略控制，Loki 保留时间由 Loki 自己的生命周期策略控制。

## 9. 生产切换

1. 复制 `production.yaml.example` 为环境 Profile，填写内部镜像仓库、ALB 域名、
   Keycloak、Loki/Prometheus、GPU 标签和配额。
2. 使用托管/外部 HA PostgreSQL；禁止 `postgres.mode=standalone`。
3. 使用 immutable digest，API/Portal 保持至少 2 副本、HPA、PDB 和软反亲和。
4. 先 `preflight`，再由变更审批后执行 `deploy`；如果需要重置，仍先执行预览。
5. 扩容 GPU 节点时仅需统一节点标签、device plugin、网络/存储挂载和资源容量；
   更新 Profile 配额后滚动部署，无需改代码或重新构建控制面。

## 10. 回滚与恢复

```bash
helm -n ray-train-platform rollback ray-platform
```

应用回滚不会回退数据库 schema。迁移保持只增；若必须恢复历史数据，请从部署前
的 PostgreSQL 备份恢复。TOS 数据、Loki 数据和 IDC 数据不属于本 Chart 的回滚或
清理范围。
