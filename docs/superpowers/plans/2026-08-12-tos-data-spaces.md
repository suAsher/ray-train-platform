# TOS 数据空间与 Ray 挂载 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `shanghai-data-transfer` 建成按个人、租户和公共范围隔离的训练存储平台；用户从“我的数据”选择目录，调试和训练均通过固定本地路径访问，且不接触长期 TOS 凭据。

**Architecture:** TOS 是用户代码、数据、训练输出的唯一持久化来源。后端从已认证的 `tenant + subject` 派生逻辑数据空间；TOS、PVC、FSX CSI、ServiceAccount、IAM Role 都是后台实现。任何数据挂载先经真实的双用户 IRSA 前缀验证，失败时拒绝创建含数据空间的 RayJob 或调试环境。

**Tech Stack:** Go 1.25、Gin、GORM/PostgreSQL、Volcengine TOS SDK v2、VKE FSX CSI/IRSA、Kubernetes、Ray/KubeRay、Vue 3、Element Plus、Helm 3。

---

## 范围、兼容与目录布局

本计划实现数据空间和运行时挂载闭环，不修改现有 Loki；物理节点 Alloy DaemonSet、DCGM、Prometheus 和 Grafana 在 GPU 节点池稳定后另立计划。

旧 `storage_assets` 与 `tos-local-*` PVC 仅供旧任务和历史产物读取。新用户页面和新任务不再让用户选择它们，不自动移动或删除旧对象。

```text
ray-train/
├── public/
└── tenants/<tenant>/
    ├── shared/
    ├── users/<subject>/
    │   ├── workspace/
    │   ├── files/
    │   ├── runs/
    │   └── snapshots/<snapshot-id>/
    └── legacy/
```

固定容器路径如下，必须定义为 Go 常量并在前端文案复用：

```text
/workspace                    调试工作区（rw）或训练快照（ro）
/mnt/storage/me               个人 TOS 根（rw）
/mnt/storage/team             租户共享（ro）
/mnt/storage/public           公共数据（ro）
/mnt/data/input               本次选择的输入（ro）
/mnt/data/output              runs/<job-id>/（rw）
/mnt/data/checkpoints         output/checkpoints/（rw）
/mnt/idc/original             IDC NFS（ro）
/mnt/idc/wellspiking          IDC NFS（ro）
/mnt/idc/shared               IDC NFS（ro）
/mnt/cache                    节点本地可丢失缓存
```

### Task 1: 验证 VKE 的受限 TOS 挂载身份

**Files:**

- Create: `ops/storage/shanghai-data-transfer/40-irsa-prefix-smoke.yaml`
- Create: `ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh`
- Modify: `docs/CLUSTER_DEPLOYMENT_GUIDE.md`

- [ ] **Step 1: 写两个用户的失败前 Smoke 定义**

创建 `40-irsa-prefix-smoke.yaml` 模板，含两个 ServiceAccount、两个 `fsx.csi.volcengine.com` 静态 PV/PVC 和两个 Pod。两个根分别是：

```text
ray-train/tenants/local/users/irsa-smoke-a/
ray-train/tenants/local/users/irsa-smoke-b/
```

模板只接收非敏感 `FSX_TOS_ATTRIBUTES_JSON`；严禁 `secretName`、AK、SK。每个 Pod 仅挂自己的 PVC，并执行：

```sh
set -eu
printf '%s\n' "$USER_ID" > /mnt/me/owner.txt
test "$(cat /mnt/me/owner.txt)" = "$USER_ID"
test ! -e /mnt/other/owner.txt
```

- [ ] **Step 2: 写出可清理的验证脚本并先运行 RED**

脚本必须要求下列变量，缺少任意一项即退出：

```text
KUBECONFIG
FSX_TOS_ATTRIBUTES_JSON
IRSA_SMOKE_A_SERVICE_ACCOUNT
IRSA_SMOKE_B_SERVICE_ACCOUNT
IRSA_SMOKE_A_ROLE_ANNOTATION
IRSA_SMOKE_B_ROLE_ANNOTATION
```

它创建唯一 namespace `ray-tos-irsa-smoke-<timestamp>`，等待 Pod Ready，检查日志与 PVC `Bound`，用 trap 删除该 namespace。先运行：

```bash
cd /opt/guofeng/vke-cluster/ray-platform
bash ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh
```

Expected: 当前未配置时明确报 `IRSA mount identity is not configured`，绝不输出 Secret 值。

- [ ] **Step 3: 建立真实最小权限并验证 GREEN**

为 A/B 配不同 IAM Role：自身前缀 `Get/List/Put/Delete`；`public/` 与 `tenants/local/shared/` 只允许 `Get/List`。脚本除验证各自写入外，必须使用挂载身份验证 A 不能读取 B 的前缀。重跑：

```bash
bash ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh
```

Expected: `IRSA prefix mount contract verified`。

- [ ] **Step 4: 记录可复现基础设施契约并提交**

在部署文档记录 csi-fsx 版本、IRSA 注解键、TOS 前缀、所需 IAM Action 和非敏感 `volumeAttributes`。不要记录 token、AK、SK。

```bash
git add ops/storage/shanghai-data-transfer/40-irsa-prefix-smoke.yaml ops/storage/shanghai-data-transfer/41-verify-irsa-prefix-mount.sh docs/CLUSTER_DEPLOYMENT_GUIDE.md
git commit -m "test: verify IRSA-scoped TOS mounts"
```

### Task 2: 建立逻辑空间和后台挂载绑定模型

**Files:**

- Create: `backend/db/migrations/0007_data_mount_bindings.up.sql`
- Create: `backend/domain/data_space.go`
- Create: `backend/domain/data_space_test.go`
- Modify: `backend/domain/data_location.go`
- Modify: `backend/domain/data_location_test.go`
- Create: `backend/domain/data_mount_binding.go`
- Create: `backend/domain/data_mount_binding_test.go`
- Create: `backend/repositories/data_spaces.go`
- Create: `backend/repositories/data_spaces_test.go`

- [ ] **Step 1: 写领域 RED 测试**

```go
func TestPersonalDataSpacesUseStableSubjectPrefix(t *testing.T) {
    spaces := domain.PersonalDataSpaces("local", "kc-7f3a")
    got := spaces[domain.DataSpaceMyFiles].RootPrefix
    want := "ray-train/tenants/local/users/kc-7f3a/files/"
    if got != want { t.Fatalf("root=%q want=%q", got, want) }
}

func TestDataLocationRejectsEscapeAndReadonlyOutput(t *testing.T) {
    if _, err := domain.NewDataLocation(domain.DataSpaceMyFiles, "../other"); err == nil { t.Fatal("traversal accepted") }
    if err := domain.ValidateOutputSpace(domain.DataSpaceTeamShared); err == nil { t.Fatal("readonly output accepted") }
}

func TestDataMountBindingRejectsLongLivedSecret(t *testing.T) {
    binding := domain.DataMountBinding{ID: "b-1", ClaimName: "data-a", Driver: "fsx.csi.volcengine.com", SecretName: "tos-fsx-credentials"}
    if err := binding.Validate(); err == nil { t.Fatal("secret-backed binding accepted") }
}
```

- [ ] **Step 2: 验证 RED**

```bash
cd backend && go test ./domain ./repositories -run 'Test(PersonalDataSpaces|DataLocation|DataMountBinding)' -count=1
```

Expected: 缺少数据空间和绑定模型而无法编译。

- [ ] **Step 3: 实现不可泄露基础设施细节的模型**

定义 `DataSpaceID`：`workspace`、`my-files`、`my-runs`、`team-shared`、`public`、`idc-original`、`idc-wellspiking`、`idc-shared`。`DataSpace` 的 `RootPrefix` 必须 `json:"-"`。`DataLocation` 只允许合法空间 ID 与不含 `..`、`\\`、NUL、绝对路径、URI 的相对路径。

`DataMountBinding` 至少包含：`ID`、`TenantID`、`UserID`、`Scope`、`ClaimName`、`ServiceAccountName`、`Driver`、`VolumeAttributesJSON`、`RootPrefix`、`ReadOnly`、`Status`、`SecretName`。`SecretName` 永远拒绝非空值；状态只允许 `PENDING`、`READY`、`FAILED`。

- [ ] **Step 4: 迁移和 repository**

迁移创建 `data_mount_bindings`，对个人绑定建立 `(tenant_id,user_id,scope)` 唯一索引、对租户绑定建立 `(tenant_id,scope)` 唯一索引、公共绑定建立 `(scope)` 唯一索引。实现：

```go
EnsurePersonalDataBinding(ctx context.Context, binding domain.DataMountBinding) (domain.DataMountBinding, error)
GetDataBinding(ctx context.Context, tenantID, subject, scope string) (domain.DataMountBinding, error)
ListDataBindings(ctx context.Context, tenantID, subject string) ([]domain.DataMountBinding, error)
UpdateDataBindingStatus(ctx context.Context, id, status string) error
```

`Get/List` 只能返回当前 subject 的个人绑定、当前 tenant 的共享绑定、公共绑定和可见 IDC 绑定；空 `user_id` 不能误作个人空间。对不存在的个人 binding，服务先创建数据库 `PENDING` 记录；仅在 Task 1 的 IRSA 模板契约可用时，才由平台创建对应的最小权限 ServiceAccount/PV/PVC 并将状态更新为 `READY`。IAM Role、信任策略和每个用户前缀策略属于云账号权限面，必须由具备 IAM 权限的管理员或自动化身份完成；平台不能以长期 TOS Secret 模拟该步骤。

- [ ] **Step 5: 验证 GREEN 并提交**

```bash
cd backend && go test ./domain ./repositories -run 'Test(PersonalDataSpaces|DataLocation|DataMountBinding|DataBinding)' -count=1
git add db/migrations/0007_data_mount_bindings.up.sql domain/data_space.go domain/data_space_test.go domain/data_location.go domain/data_location_test.go domain/data_mount_binding.go domain/data_mount_binding_test.go repositories/data_spaces.go repositories/data_spaces_test.go
git commit -m "feat: model TOS data spaces and bindings"
```

### Task 3: 将 TOS 对象操作限制在数据空间根内

**Files:**

- Modify: `backend/objectstore/store.go`
- Modify: `backend/objectstore/tos.go`
- Modify: `backend/objectstore/tos_store.go`
- Create: `backend/objectstore/tos_data_spaces_test.go`
- Create: `backend/data_space_runtime.go`
- Create: `backend/data_space_runtime_test.go`
- Modify: `backend/config/config.go`
- Modify: `backend/config/config_test.go`

- [ ] **Step 1: 写对象边界 RED 测试**

```go
func TestTOSDataEntriesNeverEscapeSpaceRoot(t *testing.T) {
    store := fakeDataStore("ray-train/tenants/local/users/a/files/train/", "ray-train/tenants/local/users/b/files/private/")
    page, err := store.ListDataEntries(context.Background(), "ray-train/tenants/local/users/a/files/", "train", "", 100)
    if err != nil || len(page.Entries) != 1 { t.Fatalf("page=%#v err=%v", page, err) }
    if _, err := store.ListDataEntries(context.Background(), "ray-train/tenants/local/users/a/files/", "../b", "", 100); err == nil { t.Fatal("escape accepted") }
}

func TestTOSDataUploadPresignCannotEscapeWritableRoot(t *testing.T) {
    _, err := store.PresignDataPut(context.Background(), "ray-train/tenants/local/users/a/files/", "../b/secret", "text/plain", time.Minute)
    if err == nil { t.Fatal("cross-user upload accepted") }
}
```

- [ ] **Step 2: 验证 RED**

```bash
cd backend && go test ./objectstore -run 'TestTOSData(Entries|Upload)' -count=1
```

Expected: 方法不存在。

- [ ] **Step 3: 实现受控 TOS 接口**

增加 `DataEntry`（目录/文件、大小、修改时间）、`DataEntryPage` 和：

```go
ListDataEntries(ctx context.Context, rootPrefix, relativePath, cursor string, limit int) (DataEntryPage, error)
CreateDataDirectory(ctx context.Context, rootPrefix, relativePath string) error
PresignDataPut(ctx context.Context, rootPrefix, relativePath, contentType string, ttl time.Duration) (PresignedPut, error)
ReadData(ctx context.Context, rootPrefix, relativePath string) (ArtifactRead, error)
RenameDataFile(ctx context.Context, rootPrefix, from, to string) error
DeleteDataFile(ctx context.Context, rootPrefix, relativePath string) error
CopyDataPrefix(ctx context.Context, sourceRoot, sourcePath, destinationRoot, destinationPath string) error
```

根与相对路径分别验证后再拼接；页大小最大 100；`.ray-platform-directory` 对用户隐藏。目录创建写 `<prefix>/.ray-platform-directory`。V1 上传使用 15 分钟单对象 PUT URL，最大 5GiB，只签 `Content-Type`。重命名只支持单文件 Copy+Delete；目录递归删除/移动返回稳定错误，不在同步 HTTP 请求里实现。

- [ ] **Step 4: 组装运行时 TOS 客户端**

实现 `newDataSpaceStore(cfg)`：复用 backend 的 `TOS_ENDPOINT`、`TOS_REGION`、`TOS_BUCKET` 和控制面 Secret。生产环境 `DATA_SPACES_ENABLED=true` 时，配置不完整让 readiness 失败。该客户端只在 backend 使用，不传入 Ray renderer 或 API 响应。

- [ ] **Step 5: 验证 GREEN 并提交**

```bash
cd backend && go test ./objectstore ./config -run 'Test(TOSData|NewDataSpaceStore|Load)' -count=1
git add objectstore/store.go objectstore/tos.go objectstore/tos_store.go objectstore/tos_data_spaces_test.go data_space_runtime.go data_space_runtime_test.go config/config.go config/config_test.go
git commit -m "feat: add scoped TOS data operations"
```

### Task 4: 提供“我的数据” API、初始化和发布流程

**Files:**

- Create: `backend/api/data_spaces.go`
- Create: `backend/api/data_spaces_test.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/main.go`
- Modify: `backend/repositories/audit.go`

- [ ] **Step 1: 写 HTTP RED 测试**

```go
func TestDataSpaceListOnlyShowsRequestersRoots(t *testing.T) {
    router := dataSpaceRouter(t, engineer("local", "user-a"))
    response := httptest.NewRecorder()
    router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/data-spaces", nil))
    if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "user-b") { t.Fatalf("status=%d body=%s", response.Code, response.Body.String()) }
}

func TestDataSpaceWriteRejectsReadonlySpace(t *testing.T) {
    router := dataSpaceRouter(t, engineer("local", "user-a"))
    response := httptest.NewRecorder()
    router.ServeHTTP(response, jsonRequest(http.MethodPost, "/api/v1/data-spaces/team-shared/folders", `{"path":"new"}`))
    if response.Code != http.StatusForbidden { t.Fatalf("status=%d", response.Code) }
}
```

- [ ] **Step 2: 验证 RED**

```bash
cd backend && go test ./api -run 'TestDataSpace' -count=1
```

Expected: handler 和路由不存在。

- [ ] **Step 3: 实现空间初始化与 API**

首次 `GET /api/v1/data-spaces` 后，在当前用户根创建 `workspace/`、`files/`、`runs/`、`snapshots/` 标记。注册：

```text
GET    /api/v1/data-spaces
GET    /api/v1/data-spaces/:id/entries?path=&cursor=
POST   /api/v1/data-spaces/:id/folders
POST   /api/v1/data-spaces/:id/uploads
GET    /api/v1/data-spaces/:id/download?path=
POST   /api/v1/data-spaces/:id/rename
DELETE /api/v1/data-spaces/:id/entries?path=
POST   /api/v1/data-spaces/publish
```

上传请求为 `{"path":"file.bin","contentType":"application/octet-stream"}`；只返回短期上传 URL、必填 Header、到期时间。下载由后端授权后流式转发为安全 attachment。响应不含 Bucket、对象 key、PVC、PV、Secret。

普通用户只能写 `workspace`、`my-files`、`my-runs`。TenantAdmin 只能从同租户个人目录发布到 `team-shared`；SuperAdmin 才能发布 `public`。发布禁止覆盖。初始化、创建、上传签发、删除、重命名、发布均写不含对象 key 和 URL 的审计记录。

- [ ] **Step 4: 处理不可用后端**

在 `api.Options` 增加 `DataSpaces`。TOS 不可用时，空间列表保留逻辑根并返回 `available:false`；浏览和任何写操作返回 `503 DATA_SPACE_STORE_UNAVAILABLE`，不回退到本地磁盘。

- [ ] **Step 5: 验证 GREEN 并提交**

```bash
cd backend && go test ./api ./repositories -run 'Test(DataSpace|Publish|DataBinding)' -count=1
git add api/data_spaces.go api/data_spaces_test.go api/jobs.go main.go repositories/audit.go
git commit -m "feat: expose governed TOS data spaces"
```

### Task 5: 将受限绑定渲染为调试和训练挂载

**Files:**

- Create: `backend/k8s/data_mounts.go`
- Create: `backend/k8s/data_mounts_test.go`
- Modify: `backend/k8s/client.go`
- Modify: `backend/k8s/client_test.go`
- Modify: `backend/k8s/raycluster.go`
- Modify: `backend/k8s/raycluster_test.go`
- Modify: `backend/k8s/rayjob.go`
- Modify: `backend/k8s/rayjob_storage_mounts_test.go`
- Modify: `backend/api/workspaces.go`
- Modify: `backend/api/submission_service.go`
- Modify: `backend/api/submission_service_test.go`

- [ ] **Step 1: 写 renderer RED 测试**

```go
func TestDevWorkspaceMountsPersonalAndReadonlySpaces(t *testing.T) {
    manifest, err := RenderDevRayCluster(validWorkspace(), WorkspaceRenderOptions{Image: validImage, DataMounts: testDataMountPlan()})
    if err != nil { t.Fatal(err) }
    worker := devWorkerPodSpec(t, manifest)
    assertMount(t, worker, "personal", "/workspace", "workspace", false)
    assertMount(t, worker, "team", "/mnt/storage/team", "", true)
    assertMount(t, worker, "public", "/mnt/storage/public", "", true)
    assertNoSecretEnv(t, worker)
}

func TestRayJobUsesSnapshotAndInputOutputAliases(t *testing.T) {
    job := validRenderJobWithInput(domain.DataLocation{SpaceID: domain.DataSpaceTeamShared, RelativePath: "images/v1"})
    manifest, err := RenderRayJob(job, RenderOptions{DataMounts: testDataMountPlan()})
    if err != nil { t.Fatal(err) }
    assertMount(t, rayWorkerPodSpec(t, manifest), "team", "/mnt/data/input", "images/v1", true)
    assertMount(t, rayWorkerPodSpec(t, manifest), "personal", "/mnt/data/output", "runs/"+job.ID, false)
    assertNoSecretEnv(t, rayWorkerPodSpec(t, manifest))
}
```

- [ ] **Step 2: 验证 RED**

```bash
cd backend && go test ./k8s ./api -run 'Test(DevWorkspaceMounts|RayJobUsesSnapshot)' -count=1
```

Expected: `DataMountPlan` 与受控渲染函数不存在。

- [ ] **Step 3: 实现后台绑定预检**

`DataMountPlan` 只含经过作用域查询后的 binding、subPath、只读标记和 ServiceAccount。`ResolveDataMountPlan` 必须检查：个人 binding 的 subject 一致、所有 binding `READY`、PVC `Bound` 且在当前 tenant namespace、ServiceAccount 存在且等于 binding 登记名称。任一失败返回稳定原因，且不得创建 RayJob/RayCluster。

`EnsureDataMountBinding` 只能操作带 `app.kubernetes.io/managed-by=ray-train-platform` 与 binding ID 标签的 PV/PVC/ServiceAccount；先 DryRun，发现同名无所有权标签即拒绝。PV 从 Task 1 的非敏感 `volumeAttributes` 生成，强制绑定 root path，不得设置 `secretName`。

- [ ] **Step 4: 渲染调试环境**

Dev RayCluster 的 Head/Worker 均使用绑定 ServiceAccount。IDE 仅在 GPU Worker；Head 不分配 GPU。两个 Pod 的 `volumeMount` 均有个人、团队、公共和 IDC 根；IDC PV 与 Mount 均显式 `readOnly:true`。个人空间分别挂 `/workspace` 的 `workspace` subPath 和 `/mnt/storage/me` 根。删除现在调试环境将 IDC PVC 以读写方式挂载的行为。

- [ ] **Step 5: 渲染训练和快照**

新增 `POST /api/v1/dev-workspaces/:id/snapshots`：将 workspace 前缀复制到 `snapshots/<snapshot-id>/`，禁止覆盖。`workspace` 代码来源只允许引用当前用户快照，删除 `workspace source requires an IDC PVC`。

Ray Head/Worker 挂：个人、团队、公共、IDC、`/mnt/data/input`、`/mnt/data/output`、`/mnt/data/checkpoints`。input 绑定选择目录且只读，output 从个人 `runs/<job-id>` 派生。Submitter 只读挂代码快照，不挂业务数据。Head/Worker/Submitter 主容器都不能有 AK、SK、endpoint、bucket、token 环境变量。

- [ ] **Step 6: 验证 GREEN 并提交**

```bash
cd backend && go test ./k8s ./api -run 'Test(DevWorkspaceMounts|RayJobUsesSnapshot|RenderRayJob.*Storage|Submission.*Data)' -count=1
git add k8s/data_mounts.go k8s/data_mounts_test.go k8s/client.go k8s/client_test.go k8s/raycluster.go k8s/raycluster_test.go k8s/rayjob.go k8s/rayjob_storage_mounts_test.go api/workspaces.go api/submission_service.go api/submission_service_test.go
git commit -m "feat: mount governed TOS spaces in Ray workloads"
```

### Task 6: 实现“我的数据”与简化的训练提交流程

**Files:**

- Create: `frontend/src/api/dataSpaces.js`
- Create: `frontend/src/api/dataSpaces.test.js`
- Create: `frontend/src/components/DataExplorer.vue`
- Create: `frontend/src/components/DataSpacePicker.vue`
- Create: `frontend/src/components/DataSpacePicker.test.js`
- Modify: `frontend/src/views/DataCache/index.vue`
- Modify: `frontend/src/views/Job/CreateJob.vue`
- Modify: `frontend/src/submission.js`
- Modify: `frontend/src/submission.test.js`
- Modify: `frontend/src/views/Devcenter/index.vue`
- Modify: `frontend/src/router/index.js`
- Modify: `frontend/src/layout/Layout.vue`

- [ ] **Step 1: 写前端 RED 测试**

```js
test('builds a logical data-space path', () => {
  assert.equal(dataSpaceEntriesPath('my-files', 'datasets/a', 'opaque+/='), '/api/v1/data-spaces/my-files/entries?path=datasets%2Fa&cursor=opaque%2B%2F%3D')
})

test('submits logical locations rather than TOS URIs or PVC assets', () => {
  const spec = buildJobSpec({ ...validForm, input: { spaceId: 'team-shared', relativePath: 'v1' }, output: { spaceId: 'my-runs', relativePath: '' } })
  assert.deepEqual(spec.input, { spaceId: 'team-shared', relativePath: 'v1' })
  assert.equal('datasetUri' in spec, false)
  assert.equal('datasetStorage' in spec, false)
})
```

- [ ] **Step 2: 验证 RED**

```bash
cd frontend && node --test src/api/dataSpaces.test.js src/submission.test.js
```

Expected: API helper 和新提交字段不存在。

- [ ] **Step 3: 将目录册替换为资源管理器**

`DataCache/index.vue` 标题改为“我的数据”。`DataExplorer` 左树固定展示：我的工作区、我的文件、我的训练结果、团队共享、公共数据、IDC 只读数据；右侧展示直接子文件/目录、大小、更新时间和只读状态。支持进入目录、返回、新建文件夹、单文件上传、下载、重命名、删除。只读目录不显示写操作。

禁止显示 TOS、Bucket、对象 key、PV、PVC、AK、SK、URI。大于 5GiB 显示“请使用管理员数据导入流程”，不经 backend 代理上传。

- [ ] **Step 4: 复用目录选择器改造任务表单**

`DataSpacePicker` 的 v-model 固定为：

```js
{ spaceId: 'team-shared', relativePath: 'datasets/version-1' }
```

输入只展示有读权限空间；输出只展示个人可写空间。`CreateJob.vue` 改为：代码和模板、数据和结果、GPU 与确认。默认输出为“我的训练结果/`<任务名>-<任务ID>`”。确认页只展示 `/mnt/data/input`、`/mnt/data/output`、`/mnt/data/checkpoints`。

- [ ] **Step 5: 修改调试页和导航**

DevCenter 展示 GPU Worker 可见挂载和读写状态；“创建训练”先创建快照再跳转已填充工作区代码来源的训练表单。导航“数据与模型产物”改为“我的数据”，但保留 `/datacache` 兼容收藏链接。

- [ ] **Step 6: 验证 GREEN 并提交**

```bash
cd frontend && node --test src/api/dataSpaces.test.js src/submission.test.js src/components/DataSpacePicker.test.js && npm run build
git add src/api/dataSpaces.js src/api/dataSpaces.test.js src/components/DataExplorer.vue src/components/DataSpacePicker.vue src/components/DataSpacePicker.test.js src/views/DataCache/index.vue src/views/Job/CreateJob.vue src/submission.js src/submission.test.js src/views/Devcenter/index.vue src/router/index.js src/layout/Layout.vue
git commit -m "feat: add personal TOS data workspace"
```

### Task 7: 添加 Helm 配置、迁移兼容与运维文档

**Files:**

- Modify: `helm/ray-train-platform/templates/rbac.yaml`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/values-test.yaml.example`
- Modify: `helm/ray-train-platform/values-prod.yaml.example`
- Create: `ops/storage/shanghai-data-transfer/50-data-space-bootstrap.sql`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/ADMIN_GUIDE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: 写 Helm RED 渲染断言**

```bash
helm template ray-platform helm/ray-train-platform -f helm/ray-train-platform/values-test.yaml.example > /tmp/ray-platform.yaml
grep -q 'DATA_SPACES_ENABLED' /tmp/ray-platform.yaml
grep -q 'ray-train-platform-role' /tmp/ray-platform.yaml
! grep -q 'tos-fsx-credentials' /tmp/ray-platform.yaml
```

Expected: 接入前第一条检查失败。

- [ ] **Step 2: 添加最小配置和 RBAC**

增加：

```yaml
dataSpaces:
  enabled: false
  mountDriver: fsx.csi.volcengine.com
  fsxVolumeAttributes: {}
  personalServiceAccountPrefix: ray-data
  idcBindings: []
  uploadMaxBytes: 5368709120
```

生产 `enabled:true` 时，Chart 要求 `fsxVolumeAttributes` 非空、driver 正确，且禁止引用 `tos.secretName` 作为工作负载挂载 Secret。ClusterRole 仅增加平台拥有资源需要的 PV/PVC/ServiceAccount 动词；不增加 `pods/exec`、ServiceAccount token 创建、任意 Secret list 权限。

- [ ] **Step 3: 编写无破坏的兼容引导 SQL 和文档**

`50-data-space-bootstrap.sql` 只能登记旧任务的 `legacy` 描述和现有共享/IDC binding 元数据；不得写 Bucket policy、PV、PVC、Secret，不得执行删除。文档说明用户使用方式、管理员发布权限、IRSA 双用户 smoke、IDC 只读、本地 cache 易失、平台控制面从单卡节点迁离、构建升级回滚。

- [ ] **Step 4: 验证 GREEN 并提交**

```bash
helm lint helm/ray-train-platform -f helm/ray-train-platform/values-test.yaml.example
helm template ray-platform helm/ray-train-platform -f helm/ray-train-platform/values-test.yaml.example > /tmp/ray-platform.yaml
grep -q 'DATA_SPACES_ENABLED' /tmp/ray-platform.yaml
! grep -q 'tos-fsx-credentials' /tmp/ray-platform.yaml
git add helm/ray-train-platform/templates/rbac.yaml helm/ray-train-platform/templates/backend-deployment.yaml helm/ray-train-platform/values.yaml helm/ray-train-platform/values-test.yaml.example helm/ray-train-platform/values-prod.yaml.example ops/storage/shanghai-data-transfer/50-data-space-bootstrap.sql docs/ARCHITECTURE.md docs/USER_GUIDE.md docs/ADMIN_GUIDE.md docs/BUILD_AND_DEPLOY.md
git commit -m "docs: operate governed TOS data spaces"
```

### Task 8: 全量验证、构建和单卡真实验收

**Files:**

- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/USER_GUIDE.md`

- [ ] **Step 1: 运行全量本地验证**

```bash
cd backend && go test ./... -count=1
cd ../frontend && node --test src/**/*.test.js && npm run build
cd .. && helm lint helm/ray-train-platform -f helm/ray-train-platform/values-test.yaml.example
```

Expected: 全部通过；测试假凭据只能是 `ak`、`sk`。

- [ ] **Step 2: 构建并受控升级**

先同步到远端 `/opt/guofeng/vke-cluster/ray-platform`，以唯一 tag 构建 backend、frontend、workspace、source-materializer。只有 Task 1 输出 `IRSA prefix mount contract verified` 才允许执行：

```bash
helm upgrade --install ray-platform helm/ray-train-platform --namespace ray-train-platform --values helm/ray-train-platform/values-vke-test-release.yaml --set backend.image.tag="$IMAGE_TAG" --set frontend.image.tag="$IMAGE_TAG" --atomic --timeout 15m
```

IRSA 不通过时，可以发布 `dataSpaces.enabled=false` 的兼容 UI/API，但不得创建数据空间挂载的训练或调试环境。

- [ ] **Step 3: 双用户、调试、训练端到端验收**

1. 用户 A 创建个人目录并上传文件；用户 B 不能列举或下载。
2. 用户 A 的 GPU Worker 可读写 `/workspace` 和 `/mnt/storage/me`；对团队、公共、IDC 写入失败。
3. 用户 A 创建快照并提交单卡 RayJob；Head/Worker 读 `/mnt/data/input`，写 `/mnt/data/output` 与 `/mnt/data/checkpoints`。
4. Portal 在“我的训练结果”显示输出；A 可下载，B 不可见。
5. 检查 Pod YAML 和主容器环境变量：无 AK、SK、endpoint、bucket、SecurityToken。

- [ ] **Step 4: 记录发布和安全回滚**

记录镜像 digest、Helm revision、IRSA smoke 时间和验证任务 ID。只允许以下回滚：

```bash
helm -n ray-train-platform rollback ray-platform <previous-revision> --wait --timeout 10m
```

不得以删除 `ray-train/`、PV、PVC 或历史 RayJob 作为回滚手段。

## 完成标准

- 用户只选择逻辑目录，不输入 TOS URI、PVC、NFS 路径或凭据。
- TOS 是训练用户的持久化数据平台；PVC 仅为后台受控挂载适配器。
- 两用户的真实 FSX/IRSA 前缀隔离验证通过；未通过时平台拒绝挂载。
- 调试 GPU Worker 与训练 Head/Worker 都看到规定路径，团队/公共/IDC 强制只读。
- 训练产物写入个人 `runs/<job-id>/`，可在 Portal 浏览下载；历史任务继续可读。
- 长期 AK/SK 仅在 backend 控制面 Secret，不进入用户 Pod、前端、日志或审计。
