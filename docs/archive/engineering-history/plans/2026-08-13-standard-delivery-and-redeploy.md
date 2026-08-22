# 标准交付与受保护重部署 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Ray Training Platform 改造成带测试/生产 Profile、受保护清理、单命令部署和验收流程的 Helm 交付项目，并用测试 Profile 从零重装当前 VKE 平台。

**Architecture:** Helm Chart 仅管理平台控制面、可选测试 PostgreSQL 和可选平台专属 Kueue 资源；KubeRay、Kueue controller、CSI、Ingress、Loki、Alloy、Keycloak 均由预检确认后复用。生产模式只接受外部 HA PostgreSQL，测试模式才创建单实例 PostgreSQL。

**Tech Stack:** Helm 3、Kubernetes、Bash、Go、Vue、GitHub Actions、KubeRay、Kueue、PostgreSQL、Loki。

---

### Task 1: 固化资源契约和环境 Profile

**Files:**
- Create: `deploy/profiles/test.yaml`
- Create: `deploy/profiles/production.yaml.example`
- Create: `deploy/secrets.env.example`
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `docs/ARCHITECTURE.md`

- [ ] **Step 1: 写入 Profile 渲染契约测试**

创建 `scripts/test-delivery-render.sh`，分别渲染测试与生产 Profile，并断言测试为单实例 PostgreSQL、生产为外部数据库与双副本 HPA/PDB：

```bash
helm template ray-platform ./helm/ray-train-platform -f deploy/profiles/test.yaml | grep -Fq 'kind: StatefulSet'
helm template ray-platform ./helm/ray-train-platform -f deploy/profiles/production.yaml.example | grep -Fq 'kind: HorizontalPodAutoscaler'
```

- [ ] **Step 2: 运行渲染测试并确认在模板补齐前失败**

Run: `bash scripts/test-delivery-render.sh`
Expected: 因 HPA/测试 PostgreSQL 模板尚不存在失败。

- [ ] **Step 3: 定义唯一、无敏感值的 Profile**

测试 Profile 固定为单副本、Chart PostgreSQL、ClusterIP+可选 Ingress、`dataSpaces.enabled=false`；生产示例固定为外部 HA PostgreSQL、双副本、HPA/PDB、Ingress、`dataSpaces.enabled=false`。

- [ ] **Step 4: 重新运行渲染测试**

Run: `bash scripts/test-delivery-render.sh`
Expected: 两份 Profile 均通过 Helm lint/template 契约。

### Task 2: 扩展 Helm 为 HA 控制面与测试数据库

**Files:**
- Create: `helm/ray-train-platform/templates/postgres.yaml`
- Create: `helm/ray-train-platform/templates/hpa.yaml`
- Create: `helm/ray-train-platform/templates/kueue-resources.yaml`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `helm/ray-train-platform/templates/frontend-deployment.yaml`
- Modify: `helm/ray-train-platform/templates/pdb.yaml`
- Modify: `helm/ray-train-platform/templates/rbac.yaml`
- Modify: `helm/ray-train-platform/Chart.yaml`

- [ ] **Step 1: 为所有 Chart 对象写入统一平台标签的渲染断言**

在 `scripts/test-delivery-render.sh` 断言 Deployment、PostgreSQL、HPA、PDB 和受管 Kueue 对象都含：

```yaml
app.kubernetes.io/part-of: ray-train-platform
app.kubernetes.io/managed-by: Helm
```

- [ ] **Step 2: 运行测试并确认失败**

Run: `bash scripts/test-delivery-render.sh`
Expected: 现有模板缺少统一标签、HPA 和测试 PostgreSQL。

- [ ] **Step 3: 实现可选 PostgreSQL 与 HA workload**

实现 `postgres.mode: standalone|external`：standalone 仅用于 test，创建带标记的 StatefulSet/PVC；external 只引用 Secret。数据库迁移由 API 在 PostgreSQL advisory lock 下启动执行，删除会先于 Chart 内置 PostgreSQL 运行的 pre-install hook。给 API/Portal 添加可选 HPA、Pod 反亲和和拓扑分布，生产 PDB 最小可用数为 1。

- [ ] **Step 4: 实现可选平台专属 Kueue 资源**

仅当 `kueue.manageResources=true` 时创建带平台标签的 ResourceFlavor/ClusterQueue；保留对预建 Kueue 的引用能力。

- [ ] **Step 5: 运行 Chart 和 Go 回归**

Run: `bash scripts/test-delivery-render.sh && (cd backend && go test ./... && go vet ./...)`
Expected: 全部通过。

### Task 3: 实现安全清理、预检、密钥和部署命令

**Files:**
- Create: `ops/platform/preflight.sh`
- Create: `ops/platform/reset.sh`
- Create: `ops/platform/bootstrap-secrets.sh`
- Create: `ops/platform/deploy.sh`
- Create: `ops/platform/verify.sh`
- Create: `ops/platform/lib/common.sh`
- Test: `ops/platform/test/reset-preview-test.sh`

- [ ] **Step 1: 编写 reset 预览测试**

以 mock `kubectl` 输出验证：脚本仅列出标签为平台的 tenant namespace/PV/PVC/Kueue 资源，且永不调用 `delete`，除非同时含 `--execute --confirm-reset-ray-platform`。

- [ ] **Step 2: 运行测试并确认失败**

Run: `bash ops/platform/test/reset-preview-test.sh`
Expected: `reset.sh` 尚不存在而失败。

- [ ] **Step 3: 实现所有运维脚本**

`preflight` 仅检查共享依赖；`reset` 默认预览、严格标签/名称白名单、删除前检查外部引用；`bootstrap-secrets` 仅从环境文件读取并通过 stdin 创建 Secret；`deploy` 必须先通过 preflight/render 后 `helm upgrade --install --atomic --wait`；`verify` 检查 release、迁移、API、Ingress、HPA/PDB/Kueue。

- [ ] **Step 4: 运行脚本单元测试与 shell 静态检查**

Run: `bash ops/platform/test/reset-preview-test.sh && bash -n ops/platform/*.sh ops/platform/lib/*.sh`
Expected: 通过；任何没有确认短语的执行均为只读预览。

### Task 4: 标准化测试、CI 与文档

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/DEPLOY_FROM_SCRATCH.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/CLUSTER_DEPLOYMENT_GUIDE.md`
- Modify: `README.md`
- Modify: `docs/ADMIN_GUIDE.md`

- [ ] **Step 1: 写入 CI 所需的本地验证命令**

CI 必须按顺序执行：

```bash
(cd backend && go test ./... && go vet ./...)
(cd frontend && npm ci && npm run build)
bash scripts/test-delivery-render.sh
bash ops/platform/test/reset-preview-test.sh
```

- [ ] **Step 2: 更新 CI 与交付文档**

文档只保留一条从零流程：准备 profile/Secret → `preflight` → 可选 `reset --preview`/确认执行 → `deploy` → `verify`。明确 TOS/Loki 桶不属于清理目标、生产数据库的 HA 前置条件和 IRSA 门禁。

- [ ] **Step 3: 执行完整本地验证**

Run: `bash scripts/test-delivery-render.sh && bash ops/platform/test/reset-preview-test.sh && (cd backend && go test ./... && go vet ./...) && (cd frontend && npm run build)`
Expected: 全部通过。

### Task 5: 当前 VKE 平台重置与测试重装

**Files:**
- Create: `deploy/profiles/vke-test.yaml`（仅在无敏感值时提交；否则在远端由 example 生成）
- Create: `deploy/release-records/vke-test-<date>.md`

- [ ] **Step 1: 对当前集群运行只读预检与清理预览**

Run: `bash ops/platform/preflight.sh --profile deploy/profiles/vke-test.yaml && bash ops/platform/reset.sh --profile deploy/profiles/vke-test.yaml`

Expected: 清单仅包含 `ray-train-platform`、受管 tenant namespace、平台历史 PV/PVC/Queue；不出现 KubeRay/Kueue controller/CSI/Loki/Alloy/TOS。

- [ ] **Step 2: 备份平台 PostgreSQL 元数据（临时文件，不提交）**

Run: `kubectl -n ray-train-platform exec postgres-0 -- pg_dump -U platform -d platform --clean --if-exists > /secure/backup/ray-platform-before-reset.sql`

Expected: 备份位于集群外或构建机受保护路径；TOS 不导出、不删除。

- [ ] **Step 3: 执行确认清理**

Run: `bash ops/platform/reset.sh --profile deploy/profiles/vke-test.yaml --execute --confirm-reset-ray-platform`

Expected: 仅删除平台资源，保留共享组件和两个 TOS 桶数据。

- [ ] **Step 4: 从零部署测试 Profile 并验收**

Run: `bash ops/platform/bootstrap-secrets.sh --profile deploy/profiles/vke-test.yaml --env-file /secure/ray-platform-test.env && bash ops/platform/deploy.sh --profile deploy/profiles/vke-test.yaml && bash ops/platform/verify.sh --profile deploy/profiles/vke-test.yaml`

Expected: Chart、数据库迁移、Portal/API、Ingress、单卡 RayJob、Loki 查询都通过；`dataSpaces.enabled` 保持关闭，直到独立 IRSA 验收成功。
