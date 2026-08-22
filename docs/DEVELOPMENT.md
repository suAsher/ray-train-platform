# 开发与迭代指南

面向接手这个仓库继续开发的人。重点是「代码放在哪、怎么改、哪些坑已经踩过」。

---

## 1. 仓库结构

```
backend/            Go 控制面（API + Reconciler 同一个二进制）
  api/              HTTP 处理器：任务、会话、配额、本地账号、源产物
  auth/             OIDC / PAT / 本地会话三种认证，按令牌前缀分发
  domain/           领域模型与校验，无外部依赖，最适合写单元测试
  k8s/              RayJob / RayCluster 渲染、Kueue、拓扑、Reconciler
  repositories/     GORM 数据访问
  db/migrations/    版本化 SQL 迁移（只增不改）
  objectstore/      TOS 对象存储
  rayapi/           Ray Jobs API 兼容网关（支持原生 ray job submit）
  spk-rayjob/           CLI
frontend/src/
  views/            页面
  layout/           侧边栏与顶栏（角色化导航在这里）
  stores/session.js 服务端解析的当前用户与角色
  auth/             本地会话 + Keycloak 的统一门面
helm/               部署 chart
k8s/                Kueue / 租户 / 模板等集群资源
scripts/            预检、冒烟、E2E 脚本
docs/               本目录
```

---

## 2. 本地开发

后端（需要一个 PostgreSQL）：

```bash
cd backend && DATABASE_URL='postgres://user:pass@localhost:5432/ray?sslmode=disable' APP_ENV=development LOCAL_AUTH_ENABLED=true BOOTSTRAP_ADMIN_PASSWORD=local-admin-pass PAT_PEPPER=$(head -c 48 /dev/urandom | base64) go run .
```

没有 Kubernetes 也能启动，K8s 集成会自动禁用（只在 `APP_ENV=production` 时强制要求）。

前端：

```bash
cd frontend && npm install && npm run dev
```

`vite.config.js` 把 `/api` 代理到后端。

---

## 3. 提交前必须跑

```bash
cd backend && gofmt -l . && go vet ./... && go test ./... && cd ../frontend && npm run build
```

CI 目前**只构建镜像、不跑测试**，所以这一步只能靠人工。补 CI 是待办项之一。

---

## 4. 已经踩过的坑（改代码前务必读）

### 4.1 单元测试跑 SQLite，生产跑 PostgreSQL

这是本项目**最容易漏的一类 bug**，已经发生过四次：SQLite 不校验 `jsonb`、不建外键、还会按模型自动补列，所以下面这些在测试里全绿、一上生产就必挂：

| 症状 | 原因 |
|---|---|
| `SQLSTATE 22P02` | 往 `jsonb` 列写了空字符串 |
| `SQLSTATE 23503` | 往可空外键写了 `''` 而不是 NULL |
| `SQLSTATE 42703` | GORM 推导的列名和迁移里的列名不一致 |

防护措施：[repositories/schema_mapping_test.go](../backend/repositories/schema_mapping_test.go) 会拿迁移 DDL 校验所有模型的列名。**新增模型或字段时必须把模型加进那个测试的 `models` 列表。**

改数据访问代码时的自查：
- 每个 `jsonb` 列都有值吗？（空字符串不是合法 JSON）
- 可空外键用 `*string` 了吗？
- 列名和迁移一致吗？不一致就加 `gorm:"column:xxx"`

### 4.2 RayJob entrypoint 不能含 shell 操作符

KubeRay 把 `spec.entrypoint` 拼到 `ray job submit -- ...` 后面。任何 `&&`、`;`、`|` 都会截断命令，结果是 **Ray 只执行前半段、报 SUCCEEDED，真正的训练在 submitter Pod 里跑并失败**——静默假成功，非常难查。

工作目录必须走 `runtimeEnvYAML` 的 `working_dir`，不要用 `cd xxx &&`。有测试守着：`TestRenderRayJobEntrypointHasNoShellOperators`。

### 4.3 不要把卷挂到 `/tmp/ray` 下面

Ray 要往 `/tmp/ray` 写 session 目录。在它下面挂卷会让 kubelet 以 root 创建父目录，Ray（uid 1000）随后启动失败。spill 卷挂在 `/tmp/ray-spill`。有测试守着：`TestRayPodsDoNotMountInsideRayTempDir`。

### 4.4 Tailwind 的 content 必须包含 `.vue`

漏掉会让所有布局类被 purge，生产构建出来的页面完全没有样式，但本地 `npm run dev` 看不出来。

### 4.5 VKE 的 serverless 虚拟节点

VCI 节点（`type=virtual-kubelet`）上报上千张「GPU」，那是弹性配额不是物理卡。算力统计已排除，Ray Pod 也通过 nodeSelector 钉在真实节点池。新写涉及节点容量的代码时要注意这一点。

---

## 5. 常见改动怎么做

### 加一个 API 接口

1. 在 `api/` 里加处理器，用 `h.writeSuccess` / `h.writeError` 保持统一 Envelope
2. 在 `main.go` 的 `registerAPIRoutesWithLocalAuth` 里挂载，注意挂在哪个组：
   - `v1`：任何认证方式都可访问（含 PAT 机器令牌）
   - `interactive`：只有人类登录（OIDC 或本地会话），PAT 会被拒
   - 管理接口在处理器内部再用 `principal.Allowed("TenantAdmin")` 二次校验
3. 写测试，参考 `api/session_test.go`

### 改数据库

只增迁移文件，不改历史迁移。文件名 `000N_描述.up.sql`，然后更新 `db/postgres_test.go` 里的版本清单，并把新模型加进 schema 映射测试。

### 改 RayJob 渲染

全部集中在 `k8s/rayjob.go`。改完务必跑 `go test ./k8s`，那里的测试覆盖了上面几个已知陷阱。

### 加一个前端页面

1. `views/` 下建目录
2. `router/index.js` 注册路由，管理员页面加 `meta: { admin: true }`
3. 如果要出现在侧边栏，加到 `layout/Layout.vue` 的 `workspaceNav` 或 `adminNav`
4. 角色判断一律用 `stores/session.js` 的 `isAdmin`，不要自己解析令牌

---

## 6. 认证模型速查

| 令牌前缀 | 类型 | 用途 | 能过 `interactive` 组吗 |
|---|---|---|---|
| `rls_` | 本地会话 | Portal 用户名密码登录 | 是 |
| `rpt_` | 个人访问令牌 | CLI / CI 机器调用 | 否（受 scope 限制） |
| 其他 | OIDC access token | 企业 SSO | 是 |

角色只有三个：`SuperAdmin`、`TenantAdmin`、`Engineer`，判定逻辑在 `auth/claims.go` 的 `Allowed()`。

**租户永远取自服务端解析的 principal，绝不信任请求体里的 tenantId。**

---

## 7. 相关文档

- [ARCHITECTURE.md](ARCHITECTURE.md) — 架构图与逐层实现状态
- [BUILD_AND_DEPLOY.md](BUILD_AND_DEPLOY.md) — 构建、部署、扩容、发版
- [ADMIN_GUIDE.md](ADMIN_GUIDE.md) — 集群前置条件、账号、Keycloak 与权限配置
