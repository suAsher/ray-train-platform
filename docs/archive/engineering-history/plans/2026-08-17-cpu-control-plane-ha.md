# CPU 控制面高可用与 VCI 退役 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将平台控制面和可观测性迁移到三台 CPU 节点，完成 VCI 下线前的全部验证，并将 Portal 品牌改为 Ray Training Platform。

**Architecture:** 三台 CPU 节点以 `platform.wellspiking.ai/pool=control-plane` 组成单可用区控制面池。Portal/API 采用两副本硬分散，Loki 保持三副本、Alloy 保持 API 发现式两副本集群；PostgreSQL 维持单实例并显式调度到 CPU 池。训练与调试 Ray Pod 继续只匹配 GPU 标签，独立于本次控制面滚动更新。

**Tech Stack:** Helm 3、Kubernetes/VKE、KubeRay、Kueue、Grafana Loki 3.7、Grafana Alloy 1.18、Vue 3/Vite、PostgreSQL 16。

---

### Task 1: 为 Portal 品牌变更建立回归测试

**Files:**
- Create: `frontend/src/brandIdentity.test.js`
- Modify: `frontend/src/views/Login/index.vue`
- Modify: `frontend/src/layout/Layout.vue`

- [ ] **Step 1: 写入失败测试**

```js
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Portal shows the approved product name without a subtitle', async () => {
  const files = await Promise.all([
    readFile(new URL('./views/Login/index.vue', import.meta.url), 'utf8'),
    readFile(new URL('./layout/Layout.vue', import.meta.url), 'utf8')
  ])
  for (const source of files) {
    assert.match(source, />Ray Training Platform</)
    assert.doesNotMatch(source, /Ray AI Platform|分布式训练控制台|多租户分布式训练控制台/)
  }
})
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd frontend && node --test src/brandIdentity.test.js`

Expected: FAIL，因为页面仍包含旧名称和副标题。

- [ ] **Step 3: 最小化实现品牌调整**

将两个模板中的标题替换为：

```vue
<!-- frontend/src/views/Login/index.vue -->
<h1 class="text-xl font-bold text-white tracking-wide">Ray Training Platform</h1>

<!-- frontend/src/layout/Layout.vue -->
<h1 class="font-bold text-base text-white tracking-wide">Ray Training Platform</h1>
```

删除紧邻标题的副标题 `<p>` 元素，不改变图标、对齐、登录逻辑或导航。

- [ ] **Step 4: 运行测试确认通过**

Run: `cd frontend && node --test src/brandIdentity.test.js && npm test && npm run build`

Expected: 新增测试、全部前端测试和生产构建均成功。

### Task 2: 将平台 Chart 的高可用调度策略参数化

**Files:**
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `helm/ray-train-platform/templates/frontend-deployment.yaml`
- Modify: `helm/ray-train-platform/templates/postgres.yaml`
- Modify: `scripts/test-delivery-render.sh`

- [ ] **Step 1: 为 CPU Profile 写入失败的渲染契约**

在 `scripts/test-delivery-render.sh` 增加 `deploy/profiles/vke-cpu-ha.yaml` 的渲染，并断言：

```bash
require "$cpu_ha_rendered" 'platform.wellspiking.ai/pool: control-plane'
require "$cpu_ha_rendered" 'replicas: 2'
require "$cpu_ha_rendered" 'whenUnsatisfiable: DoNotSchedule'
require "$cpu_ha_rendered" 'requiredDuringSchedulingIgnoredDuringExecution:'
require "$cpu_ha_rendered" 'kind: HorizontalPodAutoscaler'
require "$cpu_ha_rendered" 'name: postgres'
```

- [ ] **Step 2: 运行渲染契约确认失败**

Run: `bash scripts/test-delivery-render.sh`

Expected: FAIL，因为 CPU HA Profile 和硬分散策略尚未定义。

- [ ] **Step 3: 添加最小可复用配置**

在默认 values 中增加以下控制面调度字段，默认保持兼容：

```yaml
availability:
  requiredAntiAffinity: false
  topologySpreadWhenUnsatisfiable: ScheduleAnyway

backend:
  nodeSelector: {}
frontend:
  nodeSelector: {}

postgres:
  standalone:
    nodeSelector: {}
```

在两个 Deployment 模板中按 `requiredAntiAffinity` 渲染 `podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution`；按 `topologySpreadWhenUnsatisfiable` 渲染拓扑规则；在 PostgreSQL StatefulSet 中渲染 `postgres.standalone.nodeSelector`。同时为 Portal/API 显式设置 `RollingUpdate`、`maxUnavailable: 0`、`maxSurge: 1`。

- [ ] **Step 4: 运行渲染契约确认通过**

Run: `bash scripts/test-delivery-render.sh`

Expected: test、production 和 CPU HA 三种渲染都通过；production 仍不渲染内置 PostgreSQL。

### Task 3: 创建三 CPU 节点部署 Profile

**Files:**
- Create: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `deploy/profiles/vke-test.yaml`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: 创建 Profile 渲染失败测试输入**

CPU HA Profile 复用当前已验证的 VKE 域名、镜像仓库、TOS FSX 和本地登录；设置：

```yaml
backend:
  replicaCount: 2
  nodeSelector: {platform.wellspiking.ai/pool: control-plane}
frontend:
  replicaCount: 2
  nodeSelector: {platform.wellspiking.ai/pool: control-plane}
postgres:
  mode: standalone
  standalone:
    nodeSelector: {platform.wellspiking.ai/pool: control-plane}
availability:
  pdbEnabled: true
  minAvailable: 1
  requiredAntiAffinity: true
  topologySpreadWhenUnsatisfiable: DoNotSchedule
autoscaling:
  backend: {enabled: true, minReplicas: 2, maxReplicas: 6, targetCPUUtilizationPercentage: 70}
  frontend: {enabled: true, minReplicas: 2, maxReplicas: 6, targetCPUUtilizationPercentage: 70}
```

- [ ] **Step 2: 验证 Profile 首次渲染失败**

Run: `helm template ray-platform helm/ray-train-platform --namespace ray-train-platform --values deploy/profiles/vke-cpu-ha.yaml`

Expected: FAIL，直到 Task 2 所需模板字段实现完成。

- [ ] **Step 3: 定义 CPU HA Profile 与操作文档**

保留 `vke-test.yaml` 作为单节点验收 Profile；新增 `vke-cpu-ha.yaml` 作为当前三 CPU 节点环境唯一升级入口。文档明确：内置 PG 为单点，运行中 RayJob 不受前后端滚动升级影响，PG 重调度期间 Portal 会短暂不可用。

- [ ] **Step 4: 验证 Helm diff 输入和渲染**

Run: `helm lint helm/ray-train-platform --values deploy/profiles/vke-cpu-ha.yaml && bash scripts/test-delivery-render.sh`

Expected: exit 0。

### Task 4: 为 Loki 和 Alloy 创建 CPU 节点高可用 Profile

**Files:**
- Create: `ops/observability/loki/20-values-cpu-ha.yaml`
- Create: `ops/observability/loki/migrate-vci-to-cpu.sh`
- Modify: `ops/observability/loki/deploy-vci-ha.sh`
- Create: `ops/observability/alloy/20-values-cpu-ha.yaml`
- Modify: `ops/observability/alloy/deploy-vci-ha.sh`
- Modify: `ops/observability/loki/30-verify-loki.sh`

- [ ] **Step 1: 为 CPU Profile 写入失败渲染断言**

将两个部署脚本的名称改为通用 `deploy-ha.sh`，并使 `VALUES_FILE` 显式选择 profile。CPU Profile 的断言必须包括：

```bash
require_rendered 'platform.wellspiking.ai/pool'
require_rendered 'control-plane'
if grep -Fq 'virtual-node' "$rendered"; then
  echo 'CPU profile must not target virtual-node' >&2
  exit 1
fi
```

- [ ] **Step 2: 运行 CPU Profile 渲染确认失败**

Run: `LOKI_VALUES_FILE=ops/observability/loki/20-values-cpu-ha.yaml bash ops/observability/loki/deploy-ha.sh --render-only`

Run: `ALLOY_VALUES_FILE=ops/observability/alloy/20-values-cpu-ha.yaml bash ops/observability/alloy/deploy-ha.sh --render-only`

Expected: FAIL，因为 CPU Profile 和通用脚本尚未创建。

- [ ] **Step 3: 实现 CPU Profile**

Loki CPU Profile 使用新 release 名称 `loki-cpu`，保持现有 TOS Secret、`singleBinary.replicas: 3`、每副本 `50Gi ebs-ssd`、PDB `minAvailable: 2`，并设置 CPU 池 nodeSelector、主机级硬反亲和、主机级 `DoNotSchedule` 分散、空 VCI toleration 和无 VCI burst annotation。Gateway 使用两副本、CPU 池 nodeSelector 和主机级硬分散。新 Gateway 服务固定为 `loki-cpu-gateway`。

`migrate-vci-to-cpu.sh` 在安装新 release 前必须查询旧 Loki `/ready` 和历史日志基线，再通过 Helm 禁用旧 release 的 PDB、将旧 `singleBinary` 和 gateway 缩容为零，等待 WAL 按 `flush_on_shutdown: true` 退出；旧 `loki` PVC/PV/Helm release 均保留。随后以 `loki-cpu` release 安装 CPU Profile，等待三个新实例 Ready 并验证同一 TOS bucket 的历史查询可用。

Alloy Profile 保持 `controller.type: statefulset`、`replicas: 2`、集群模式和 Kubernetes API 日志发现；改为 CPU 池 nodeSelector、主机级硬分散、PDB `minAvailable: 1`，并移除 VCI toleration/annotation。

- [ ] **Step 4: 验证 CPU Profile 渲染**

Run: 两个 Task 4 Step 2 命令，并运行 `bash ops/observability/loki/migrate-vci-to-cpu.sh --dry-run`。

Expected: 两个命令均输出各自的 render contract verified，Loki dry-run 输出旧 release 保留与新 release 名称，且渲染结果中不包含 `virtual-node` 或 `burst-to-vci`。

### Task 5: 创建 VCI 退役前置检查与迁移工具

**Files:**
- Create: `ops/ha/label-control-plane-nodes.sh`
- Create: `ops/ha/migrate-vci-control-plane.sh`
- Create: `ops/ha/verify-vci-retirement.sh`
- Create: `ops/ha/test/verify-vci-retirement-test.sh`

- [ ] **Step 1: 写入失败的离线 manifest 测试**

`verify-vci-retirement-test.sh` 使用临时 `kubectl` stub，验证检查脚本对下列任一节点类型为 `virtual-node` 的关键 Pod 返回非零：

```text
loki/loki-0
monitoring/alloy-0
ray-train-platform/ray-train-backend-0
kuberay-system/kuberay-operator-0
kueue-system/kueue-controller-manager-0
kube-system/coredns-0
```

- [ ] **Step 2: 运行检查确认失败**

Run: `bash ops/ha/test/verify-vci-retirement-test.sh`

Expected: FAIL，因为工具尚未实现。

- [ ] **Step 3: 实现安全、可重入的操作工具**

`label-control-plane-nodes.sh` 仅允许精确的三台 CPU 节点，并应用：

```bash
kubectl label node 172.28.2.65 platform.wellspiking.ai/pool=control-plane --overwrite
kubectl label node 172.28.2.66 platform.wellspiking.ai/pool=control-plane --overwrite
kubectl label node 172.28.2.67 platform.wellspiking.ai/pool=control-plane --overwrite
```

`migrate-vci-control-plane.sh` 必须分阶段执行而非删除：标签校验、平台 Helm 升级、调用 `loki/migrate-vci-to-cpu.sh` 进行蓝绿 Loki 切换、Gateway/Alloy 升级、KubeRay/Kueue CPU 调度更新、VCI cordon、VKE 管理控制器滚动重启、逐项 rollout 状态检查。任何步骤失败立即停止，绝不删除 PVC、RayJob、RayCluster、Namespace 或 VCI 节点。

`verify-vci-retirement.sh` 必须验证三类条件：关键 Pod 都位于带 CPU 池标签的物理节点；所有 Ready 副本满足数量；VCI 节点没有关键工作负载。输出清晰的剩余 Pod 列表并返回非零。

- [ ] **Step 4: 运行工具单元测试与 shell 静态检查**

Run: `bash ops/ha/test/verify-vci-retirement-test.sh && bash -n ops/ha/*.sh`

Expected: exit 0。

### Task 6: 构建、发布、迁移与验收

**Files:**
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `docs/BUILD_AND_DEPLOY.md`

- [ ] **Step 1: 在发布前执行完整本地验证**

Run: `cd frontend && npm test && npm run build`

Run: `cd backend && go test ./...`

Run: `bash scripts/test-delivery-render.sh && bash ops/ha/test/verify-vci-retirement-test.sh`

Expected: 所有命令 exit 0。

- [ ] **Step 2: 构建新版 Portal 并发布不可变标签**

Run: `USE_BUILDX=false PUSH_IMAGE=true IMAGE_TAG=cpu-ha-r1 BUILD_TARGETS=frontend bash build-image.sh`

Expected: 输出前端镜像摘要；将 tag/digest 写入 CPU HA Profile。后端沿用已验证镜像摘要。

- [ ] **Step 3: 先完成迁移前备份与健康基线**

Run: `bash ops/platform/backup-state.sh --profile deploy/profiles/vke-cpu-ha.yaml`

Run: `bash ops/observability/loki/30-verify-loki.sh`

Run: `kubectl get rayjob -A && kubectl get raycluster -A`

Expected: 元数据备份完成、Loki 查询可用、现有 Ray 资源清单已记录。

- [ ] **Step 4: 分阶段执行迁移**

Run: `bash ops/ha/migrate-vci-control-plane.sh --profile deploy/profiles/vke-cpu-ha.yaml --stage platform`

Run: `bash ops/ha/migrate-vci-control-plane.sh --profile deploy/profiles/vke-cpu-ha.yaml --stage loki`

Run: `bash ops/ha/migrate-vci-control-plane.sh --profile deploy/profiles/vke-cpu-ha.yaml --stage controllers`

Expected: 每个阶段仅在健康检查通过后才完成；若任一阶段失败，保留已验证工作负载并停止。

- [ ] **Step 5: 完成端到端验收，不下线 VCI**

Run: `bash ops/platform/verify.sh --profile deploy/profiles/vke-cpu-ha.yaml`

Run: `bash ops/ha/verify-vci-retirement.sh`

Run: `bash scripts/e2e-training.sh`

Expected: 控制面、日志、训练提交和 VCI 退役检查均成功。仅在这之后通知管理员可以从 VKE 下线 VCI。
