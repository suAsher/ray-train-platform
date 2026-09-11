# SuperAdmin 团队调整与稳定个人空间设计

## 背景

平台已经支持一个身份拥有多条 `tenant_memberships` 和自行切换 `active_tenant_id`，但旧的
`users.tenant_id`、个人数据根目录和若干资源外键仍按单租户建模。现网已证明：当用户存在旧团队
PAT 时，直接修改旧 `users.tenant_id` 会触发外键并回滚；只修改 `active_tenant_id` 则会让后续
`EnsureIdentity` 再次尝试改旧字段，导致提交、上传或签发新 PAT 失败。

本期要让 SuperAdmin 能安全地创建和重命名团队、调整用户当前团队并停用旧成员关系，同时保证
用户个人文件不复制、不丢失，不改变团队配额，不触碰运行中的训练资源。

## 决策

采用“全局身份与个人存储稳定，团队是可切换执行上下文”的模型：

- Identity 决定用户名、稳定 `storage_key` 和个人存储归属；
- active membership 决定当前请求的角色、团队配额、队列、团队共享数据和新任务归属；
- 历史任务、团队共享数据、数据集及旧 PAT 继续属于创建时的团队；
- 停用旧 membership 会使旧团队 PAT 立即失效，但不删除令牌记录或历史资源；
- 团队改名只改显示名，内部 ID、namespace、LocalQueue 和配额不变。

不采用复制个人目录或保留旧团队权限作为兼容手段。前者代价高且产生双写，后者不能真正移除
团队访问权。

## 数据模型

### 稳定身份和个人存储

为 `local_users` 增加显式 `storage_tenant_id`，现有数据从原始 `tenant_id` 回填。它只标识现存个人
对象根的历史归属，不随 active team 改变。旧 `tenant_id` 在兼容期保留为首次/归档团队字段，停止
在每次请求中改写。

个人数据挂载拆成两层含义：

- `data_mount_bindings.tenant_id`：PVC/ServiceAccount 所在的当前工作负载 namespace；
- `data_mount_bindings.storage_tenant_id`：个人对象根的稳定归属。

因此切换到 `devops` 后，可以在 `tenant-devops` 创建一条只属于该用户的挂载适配器，仍指向原有
`ray-train/tenants/local/users/<storage_key>/`。根路径只能由后端根据身份记录生成，API 请求不得传入
tenant、root prefix、PVC、CSI 参数或凭据。团队共享目录仍严格使用 active tenant，不允许跨团队。

个人文件浏览、上传、workspace snapshot、source artifact 和训练输出统一从已授权的个人 binding
取得根目录，不能再用 active tenant 字符串自行拼接。这样无需移动对象，也不会产生空白的新个人空间。

### 历史资源与外键

把以下 `(user_id, tenant_id)` 所有权外键从单行 `users(id, tenant_id)` 改为
`tenant_memberships(identity_id, tenant_id)`：

- `personal_access_tokens`；
- `source_artifacts`；
- `source_artifact_requests`；
- `data_space_uploads`。

membership 只改变 `status`，不物理删除，因此历史资源的引用完整性仍然成立。迁移前检查每条现有
资源都有对应 membership；存在孤儿时迁移失败并停止发布，不自动补造权限。

`EnsureIdentity` 只更新全局用户名、邮箱和时间戳，不再修改旧 home tenant 或把当前团队角色覆盖到
全局用户行。管理员用户摘要从 `local_users.active_tenant_id` 和 active membership 读取，不再展示陈旧
的 `users.tenant_id`。

## 管理 API

### 原子调整当前团队

新增 `PUT /api/v1/users/:id/active-membership`，只允许 SuperAdmin。请求包含：

```json
{
  "tenantId": "devops",
  "roles": ["Engineer"],
  "deactivateOtherMemberships": true
}
```

后端在一个事务中：

1. 锁定目标身份并确认身份、目标团队均未退役；
2. 创建或激活目标 membership，并校验团队角色，禁止授予全局 `SuperAdmin`；
3. 更新 `active_tenant_id`；
4. 按请求停用其他 memberships，但不删除记录；
5. 写入包含旧/新 active tenant 和停用列表的审计日志；
6. 返回最终 membership 列表。

任何一步失败都整笔回滚。该接口不修改配额、不移动数据、不删除 PAT，不接受用户自行指定存储根。
用户现有的自助切换接口继续只允许切到已有 ACTIVE membership。

### 团队创建和显示名

保留现有 `POST /api/v1/tenants` 创建接口并在 Portal 暴露给 SuperAdmin。新增
`PATCH /api/v1/tenants/:id`，本期只允许修改经过裁剪且长度受限的显示名。稳定 ID、namespace、
LocalQueue、accelerator class 和 GPU quota 不可通过该接口修改。

将现网 `local` 的显示名改为“感知应用算法团队”，其 ID 保持 `local`，GPU 配额保持 24。

## Portal 管理界面

在迁移后的 Portal 管理页增加：

- 创建团队；
- 修改团队显示名；
- 查看用户当前团队、所有 ACTIVE/INACTIVE memberships；
- 选择目标团队和团队角色；
- “移出其他团队”确认项，并明确提示旧团队 PAT 将失效、历史团队数据不会迁移；
- 调整成功后刷新用户、团队和 membership 数据，不在前端乐观伪造状态。

旧独立前端不增加新交互，但现有接口和页面必须继续工作；后端响应保持向前兼容。

## 本次现网操作

代码和数据库迁移验证通过并上线后执行：

1. 将 `local.name` 改为“感知应用算法团队”，不改任何配额；
2. 将 `zihao.liu`、`xin.gong` 的 active membership 设为 `devops/Engineer`；
3. 停用两人的 `local` membership；
4. 验证两人的个人 binding 仍指向原个人对象根；
5. 验证 `xin.gong` 的旧 `local` PAT 因 membership inactive 而无法认证；
6. 验证 `devops` GPU quota 仍为 0，GPU 提交返回明确的团队配额错误。

## 测试与安全闸门

后端测试必须覆盖：

- 有旧 PAT 和个人文件的用户仍可原子切换；
- 个人根、`storage_key` 和对象清单摘要在切换前后不变；
- 新团队提交、上传、snapshot、source artifact 和 PAT 使用 active membership；
- 旧团队 PAT 在旧 membership 停用后失效；
- 非 SuperAdmin、伪造 tenant、退役团队、最后 membership 和非法角色均被拒绝；
- 任一步失败时 active tenant 和 membership 状态完全回滚；
- 团队改名不改变 ID、namespace、queue、accelerator 或 quota；
- 数据库迁移在存在孤儿资源时失败，在完整 membership 数据上成功。

Portal 候选必须在构建机通过 lint、Element Plus 残留和 store 检查。上线后验证新旧 UI 登录、用户列表、
个人文件浏览以及未认证新 API 返回 401 而不是 404。

## 发布与回滚

先发布只包含向前兼容迁移和 API 的后端，再发布 Portal。数据库迁移不删除列或对象，旧后端仍能读取；
若新后端异常，Helm 回滚镜像即可。实际用户切换放在接口和 UI 验收之后，并逐用户校验。

发布过程不重启、删除或修改任何 RayJob、RayCluster、Workload 或训练 Pod。若存在运行中训练，只滚动
平台后端 Deployment，并在发布前后核对训练资源数量与 UID。任何校验失败均停止后续用户切换。

## 完成标准

- SuperAdmin 能创建团队、修改显示名并原子调整用户团队；
- `local` 显示为“感知应用算法团队”，配额仍为 24；
- 两位目标用户只保留 ACTIVE 的 `devops` membership，且 `devops` 配额仍为 0；
- 两位用户原个人文件无需复制即可在新团队继续浏览、上传和挂载；
- 历史 local 任务和团队数据不迁移、不删除，旧 local PAT 自动失效；
- 后端四端源码一致，Portal `dev` CI/CD 成功，现网健康检查和权限负向测试通过。
