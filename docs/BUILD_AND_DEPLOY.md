# 日常构建与发布

本文是唯一的部署入口，覆盖首次安装、日常发版、原子升级、验收与回滚。

> 当前生产拓扑已包含 Frontend/Backend/CLI 发布服务双副本、私网 ALB、属主限定 Ray Dashboard、持久 MLflow 实验中心、同域原生 MLflow 管理界面、工作区源码快照与终态任务归档。Profile 暂用 `APP_ENV=development`，因为平台 PostgreSQL 仍是内置单实例且 Keycloak OIDC 尚未成为强制认证。不要只改这个开关；严格生产模式会拒绝内置 PostgreSQL 和缺失的 OIDC。完成外部 HA PostgreSQL 与 OIDC 配置后，再将 `backend.appEnv` 改为 `production` 并走本手册的完整 preflight/deploy/verify。

## 1. 日常发布原则

- 不使用 `helm --set`、`--reuse-values` 或历史 release values；它们会重新引入配置漂移。
- 每次发布使用新的镜像 tag；生产 API/Portal 使用 `@sha256` digest。
- 机密只通过 Kubernetes Secret 注入，绝不写入 Profile、构建参数或 Git。
- Chart 不管理 KubeRay、Kueue Controller、CSI、ALB、Loki、Alloy、Keycloak。
- Prometheus/Grafana 是独立的共享监控发布，使用本仓库固定的
  `kube-prometheus-stack` Chart，不随每次 Portal/API 镜像发布重复安装。
- MLflow 使用独立 `mlflow-system` namespace、独立 PostgreSQL 和独立部署流程，不复用平台数据库，也不随每次 Portal/API 发版重复安装。
- 构建和部署必须来自一个无未提交改动的 Git commit；生产验收通过后创建同版本 annotated tag。镜像 digest、Helm revision 和 tag 共同构成回滚证据，不能只保留构建机上的工作目录。

## 2. 本地校验

```bash
cd backend
gofmt -l .
go vet ./...
go test ./...

cd ../frontend
npm test
npm run build

cd ..
bash scripts/test-delivery-render.sh
bash ops/platform/test/reset-preview-test.sh
```

校验通过后先固化源码版本，再从该版本构建。示例中的身份使用维护者自己的 Git 配置，不要把个人访问令牌写入 remote URL：

```bash
git status --short                 # 必须无意外的临时文件或凭据
git diff --check
git add -A
git commit -m "feat: deliver Ray training platform v1.1.0"
git tag -a v1.1.0 -m "Ray training platform v1.1.0"
git show --stat --oneline v1.1.0
```

若内部 Git 尚未配置，可以先在本地创建 commit/tag；配置受保护的内部 remote 后再推送分支和 tag。构建机只接收源码副本，不作为 Git 历史的唯一保存位置。

## 3. 同步到构建机并构建

先在发布 shell 中填写本环境参数。尖括号占位符必须替换；不要把这组本地参数提交到
Git。`PLATFORM_REPO_ROOT` 必须是构建机上的绝对目录，`REGISTRY_PROJECT` 是当前环境
获准推送的 Harbor 项目：

```bash
BUILD_USER='<ssh-user>'
BUILD_HOST='<build-host>'
SSH_KEY='<path-to-private-key>'
PLATFORM_REPO_ROOT='<absolute-build-directory>'
REGISTRY_PROJECT='<registry-project>'
CORPORATE_DNS_A='<dns-server-a>'
CORPORATE_DNS_B='<dns-server-b>'
CERTIFICATE_ID='<certificate-id>'

export BUILD_USER BUILD_HOST SSH_KEY PLATFORM_REPO_ROOT REGISTRY_PROJECT
export CORPORATE_DNS_A CORPORATE_DNS_B CERTIFICATE_ID

test -f "$SSH_KEY"
case "$PLATFORM_REPO_ROOT" in
  /*) ;;
  *) echo 'PLATFORM_REPO_ROOT 必须是绝对目录' >&2; exit 2 ;;
esac
```

先创建目标目录，再安全同步源码；不把 `.git`、`node_modules`、构建产物或恢复备份同步
进去：

```bash
ssh -i "$SSH_KEY" "${BUILD_USER}@${BUILD_HOST}" \
  "mkdir -p '$PLATFORM_REPO_ROOT'"

rsync -az \
  --exclude .git --exclude .playwright-cli --exclude frontend/node_modules \
  --exclude frontend/dist --exclude output --exclude deploy/release-records \
  -e "ssh -i $SSH_KEY" \
  ./ "${BUILD_USER}@${BUILD_HOST}:${PLATFORM_REPO_ROOT}/"
```

登录构建机后重新设置仓库根目录，再创建新标签并推送。后续所有部署命令也都从这个目录
执行：

```bash
export PLATFORM_REPO_ROOT='<absolute-build-directory>'
cd "${PLATFORM_REPO_ROOT:?set PLATFORM_REPO_ROOT to the checkout root}"
IMAGE_TAG=prod-$(date -u +%Y%m%d-%H%M%S) \
BUILD_TARGETS=backend,frontend,spk-rayjob,source-materializer,workspace,train-pytorch \
PUSH_IMAGE=true USE_BUILDX=true \
bash build-image.sh
```

生产 GPU 镜像必须使用 Buildx：它会向 Harbor 推送标准 OCI manifest，避免旧 Docker
builder 在大镜像上出现“本地构建成功、远端节点无法拉取”的 manifest 兼容性问题。构建机
已安装 Buildx；如新建构建机，先执行 `docker buildx version` 确认可用。

构建完成后更新目标 Profile：

- `backend.image.tag`、`frontend.image.tag` 与 `spkRayjobRelease.image.tag` 使用新 tag；
- `backend.workspaceImage`、`backend.sourceMaterializerImage` 使用输出的 digest；
- 生产 Profile 还要填写 API/Portal 的 `image.digest` 并保留
  `release.requireImageDigests=true`。

Profile 是审阅对象：将环境 Profile 与代码一并提交到内部 Git，或由 GitOps 仓库
维护。不要把数据库 URL、AK/SK 或管理员密码放入其中。

## 4. 原子升级与验收

首次安装时，先在构建机的受保护目录准备 Secret 输入；日常升级已经存在的环境时跳过这一小节。模板不含真实凭据，填写后不得提交到 Git：

```bash
export PLATFORM_REPO_ROOT='<absolute-build-directory>'
SECRETS_DIR='<protected-secrets-directory>'
cd "${PLATFORM_REPO_ROOT:?set PLATFORM_REPO_ROOT to the checkout root}"

install -d -m 700 "$SECRETS_DIR"
cp deploy/secrets.env.example "$SECRETS_DIR/production.env"
chmod 600 "$SECRETS_DIR/production.env"
vi "$SECRETS_DIR/production.env"

bash ops/platform/bootstrap-secrets.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml \
  --env-file "$SECRETS_DIR/production.env"
```

```bash
cd "${PLATFORM_REPO_ROOT:?set PLATFORM_REPO_ROOT to the checkout root}"

# 首次部署，或专用 ALB 被重建后执行一次；它会创建/认领私网 ALB，待其 Running 后再创建 IngressClass。
bash ops/platform/bootstrap-alb.sh --profile deploy/profiles/vke-cpu-ha.yaml --timeout 15m

bash ops/platform/preflight.sh --profile deploy/profiles/vke-cpu-ha.yaml
bash ops/platform/deploy.sh --profile deploy/profiles/vke-cpu-ha.yaml --verify-fsx-irsa --timeout 15m
bash ops/platform/verify.sh --profile deploy/profiles/vke-cpu-ha.yaml
```

`--verify-fsx-irsa` 会创建两个隔离的临时前缀挂载探针并在退出时自动删除。新加入的 GPU
节点第一次使用 TOS 时，FSX 的 TOS FUSE 守护进程可能发生一次冷启动；探针会在首个有界
等待窗口失败后自动重试一次。若第二次仍失败，不要跳过门禁：先分别验证节点对企业 DNS
的 TOS/STS 解析，再检查对应节点的 `fsx-agent` 与 `csi-fsx-node` 事件。DNS 地址和待解析
域名来自当前环境配置，不写死在公开手册中：

```bash
CORPORATE_DNS_A='<dns-server-a>'
CORPORATE_DNS_B='<dns-server-b>'
TOS_DNS_NAME='<tos-private-endpoint-host>'
STS_DNS_NAME='<sts-endpoint-host>'

for dns_server in "$CORPORATE_DNS_A" "$CORPORATE_DNS_B"; do
  dig +short @"$dns_server" "$TOS_DNS_NAME" | grep -q . || exit 1
  dig +short @"$dns_server" "$STS_DNS_NAME" | grep -q . || exit 1
done
```

只有隔离探针和 DNS 检查都通过后，才允许发布或接纳该节点承载训练。

生产入口是私网 HTTPS `https://raytrain.wellspiking.ai`。ALB、IngressClass 与证书中心
证书属于共享网络层，由 `bootstrap-alb.sh` 在 Helm 之外一次性管理；Helm 只升级平台
工作负载和路由。Frontend、Backend 与 spk-rayjob Release 全部保持 `ClusterIP`，不创建
用户可访问的 NodePort；ALB 仅通过 Ingress 路由到这些集群内部服务。

当前企业 DNS 由 IDC Ingress 统一承载：`raytrain.wellspiking.ai` 应解析到 IDC Ingress，
再由其中的 `raytrain-vke-proxy` 以 HTTPS、保留 Host/SNI 的方式转发至 VKE 私网 ALB。
不要把用户 DNS 直接指向 VKE ALB 地址，也不要为 Frontend 或 spk-rayjob 增加 NodePort。
IDC 侧代理对象是独立共享网络资源，变更 ALB 或证书后必须一并复验该代理。
原生 Ray `--working-dir` 会上传代码包，IDC Nginx Ingress 必须至少包含：

```yaml
metadata:
  annotations:
    nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
    nginx.ingress.kubernetes.io/upstream-vhost: "raytrain.wellspiking.ai"
    nginx.ingress.kubernetes.io/proxy-ssl-server-name: "on"
    nginx.ingress.kubernetes.io/proxy-ssl-name: "raytrain.wellspiking.ai"
    nginx.ingress.kubernetes.io/proxy-body-size: "2g"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
```

缺少 `proxy-body-size` 时，小请求和 `/healthz` 仍会正常，但实际代码目录上传会返回
`413 Request Entity Too Large`。不要用 NodePort 绕过这个问题。

证书 ID 同样由环境参数提供，并写入 Profile 的 `ingress.tls.certificateId`；不要在公开
文档固化资源 ID，也不要将 PEM 私钥复制进 Git 或 Kubernetes Secret：

```bash
CERTIFICATE_ID='<certificate-id>'
test -n "$CERTIFICATE_ID"
```

专用 ALB 绑定证书且 DNS 生效后，发布机验证：

```bash
curl -fsS https://raytrain.wellspiking.ai/healthz
curl -fsS https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS
```

Ray 2.35 Jobs API 的版本响应必须把协议版本和 Ray 版本分开。发布后检查：

```bash
curl -fsS https://raytrain.wellspiking.ai/ray/api/version | jq .
```

正确形状为 `version: "4"`、`ray_version: "2.35.0"`，并包含字符串字段
`ray_commit`、`session_name`。如果错误地把 `version` 也设成 `2.35.0`，任务会先被接收，
随后官方 CLI 因 `int("2.35.0")` 失败而返回非零退出码，造成“提交成功但本地显示失败”。

CLI 发布文件必须用标准相对文件名生成校验表。构建机验收：

```bash
mkdir -p /tmp/spk-rayjob-install && cd /tmp/spk-rayjob-install
curl -fLO https://raytrain.wellspiking.ai/downloads/spk-rayjob/spk-rayjob-linux-amd64
curl -fLO https://raytrain.wellspiking.ai/downloads/spk-rayjob/SHA256SUMS
grep 'spk-rayjob-linux-amd64$' SHA256SUMS | sha256sum -c -
```

如需验证完整单卡训练：

```bash
REGISTRY_PROJECT='<registry-project>'
SECRETS_DIR='<protected-secrets-directory>'
SMOKE_IMAGE_DIGEST='<sha256-digest>'

bash ops/platform/verify.sh \
  --profile deploy/profiles/vke-cpu-ha.yaml --smoke \
  --api-url https://raytrain.wellspiking.ai \
  --smoke-image "harbor.wellspiking.ai/${REGISTRY_PROJECT}/ray-test@sha256:${SMOKE_IMAGE_DIGEST}" \
  --env-file "$SECRETS_DIR/vke-test.env"
```

### 三种提交入口验收

先在构建机完成 `spk-rayjob login`，再串行执行，避免验收任务抢占所有 GPU：

```bash
REGISTRY_PROJECT='<registry-project>'
IMAGE="harbor.wellspiking.ai/${REGISTRY_PROJECT}/ray-train-pytorch@sha256:<approved-image-digest>"

# 1. spk-rayjob：日常外部开发机入口
cd examples/distributed-demo
spk-rayjob submit --dir . --image "$IMAGE" \
  --entrypoint 'python storage_gpu_smoke.py' \
  --input-space public \
  --input-path bevfusion/fz-3dod-v1/platform-validation/annotations/fz-0429-platform-smoke-128 \
  --output-path acceptance/manual-spk --watch

# 2. 官方 Ray 2.35 working-dir 入口
cd ../..
bash scripts/e2e_native_ray_submit.sh --image "$IMAGE"

# 3. 与网页完全相同的“上传工作区 → 快照 → Portal JobSpec”入口
bash scripts/e2e_portal_submit.sh --image "$IMAGE"
```

2026-08-21 使用普通平台验收用户完成两个分支 × 三个入口共六个 `2×8 GPU` 任务；六项均为 `SUCCEEDED`。随后最终 BEVFusion r9 运行镜像又通过代表性 `2×8` 回归任务。这些任务确认代码随任务上传、Kueue 准入、KubeRay 自动建群、跨两台 RTX 4090 节点的 NCCL/DDP、公共数据读取、Loki 日志、验证和个人 checkpoint。完整任务 ID 见 [BEVFusion 代码改造与验收](BEVFUSION_CODE_CHANGES.md)。

revision 69 另用短时 1 卡任务验收了 RayCluster 自动创建和 Dashboard 访问链：Head Service 保持 `ClusterIP`，签名链接交换会话返回 302，Dashboard 页面、静态资源和 Ray 版本 API 均返回 200，写请求返回 405。验收任务已停止并清理 RayCluster。

revision 71 统一公共数据根为 `ray-train/public/`，校准 `local` 租户配额为当前物理容量 16 GPU，并实测调试 Worker 只暴露 `/mnt/storage/me`、`/mnt/storage/team`、`/mnt/storage/public`。个人目录可写，团队与公共目录只读。历史终态任务使用 `archived_at` 软归档从默认列表隐藏，数据库审计记录和 Loki 日志不做物理删除。

## 5. 共享监控组件发布

首次部署、升级 Prometheus Operator 或更新其固定镜像时，使用独立流程：

```bash
cd "${PLATFORM_REPO_ROOT:?set PLATFORM_REPO_ROOT to the checkout root}"
bash ops/observability/prometheus-operator/deploy.sh
```

该发布包含 2 副本 Prometheus、2 副本 Alertmanager、跨 CPU 节点的 2 副本 Grafana，
每个 Prometheus PVC 仅为 50Gi。它会实际查询 `DCGM_FI_DEV_GPU_UTIL`，只有 GPU
指标链路可用才返回成功。详细说明与旧临时栈清理限制见
[Prometheus Operator 运维说明](../ops/observability/prometheus-operator/README.md)。

MLflow 首次安装或自身版本升级使用：

```bash
cd "${PLATFORM_REPO_ROOT:?set PLATFORM_REPO_ROOT to the checkout root}"
bash ops/mlflow/deploy.sh
bash ops/mlflow/verify.sh
```

MLflow 保持 ClusterIP。普通训练 Pod 只访问写入网关；实验中心是平台筛选视图，原生管理界面统一从 `https://raytrain.wellspiking.ai/mlflow/` 访问。该路径先到 Frontend，再由 Backend 完成平台鉴权和反向代理；MLflow 自身不创建 NodePort、不创建独立 Ingress。

原生 MLflow 全功能开放是当前明确策略：所有平台认证用户都能管理全平台实验、Run、模型注册条目和 MLflow Artifact。部署验收必须覆盖登录票据、共享 CRUD、Artifact 上传下载和同源保护。开放 Artifact 不改变治理训练数据策略，不允许从 `/mnt/storage/public` 下载数据，也不向浏览器或训练 Pod 暴露 TOS AK/SK。详细拓扑、数据库、TOS Artifact 和 NetworkPolicy 约束见 [MLflow 运维说明](../ops/mlflow/README.md)。

## 6. 扩容与生产发布

新 4090 节点加入集群后，先验证 NVIDIA device plugin、网络、IDC 只读 NFS 挂载和
本地缓存盘，再统一打 Profile 约定的标签，例如：

```bash
kubectl label node <gpu-node-a> <gpu-node-b> accelerator=nvidia-rtx-4090 --overwrite
```

然后更新生产 Profile 的 `training.max*` 与 Kueue 配额，执行同一套
`preflight → deploy → verify`。生产数据库必须为外部/托管 HA PostgreSQL，Portal
与 API 维持至少两个副本、HPA、PDB 和软反亲和。

### 启用 GPU 节点 NVMe 缓存

当前生产 Profile 中该能力关闭，因为集群尚无 `ray-cache-local` StorageClass。不要只把 `enabled` 改为 `true`；顺序必须是：

1. 在每个 GPU 节点确认 `/data1`、`/data2` 为独立可丢弃缓存盘。
2. 安装经评审的 VKE 本地 CSI/LVM 驱动，建立 `ray-cache-local`（RWO、`WaitForFirstConsumer`、Delete）。这一步包含集群级 RBAC，必须单独审批。
3. 在两台 GPU 节点各创建一个临时 PVC/Pod，验证定位、写入、删除和容量回收。
4. 更新 Profile：

```yaml
training:
  localCache:
    enabled: true
    storageClass: ray-cache-local
    size: 200Gi
    mountPath: /mnt/cache
```

5. 提交 1×1 smoke，确认 worker 中有 `PLATFORM_CACHE_PATH=/mnt/cache`，Ray `temp-dir` 和 object spilling 位于该卷，任务删除后临时 PVC 自动回收。

该能力不改变数据读写契约：数据从 TOS/IDC 读，结果写 `PLATFORM_OUTPUT_PATH`。

### 公共数据根目录迁移

公共根统一为 `tos://shanghai-data-transfer/ray-train/public/`，Profile 必须保持 `dataSpaces.publicRoot: ray-train/public`。数据按 `<dataset>/<version>/` 发布，部署时使用 FSX IRSA 前缀验收；网页、`spk-rayjob`、原生 Ray 和调试环境都必须读取这一个根，不保留双根选择。

切换用户 TOS 数据空间前，必须完成 FSX CSI 组件级 IRSA 前缀隔离验收；未验收时
保持 `dataSpaces.enabled=false`。验收需要 FSX CSI 的 `fsx-agent` 已绑定具备目标桶最小读写权限的 IAM 角色，然后在部署机执行：

```bash
KUBECONFIG_PATH='<path-to-kubeconfig>'

KUBECONFIG="$KUBECONFIG_PATH" \
FSX_TOS_SERVER=tos-cn-shanghai.ivolces.com \
FSX_TOS_REGION=cn-shanghai \
FSX_TOS_BUCKET=shanghai-data-transfer \
TRAINING_NODE_SELECTOR=accelerator=nvidia-rtx-4090 \
SMOKE_IMAGE=<可在训练节点拉取的调试镜像@sha256:...> \
bash ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh
```

只有输出 `IRSA prefix mount contract verified` 后，才在 Profile 中设置：

```yaml
dataSpaces:
  enabled: true
  mountCapacity: 1Ti
  fsxVolumeAttributes:
    type: TOS
    server: tos-cn-shanghai.ivolces.com
    region: cn-shanghai
    bucket: shanghai-data-transfer
```

首次启用硬配额还需设置 `personalStorage.objectSetQuota.enabled: true`，然后由 SuperAdmin 在 Portal 确认一次 ObjectSet 目录初始化。该确认和 FSX 挂载验收相互独立，均不会向用户 Pod 注入静态 TOS 凭据。IDC NFS 使用 `idcDataSpaces` 的三个管理员登记导出：
它和 TOS 验收独立，只有节点到 NFS 的连通性、权限和只读挂载已验收后才可启用。不要
将 SSHFS、节点个人目录或旧的 `storage.existingClaim` 作为平台数据空间。

### IRSA 排障：`HeadBucket` 失败

FSX CSI 显示 `CREDENTIALS_TYPE=IRSA` 只说明组件已选择角色，**不代表该角色已经能访问目标桶**。如果前缀验收报 `HeadBucket failed`，在 IAM 控制台核对以下两层授权都授予了绑定到 csi-fsx 的角色，而不只是某个 AK 所属的 IAM 用户：

1. 角色身份策略至少允许 `tos:HeadBucket`、`tos:ListBucket`、`tos:GetObject`、`tos:PutObject`、`tos:DeleteObject`、`tos:AbortMultipartUpload`，范围覆盖目标桶与 `ray-train/*`。
2. 如果该桶使用了 Bucket Policy，`Principal` 也必须包含**该角色**；只包含 AK 所属用户不会让 IRSA 角色继承权限。

角色的 OIDC 信任关系必须将 `oidc:aud` 固定为 `sts.volcengine.com`，`oidc:iss` 精确等于集群供应商 URL，`oidc:sub` 精确为 `system:serviceaccount:kube-system:fsx-agent`。完成修改后重新执行前缀验收，不要用 TOS AK/SK Secret 绕过失败。

### IRSA 排障：节点 DNS 正常但 FSX agent 无法解析 TOS

如果 GPU 节点自身可以解析并访问私网 TOS，而某个 `fsx-agent` 仍在日志中出现解析失败或 TOS 挂载超时，先不要改成静态 AK/SK，也不要修改节点 DNS。该现象通常是 agent 已缓存旧的解析状态。确认该 GPU 节点没有用户运行中的训练或工作区后，仅重启受影响节点上的 `fsx-agent`，再重新执行前缀验收。只有 `IRSA prefix mount contract verified` 通过后，才能恢复该节点承接用户任务。

## 7. 回滚

```bash
helm -n ray-train-platform rollback ray-platform
```

Helm 回滚不回退数据库 schema。数据库恢复走部署前的 `backup-state.sh` 产物；TOS、
Loki 与 IDC 数据不属于平台回滚范围。
