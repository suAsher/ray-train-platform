# 本地账户安全与 Ray 运行态可信化实施计划

> **For implementation:** 按测试先行执行；每个阶段完成后运行该阶段列出的验证命令，再继续下一阶段。不得删除、取消或重建任何现有 RayJob/RayCluster。

**目标：** 让 Portal 的本地账号可以安全改密、让管理员管理本租户账号并立即撤销会话；准确展示 KubeRay 1.3 的任务状态；让调试工作区和日志/指标页面如实反映当前后端能力。

**架构：** PostgreSQL 中的 `local_users` 与 `local_sessions` 是本地认证事实来源。改密、管理员重置和禁用通过批量撤销 session 立即生效；审计只保存动作元数据。Kubernetes RayJob `status` 是训练状态事实来源，状态适配器将 KubeRay 部署态映射到平台状态。Vue 仅轮询并展示服务端事实，不能以装饰性状态替代真实资源状态。

**技术栈：** Go、Gin、GORM/PostgreSQL、Kubernetes dynamic client、Vue 3、Element Plus、Vite、Helm。

---

## 文件地图

### 后端

- 修改 `backend/api/local_auth.go`：改密后撤销全部本地会话。
- 修改 `backend/api/local_users.go`：创建角色边界、重置密码、启用/禁用与审计。
- 修改 `backend/api/local_auth_test.go`、`backend/api/local_users_test.go`：覆盖会话撤销、租户隔离、特权账号保护和密码泄露防护。
- 修改 `backend/repositories/local_auth.go`：按用户查询、批量撤销会话、禁用状态更新。
- 新建 `backend/repositories/audit.go`、`backend/repositories/audit_test.go`：映射已有 `audit_logs` 表并安全记录动作。
- 修改 `backend/repositories/schema_mapping_test.go`：验证新增模型与现有 migration 的列一致。
- 修改 `backend/k8s/rayjob.go`、`backend/k8s/rayjob_test.go`：KubeRay 1.3 状态映射、状态说明和 Submitter `restartPolicy` 断言。
- 修改 `backend/k8s/client.go`、新增/修改对应测试：在创建前校验模板的 Submitter restart policy；已有同名 RayJob 仍保持只读幂等，不原地修改。

### 前端

- 新建 `frontend/src/views/AccountSecurity/index.vue`：本地用户自助改密，Keycloak 用户只读提示。
- 修改 `frontend/src/router/index.js`、`frontend/src/layout/Layout.vue`：加入账户与安全路由和用户菜单入口。
- 修改 `frontend/src/auth/localSession.js`：实现受认证保护的本地改密请求，成功后清除本地会话。
- 修改 `frontend/src/views/QuotaManage/index.vue`：用户状态、重置密码、启用/禁用操作；按当前角色限制租户选择。
- 修改 `frontend/src/views/Devcenter/index.vue`：显示最后同步时间、显式刷新，停止状态不再呈现为 GPU 正在使用。
- 修改 `frontend/src/views/Job/JobDetail.vue`：明确显示 Loki/Prometheus 查询不可用原因，区分“无数据”和“服务未接入”。
- 新建/修改 `frontend/src/**/*.test.js`：无浏览器依赖的状态/请求辅助函数测试；Vite production build 作为完整页面验证。

### 文档

- 修改 `docs/USER_GUIDE.md`：用户改密、工作区状态、日志/指标前提和数据产物说明。
- 修改 `docs/ADMIN_GUIDE.md`：本地账户创建、禁用、重置边界；RayJob/Pod/Loki 的保留策略。
- 修改 `docs/BUILD_AND_DEPLOY.md`：本版本构建、Helm 发布、回滚和新任务验收步骤。

---

## 阶段 1：补足 KubeRay 状态适配与新任务模板防线

**文件：**

- 修改：`backend/k8s/rayjob.go`
- 修改：`backend/k8s/rayjob_test.go`
- 修改：`backend/k8s/client.go`
- 新建或修改：`backend/k8s/client_test.go`

- [ ] 先扩展 `TestMapRayJobStatus`，覆盖 `jobDeploymentStatus=Suspended` -> `QUEUED`、`Initializing` -> `PROVISIONING`，并断言在 `jobStatus` 为空时 `Message` 使用部署态而不是空字符串。
- [ ] 实现状态映射：保留已有终态逻辑；仅在未知值时输出 `UNKNOWN`。`reason/message` 依次使用状态字段、部署态和 Kubernetes Event 可用信息，不伪造 `RUNNING`。
- [ ] 提取并测试 Submitter 模板校验：任何渲染后的 RayJob 在创建前必须有 `spec.submitterPodTemplate.spec.restartPolicy: Never`。该校验用于阻止未来坏模板进入集群。
- [ ] 保持 `EnsureRayJob` 的幂等行为：遇到已有且平台拥有的 RayJob 只返回现有资源，不做不可预测的 spec patch；测试明确断言该行为。
- [ ] 运行 `cd backend && go test ./k8s -run 'Test(MapRayJobStatus|RenderRayJob|EnsureRayJob)' -count=1`。

## 阶段 2：完成本地账号、会话撤销与审计后端

**文件：**

- 修改：`backend/api/local_auth.go`
- 修改：`backend/api/local_users.go`
- 修改：`backend/api/local_auth_test.go`
- 修改：`backend/api/local_users_test.go`
- 修改：`backend/repositories/local_auth.go`
- 新建：`backend/repositories/audit.go`
- 新建：`backend/repositories/audit_test.go`
- 修改：`backend/repositories/schema_mapping_test.go`

- [ ] 扩展 `LocalAuthStore`：增加按 ID 查询用户、按 user ID 批量撤销活跃本地 session、更新禁用状态、写入审计记录的接口。所有错误在 HTTP 层转换为无敏感信息的统一错误码。
- [ ] 先写改密测试：当前密码错误返回通用认证错误；有效改密写入新 bcrypt 哈希、批量撤销该用户 session、响应及审计均不包含新旧密码。实现 `changePassword` 以通过测试。
- [ ] 先写管理员操作测试，再注册接口：
  - `POST /api/v1/local-users/:id/reset-password`
  - `POST /api/v1/local-users/:id/disable`
  - `POST /api/v1/local-users/:id/enable`

  TenantAdmin 仅可管理本租户 Engineer；SuperAdmin 可管理非 SuperAdmin 账号；普通管理员入口拒绝修改任何 SuperAdmin。禁用、重置密码和改密均必须撤销全部 session。
- [ ] 修复现有创建用户越权：TenantAdmin 不得创建 TenantAdmin 或 SuperAdmin；SuperAdmin 创建用户时可显式指定目标租户，TenantAdmin 忽略请求中的 `tenantId` 并固定使用自身租户。
- [ ] 新建 `AuditLogRecord` 映射已有 `audit_logs`，记录 `actor_user_id`、租户、动作、资源类型/ID、请求 ID 与不含秘密的结果元数据；若现有表缺少 actor 专列，则将 actor 放入受控 JSON payload。测试验证密码与 session token 不会写入 payload。
- [ ] 更新 schema 映射测试；不修改数据库 schema，复用现有 `audit_logs` migration，避免生产数据迁移风险。
- [ ] 运行 `cd backend && go test ./api ./auth ./repositories -count=1`。

## 阶段 3：实现 Portal 账户安全与真实状态体验

**文件：**

- 新建：`frontend/src/views/AccountSecurity/index.vue`
- 修改：`frontend/src/router/index.js`
- 修改：`frontend/src/layout/Layout.vue`
- 修改：`frontend/src/auth/localSession.js`
- 修改：`frontend/src/views/QuotaManage/index.vue`
- 修改：`frontend/src/views/Devcenter/index.vue`
- 修改：`frontend/src/views/Job/JobDetail.vue`
- 新建/修改：`frontend/src/auth/localSession.test.js`、`frontend/src/views/*.test.js`（只测试抽离的纯函数或 API 请求行为）

- [ ] 先为 `changeLocalPassword` 写 Node test：它带 Bearer Token、使用 `POST /api/v1/auth/password`、成功不保留本地 token；HTTP 错误不泄露提交密码。实现该 helper。
- [ ] 新建“账户与安全”页：当前身份来源只读显示；本地用户使用当前密码/新密码/确认密码表单；Keycloak 用户显示由企业身份系统管理。成功后清会话并跳转 `/login`。
- [ ] 将“账户与安全”加入 Layout 的用户下拉菜单与受保护路由；不向侧边栏暴露敏感入口。
- [ ] 扩展用户表：展示启用状态；仅对可管理的用户出现重置密码、禁用或启用按钮。重置使用临时输入框，不回显值；完成后清空表单并重新拉取列表。
- [ ] 修复 Dev Center 的状态视觉：`STOPPED/FAILED` 不使用绿色 pulse 或“GPU 在用”；展示状态标签、最后一次同步时间、手动刷新按钮。仍保持 5 秒自动轮询，手动刷新只是辅助。
- [ ] 修复 JobDetail 可观测性表达：捕获日志/指标接口错误并分别显示“服务未接入或不可达”，空成功结果仍显示“暂无数据”。不再把所有错误吞为 `null`。
- [ ] 运行 `cd frontend && npm test && npm run build`。

## 阶段 4：发布前综合验证与文档

**文件：**

- 修改：`docs/USER_GUIDE.md`
- 修改：`docs/ADMIN_GUIDE.md`
- 修改：`docs/BUILD_AND_DEPLOY.md`

- [ ] 写明本地账号初始密码仅从 Kubernetes Secret 取得；修改 Secret 不会重置已经创建的账号；禁止在工单、聊天或日志中传递密码。
- [ ] 写明状态更新周期、调试工作区状态语义和单 GPU 测试节点的资源竞争行为。
- [ ] 写明保留策略：RayCluster 完成后关闭、RayJob 默认 600 秒、数据库历史长期保留；没有 Loki 时 Pod 删除后的日志不可恢复；生产需要分别配置成功/失败 TTL 和 Loki retention。
- [ ] 在本地运行 `cd backend && go test ./...` 与 `cd frontend && npm test && npm run build`。
- [ ] 同步源码到远端；在远端用现有 `build-image.sh` 构建唯一新 tag；使用 `helm upgrade --install` 和已验证的 `values-vke-test-release.yaml` 发布。发布后只提交一个**全新**的最小训练任务验证 Submitter 模板，不修改存量任务。
- [ ] 验收 API：本地登录、改密导致旧 token 返回 401、管理员禁用/启用测试账号、`/api/v1/jobs/{id}` 状态为 `QUEUED/PROVISIONING` 而非 `UNKNOWN`、Dev Center 自动状态同步。确认不在任何输出中打印密码/Token。

## 完成标准

- 所有新增后端安全测试、KubeRay 适配测试及前端测试/构建均通过。
- TenantAdmin 无法创建或管理更高权限账号，所有本地认证会话在敏感账户变更后失效。
- 新提交任务的 RayJob Submitter 规格经过代码和 Kubernetes dry-run 双重校验；旧任务未经用户操作不变更。
- Portal 对停止的调试环境、Loki/Prometheus 不可用和无数据三种情况展示不同且可理解的状态。
- 本地与远端源码目录同步，Helm 发布和回滚过程写入运维文档。
