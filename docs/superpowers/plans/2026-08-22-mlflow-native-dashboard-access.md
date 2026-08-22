# MLflow Native Dashboard Full Access Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让所有已登录平台用户通过 `https://raytrain.wellspiking.ai/mlflow/` 使用完整 MLflow 原生界面，同时保持 MLflow Service 为 ClusterIP、平台统一登录、跨副本安全会话和可审计操作。

**Architecture:** 平台后端签发数据库支持的单次访问票据，浏览器把票据交换为 `/mlflow/` 范围内的 HttpOnly 会话 Cookie；后端随后将 MLflow UI、API、模型注册和 Artifact 请求透明代理到集群内 Tracking Server。MLflow 使用 `--static-prefix=/mlflow`，前端 Nginx 只把该子路径转给后端；训练 Pod 继续使用独立的写入网关，不获得 MLflow 管理面或对象存储凭据。

**Tech Stack:** Go 1.24、Gin、GORM/PostgreSQL、Vue 3、Element Plus、Nginx、Helm、Kubernetes NetworkPolicy、MLflow 3.14、Node test runner。

---

## File map

- `backend/domain/mlflow_dashboard_access.go`: MLflow 会话声明、签名和过期校验。
- `backend/domain/mlflow_dashboard_access_test.go`: HMAC 绑定、过期和弱密钥测试。
- `backend/db/migrations/0019_mlflow_dashboard_tickets.up.sql`: HA 后端共享的单次票据表。
- `backend/repositories/mlflow_dashboard.go`: 创建、消费单次票据及写固定字段审计记录。
- `backend/repositories/mlflow_dashboard_test.go`: 单次消费、过期、主体绑定和审计脱敏测试。
- `backend/api/mlflow_dashboard.go`: 访问 URL API、Cookie 交换、同源检查和完整反向代理。
- `backend/api/mlflow_dashboard_test.go`: 未登录、票据、Cookie、CRUD 方法、子路径和日志边界测试。
- `backend/api/jobs.go`: 向主 Handler 注入票据存储、审计器和 MLflow 代理配置。
- `backend/config/config.go`: 读取并校验 MLflow 公共 Origin、代理路径和会话时长。
- `backend/config/mlflow_config_test.go`: 新配置的生产约束测试。
- `backend/main.go`: 把公开代理路由和受保护票据 API接入现有认证链。
- `frontend/src/api/mlflowDashboard.js`: 请求短期访问 URL。
- `frontend/src/mlflowDashboard.test.js`: API 路径和页面按钮契约。
- `frontend/src/views/Experiments/index.vue`: “打开 MLflow 管理界面”入口和全局变更风险提示。
- `frontend/nginx.conf`: `/mlflow/` 到后端的长连接、大请求体代理。
- `frontend/nginx-config.test.js`: MLflow 子路径、Host、Cookie 和上传限制契约。
- `ops/mlflow/values-vke.yaml`: 启用 `server.staticPrefix: /mlflow` 并允许后端 Host。
- `ops/mlflow/contract-test.sh`: 固定 ClusterIP、静态前缀和全功能 Tracking Server 合同。
- `ops/mlflow/verify.sh`: 集群内健康、前缀和无 NodePort 验收。
- `helm/ray-train-platform/values.yaml`: MLflow Dashboard 代理开关、公共 Origin 和会话时长。
- `helm/ray-train-platform/templates/backend-deployment.yaml`: 将配置注入后端。
- `ops/platform/test/mlflow-dashboard-template-test.sh`: Helm 渲染合同。
- `docs/USER_GUIDE.md`, `docs/BUILD_AND_DEPLOY.md`, `ops/mlflow/README.md`, `README.md`, `docs/ARCHITECTURE.md`: 用户入口、权限、网络和运维说明。
- `docs/BEVFUSION_END_TO_END_GUIDE.md`, `docs/BEVFUSION_RUNBOOK.md`, `docs/SUBMIT_GUIDE.md`: 修正资源规格、安装前提和 `.rayignore` 语义。
- `scripts/test_docs.py`: 锁定公开文档的一致性。

### Task 1: Signed browser session domain model

**Files:**
- Create: `backend/domain/mlflow_dashboard_access.go`
- Create: `backend/domain/mlflow_dashboard_access_test.go`

- [ ] **Step 1: Write failing token tests**

```go
func TestMLflowDashboardSessionBindsTenantSubjectAndNonce(t *testing.T) {
    now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
    token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, time.Hour)
    if err != nil { t.Fatal(err) }
    if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), now.Add(time.Minute)); err != nil { t.Fatal(err) }
    if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-b", testPepper(), now); err == nil { t.Fatal("subject mismatch accepted") }
}

func TestMLflowDashboardSessionExpiresAndRejectsWeakKey(t *testing.T) {
    now := time.Now().UTC()
    if _, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", []byte("short"), now, time.Hour); err == nil { t.Fatal("weak key accepted") }
    token, err := IssueMLflowDashboardSession("tenant-a", "user-a", "nonce-a", testPepper(), now, time.Second)
    if err != nil { t.Fatal(err) }
    if err := VerifyMLflowDashboardSession(token, "tenant-a", "user-a", testPepper(), now.Add(2*time.Second)); err == nil { t.Fatal("expired token accepted") }
}
```

- [ ] **Step 2: Run the focused test and confirm RED**

Run: `cd backend && go test ./domain -run MLflowDashboardSession -count=1`

Expected: build failure because the issue and verify functions do not exist.

- [ ] **Step 3: Implement the signed session**

```go
const (
    MLflowDashboardTicketTTL  = 2 * time.Minute
    MLflowDashboardSessionTTL = 8 * time.Hour
)

func IssueMLflowDashboardSession(tenantID, subject, nonce string, key []byte, now time.Time, ttl time.Duration) (string, error) {
    if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(subject) == "" || strings.TrimSpace(nonce) == "" { return "", fmt.Errorf("tenant, subject and nonce are required") }
    if err := validatePATPepper(key); err != nil { return "", err }
    if ttl <= 0 { ttl = MLflowDashboardSessionTTL }
    expiry := strconv.FormatInt(now.UTC().Add(ttl).Unix(), 10)
    payload := base64.RawURLEncoding.EncodeToString([]byte(tenantID + "\x00" + subject + "\x00" + nonce + "\x00" + expiry))
    mac := hmac.New(sha256.New, key)
    _, _ = mac.Write([]byte("mlflow-dashboard-session\x00" + payload))
    return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}
```

`VerifyMLflowDashboardSession` must decode the payload, compare the HMAC with `hmac.Equal`, verify the supplied tenant and subject, and reject `now.Unix() > expiry`.

- [ ] **Step 4: Run the domain tests and confirm GREEN**

Run: `cd backend && go test ./domain -run MLflowDashboardSession -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the domain unit**

```bash
git add backend/domain/mlflow_dashboard_access.go backend/domain/mlflow_dashboard_access_test.go
git commit -m "feat: add MLflow dashboard session tokens"
```

### Task 2: HA-safe single-use tickets and audit records

**Files:**
- Create: `backend/db/migrations/0019_mlflow_dashboard_tickets.up.sql`
- Create: `backend/repositories/mlflow_dashboard.go`
- Create: `backend/repositories/mlflow_dashboard_test.go`
- Modify: `backend/repositories/schema_mapping_test.go`

- [ ] **Step 1: Write failing repository tests**

Create tests that issue a ticket, consume it once, reject a second consume, reject an expired ticket, and assert the stored audit payload contains only `method`, `path`, `status`, `duration_ms`, `actor_username`, `auth_type`, and `outcome`.

```go
ticket := MLflowDashboardTicketRecord{TokenHash: strings.Repeat("a", 64), TenantID: "tenant-a", UserID: "user-a", ExpiresAt: now.Add(time.Minute)}
if err := repo.CreateMLflowDashboardTicket(ctx, ticket); err != nil { t.Fatal(err) }
got, err := repo.ConsumeMLflowDashboardTicket(ctx, ticket.TokenHash, now)
if err != nil || got.UserID != "user-a" { t.Fatalf("consume = %#v, %v", got, err) }
if _, err := repo.ConsumeMLflowDashboardTicket(ctx, ticket.TokenHash, now); !errors.Is(err, ErrMLflowDashboardTicketInvalid) { t.Fatalf("second consume = %v", err) }
```

- [ ] **Step 2: Run repository tests and confirm RED**

Run: `cd backend && go test ./repositories -run MLflowDashboard -count=1`

Expected: build failure for missing repository methods and record.

- [ ] **Step 3: Add migration and repository implementation**

```sql
CREATE TABLE IF NOT EXISTS mlflow_dashboard_tickets (
  token_hash TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_mlflow_dashboard_tickets_expiry ON mlflow_dashboard_tickets(expires_at);
```

Consume with one atomic update so two backend replicas cannot accept the same ticket:

```go
result := r.db.WithContext(ctx).Model(&MLflowDashboardTicketRecord{}).
    Where("token_hash = ? AND consumed_at IS NULL AND expires_at >= ?", tokenHash, now.UTC()).
    Update("consumed_at", now.UTC())
if result.Error != nil { return MLflowDashboardTicketRecord{}, result.Error }
if result.RowsAffected != 1 { return MLflowDashboardTicketRecord{}, ErrMLflowDashboardTicketInvalid }
```

Add `CreateMLflowAuditLog` using the existing `audit_logs` table with `resource_type=mlflow`, a normalized path without query text, and a fixed JSON map; never accept headers or body as audit input.

- [ ] **Step 4: Run repository and migration checks**

Run: `cd backend && go test ./repositories ./db -count=1`

Expected: PASS, including schema and migration target checks.

- [ ] **Step 5: Commit persistence changes**

```bash
git add backend/db/migrations/0019_mlflow_dashboard_tickets.up.sql backend/repositories/mlflow_dashboard.go backend/repositories/mlflow_dashboard_test.go backend/repositories/schema_mapping_test.go
git commit -m "feat: persist single-use MLflow access tickets"
```

### Task 3: Backend access API and full reverse proxy

**Files:**
- Create: `backend/api/mlflow_dashboard.go`
- Create: `backend/api/mlflow_dashboard_test.go`
- Modify: `backend/api/jobs.go`
- Modify: `backend/main.go`
- Modify: `backend/config/config.go`
- Modify: `backend/config/mlflow_config_test.go`

- [ ] **Step 1: Write failing API and configuration tests**

Cover these exact cases:

```go
func TestMLflowDashboardRequiresInteractiveAuthentication(t *testing.T) {}
func TestMLflowDashboardTicketIsSingleUseAndBecomesStrictCookie(t *testing.T) {}
func TestMLflowDashboardForwardsGetPostPutPatchDelete(t *testing.T) {}
func TestMLflowDashboardRejectsCrossOriginMutation(t *testing.T) {}
func TestMLflowDashboardRewritesLocationAndAbsoluteAPIPaths(t *testing.T) {}
func TestMLflowDashboardAuditOmitsQueryAuthorizationAndBody(t *testing.T) {}
```

Config tests must require an HTTPS `MLFLOW_PUBLIC_ORIGIN` with no path/query/credentials in production and an in-cluster `MLFLOW_TRACKING_URL`.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `cd backend && go test ./api ./config -run 'MLflowDashboard|MLflowConfig' -count=1`

Expected: build or assertion failure because routes and options are absent.

- [ ] **Step 3: Add handler dependencies and configuration**

```go
type MLflowDashboardStore interface {
    CreateMLflowDashboardTicket(context.Context, repositories.MLflowDashboardTicketRecord) error
    ConsumeMLflowDashboardTicket(context.Context, string, time.Time) (repositories.MLflowDashboardTicketRecord, error)
    CreateMLflowAuditLog(context.Context, repositories.MLflowAuditEvent) error
}

type Options struct {
    // existing fields remain unchanged
    MLflowDashboardStore  MLflowDashboardStore
    MLflowTrackingURL     string
    MLflowPublicOrigin    string
    MLflowDashboardPepper []byte
    MLflowSessionTTL      time.Duration
}
```

Config keys:

```text
MLFLOW_DASHBOARD_ENABLED=true
MLFLOW_PUBLIC_ORIGIN=https://raytrain.wellspiking.ai
MLFLOW_DASHBOARD_SESSION_HOURS=8
```

The feature requires `MLFLOW_ENABLED=true`, a 32-byte `PAT_PEPPER`, and a valid internal tracking URL.

- [ ] **Step 4: Implement ticket issuing and exchange**

Issue 32 random bytes, store only `sha256(ticket)` in PostgreSQL, return `/mlflow/?access_token=<base64url-ticket>`, atomically consume the ticket, then set one cookie:

```go
http.SetCookie(c.Writer, &http.Cookie{
    Name: "ray_mlflow_dashboard", Value: session, Path: "/mlflow/",
    MaxAge: int(h.mlflowSessionTTL.Seconds()), HttpOnly: true,
    Secure: true, SameSite: http.SameSiteStrictMode,
})
c.Header("Cache-Control", "no-store")
c.Redirect(http.StatusFound, "/mlflow/")
```

Do not place tenant or subject in the query string.

- [ ] **Step 5: Implement transparent proxy and mutation guard**

Mount `router.Any("/mlflow/*path", handler.ProxyMLflowDashboard)` outside bearer middleware and mount `POST /api/v1/mlflow-dashboard-access` inside the interactive group. The reverse proxy must:

```go
proxy.Director = func(req *http.Request) {
    originalDirector(req)
    req.URL.Path = "/mlflow/" + strings.TrimPrefix(c.Param("path"), "/")
    req.URL.RawPath = ""
    req.Host = target.Host
    req.Header.Del("Authorization")
    req.Header.Del("Cookie")
    req.Header.Del("Accept-Encoding")
}
```

For `POST`, `PUT`, `PATCH`, and `DELETE`, require `Origin == MLFLOW_PUBLIC_ORIGIN`; accept same-origin `Referer` only when a browser omits Origin. Forward request and response streaming without buffering Artifact bodies. Rewrite `Location: /mlflow/...` to the same public path and patch absolute `"/ajax-api/` and `"/api/` references in uncompressed HTML/JavaScript only.

- [ ] **Step 6: Add fixed-field audit after every proxied request**

Wrap the response writer to capture status and duration, then call the repository with method, normalized path, status, request ID and principal. The audit event must never include query values, cookies, authorization, request body, response body or Artifact name content beyond the normalized API path.

- [ ] **Step 7: Run backend tests and race checks**

Run: `cd backend && go test ./domain ./repositories ./api ./config -count=1`

Run: `cd backend && go test -race ./domain ./repositories ./api -count=1`

Expected: PASS.

- [ ] **Step 8: Commit backend proxy**

```bash
git add backend/api/mlflow_dashboard.go backend/api/mlflow_dashboard_test.go backend/api/jobs.go backend/main.go backend/config/config.go backend/config/mlflow_config_test.go
git commit -m "feat: proxy authenticated MLflow dashboard"
```

### Task 4: Portal button and same-domain Nginx route

**Files:**
- Create: `frontend/src/api/mlflowDashboard.js`
- Create: `frontend/src/mlflowDashboard.test.js`
- Modify: `frontend/src/views/Experiments/index.vue`
- Modify: `frontend/nginx.conf`
- Modify: `frontend/nginx-config.test.js`
- Modify: `frontend/src/dataDownloadPolicy.test.js`

- [ ] **Step 1: Write failing frontend contracts**

```js
test('requests an authenticated MLflow access URL', async () => {
  const calls = []
  const result = await requestMLflowDashboardAccess(async (path, options) => {
    calls.push([path, options])
    return { data: { url: '/mlflow/?access_token=ticket' } }
  })
  assert.deepEqual(calls, [['/api/v1/mlflow-dashboard-access', { method: 'POST' }]])
  assert.equal(result, '/mlflow/?access_token=ticket')
})
```

The page source test must require the button text `打开 MLflow 管理界面`, `window.open`, and the warning that all platform users share the native view. The data download policy test must continue prohibiting training-data download buttons while explicitly excluding the separate MLflow Artifact surface from that assertion.

- [ ] **Step 2: Run frontend tests and confirm RED**

Run: `cd frontend && npm test`

Expected: failure because the MLflow access helper and button do not exist.

- [ ] **Step 3: Add API helper and page action**

```js
export async function requestMLflowDashboardAccess(request = apiRequest) {
  const payload = await request('/api/v1/mlflow-dashboard-access', { method: 'POST' })
  const url = payload?.data?.url ?? payload?.url
  if (!url || !url.startsWith('/mlflow/?access_token=')) throw new Error('平台没有返回有效的 MLflow 访问地址')
  return url
}
```

Open a blank tab synchronously on click to avoid popup blocking, request the URL, set `tab.opener = null`, navigate it, and close the tab with an Element Plus error message if the API fails.

- [ ] **Step 4: Add `/mlflow/` Nginx proxy**

```nginx
location /mlflow/ {
    proxy_pass http://ray-train-backend:8080/mlflow/;
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    proxy_request_buffering off;
    proxy_buffering off;
    client_max_body_size 2048m;
}
```

- [ ] **Step 5: Run frontend tests and build**

Run: `cd frontend && npm test && npm run build`

Expected: all tests pass and Vite build exits 0.

- [ ] **Step 6: Commit Portal changes**

```bash
git add frontend/src/api/mlflowDashboard.js frontend/src/mlflowDashboard.test.js frontend/src/views/Experiments/index.vue frontend/nginx.conf frontend/nginx-config.test.js frontend/src/dataDownloadPolicy.test.js
git commit -m "feat: expose MLflow management dashboard in portal"
```

### Task 5: Helm and MLflow subpath delivery

**Files:**
- Modify: `ops/mlflow/values-vke.yaml`
- Modify: `ops/mlflow/contract-test.sh`
- Modify: `ops/mlflow/verify.sh`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Create: `ops/platform/test/mlflow-dashboard-template-test.sh`

- [ ] **Step 1: Write failing shell contracts**

Require `server.staticPrefix: /mlflow`, ClusterIP, disabled direct Ingress, backend dashboard settings, and no NodePort. Rendered backend env must contain:

```text
MLFLOW_DASHBOARD_ENABLED
MLFLOW_PUBLIC_ORIGIN
MLFLOW_DASHBOARD_SESSION_HOURS
```

- [ ] **Step 2: Run contracts and confirm RED**

Run: `bash ops/mlflow/contract-test.sh`

Run on the build host with Helm available: `bash ops/platform/test/mlflow-dashboard-template-test.sh`

Expected: failure for missing static prefix and backend env.

- [ ] **Step 3: Add values and backend environment**

```yaml
backend:
  mlflow:
    enabled: false
    trackingURL: http://mlflow.mlflow-system.svc.cluster.local:5000
    ingestURL: http://mlflow-ingest.mlflow-system.svc.cluster.local:8080
    experimentPrefix: raytrain
    dashboardEnabled: false
    publicOrigin: ""
    dashboardSessionHours: 8
```

In `ops/mlflow/values-vke.yaml` add:

```yaml
server:
  staticPrefix: /mlflow
```

Keep `service.type=ClusterIP` and `ingress.enabled=false`.

- [ ] **Step 4: Extend live verification**

Verify `/mlflow/health` through the Kubernetes service proxy, verify all Services in `mlflow-system` are not NodePort/LoadBalancer, and verify the Tracking Deployment has `--static-prefix=/mlflow`.

- [ ] **Step 5: Run shell and Helm contracts**

Run: `bash ops/mlflow/contract-test.sh`

Run on build host: `bash ops/platform/test/mlflow-dashboard-template-test.sh`

Expected: PASS.

- [ ] **Step 6: Commit delivery configuration**

```bash
git add ops/mlflow/values-vke.yaml ops/mlflow/contract-test.sh ops/mlflow/verify.sh helm/ray-train-platform/values.yaml helm/ray-train-platform/templates/backend-deployment.yaml ops/platform/test/mlflow-dashboard-template-test.sh
git commit -m "feat: deliver MLflow under authenticated subpath"
```

### Task 6: Correct public documentation and contracts

**Files:**
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/USER_GUIDE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `ops/mlflow/README.md`
- Modify: `docs/BEVFUSION_END_TO_END_GUIDE.md`
- Modify: `docs/BEVFUSION_RUNBOOK.md`
- Modify: `docs/SUBMIT_GUIDE.md`
- Modify: `scripts/test_docs.py`

- [ ] **Step 1: Add failing documentation assertions**

Add assertions for:

```python
self.assertIn("打开 MLflow 管理界面", user_guide)
self.assertIn("/mlflow/", build_guide)
self.assertNotIn("根锚定 `/` 无效", end_to_end)
self.assertIn("64 CPU", end_to_end)
self.assertIn("256GiB", end_to_end)
self.assertIn("32 CPU / 128GiB 仅用于 smoke", runbook)
```

Also require the end-to-end guide to list account, GitLab access, approved image and 16-GPU team quota before clone instructions, and require a platform-page installation path for non-Linux users.

- [ ] **Step 2: Run documentation tests and confirm RED**

Run: `python3 scripts/test_docs.py`

Expected: assertion failures for stale MLflow and resource text.

- [ ] **Step 3: Update user and operator documents**

Document these exact boundaries:

- The platform experiment list remains identity-filtered.
- The native MLflow view is global and all authenticated users can create, modify, delete, register models and transfer MLflow Artifacts.
- MLflow Artifact access does not grant `/mnt/storage/public` training-data download or expose TOS credentials.
- MLflow stays ClusterIP and is reached only through `/mlflow/`.
- Daily flow is edit → `spk-rayjob submit --watch` → `logs -f` → Portal experiment view → optional native MLflow → resume or submit again.
- Production 2×8 begins at 64 CPU / 256GiB per Worker; 32 CPU / 128GiB is smoke only.
- `.rayignore` supports root anchoring; do not use unanchored `datasets/` when source contains `mmdet3d/datasets/`.

- [ ] **Step 4: Run documentation tests**

Run: `python3 scripts/test_docs.py`

Expected: PASS.

- [ ] **Step 5: Commit documentation corrections**

```bash
git add README.md docs/ARCHITECTURE.md docs/USER_GUIDE.md docs/BUILD_AND_DEPLOY.md ops/mlflow/README.md docs/BEVFUSION_END_TO_END_GUIDE.md docs/BEVFUSION_RUNBOOK.md docs/SUBMIT_GUIDE.md scripts/test_docs.py
git commit -m "docs: publish MLflow dashboard and corrected training workflow"
```

### Task 7: Full local verification, deployment and production acceptance

**Files:**
- Modify only if a verification defect is found in a file owned by Tasks 1–6.

- [ ] **Step 1: Run the full local verification suite**

Run:

```bash
cd backend && gofmt -w domain/mlflow_dashboard_access.go domain/mlflow_dashboard_access_test.go repositories/mlflow_dashboard.go repositories/mlflow_dashboard_test.go api/mlflow_dashboard.go api/mlflow_dashboard_test.go config/config.go config/mlflow_config_test.go main.go api/jobs.go
cd backend && go test ./... -count=1
cd frontend && npm test && npm run build
bash ops/mlflow/contract-test.sh
python3 scripts/test_docs.py
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 2: Security review before building**

Search tracked diffs for PATs, GitLab tokens, TOS keys, private keys, passwords and Authorization headers. Confirm the proxy strips browser `Authorization` and `Cookie`, audit records omit query/body/header contents, mutation routes enforce same origin, and no Service changes to NodePort.

- [ ] **Step 3: Synchronize source to the build host**

Use the existing scoped synchronization process to update `/opt/guofeng/vke-cluster/ray-platform/` from this checkout without copying `.git`, local credentials or build output. Verify checksums for the files changed in Tasks 1–6 on both sides.

- [ ] **Step 4: Build and push immutable images**

Build backend and frontend with a release tag derived from the commit, push to `harbor.wellspiking.ai/guofeng.su`, inspect pushed digests, and place only the immutable digests in the production values override.

- [ ] **Step 5: Upgrade MLflow before the platform**

Run `bash ops/mlflow/deploy.sh`, then `bash ops/mlflow/verify.sh`. Confirm two Tracking replicas, two ingest replicas, independent PostgreSQL, static prefix, NetworkPolicy and ClusterIP-only Services.

- [ ] **Step 6: Upgrade the platform atomically**

Set production overrides:

```yaml
backend:
  mlflow:
    enabled: true
    dashboardEnabled: true
    publicOrigin: https://raytrain.wellspiking.ai
    dashboardSessionHours: 8
```

Run the standard `ops/platform/deploy.sh` flow and `ops/platform/verify.sh`; do not patch Deployments with imperative `kubectl set env` or expose a NodePort.

- [ ] **Step 7: Browser and API acceptance**

As a normal authenticated user:

1. Open Experiment Center and click `打开 MLflow 管理界面`.
2. Confirm redirect removes `access_token` and sets only a `/mlflow/` HttpOnly cookie.
3. Create an experiment named `acceptance-mlflow-<timestamp>`.
4. Create and update a Run, compare it with an existing finished Run, and register a test model.
5. Upload, list and download a small text Artifact.
6. Delete and restore only the acceptance experiment/run.
7. Confirm no TOS AK/SK, database URL or internal Service address appears in browser responses.

- [ ] **Step 8: Negative and HA acceptance**

Verify an unauthenticated `/mlflow/` request is 401, a consumed or expired ticket is rejected, a cross-origin mutation is 403, and refreshing the page still works after deleting one backend Pod. Verify platform experiment list, task details, MLflow training ingest and an existing finished Run are unchanged.

- [ ] **Step 9: Record immutable release evidence**

Record commit, image digests, Helm revisions and acceptance object names in the existing release documentation; delete only test-created MLflow objects. Commit any verification-only correction with a conventional message and tag the release only after all checks pass.

## Self-review

- Spec coverage: authenticated entry, all roles, global full access, Artifact operations, same-domain proxy, ClusterIP-only delivery, static prefix, audit, HA ticket consumption, docs and rollback all map to Tasks 1–7.
- Placeholder scan: the plan contains no deferred implementation markers; every code-changing task names exact files, commands and expected outcomes.
- Type consistency: `MLflowDashboardTicketRecord`, `MLflowDashboardStore`, session issuer/verifier, configuration names and `/mlflow/` path use the same names across persistence, API, Helm and tests.

