# User Personal TOS Space Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every platform-created local account receive a usable, private TOS directory automatically, while keeping object-space readiness separate from GPU mount readiness.

**Architecture:** The local-account handler receives a narrow initializer that derives the fixed TOS root solely from the newly created principal and materializes its marker objects before persisting the account. The data-space API reports object readiness and workload-mount readiness independently, allowing the Portal browser to work while CSI mounting stays safely disabled.

**Tech Stack:** Go, Gin, GORM repository, Volcengine TOS client, Vue 3, Element Plus, Node test runner.

---

### Task 1: Lock account provisioning behavior with tests

**Files:**
- Modify: `backend/api/local_users_test.go`
- Modify: `backend/api/local_auth_test.go`
- Modify: `backend/api/local_auth.go`

- [x] **Step 1: Write the failing account-creation test**

```go
func TestAdminCreatesAccountOnlyAfterPersonalDataSpaceIsInitialized(t *testing.T) {
    initializer := &fakePersonalDataInitializer{}
    handler := localAuthHandlerWithInitializer(newFakeLocalAuthStore(), initializer)
    response := postUser(localUserAdminRouter(handler, adminPrincipal()),
        `{"username":"alice","password":"engineer-pass","roles":["Engineer"]}`)
    if response.Code != http.StatusCreated || initializer.principal.Username != "alice" {
        t.Fatalf("personal data root must be prepared before user creation")
    }
}
```

- [x] **Step 2: Run the focused test and verify it fails**

Run: `cd backend && go test ./api -run TestAdminCreatesAccountOnlyAfterPersonalDataSpaceIsInitialized -count=1`

Expected: FAIL because the local-auth handler has no personal-data initializer.

- [x] **Step 3: Add a narrow `PersonalDataInitializer` dependency**

```go
type PersonalDataInitializer interface {
    EnsurePersonalDataSpace(context.Context, auth.Principal) error
}
```

Call it after tenant identity setup and before `CreateLocalUser`; map a failure to `503 PERSONAL_DATA_SPACE_INITIALIZATION_FAILED` and do not create the account.

- [x] **Step 4: Run the focused test and verify it passes**

Run: `cd backend && go test ./api -run TestAdminCreatesAccountOnlyAfterPersonalDataSpaceIsInitialized -count=1`

Expected: PASS.

- [x] **Step 5: Add and run the failure-path test**

```go
func TestAdminCannotCreateAccountWhenPersonalDataSpaceInitializationFails(t *testing.T) {
    initializer := &fakePersonalDataInitializer{err: objectstore.ErrUnavailable}
    // Expect 503, PERSONAL_DATA_SPACE_INITIALIZATION_FAILED, and no stored user.
}
```

Run: `cd backend && go test ./api -run 'TestAdmin.*PersonalDataSpace' -count=1`

Expected: PASS.

### Task 2: Reuse governed data-space initialization for newly created users

**Files:**
- Modify: `backend/api/data_spaces.go`
- Modify: `backend/main.go`
- Modify: `backend/main_source_artifacts_test.go`

- [x] **Step 1: Write the failing initialization test**

```go
func TestPersonalDataSpaceInitializerCreatesOnlyFixedTOSMarkers(t *testing.T) {
    initializer := NewPersonalDataSpaceInitializer(fakeMarkerStore{})
    err := initializer.EnsurePersonalDataSpace(context.Background(), auth.Principal{
        Subject: "user-a", TenantID: "tenant-a", AuthType: auth.AuthTypeLocal,
    })
    // Assert the only root is ray-train/tenants/tenant-a/users/user-a/.
}
```

- [x] **Step 2: Run the focused test and verify it fails**

Run: `cd backend && go test ./api -run TestPersonalDataSpaceInitializerCreatesOnlyFixedTOSMarkers -count=1`

Expected: FAIL because no reusable initializer exists.

- [x] **Step 3: Implement an adapter around `objectstore.PersonalDataDirectoryInitializer`**

Derive the root using `domain.PersonalDataSpacesFor`, select `workspace.RootPrefix`, and call `EnsurePersonalDataDirectories`. Never accept a user-provided bucket, prefix, PVC, or filesystem path.

- [x] **Step 4: Wire the adapter into `newLocalAuthComponents`**

Pass the existing TOS directory initializer from `main` into `LocalAuthOptions`. If no complete TOS client is configured, account provisioning returns the retryable 503 instead of silently provisioning an unusable account.

- [x] **Step 5: Run targeted Go tests**

Run: `cd backend && go test ./api ./objectstore -run 'Test(Admin.*PersonalDataSpace|PersonalDataSpaceInitializer|TOSStoreInitializes)' -count=1`

Expected: PASS.

### Task 3: Expose storage readiness without claiming GPU mount readiness

**Files:**
- Modify: `backend/api/data_spaces.go`
- Modify: `backend/api/data_spaces_test.go`
- Modify: `frontend/src/dataSpaceReadiness.js`
- Modify: `frontend/src/dataSpaceReadiness.test.js`
- Modify: `frontend/src/views/DataCache/index.vue`

- [x] **Step 1: Write a failing API response test**

```go
func TestDataSpacesShowObjectStorageReadyWhenTOSBrowserIsAvailableButMountIsDisabled(t *testing.T) {
    // Expect storageStatus=ready and mountStatus=not-configured.
}
```

- [x] **Step 2: Run the focused test and verify it fails**

Run: `cd backend && go test ./api -run TestDataSpacesShowObjectStorageReadyWhenTOSBrowserIsAvailableButMountIsDisabled -count=1`

Expected: FAIL because the response lacks `storageStatus`.

- [x] **Step 3: Add `storageStatus` to the public response**

Set TOS space `storageStatus` to `ready` only when the backend has the TOS browsing/data-store capability; set `not-configured` otherwise. Keep `mountStatus` unchanged and do not expose storage implementation fields.

- [x] **Step 4: Update frontend readiness and explanatory copy**

```js
if (space.storageStatus !== 'ready') {
  return { ready: false, message: '个人对象空间尚未就绪，请联系平台管理员处理。' }
}
```

Display “个人空间已就绪” independently from “GPU 挂载等待平台配置”, and keep training disabled until `mountStatus === 'ready'`.

- [x] **Step 5: Run backend and frontend focused tests**

Run: `cd backend && go test ./api -run 'TestDataSpaces.*(Storage|Logical)' -count=1 && cd ../frontend && npm test -- dataSpaceReadiness.test.js`

Expected: PASS.

### Task 4: Make account provisioning understandable in the Portal and guides

**Files:**
- Modify: `frontend/src/views/QuotaManage/index.vue`
- Modify: `docs/ADMIN_GUIDE.md`
- Modify: `docs/USER_GUIDE.md`

- [x] **Step 1: Add a provisioning promise to the account dialog**

```vue
<p>创建账号会同时初始化其个人文件、工作区、训练产物和快照目录；失败时账号不会创建。</p>
```

- [x] **Step 2: Document administrator and user flows**

Describe account creation, automatic private root provisioning, login, “我的数据” verification, and the distinct requirement to enable validated CSI mounts for GPU workload access.

- [x] **Step 3: Build the frontend**

Run: `cd frontend && npm run build`

Expected: PASS.

### Task 5: Build, deploy, and validate the real VKE flow

**Files:**
- Modify: `deploy/profiles/vke-test.yaml`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [x] **Step 1: Build immutable test images on the remote builder**

Run: `cd /opt/guofeng/vke-cluster/ray-platform && USE_BUILDX=false PUSH_IMAGE=true IMAGE_TAG=dev-20260813-personal-space-r1 BUILD_TARGETS=backend,frontend bash build-image.sh`

- [x] **Step 2: Upgrade the VKE test release and run built-in verification**

Run: `bash ops/platform/deploy.sh --profile deploy/profiles/vke-test.yaml --timeout 12m && bash ops/platform/verify.sh --profile deploy/profiles/vke-test.yaml`

- [x] **Step 3: Run the API end-to-end account flow using a unique disposable account**

Authenticate as the existing administrator, create a unique Engineer account, authenticate as it, list `workspace`, `my-files`, and `my-runs`, assert `storageStatus=ready`, `mountStatus=not-configured`, create and re-list one explicitly named directory in `my-files`, and assert no bucket, key, secret, prefix, PVC, or NFS source appears in public responses.

- [x] **Step 4: Remove only the disposable account's test objects and account after verification**

No account deletion API exists, so the clearly named account was disabled after verification to preserve an auditable record. Its one explicitly named TOS test directory was retained; direct database or object-store deletion was intentionally not used.

- [x] **Step 5: Capture final deployment evidence**

Record image references, Helm revision, Portal HTTP status, backend/frontend readiness, creation/login/list status codes, and marker-directory count. Do not print passwords, bearer tokens, access keys, Secret content, or raw object prefixes.
