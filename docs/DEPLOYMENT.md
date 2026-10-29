# 部署到自己的 Kubernetes 集群

本文面向全新安装。不会修改任何既有环境，也不是旧私有部署的原地升级方案。所有示例域名和镜像仓库都需要替换；仓库不保证占位名称已有可拉取镜像。

## 1. 集群前置条件

- 可访问的 Kubernetes 集群与管理员授权，kubectl、Helm 3。
- NVIDIA 驱动、匹配的容器运行时和 device plugin。节点应真实报告 `nvidia.com/gpu` allocatable。
- 可用的默认 StorageClass，供评估数据库申请磁盘；训练数据按需求提供可跨节点挂载的 PVC。
- KubeRay Operator及RayJob/RayCluster CRD。项目runtime包含Ray2.35.0与可选2.56.1系列，Operator应与所选runtime兼容。
- Kueue控制器、RayJob集成及当前Chart使用的`kueue.x-k8s.io/v1beta2` CRD。不要仅安装旧CRD就启用quickstart队列。检查控制器版本与其官方安装文档。
- 集群节点能访问镜像仓库、Git来源和配置的持久存储。

Chart不安装GPU驱动、KubeRay、Kueue、存储系统或Ingress控制器。先核对当前context，并在目标测试集群验证前置组件：

```bash
kubectl config current-context
kubectl get nodes
kubectl get storageclass
kubectl get crd rayjobs.ray.io rayclusters.ray.io
kubectl get crd clusterqueues.kueue.x-k8s.io resourceflavors.kueue.x-k8s.io
```

管理员需要审阅`helm/ray-train-platform/templates/rbac.yaml`。平台管理租户namespace及训练资源，当前按固定资源名支持每集群一套安装。

## 2. 构建与推送镜像

从仓库根目录构建。下面展示四个不同的构建context，不要混用：

```bash
export REGISTRY=registry.example.com/your-project
export IMAGE_TAG=dev

docker build -f Dockerfile -t "$REGISTRY/ray-train-platform:$IMAGE_TAG" .
docker build -f images/source-materializer/Dockerfile -t "$REGISTRY/ray-source-materializer:$IMAGE_TAG" .
docker build -f images/workspace/Dockerfile -t "$REGISTRY/ray-workspace:$IMAGE_TAG" images/workspace
docker build -f images/train-pytorch/Dockerfile -t "$REGISTRY/ray-train-pytorch:$IMAGE_TAG" images

for image in ray-train-platform ray-source-materializer ray-workspace ray-train-pytorch; do
  docker push "$REGISTRY/$image:$IMAGE_TAG"
done
```

等价脚本：

```bash
BUILD_TARGETS=platform,source-materializer,workspace,train-pytorch \
  PUSH_IMAGE=true bash build-image.sh
```

默认workspace/train-pytorch目标使用Ray2.35.0 legacy系列。需要受管RayTrain时应显式选择Dockerfile内相应target，核对Ray版本、launcher及依赖合同，并配置runtimeCatalog与功能开关；不能只修改版本字符串。

其他环境镜像（如`images/raytrain-base`）不属于基础构建目标，必须提供其Dockerfile要求的基础镜像ARG并验证运行时依赖；没有预先发布镜像的承诺。

每个镜像推送后使用`docker buildx imagetools inspect <完整镜像:tag>`记录registry返回的摘要。在个人values中填写helper、workspace的`@sha256:<摘要>`；应用生产镜像单独填写`backend.image.digest`。不能把平台镜像摘要复用到其他镜像。

裸`rayproject/ray`镜像不一定含平台launcher。训练使用本项目runtime或经过管理员验证、包含所需入口的镜像。私有仓库的imagePullSecret需覆盖实际拉取镜像的namespace，特别是租户namespace。

## 3. 初始化认证与数据库Secret

以下是开发/评估安装示例，使用Chart自带的单实例PostgreSQL。生产见后文。以bash执行，Secret临时文件为0600；不要打印或提交它们：

```bash
export RAYTRAIN_NS=ray-train-platform
kubectl create namespace "$RAYTRAIN_NS"
umask 077
secret_dir=$(mktemp -d)
trap 'rm -rf "$secret_dir"' EXIT
pg_password=$(openssl rand -hex 24)
pat_pepper=$(openssl rand -hex 32)
read -r -s -p 'Initial admin password: ' admin_password
printf '\n'
printf 'POSTGRES_USER=platform\nPOSTGRES_PASSWORD=%s\nDATABASE_URL=postgres://platform:%s@postgres:5432/platform?sslmode=disable\n' \
  "$pg_password" "$pg_password" > "$secret_dir/postgres.env"
printf 'PAT_PEPPER=%s\n' "$pat_pepper" > "$secret_dir/pat.env"
printf 'BOOTSTRAP_ADMIN_PASSWORD=%s\n' "$admin_password" > "$secret_dir/bootstrap.env"
kubectl -n "$RAYTRAIN_NS" create secret generic ray-platform-postgres --from-env-file="$secret_dir/postgres.env"
kubectl -n "$RAYTRAIN_NS" create secret generic ray-platform-pat --from-env-file="$secret_dir/pat.env"
kubectl -n "$RAYTRAIN_NS" create secret generic ray-platform-bootstrap-admin --from-env-file="$secret_dir/bootstrap.env"
unset pg_password pat_pepper admin_password
rm -rf "$secret_dir"
trap - EXIT
```

请使用满足平台密码策略的强密码（建议至少16个字符）。初始账号默认`admin`，团队默认`local`。引导密码用于首次创建管理员，不是每次启动都重置密码。PAT_PEPPER应稳定保管，变更会影响既有令牌验证。不要复用示例凭据。实际密码不得包含env-file格式不支持的换行。

## 4. 配置并渲染

复制示例为本地配置：

```bash
cp deploy/profiles/quickstart.yaml deploy/profiles/my-cluster.local.yaml
```

至少填写：

- `global.imageRegistry`：自己推送的镜像仓库；必要时配置拉取Secret。
- `backend.image`：本次统一应用镜像。
- `backend.sourceMaterializerImage`、`backend.workspaceImage`：各自已构建的完整不可变引用。
- `backend.imageAllowlist`、`backend.gitAllowlist`：自己信任的镜像前缀和Git主机。
- `training.nodeSelector`、资源上限及Kueue Flavor标签：与实际GPU资源池一致。
- PostgreSQL StorageClass及容量。

基础模式保持`dataSpaces.enabled: false`。运行基础训练使用固定Git commit和登记的PVC；逻辑数据空间和working-dir上传不属于未配置对象存储的基础模式。

```bash
helm lint helm/ray-train-platform -f deploy/profiles/my-cluster.local.yaml
helm template raytrain helm/ray-train-platform \
  --namespace "$RAYTRAIN_NS" -f deploy/profiles/my-cluster.local.yaml > /tmp/raytrain-rendered.yaml
```

审阅资源、RBAC、镜像、存储和调度设置，然后安装：

```bash
helm upgrade --install raytrain helm/ray-train-platform \
  --namespace "$RAYTRAIN_NS" -f deploy/profiles/my-cluster.local.yaml \
  --wait --timeout 10m
kubectl -n "$RAYTRAIN_NS" get pods
kubectl -n "$RAYTRAIN_NS" port-forward service/ray-train-backend 8080:8080
```

打开`http://127.0.0.1:8080`。若实际渲染的Service名称不同，以渲染结果为准。通过同一入口访问UI与API，不部署独立前端服务。

## 5. 第一个训练任务

1. 登录后建立团队/用户，分配团队GPU额度。
2. 核对租户namespace，创建已Bound的数据/结果PVC，并在管理页面登记为`pvc`资产，见[存储指南](STORAGE.md)。
3. 登记自己构建的训练镜像及正确Ray版本，确认镜像允许列表。
4. 准备一个能在所选镜像执行的Git项目，使用固定commit。
5. 在Web创建任务，选择1Worker×1GPU、数据资产和可写结果资产。
6. 查看RayJob/Workload准入、Pod真实落点和容器日志，确认结果写入持久卷。

缺少Loki时不能假定历史日志功能完整；首先使用目标集群的Pod日志排查。未通过真实GPU任务之前，只能称为控制面安装完成。

## 6. 生产配置

使用`deploy/profiles/production.yaml.example`作为叠加配置，并提供完整的个人values。生产要求：

- 外部PostgreSQL及其`DATABASE_URL` Secret；TLS、HA、备份与恢复由部署者配置。Chart拒绝production搭配内置standalone数据库。
- 应用不可变镜像摘要、至少两副本、PDB、合理requests/limits。
- Ingress控制器、域名与TLS Secret。保留认证，不公开内部数据库和Ray端点。
- 训练共享存储、Git来源、镜像拉取、GPU插件及队列故障演练。
- 明确的Loki/Prometheus/MLflow接入与权限策略；按需开启功能，不把空地址当成已集成。

本地账号与PAT可用于production，OIDC为可选项。`backend.webOIDCEnabled` 默认false，显式设为true才启用浏览器Keycloak登录；`backend.oidcRequired` 控制API验证要求，两者是独立开关。使用OIDC时配置issuer/audience、浏览器client及回调地址，并验证角色边界。TAS、节点偏好和专属节点配置见[调度指南](SCHEDULING.md)；保持抢占关闭直至安全恢复设计验收完成。

## 升级与恢复

升级前备份数据库和当前values，检查迁移兼容性，渲染并审阅差异。只更新计划中的镜像和配置；不要把旧环境overlay带到新集群。数据库迁移由应用启动执行，旧迁移只保留不重写。

Helm回滚仅处理Kubernetes资源，不回滚已提交的schema迁移。恢复方案需分别覆盖数据库、训练数据和应用镜像，且必须在隔离环境验证。不能为验收停止其他用户任务。
