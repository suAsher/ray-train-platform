# 构建与部署流程

适用环境：火山云 VKE + KubeRay 1.3.0 + Kueue 0.19.0 + PostgreSQL + Nginx/Vue Portal。

构建机：`root@14.103.49.106`（已安装 Docker 29.x、Node 20、helm、kubectl；kubeconfig 指向真实 VKE 集群；已 `docker login` 阿里云仓库）。构建机上没有 Go —— 后端在 Go builder 容器里编译，不需要本机装 Go。

---

## 0. 一次性前置检查

```bash
ssh -i ~/.ssh/qomolo-desktop.pem root@14.103.49.106
```

```bash
kubectl get nodes -L accelerator && kubectl get crd | grep -E 'ray|kueue' && helm list -A
```

要点：
- 训练节点必须有 `accelerator=nvidia-rtx-4090` 标签，Ray worker 的 nodeSelector 依赖它。
- VCI 虚拟节点（`type=virtual-kubelet`）会上报上千张弹性 GPU，那是购买上限不是物理卡，平台已在算力统计中排除。
- 每个租户 namespace 需要一个 LocalQueue 绑定到 `cluster-gpu-queue`；提交任务时后端会自动创建缺失的 LocalQueue。

---

## 1. 本地校验（推送前必做）

```bash
cd backend && gofmt -l . && go vet ./... && go test ./...
```

```bash
cd frontend && npm ci && npm run build
```

两条都必须 exit 0。当前 CI（`.github/workflows/ci.yml`）只构建镜像、不跑这两步，所以这一步目前只能靠人工把关。

---

## 2. 同步代码到构建机

```bash
rsync -az --delete -e "ssh -i ~/.ssh/qomolo-desktop.pem" --exclude node_modules --exclude dist --exclude .git --exclude '*.orig' --exclude '*.bak' ./ root@14.103.49.106:/root/ray-train-platform/
```

---

## 3. 构建并推送镜像

```bash
cd /root/ray-train-platform && BUILD_TARGETS=backend,frontend IMAGE_TAG=dev-$(date -u +%Y%m%d-%H%M%S) PUSH_IMAGE=true bash build-image.sh
```

`build-image.sh` 的关键参数：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `REGISTRY` | `registry.cn-shanghai.aliyuncs.com/ashersu` | 目标仓库 |
| `BUILD_TARGETS` | `all` | `backend,frontend,source-materializer,test-training` |
| `IMAGE_TAG` | `test-<UTC时间戳>` | 生产请用不可变 tag 或 digest |
| `PUSH_IMAGE` | `false` | 默认只构建不推送 |
| `DRY_RUN` | `false` | 只打印将要执行的命令 |

脚本结束会打印每个镜像的 `@sha256` digest。`backend.workspaceImage` 和 `backend.sourceMaterializerImage` **必须**填 digest —— 后端的 `ValidatePinnedImage` 会拒绝可变 tag。

---

## 4. Helm 部署

保留现有 values 再覆盖镜像 tag，避免手滑丢配置：

```bash
helm -n ray-train-platform get values ray-platform -o yaml > /tmp/cv.yaml
```

```bash
helm upgrade ray-platform ./helm/ray-train-platform -n ray-train-platform -f /tmp/cv.yaml --set backend.image.tag=<TAG> --set frontend.image.tag=<TAG> --wait --timeout 5m
```

回滚：

```bash
helm -n ray-train-platform rollback ray-platform
```

migrations 由 `migrations-job.yaml` 在 API 滚动更新前执行；migration 失败时 Helm 会中止，不会把新 API 推上去。

---

## 5. 部署后验证

```bash
kubectl -n ray-train-platform get pods && curl -s http://172.28.0.167:31113/api/v1/me && curl -s http://172.28.0.167:31113/api/v1/cluster/topology
```

预期：
- 三个 Pod 全部 `Running`。
- `/api/v1/me` 返回后端解析出的身份与租户（demo 模式下是 `local` / `authType: demo`）。
- `/api/v1/cluster/topology` 的 `totalGpus` 等于物理卡数，不含 VCI 弹性配额。

本机预览 Portal：

```bash
ssh -i ~/.ssh/qomolo-desktop.pem -N -L 31113:172.28.0.167:31113 root@14.103.49.106
```

然后浏览器打开 <http://127.0.0.1:31113>。

---

## 6. 登录方式

平台支持两种登录，可以同时开启，互不依赖：

**本地账号（默认开启）** —— 不需要 Keycloak，开箱即可测试。

首次部署时，如果 `local_users` 表为空且配置了 `BOOTSTRAP_ADMIN_PASSWORD`，后端会自动创建一个 SuperAdmin。之后重启不会覆盖已有账号，也不会重置密码。

```bash
kubectl -n ray-train-platform create secret generic ray-platform-bootstrap-admin --from-literal=BOOTSTRAP_ADMIN_PASSWORD='<设置一个强密码>'
```

相关 values：

| 键 | 默认值 | 说明 |
|---|---|---|
| `backend.localAuth.enabled` | `true` | 关闭后 `/api/v1/auth/login` 返回 404 |
| `backend.localAuth.sessionHours` | `12` | 会话有效期 |
| `backend.localAuth.bootstrapUsername` | `admin` | 首个管理员用户名 |
| `backend.localAuth.bootstrapTenant` | `local` | 首个管理员所属租户 |
| `backend.localAuth.bootstrapSecret` | `ray-platform-bootstrap-admin` | 留空则不创建 |

会话令牌是 `rls_` 前缀的不透明令牌，数据库只保存 HMAC 摘要（与 PAT 同一套构造，共用 `PAT_PEPPER`）。登录接口对同一用户名连续失败 5 次会锁定 5 分钟，且对不存在的用户也会走完 bcrypt 比对，避免用响应时间枚举账号。

**企业 SSO（Keycloak/OIDC）** —— 设置 `backend.oidcRequired=true` 与 `frontend.runtimeConfig.authRequired=true` 后，登录页会出现 SSO 按钮。三种令牌按前缀分发：`rls_` 本地会话、`rpt_` 个人访问令牌、其余按 OIDC 校验。

## 7. 租户 namespace 的前置资源

训练 Pod 跑在租户 namespace（`tenant-<租户ID>`），不是平台 namespace。以下资源必须在租户 namespace 中存在，否则 Pod 会停在 `CreateContainerConfigError`：

```bash
kubectl -n tenant-<租户ID> create secret generic tos-credentials --from-literal=AWS_ACCESS_KEY_ID='<AK>' --from-literal=AWS_SECRET_ACCESS_KEY='<SK>'
```

RayJob 渲染器以 `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` 这两个键名注入环境变量，和平台自身读取 TOS 用的 `access-key` / `secret-key` 键名不同，同一个 Secret 里两套键都要有。

LocalQueue 由后端在提交任务时自动创建并绑定到 `kueue.clusterQueueName`，不需要手工建。

## 8. 从 demo 模式切到生产模式

当前部署是 `appEnv=development` + `demoMode=true` + `oidcRequired=false`，任何人不登录就是 `local` 租户的 SuperAdmin。切生产必须同时改这几项：

```yaml
backend:
  appEnv: production
  demoMode: false
  oidcRequired: true
  oidc:
    issuerURL: https://<真实 Keycloak>/realms/ai
    clientID: ray-training-platform
    audience: ray-training-platform-api
frontend:
  runtimeConfig:
    authRequired: true
    keycloakURL: https://<真实 Keycloak>
    keycloakRealm: ai
    keycloakClientID: ray-training-platform
ingress:
  enabled: true          # 私网 ALB，替代 NodePort
```

`config.Load()` 在 `APP_ENV=production` 且 `DEMO_MODE=true` 时会直接拒绝启动，这是有意的保护。

Keycloak 侧需要：public client、Authorization Code + PKCE(S256)、redirect URI 精确包含 `/silent-check-sso.html`、realm role `SuperAdmin`/`TenantAdmin`/`Engineer`、group `platform/tenants/<tenant-id>`、audience 与 `OIDC_AUDIENCE` 一致。详见 [CLUSTER_DEPLOYMENT_GUIDE.md](CLUSTER_DEPLOYMENT_GUIDE.md) 第 1.4 节。
