# 从零部署 / 重新部署

适用场景：

- 当前 GPU 节点要踢出集群，平台需要在新机器上重新起来
- 换一套集群部署
- 从测试部署切换成正式部署（见 [第 8 节](#8-从测试部署切到正式部署)）

阅读顺序：先看第 0 节判断**数据要不要保**，再照 1–7 节执行。

---

## 0. 先决定：数据保不保

平台的状态全部在 PostgreSQL 里（账号、租户、任务历史、镜像目录、凭证引用）。

**PostgreSQL 不由 Helm 管理**（它是 `k8s/base/postgres.yaml` 里的 StatefulSet），所以 `helm uninstall` 不会删掉它，PVC 也会保留。

| 你的情况 | 做法 |
|---|---|
| 要保留账号和历史 | 走第 1 节备份，之后恢复 |
| 全新开始 | 跳过备份，第 5 节会自动创建 bootstrap 管理员 |

> **EBS 卷绑定可用区**。当前卷在 `cn-shanghai-a`。新节点必须在同一可用区，否则 `postgres-0` 会一直 Pending 报无法挂载。跨可用区必须走「备份 → 删 PVC → 新区重建 → 恢复」。

---

## 1. 备份（要保数据才做）

```bash
kubectl -n ray-train-platform exec postgres-0 -- pg_dump -U platform -d platform --clean --if-exists > rayplatform-$(date +%Y%m%d-%H%M%S).sql
```

把这个文件拷到集群外。同时记录当前镜像 digest，重建时要用：

```bash
helm -n ray-train-platform get values ray-platform -o yaml > values-backup.yaml
```

---

## 2. 集群前置条件

新节点就位后确认：

```bash
kubectl get nodes -L accelerator && kubectl get crd | grep -E 'ray|kueue' && kubectl get storageclass
```

必须满足：

| 项 | 要求 |
|---|---|
| GPU 节点标签 | `accelerator=nvidia-rtx-4090`（或你自定义的值，需与 ResourceFlavor 一致） |
| KubeRay Operator | 已安装，`rayjobs.ray.io` / `rayclusters.ray.io` 存在 |
| Kueue | 已安装，有 ClusterQueue 与 ResourceFlavor |
| StorageClass | 有可用的块存储（当前用 `ebs-ssd`） |
| NVIDIA device plugin | 节点 `nvidia.com/gpu` 可分配 |

Kueue 基础资源如果还没建：

```bash
kubectl apply -f k8s/base/kueue-resources.yaml
```

---

## 3. 创建命名空间与密钥

```bash
kubectl create namespace ray-train-platform
```

**数据库连接串**（外部 PG 就填外部地址；用集群内 PG 就用下面的）：

```bash
kubectl -n ray-train-platform create secret generic ray-platform-postgres --from-literal=DATABASE_URL='postgres://platform:<密码>@postgres.ray-train-platform.svc.cluster.local:5432/platform?sslmode=disable' --from-literal=POSTGRES_USER='platform' --from-literal=POSTGRES_PASSWORD='<密码>'
```

**令牌签名密钥**（PAT、本地会话、工作区访问共用，至少 32 字节）：

```bash
kubectl -n ray-train-platform create secret generic ray-platform-pat --from-literal=PAT_PEPPER="$(head -c 48 /dev/urandom | base64)"
```

**首个管理员密码**：

```bash
kubectl -n ray-train-platform create secret generic ray-platform-bootstrap-admin --from-literal=BOOTSTRAP_ADMIN_PASSWORD='<强密码>'
```

**对象存储凭证**（平台侧）：

```bash
kubectl -n ray-train-platform create secret generic tos-credentials --from-literal=access-key='<AK>' --from-literal=secret-key='<SK>' --from-literal=region='cn-shanghai'
```

**镜像仓库拉取凭证**：

```bash
kubectl -n ray-train-platform create secret docker-registry <pull-secret-名> --docker-server=<仓库地址> --docker-username=<用户> --docker-password=<密码>
```

---

## 4. 部署 PostgreSQL

```bash
kubectl apply -f k8s/base/postgres.yaml && kubectl -n ray-train-platform rollout status statefulset/postgres --timeout=300s
```

要恢复数据的话：

```bash
kubectl -n ray-train-platform exec -i postgres-0 -- psql -U platform -d platform < rayplatform-<时间戳>.sql
```

---

## 5. 构建镜像

在构建机上（需要 Docker，且已 `docker login` 目标仓库）：

```bash
rsync -az --delete --exclude node_modules --exclude dist --exclude .git ./ <构建机>:/root/ray-train-platform/
```

```bash
cd /root/ray-train-platform && REGISTRY=<你的仓库> BUILD_TARGETS=all IMAGE_TAG=v1.0.0 PUSH_IMAGE=true bash build-image.sh
```

会构建六个镜像：`backend`、`frontend`、`source-materializer`、`workspace`、`train-pytorch`、`test-training`。

**记下输出里的 `@sha256:` digest**，下一步要用。其中这两个**必须**用 digest（后端会拒绝可变 tag）：

- `backend.sourceMaterializerImage`
- `backend.workspaceImage`

---

## 6. Helm 部署

用一份**审阅过的 values 文件**，不要靠一串 `--set` 累积：

```bash
cp helm/ray-train-platform/values-prod.yaml.example values-prod.yaml
```

按注释逐项改完后：

```bash
helm install ray-platform ./helm/ray-train-platform -n ray-train-platform -f values-prod.yaml --wait --timeout 10m
```

migrations 会作为 pre-upgrade hook 先跑，失败则中止，不会把新版本推上去。

---

## 7. 部署后验证

```bash
kubectl -n ray-train-platform get pods
```

```bash
curl -s http://<平台地址>/api/v1/auth/providers && curl -s http://<平台地址>/ray/api/version
```

预期：三个 Pod `Running`；`providers` 返回可用登录方式；`/ray/api/version` 返回 `{"ray_version":"2.35.0",...}` 而**不是** HTML（返回 HTML 说明 nginx 没代理 `/ray/`）。

用 bootstrap 管理员登录，然后：

1. 「租户与配额」→ 登记训练镜像和调试镜像，各设一个默认
2. 建租户、建用户账号
3. 每个租户 namespace 补 `tos-credentials`：

```bash
kubectl -n tenant-<id> create secret generic tos-credentials --from-literal=AWS_ACCESS_KEY_ID='<AK>' --from-literal=AWS_SECRET_ACCESS_KEY='<SK>'
```

最后跑一个冒烟任务：

```bash
API_URL=http://<平台地址> PLATFORM_USER=admin PLATFORM_PASSWORD=<密码> IMAGE=<训练镜像@sha256:...> python3 scripts/submit_smoke_job.py
```

看到 `TRAINING COMPLETE` 且 GPU 自动释放，即部署成功。

---

## 8. 从测试部署切到正式部署

### 8.1 需要先 uninstall 吗

**技术上不需要**——镜像仓库、Git、存储这些都是 values 变更，`helm upgrade` 就能改。

**但建议重装**，原因是当前测试 release 已经累积了 30 多次修订的配置漂移：

| 当前值 | 生产应为 |
|---|---|
| `appEnv: development` | `production` |
| `oidc.issuerURL: keycloak.test.invalid` | 真实 Keycloak（或彻底关掉 OIDC 只用本地账号） |
| `imageAllowlist: [ray-test]` | 内部仓库路径 |
| `gitAllowlist: [github.com]` | 内部 Git 主机 |
| `replicaCount: 1`（前后端） | ≥ 2 |
| `pdbEnabled: false` | `true` |
| `service.type: NodePort` | Ingress（私网 ALB） |
| `lokiURL` / `prometheusURL` | 指向不存在的服务，需改成真实地址 |

逐条 `--set` 修正这些，比从一份审阅过的 values 重新装一遍更容易漏。而且**数据不会丢**——PostgreSQL 不归 Helm 管。

### 8.2 切换步骤

```bash
kubectl -n ray-train-platform exec postgres-0 -- pg_dump -U platform -d platform --clean --if-exists > pre-prod-backup.sql
```

```bash
helm uninstall ray-platform -n ray-train-platform
```

> `helm uninstall` 只删 backend / frontend / Service / Ingress / RBAC。PostgreSQL StatefulSet、PVC、所有 Secret、租户 namespace、运行中的 RayJob 都不受影响。

然后按第 5–7 节重新构建部署（用内部仓库地址和 `values-prod.yaml`）。

### 8.3 全内部化清单

| 项 | 改哪里 |
|---|---|
| 镜像仓库 | `global.imageRegistry`、`global.imagePullSecrets`；构建时 `REGISTRY=<内部仓库>` |
| 基础镜像来源 | `build-image.sh` 的 `GO_BUILDER_IMAGE` / `NODE_BUILDER_IMAGE` / `ALPINE_RUNTIME_IMAGE` / `NGINX_RUNTIME_IMAGE` / `RAY_BASE_IMAGE` 改成内部镜像 |
| pip / npm 源 | `PIP_INDEX_URL`、`NPM_REGISTRY` 改成内部源 |
| Git 主机 | `backend.gitAllowlist`；私有仓库在「租户与配额」页配凭证 |
| 镜像 allowlist | `backend.imageAllowlist`；更重要的是把内部镜像登记进镜像目录 |
| 对象存储 | `tos.endpoint` / `tos.bucket` + `tos-credentials` Secret |
| 身份认证 | 内部 Keycloak，或只用本地账号（`localAuth.enabled=true` + `oidcRequired=false`） |
| 日志与监控 | `backend.lokiURL` / `backend.prometheusURL` 指向内部 Loki / Prometheus |
| 入口 | `ingress.host` 内网域名 + 私网 ALB |

> **构建机需要能访问内部仓库**。如果构建机在外网而仓库在内网，要么把构建挪到内网机器，要么用 CI。

---

## 9. 回滚

```bash
helm -n ray-train-platform rollback ray-platform
```

数据库如果已被新版本 migration 改过，回滚应用版本不会回滚 schema。迁移都是只增不改的，旧版本一般仍可运行；确实需要回退数据时用备份恢复。
