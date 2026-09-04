# 日常构建与发布

> 分布式 Parquet 发布器采用显式开关：默认保留原有单 Job `cloud_publish`。
> 启用 `datasetPublisher.distributed.enabled` 后，新版本依次执行 CPU-only
> `plan → Indexed pack → finalize`；每个 Indexed 分区写不可变回执，后续
> 版本会校验源对象 identity 后复用未变化分区。发布进度在数据集治理页展示
> 汇总计数，不展示对象路径、Kubernetes Job 名或凭据。生产启用时仅附加
> `deploy/overlays/distributed-parquet-publisher.yaml`，并保留现有 IDC overlay
> 与 release values；该开关不会修改已创建 RayJob 的模板。

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
- Ray 编排 DDP 与 Ray Train 托管使用并行的不可变运行时镜像。生产集群已部署 KubeRay 1.6.2，托管生产版本固定 Ray 2.56.1 并向全部团队开放；Ray 2.58.0 canary 保持关闭。后续升级 KubeRay 仍须走独立的零负载升级流程。

双引擎发布参数、三种提交入口和代码适配见 [Ray Train 托管指南](RAY_TRAIN_MANAGED_GUIDE.md)；KubeRay 备份、升级、核验与回滚门禁见 [生产运维手册](OPERATIONS_GUIDE.md)。当前生产验收基线见托管指南，不能以文档中的未来 canary 参数替代生产发布值。

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

## 3. 在构建机检出确定版本并构建

生产发布使用 Git，不使用双向 `rsync`。本地开发目录、Git 远端和构建机 checkout 的职责必须分开：本地负责修改和测试，Git 保存唯一历史，构建机只从已经推送的 commit 构建。

先在本地提交、推送并记录完整 commit：

```bash
git status --short
git diff --check
git push origin main
git rev-parse HEAD
git ls-remote origin refs/heads/main
```

`git status --short` 必须为空，最后两条命令的 commit 必须一致。生产验收前不要提前移动正式 release tag；tag 在验证成功后创建。

登录构建机，在受控的绝对目录 checkout 后执行：

```bash
export PLATFORM_REPO_ROOT='<absolute-build-directory>'
export EXPECTED_COMMIT='<full-commit-from-reviewed-main>'

cd "${PLATFORM_REPO_ROOT:?}"
git fetch --prune origin
git checkout main
git pull --ff-only origin main
test "$(git rev-parse HEAD)" = "$EXPECTED_COMMIT"
test -z "$(git status --short)"
```

如果最后两个检查失败，停止发布：不要在构建机现场修改代码，不要用 `git reset --hard` 覆盖不明改动，也不要用本地目录覆盖构建目录。先确认改动所有者并回到 Git 流程处理。

使用时间戳或正式候选版本作为镜像 tag，再由构建脚本推送 Harbor：

```bash
IMAGE_TAG=prod-$(date -u +%Y%m%d-%H%M%S) \
BUILD_TARGETS=backend,frontend,spk-rayjob,source-materializer,workspace,train-pytorch \
PUSH_IMAGE=true USE_BUILDX=true \
bash build-image.sh
```

生产 GPU 镜像必须使用 Buildx：它会向 Harbor 推送标准 OCI manifest，避免旧 Docker
builder 在大镜像上出现“本地构建成功、远端节点无法拉取”的 manifest 兼容性问题。构建机
已安装 Buildx；如新建构建机，先执行 `docker buildx version` 确认可用。

构建完成后记录每个镜像的 digest，并更新目标 Profile：

- `backend.image.tag`、`frontend.image.tag` 与 `spkRayjobRelease.image.tag` 使用新 tag；
- `backend.workspaceImage`、`backend.sourceMaterializerImage` 使用输出的 digest；
- 生产 Profile 还要填写 API/Portal 的 `image.digest` 并保留
  `release.requireImageDigests=true`。

Profile 是审阅对象：把 digest 更新提交并推送后，再让构建机 checkout 这一份最终发布 commit。环境 Profile 可以与代码放在同一受保护仓库，也可以由专用 GitOps 仓库维护，但不能只存在于构建机。不要把数据库 URL、AK/SK 或管理员密码放入其中。

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
`spk-rayjob submit` 和原生 Ray `--working-dir` 都会把代码包上传到平台中转端点，
再由平台写入 TOS。用户主机只需能访问 `raytrain.wellspiking.ai`，不需要解析或
直连 TOS endpoint。IDC Nginx Ingress 必须至少包含：

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
`413 Request Entity Too Large`。缺少长超时时，大代码包可能在上传中断。不要让用户改为直传
TOS，也不要用 NodePort 绕过这个问题。

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

Ray Jobs API 的版本响应必须把协议版本和 Ray 版本分开。当前生产托管运行时为 Ray 2.56.1；下面的检查应看到协议版本 `4`，而托管能力与生产运行时应通过 `/api/v1/limits` 验收：

```bash
curl -fsS https://raytrain.wellspiking.ai/ray/api/version | jq .
```

正确形状为协议字段 `version: "4"`，并包含字符串字段 `ray_commit`、`session_name`。`/api/v1/limits` 的 `runtime.availableEngines` 必须包含 `ray-ddp` 与 `ray-train`，`runtime.productionRayVersion` 必须为 `2.56.1`。如果错误地把协议字段 `version` 也设成 Ray 版本，任务会先被接收，
随后官方 CLI 因尝试把语义版本转为协议整数而返回非零退出码，造成“提交成功但本地显示失败”。

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
spk-rayjob submit --dir . --engine ray-ddp --image "$IMAGE" \
  --entrypoint 'python storage_gpu_smoke.py' \
  --input-space public \
  --input-path bevfusion/fz-3dod-v1/platform-validation/annotations/fz-0429-platform-smoke-128 \
  --output-path acceptance/manual-spk --watch

# 2. 官方 Ray 2.56.1 working-dir 入口（与 spk-rayjob 同为 ray-cli；另核对 externalSubmissionId）
# 按 RAY_TRAIN_MANAGED_GUIDE.md 的原生 Ray API 完整示例提交，
# 并在持久化任务详情核对 engine、rayVersion、origin 与拓扑。

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

MLflow Artifact 存储必须使用 `vke-cluster/ray-train/platform/mlflow-artifacts/` 专用前缀的 FSX CSI 静态 PV/PVC。MLflow Pod 只看到 `/mlflow-artifacts` 挂载根，并以 `file:///mlflow-artifacts` 作为 Artifact destination；不允许 Pod 直连对象存储，也不注入 TOS/AWS AK/SK。底层挂载由集群 `csi-fsx-node` DaemonSet 中的 `fsx-agent` 通过 IRSA 完成：`CREDENTIALS_TYPE=IRSA`，且 `ROLE_NAME_FOR_IRSA` 非空。MLflow Pod、PV 和 PVC 都不包含 AK/SK 或 Secret 引用，不得把静态 TOS 凭据复制到 `mlflow-system` 或通过环境变量绕过该边界。

`ops/mlflow/deploy.sh` 在任何资源变更前执行失败关闭的存储预检：确认 `fsx.csi.volcengine.com` CSIDriver 存在；确认 `kube-system/csi-fsx-node` DaemonSet 全部可用；并校验其 driver 容器为 `CREDENTIALS_TYPE=IRSA`、`ROLE_NAME_FOR_IRSA` 非空。任一条件不满足都必须先修复集群 FSX CSI/IRSA，不能改用 AK/SK Secret 继续部署。

新版使用 `mlflow-artifacts-irsa` PVC 和 `mlflow-artifacts-irsa-pv` PV，与旧版 Secret 型卷使用不同的 Kubernetes 资源名，但仍指向同一个专用 TOS 前缀。这避免了修改不可变 PV 源；旧 PV/PVC 在新版 Artifact CRUD 验收完成前保留为回滚通道，不得在发版前手工删除。

原生 MLflow 全功能开放是当前明确策略：所有平台认证用户都能管理全平台实验、Run、模型注册条目和 MLflow Artifact。部署验收必须覆盖登录票据、共享 CRUD、Artifact 上传下载、PVC 绑定、挂载根限定、Pod 无对象存储凭据和同源保护。MLflow Artifact 与 `/mnt/storage/public` 治理数据隔离；开放 Artifact 不允许读取公共或团队训练数据。详细拓扑、数据库、Artifact 存储和 NetworkPolicy 约束见 [MLflow 运维说明](../ops/mlflow/README.md)。

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

当前生产已安装两套本地供应器与 StorageClass，分别管理 `/data1/ray-cache` 和 `/data2/ray-cache`。新集群或新 GPU 节点仍必须按以下顺序交付：

1. 确认每个 GPU 节点的 `/data1`、`/data2` 是独立、可丢弃的缓存盘。
2. 执行 `bash ops/storage/nvme-cache/preflight.sh`，再执行 `bash ops/storage/nvme-cache/install.sh`。该安装包含集群级 RBAC，首次安装必须经过审批。
3. 执行 `verify.sh` 与 `verify-dual.sh`，验证双盘定位、写入、删除和宿主机目录回收。
4. 在 Profile 保持缓存基础设施可用、任务默认关闭，并登记两套 StorageClass：

```yaml
training:
  localCache:
    available: true
    storageClassData1: ray-cache-local-data1
    storageClassData2: ray-cache-local-data2
    mountPathData1: /mnt/cache
    mountPathData2: /mnt/cache2
    policy:
      defaultMode: "off"
      allowedSizes: [200Gi, 500Gi, 1Ti, 2Ti, 4Ti, 5Ti]
      defaultSize: 200Gi
      maxSize: 5Ti
```

5. 分别提交 off、runtime、preload 的 1×1 smoke，确认环境变量、Ray 临时目录、输入路径切换和任务结束后的 PVC/宿主机目录回收。

该能力不改变数据读写契约：持久数据来自 TOS/IDC，结果写 `PLATFORM_OUTPUT_PATH`。用户参数和性能 A/B 见 [NVMe 缓存指南](NVME_CACHE_GUIDE.md)。

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

首次启用硬配额还需设置 `personalStorage.objectSetQuota.enabled: true`，然后由 SuperAdmin 在 Portal 确认一次 ObjectSet 目录初始化。该确认和 FSX 挂载验收相互独立，均不会向用户 Pod 注入静态 TOS 凭据。

IDC NFS 使用 `idcDataSpaces` 管理员登记的五个只读导出：`original`、`wellspiking`、`shared`、`spk-hybrid`、`spk-ssd`，在 Pod 内分别固定为 `/mnt/.original`、`/mnt/.wellspiking`、`/mnt/.shared`、`/mnt/.spk-hybrid`、`/mnt/.spk-ssd`。首次加入 GPU 节点时先以 root 运行 `ops/storage/idc-nfs/10-configure-gpu-node-dns.sh`，并安装 `nfs-common`；随后执行 `ops/storage/idc-nfs/41-verify-readonly-mount.sh` 验收。它和 TOS 验收独立，只有节点到 NFS 的连通性、权限和只读挂载已验收后才可启用。不要
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
