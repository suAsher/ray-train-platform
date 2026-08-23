# NVMe Cache and GPU Fallback Placement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 全局提供 GPU 节点 NVMe 缓存能力，让每个新训练任务显式选择 `off` 或 `runtime`，并把平台与可观测性服务改成 CPU 节点优先、GPU 节点可兜底，同时证明默认关闭和升级都不影响既有训练。

**Architecture:** 使用固定版本 Rancher Local Path Provisioner 为 `/data1/ray-cache` 和 `/data2/ray-cache` 动态创建带节点亲和的本地 PV；平台发布全局 availability/policy，但 Backend 只为每任务选择 `runtime` 的 Ray Head/Worker 渲染挂到 `/mnt/cache` 的 generic ephemeral PVC，默认 `off`。Web、`spk-rayjob` 和平台网关把同一 mode/size 契约送入任务创建链路；训练镜像保持不变。供应脚本执行 15% 剩余容量门禁，独立只读监控 DaemonSet 暴露磁盘与缓存目录指标；控制面组件保留 CPU/内存请求和反亲和，只把硬 `nodeSelector` 改成 CPU 节点 preferred affinity。

**Tech Stack:** Kubernetes/VKE、KubeRay、Kueue、Helm 3、Rancher Local Path Provisioner v0.0.36、Prometheus Operator、node-exporter、Bash、Go、Harbor。

---

## 执行约束

- 当前工作树已有其他前后端未提交改动。每个提交只能 `git add` 本任务明确列出的路径，禁止 `git add .`。
- 本地目录 `/Users/ashersu/Desktop/西井/ray-train-platform` 是代码真相；构建机 `/opt/guofeng/vke-cluster/ray-platform` 只接收已审查的同步结果。
- 所有生产镜像必须来自 `harbor.wellspiking.ai` 并固定 digest；部署脚本不得在生产阶段访问公网。
- 不修改或删除现有 RayJob、RayCluster、训练 Pod、PVC/PV；不重启节点级核心服务。
- 缓存只改变平台 Backend、Frontend、`spk-rayjob` 和基础设施；不修改、不重建训练镜像，也不要求训练入口代码升级。
- 省略缓存参数必须保持历史行为；`runtime` 只配置临时目录和 object spilling，不自动复制 `/mnt/storage/public` 或加速 DataLoader。
- 每个任务先运行 RED 测试，确认失败原因与预期一致，再写最小实现并运行 GREEN 测试。

## Task 1: 固化每任务缓存文档契约和测试入口

**Files:**
- Modify: `docs/superpowers/specs/2026-08-23-nvme-cache-and-gpu-fallback-placement-design.md`
- Modify: `docs/PLATFORM_ROADMAP.md`
- Modify: `scripts/test_docs.py`
- Create: `scripts/test-nvme-cache-delivery.sh`
- Modify: `scripts/test-delivery-render.sh`

- [ ] 先在 `scripts/test_docs.py` 写失败契约，要求全局 availability + 每任务 `off`/`runtime`、100Gi/200Gi/500Gi allowlist、原生 metadata、训练镜像不变和 dataset 模式门禁，并拒绝全局启用步骤。
- [ ] 运行 `python3 scripts/test_docs.py`，预期因现有设计仍描述全局缓存而 RED。
- [ ] 在 `scripts/test-nvme-cache-delivery.sh` 写交付失败测试，要求本地缓存 Chart、生产 values、容量脚本、监控资源、PrometheusRule 和运维文档均存在。
- [ ] 运行 `bash scripts/test-nvme-cache-delivery.sh`，预期因 `helm/ray-cache-local/Chart.yaml` 不存在而失败。
- [ ] 把新测试接入 `scripts/test-delivery-render.sh` 的末尾，保证总交付检查不会漏掉缓存子系统。
- [ ] 更新设计和路线图，明确默认 `off`、三种入口、容量政策、runtime 边界、训练镜像不变和 dataset 模式门禁。
- [ ] 运行 `python3 scripts/test_docs.py`，预期每任务缓存契约、文档链接与命令块检查全部通过。
- [ ] 只提交本任务文档和测试入口：`git commit -m "docs: plan nvme cache delivery roadmap"`。

## Task 2: 新建固定版本本地卷 Helm Chart

**Files:**
- Create: `helm/ray-cache-local/Chart.yaml`
- Create: `helm/ray-cache-local/values.yaml`
- Create: `helm/ray-cache-local/values-vke-production.yaml`
- Create: `helm/ray-cache-local/templates/service-account.yaml`
- Create: `helm/ray-cache-local/templates/rbac.yaml`
- Create: `helm/ray-cache-local/templates/configmap.yaml`
- Create: `helm/ray-cache-local/templates/deployment.yaml`
- Create: `helm/ray-cache-local/templates/storageclass.yaml`
- Create: `helm/ray-cache-local/templates/_helpers.tpl`
- Create: `helm/ray-cache-local/tests/render-contract.sh`

- [ ] 先在 `helm/ray-cache-local/tests/render-contract.sh` 写渲染契约：供应器为 `rancher.io/local-path`，StorageClass 名为 `ray-cache-local`，`WaitForFirstConsumer`、`Delete`、非默认、禁止扩容；生产节点映射仅包含 `172.28.1.232` 和 `172.28.1.233` 的 `/data1/ray-cache`、`/data2/ray-cache`，未知节点路径为空。
- [ ] 测试还必须拒绝：公开 registry、浮动 `latest`、`allowUnsafePathPattern`、宽泛 `/data1` 或 `/data2` 根路径、CPU 节点路径映射。
- [ ] 运行 `bash helm/ray-cache-local/tests/render-contract.sh`，预期 Helm Chart 缺失而失败。
- [ ] 创建 Chart，固定应用版本 `0.0.36`；默认 values 不包含任何具体节点，生产节点放在 `values-vke-production.yaml`，便于后续新集群提供独立 profile。
- [ ] 供应器 Deployment 只保留最小 ClusterRole：读取 Node/PVC/ConfigMap，创建/更新/删除 PV 与 helper Pod；ServiceAccount token 只给供应器，不给监控 Pod。
- [ ] 供应器自身使用 CPU 控制面 preferred node affinity，同时排除 virtual-node；helper Pod 由选定 PV 的目标节点决定。
- [ ] 镜像字段只接受 `repository@sha256:digest`。生产 values 使用内部 Harbor 的 Local Path Provisioner 和 BusyBox 镜像 digest；若 registry 中尚无 digest，先在构建阶段镜像同步后再提交该 values，不允许临时 tag 上线。
- [ ] 运行 `helm lint helm/ray-cache-local -f helm/ray-cache-local/values-vke-production.yaml` 和渲染契约，预期通过。
- [ ] 提交：`git commit -m "feat: add pinned local cache provisioner chart"`。

## Task 3: 实现安全容量门禁与精确回收

**Files:**
- Create: `helm/ray-cache-local/files/setup`
- Create: `helm/ray-cache-local/files/teardown`
- Create: `helm/ray-cache-local/tests/capacity-contract.sh`
- Modify: `helm/ray-cache-local/templates/configmap.yaml`

- [ ] 先写 `capacity-contract.sh`，在临时目录和伪造的 `df` 输出下覆盖：正常创建、请求后低于 15% 拒绝、未知根路径拒绝、零/非法 `VOL_SIZE_BYTES` 拒绝、目标已存在幂等、只删除精确卷 UID 目录、拒绝删除缓存根和任意父目录。
- [ ] 运行测试，预期因 `files/setup` 与 `files/teardown` 不存在而失败。
- [ ] `setup` 只接受 `/data1/ray-cache/<namespace>-<pvc>-<uid>` 或 `/data2/ray-cache/<namespace>-<pvc>-<uid>`；读取 `VOL_SIZE_BYTES`，按所在文件系统总量/可用量计算，创建后预计可用空间低于 15% 时返回非零。
- [ ] `setup` 用 `install -d -m 0770` 创建目录并设置训练运行 UID/GID 1000；不得 chmod 宿主机根目录。
- [ ] `teardown` 对目标做规范化和根路径保护，只删除匹配的单个卷目录；失败时在对应盘的 `.ray-cache-metrics` 记录递增计数，不使用通配符清理。
- [ ] ConfigMap 用 `.Files.Get` 嵌入两份脚本，避免 YAML 与源脚本双份漂移。
- [ ] 运行容量契约、`shellcheck` 和 Helm 渲染测试，预期全部通过。
- [ ] 提交：`git commit -m "feat: guard local cache capacity and cleanup"`。

## Task 4: 增加缓存监控与告警

**Files:**
- Create: `helm/ray-cache-local/templates/monitor-daemonset.yaml`
- Create: `helm/ray-cache-local/templates/monitor-service.yaml`
- Create: `helm/ray-cache-local/templates/servicemonitor.yaml`
- Create: `helm/ray-cache-local/templates/prometheusrule.yaml`
- Create: `helm/ray-cache-local/files/collect-cache-metrics`
- Create: `helm/ray-cache-local/tests/monitor-contract.sh`
- Modify: `helm/ray-cache-local/values.yaml`
- Modify: `helm/ray-cache-local/values-vke-production.yaml`

- [ ] 先写监控渲染失败测试，要求 DaemonSet 只调度到 `accelerator=nvidia-rtx-4090` 且 `gpu-pool=production` 的节点，`/data1`、`/data2` 必须只读挂载，Pod 不 privileged、不申请 GPU、关闭 ServiceAccount token。
- [ ] 要求监控使用内部 Harbor 固定 digest 的 node-exporter；BusyBox sidecar 每 30 秒把两个盘的容量、可用量、缓存卷目录数和清理失败计数写到共享 textfile collector 目录。
- [ ] PrometheusRule 必须包含 75% warning、85% high、92% critical、供应器不可用、PVC 长时间 Pending 和 teardown 失败告警；告警不执行自动删除或节点隔离。
- [ ] 运行 `bash helm/ray-cache-local/tests/monitor-contract.sh`，预期资源缺失而失败。
- [ ] 实现只读监控 DaemonSet、ClusterIP Service、ServiceMonitor 和 PrometheusRule；资源请求初始为 sidecar `25m/32Mi`、node-exporter `50m/64Mi`，均不设置 `nvidia.com/gpu`。
- [ ] 运行 Helm 渲染测试并检查 PrometheusRule 表达式可引用实际导出的 metric 名。
- [ ] 提交：`git commit -m "feat: monitor gpu node cache capacity"`。

## Task 5: 增加安装、预检、扩容和安全卸载流程

**Files:**
- Create: `ops/storage/nvme-cache/install.sh`
- Create: `ops/storage/nvme-cache/preflight.sh`
- Create: `ops/storage/nvme-cache/verify.sh`
- Create: `ops/storage/nvme-cache/register-node.sh`
- Create: `ops/storage/nvme-cache/uninstall.sh`
- Create: `ops/storage/nvme-cache/smoke-pod.yaml`
- Create: `ops/storage/nvme-cache/test/ops-contract-test.sh`
- Modify: `scripts/test-nvme-cache-delivery.sh`

- [ ] 先写 ops 契约测试，要求所有脚本使用 `set -euo pipefail`，不出现 `kubectl apply` 公网 URL、`rm -rf` 宽路径、默认 StorageClass patch、节点服务重启命令或无确认卸载。
- [ ] `preflight.sh` 只读检查每个登记节点：标签、Ready、`/data1`/`/data2` 独立挂载、可写、剩余空间大于 20%、缓存根权限、Harbor 镜像可拉、Prometheus Operator CRD 存在。
- [ ] `install.sh` 固定 release/namespace/chart/profile，先运行本地测试与 preflight，再 `helm upgrade --install --atomic --wait`；不得接受任意 `--set`。
- [ ] `register-node.sh` 只生成 values patch 和验收报告，不直接修改生产 release；新增节点必须先完成双盘写入/删除 smoke，再由代码评审合入 profile。
- [ ] `verify.sh` 在两个 GPU 节点分别创建绑定到目标节点的临时 PVC/Pod，验证写入、节点亲和、删除、PVC/PV 消失和宿主机目录回收；资源名带随机后缀，trap 只清理本脚本创建的对象。
- [ ] `uninstall.sh` 默认只读列出所有 `ray-cache-local` PVC/PV；存在任何绑定或释放中卷时拒绝卸载，只有显式 `--confirm-empty` 才卸载 Helm release，绝不删除宿主机缓存根。
- [ ] 运行全部 shell 契约和渲染测试，预期通过。
- [ ] 提交：`git commit -m "feat: add safe nvme cache operations"`。

## Task 6: 平台 Chart 支持 CPU 优先、GPU 兜底和缓存 availability/policy

**Files:**
- Modify: `helm/ray-train-platform/values.yaml`
- Modify: `helm/ray-train-platform/templates/_helpers.tpl`
- Modify: `helm/ray-train-platform/templates/backend-deployment.yaml`
- Modify: `helm/ray-train-platform/templates/frontend-deployment.yaml`
- Modify: `helm/ray-train-platform/templates/spk-rayjob-release.yaml`
- Modify: `helm/ray-train-platform/templates/postgres.yaml`
- Modify: `deploy/profiles/vke-cpu-ha.yaml`
- Modify: `scripts/test-delivery-render.sh`
- Modify: `ops/platform/test/ha-rollout-template-test.sh`
- Modify: `backend/config/vke_cpu_ha_profile_test.go`

- [ ] 先把渲染测试改成新契约并确认 RED：生产控制面 Pod 不得存在 `platform.wellspiking.ai/pool: control-plane` 的硬 `nodeSelector`；必须有权重 100 的 preferred node affinity；仍必须排除 virtual-node；训练环境变量 `TRAINING_NODE_SELECTOR` 保持生产 GPU 硬标签；缓存策略必须全局可用但每任务默认 `off`。
- [ ] 新增统一 values：`placement.preferredNodeSelector` 和 `placement.allowGPUNodeFallback`。默认可移植 profile 不设置偏好；VKE profile 设置 control-plane 偏好并启用 GPU fallback。
- [ ] helper 负责渲染节点偏好，四个模板复用同一实现；已有组件级 `nodeSelector` 保留为兼容性逃生口，但生产 profile 清空这些硬约束。
- [ ] 保留现有 PDB、required pod anti-affinity、topology spread、CPU/内存 request/limit；任何控制面 Pod 均不得申请 GPU。
- [ ] 用 `training.localCache.available` 表达基础设施能力，生产初始设为 `true`；配置 `storageClass: ray-cache-local`、`mountPath: /mnt/cache`，并把 `training.localCache.policy.allowedSizes` 设为 `[100Gi, 200Gi, 500Gi]`、`training.localCache.policy.defaultSize` 设为 `200Gi`、`training.localCache.policy.maxSize` 设为 `500Gi`、`defaultMode` 设为 `off`。availability 不是给所有任务挂载缓存的开关。
- [ ] 配置解析必须拒绝空 StorageClass、非绝对 mountPath、重复/非法容量、defaultSize 不在 allowlist、maxSize 超过 `500Gi` 或默认 mode 非 `off`；默认可移植 profile 可设置 `available: false`，但省略每任务 cache 参数的渲染始终保持旧模板。
- [ ] 运行 `go test ./backend/config ./backend/k8s`、Helm lint、`scripts/test-delivery-render.sh`，预期通过。
- [ ] 提交：`git commit -m "feat: allow gpu fallback for platform services"`。

## Task 7: MLflow、Loki、Prometheus/Grafana、Alloy 使用相同调度语义

**Files:**
- Modify: `ops/mlflow/values-vke.yaml`
- Modify: `ops/mlflow/10-database.yaml`
- Modify: `ops/mlflow/20-bootstrap.yaml`
- Modify: `ops/mlflow/22-db-upgrade.yaml`
- Modify: `ops/mlflow/25-artifact-acceptance.yaml`
- Modify: `ops/mlflow/30-policy.yaml`
- Modify: `ops/mlflow/35-fsx-health-probe.yaml`
- Modify: `ops/mlflow/40-smoke.yaml`
- Modify: `ops/mlflow/contract-test.sh`
- Modify: `ops/mlflow/verify.sh`
- Modify: `ops/observability/loki/20-values-cpu-ha.yaml`
- Modify: `ops/observability/loki/deploy-ha.sh`
- Modify: `ops/observability/loki/test/verify-loki-contract-test.sh`
- Modify: `ops/observability/prometheus-operator/20-values-production.yaml`
- Modify: `ops/observability/prometheus-operator/render-test.sh`
- Modify: `ops/observability/alloy/20-values-cpu-ha.yaml`

- [ ] 先更新各自契约测试并确认 RED：不得硬绑 control-plane 或 GPU pool，必须优先 control-plane、排除 virtual-node、保留反亲和、无 GPU request。
- [ ] MLflow Server、独立 PostgreSQL、bootstrap/upgrade/smoke/probe Jobs 全部统一 preferred affinity；Job 允许落到 GPU 节点但不申请 GPU。
- [ ] Loki StatefulSet 与 gateway 改 preferred affinity；EBS PVC 与三副本/PDB/反亲和不变。
- [ ] Prometheus Operator、Prometheus、Alertmanager、Grafana 改 preferred affinity；EBS 数据卷、两副本和 retention 不变。
- [ ] Alloy 的两个 StatefulSet 采集副本改 preferred affinity。节点级日志采集 DaemonSet 保持按职责覆盖节点，不加排除 GPU 的选择器。
- [ ] 运行 MLflow、Loki、Prometheus Operator、Alloy 的全部 render/contract tests，预期通过。
- [ ] 提交：`git commit -m "feat: prefer cpu nodes for shared services"`。

## Task 8: 实现每任务 API、平台网关 metadata 和 RayJob runtime 渲染

**Files:**
- Modify: Backend 训练任务请求/响应 schema、校验和持久化模块
- Modify: 平台网关原生 Ray Jobs API metadata 解析模块
- Modify: Backend RayJob/RayCluster Pod 模板渲染模块
- Modify: 对应 Backend 单元与集成测试
- Modify: `ops/platform/preflight.sh`
- Modify: `ops/platform/deploy.sh`
- Create: `ops/platform/test/preflight-local-cache-test.sh`
- Modify: `scripts/test-delivery-render.sh`

- [ ] 先写 Backend 表驱动 RED 测试，覆盖：字段省略和显式 `off` 都生成与历史完全相同的 Pod 模板；`runtime` + 100Gi/200Gi/500Gi 通过；`runtime` 省略 size 得到 200Gi；未知 mode、非 allowlist size、仅提供 size、超过 500Gi 和 availability=false 时请求 runtime 都在创建 RayCluster 前失败。
- [ ] 为每任务 API 增加可选 cache mode/size，并规范化为一个不可变值对象；校验顺序固定为 availability、mode、mode/size 组合、allowlist、default 和 max，错误响应明确指出允许值。未提供缓存参数的任务不写入新字段，保证旧客户端与历史审计输出不漂移。
- [ ] 先写平台网关 RED 测试，要求原生 `ray job submit` metadata 键 `platform.cache.mode` 和 `platform.cache.size` 进入同一规范化/校验路径；拒绝未知 `platform.cache.*` 键和重复冲突值，不把这些平台键传给训练入口脚本。
- [ ] 平台网关只解析 metadata，不从 command、environment 或 working directory 猜测缓存意图；省略两个键保持历史行为，`runtime` 省略 `platform.cache.size` 时使用 200Gi。
- [ ] 先写 RayJob 渲染 RED 测试：只有 `runtime` 为 Head/Worker 各加独立 generic ephemeral PVC 和 `/mnt/cache`，设置 `PLATFORM_CACHE_PATH=/mnt/cache`、Ray temp-dir `/mnt/cache/ray`、object spilling `/mnt/cache/ray-spill/objects`；Submitter 无挂载，`off`/省略参数的完整模板与基线一致。
- [ ] 实现最小渲染；不得自动复制 `/mnt/storage/public`，不得修改 DataLoader，不得改变 output/checkpoint 路径，也不得依赖训练镜像内新增脚本。所有请求对象以新值返回，不原地修改共享模板。
- [ ] preflight 在 `training.localCache.available=true` 时只读确认 StorageClass 的 provisioner、binding mode、reclaim policy、非默认状态和供应器 Ready；不满足时拒绝平台发布，`available=false` 时允许供应器独立安装或逻辑回滚。
- [ ] 运行 Backend 配置、API、网关 metadata、RayJob 渲染、preflight 和完整交付测试，预期全部通过。
- [ ] 提交：`git commit -m "feat: add per-job runtime cache contract"`。

## Task 9: Web、spk-rayjob、镜像构建与离线交付

**Files:**
- Modify: Frontend 训练创建/重提表单、API 类型和组件测试
- Modify: `spk-rayjob` 提交参数、请求映射、帮助文本和 CLI 测试
- Modify: `build-image.sh`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `ops/observability/prometheus-operator/IMAGE-SOURCES.md`
- Create: `ops/storage/nvme-cache/IMAGE-SOURCES.md`

- [ ] 先写 Web RED 测试：创建和重提表单默认显示“缓存关闭”；选择 `runtime` 后才显示 100Gi/200Gi/500Gi，默认 200Gi；切回 `off` 清除 size；服务端 allowlist 或 availability 变化后界面不保留非法旧值；错误可访问且可操作。
- [ ] 实现 Web 控件并通过每任务 API 发送 mode/size。复制旧任务或从旧草稿进入时，缺少 cache 字段必须解释为 `off`，不得静默变成 `runtime`。
- [ ] 先写 `spk-rayjob` RED 测试，为 submit 增加 `--cache-mode off|runtime` 和 `--cache-size 100Gi|200Gi|500Gi`；省略两个参数时请求体与旧版本字节级等价，`runtime` 省略 size 由服务端默认 200Gi，非法组合在本地快速失败且服务端仍重复校验。
- [ ] 更新 `spk-rayjob` 帮助和提交映射；文档示例同时给出 Web、`spk-rayjob submit --cache-mode runtime --cache-size 200Gi` 和原生 `ray job submit --metadata-json` 的 `platform.cache.mode`/`platform.cache.size` 用法。
- [ ] 构建脚本测试必须要求 `cache-dependencies` 目标把 Local Path Provisioner、helper BusyBox 和 cache monitor/node-exporter 镜像复制到内部 Harbor并输出 digest 清单，同时只重建 Backend、Frontend 和 `spk-rayjob` 镜像，不重建训练镜像。
- [ ] 只从已配置的 Harbor Docker Hub mirror 或构建机代理拉取基础设施依赖，推送到内部 Harbor，再以 registry 返回的 digest 更新生产 values；生产渲染不得出现公网 registry 或浮动 tag。
- [ ] 在构建机临时目录重新检出当前 release commit，运行 Backend、Frontend、`spk-rayjob`、Helm 渲染、缓存契约和离线镜像检查。记录训练镜像升级前后 digest 相同，Backend、Frontend 和 `spk-rayjob` 镜像 digest 已更新。
- [ ] 提交：`git commit -m "feat: expose per-job cache options"`。

## Task 10: 分阶段生产上线并证明既有训练连续

**Files:**
- Create: `docs/acceptance/NVME_CACHE_ROLLOUT_20260823.md`
- Modify: `docs/OPERATIONS_GUIDE.md`

- [ ] 上线前保存只读基线：节点、StorageClass、平台/MLflow/Loki/Prometheus/Alloy Pod、当前 RayJob/RayCluster、训练 Pod UID/重启次数、Kueue 占用、磁盘容量。
- [ ] 先部署 `ray-cache-local`，平台 profile 仍保持缓存关闭；执行两个节点、两条路径的供应/写入/回收 smoke。
- [ ] 用普通用户启动一个持续至少 30 分钟、每 30 秒输出心跳并持续写个人输出目录的 1 GPU 训练，记录 Job ID、Pod UID、最后日志序号和 MLflow Run ID。
- [ ] 分别滚动升级平台、MLflow、Loki、Prometheus/Grafana、Alloy；每个 release 完成后确认持续训练 Pod UID 与 restartCount 不变、心跳连续、checkpoint/输出仍可写。
- [ ] 发布 `training.localCache.available=true` 及管理员 policy，确认默认 mode 仍为 `off`；只滚动 Backend、Frontend、`spk-rayjob` 和所需控制面 Pod，不重建训练镜像，不 patch 现有 RayJob/RayCluster。
- [ ] 分别用 Web、`spk-rayjob` 和经平台网关的原生 metadata 提交省略参数与显式 `off` 回归，确认不创建缓存 PVC、Pod 模板和历史行为不变；再验证非法 mode/size 在创建 RayCluster 前失败。
- [ ] 从三个入口分别提交新的 1×1 `runtime` cache smoke：Head/Worker 有 `/mnt/cache`，Submitter 无；未指定 size 时为 200Gi，显式 100Gi/500Gi 可用；PVC/PV 绑定到同一节点；Ray session 与 spilling 在缓存盘；结束后目录回收。
- [ ] 按 `docs/BEVFUSION_END_TO_END_GUIDE.md` 用普通用户提交 2×8 回归，验证两个 Worker 各 8 GPU、NCCL、loss、MLflow、日志和 checkpoint；禁止用管理员 kubeconfig替代用户验收路径。
- [ ] 记录所有命令、Job ID、时间、Pod UID、PVC/PV、节点路径、指标截图和结果到验收报告；任一步失败立即停止，不继续扩大变更。

## Task 11: 性能基准、回滚演练和最终文档

**Files:**
- Modify: `docs/DATA_IO_BENCHMARK.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/BUILD_AND_DEPLOY.md`
- Modify: `docs/OPERATIONS_GUIDE.md`
- Modify: `docs/USER_GUIDE.md`
- Modify: `README.md`
- Modify: `docs/PLATFORM_ROADMAP.md`
- Modify: `docs/acceptance/NVME_CACHE_ROLLOUT_20260823.md`

- [ ] 使用完全相同的数据目录、文件清单、总字节数、训练镜像和 Worker 数执行 `off`/`runtime`、冷读/热读基准；记录 MiB/s、files/s、P50/P95、GPU 利用率和端到端训练时间。
- [ ] 明确说明任务级缓存只加速 Ray temp/object spill 和用户显式写入 `/mnt/cache` 的临时文件；它不会自动把 `/mnt/storage/public` 的 DataLoader 文件复制到 NVMe。
- [ ] 演练逻辑回滚：设置 `training.localCache.available=false`，确认新的 `runtime` 请求被拒绝、`off`/省略参数任务保持可提交、已有 `runtime` 任务继续到结束；恢复 availability 并再次提交 smoke。所有缓存 PVC/PV 清理前不得卸载供应器。
- [ ] 演练服务调度回滚：恢复硬 selector 的上一 Helm revision，但不操作 RayJob/RayCluster；记录回滚时长与训练连续性。
- [ ] 更新架构、构建部署、运维和用户文档；用户文档只新增每任务 `off`/`runtime`、allowlist、`/mnt/cache` 用途与不可持久化警告，不改变 `/mnt/storage/public`、个人、团队和输出路径契约。
- [ ] 将未来 dataset 模式继续保持在范围外；只有不可变数据集 manifest、可恢复且带校验的预热流程和设计规定的基准门禁全部通过，才允许另立设计进入实施。
- [ ] 运行 `python3 scripts/test_docs.py`、`bash scripts/test-delivery-render.sh`、`go test ./backend/...`、前端测试和所有 ops contract tests。
- [ ] 执行安全检查：扫描仓库中的 AK/SK、PAT、GitLab Token、密码和 kubeconfig；确认本任务未提交任何凭据。
- [ ] 仅在全部验收通过后创建 release commit：`git commit -m "feat: deliver guarded gpu nvme task cache"`，再创建可追溯 release tag；推送前展示最终 diff 与测试报告。

## 完成定义

- 两块 NVMe 都能安全供应和回收任务缓存，未知节点与 CPU 节点不能供应；
- 缓存基础设施全局可用，但每任务默认 `off`；Web、`spk-rayjob`、原生 metadata 的 `off`/`runtime` 语义一致；
- 100Gi/200Gi/500Gi allowlist、runtime 默认 200Gi 和管理员最大 500Gi 在客户端与服务端生效；
- 未提供缓存参数的新任务保持历史行为，既有任务不中断；关闭 availability 可即时阻止新的 `runtime` 请求而不影响 `off` 任务；
- 高水位阻止新卷但不删除运行中目录，告警能在 Prometheus/Grafana 中看到；
- 平台与共享服务优先 CPU、资源不足时可落 GPU，且不占用 `nvidia.com/gpu`；
- 1×1 cache smoke、持续训练升级验证、2×8 BEVFusion 回归、MLflow、Loki、checkpoint 全部通过；
- runtime 只提供 `/mnt/cache`、`PLATFORM_CACHE_PATH`、Ray temp-dir 和 object spilling，不自动复制公共数据或加速 DataLoader；
- Backend、Frontend 和 `spk-rayjob` 镜像已重建，训练镜像 digest 保持不变；dataset 模式仍受 manifest/预热/基准门禁约束；
- 文档、部署清单、镜像 digest、release commit/tag 与线上 Helm revision 可相互追溯。
