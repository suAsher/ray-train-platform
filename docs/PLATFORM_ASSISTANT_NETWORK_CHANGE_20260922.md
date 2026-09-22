# 平台助手 idle GPU 网络隔离变更计划（2026-09-22）

本文是 `PLATFORM_ASSISTANT_PLAN_20260922.md` 的网络隔离实施附件。目标是在不修改用户训练任务、不读写 Secret、不绕过平台鉴权的前提下，让 `raytrain-assistant-canary` 中的 RayService idle 推理满足可验证的工作负载网络隔离。当前结论是：在 Cello/Cilium `CILIUM_ENABLE_POLICY=never` 下不能上线启用 idle GPU；必须先通过 VKE 托管的 vpc-cni/Cello 配置启用策略执行，并完成负向连通性验收。

## 已核对的现网事实

候选代码目录为 `/tmp/rtp-assistant-backend-20260922`，HEAD `7ffeed6`。本轮只读查询未修改集群资源。

现网 CNI：

- `kube-system` 中 `cello` DaemonSet 为 10/10 Ready，镜像 `vke-cn-shanghai.cr.volces.com/vke/cello:v1.9.8`，容器包含 `cello,cilium`。
- Cilium CRD 已存在：`ciliumnetworkpolicies.cilium.io`、`ciliumclusterwidenetworkpolicies.cilium.io`、`ciliumendpoints.cilium.io` 等。
- `cello` DaemonSet 的 Cilium 容器环境变量包含：
  - `CILIUM_CNI_CHAINING_MODE=cello-chainer`
  - `CILIUM_LABELS=k8s:cilium-policy-role`
  - `CILIUM_ENABLE_POLICY=never`
- `kube-system/cello-config` 显示网络模式为 `eni_shared`，`enableTrunk=false`。`kube-system/cilium-config` 仅见 `debug`，未见可直接修改的 policy enforcement 配置。

官方依据：

- 火山引擎 VPC-CNI 最佳实践说明，VPC-CNI 组件配置由容器服务后台托管，不应直接修改 vpc-cni ConfigMap；组件参数调整应在 VKE 控制台目标集群的“组件管理 / 网络 / vpc-cni 配置”完成。参考：火山引擎《正确使用容器网络》（https://www.volcengine.com/docs/6460/1171741）与《为 Pod 配置固定 IP》（https://www.volcengine.com/docs/6460/1200142?lang=zh）。
- Cilium Policy Enforcement Modes 文档说明：`never` 模式下，即使策略选择了 endpoint，policy enforcement 也被禁用。参考：Cilium stable 文档 `Policy Enforcement Modes`（https://docs.cilium.io/en/stable/security/policy/intro/）。

这意味着当前 Kubernetes `NetworkPolicy` 对 assistant/MLflow 的对象存在，但不会成为可验证安全边界。

## 现有对象和流量矩阵

### 平台 backend

Namespace `ray-train-platform` 标签：

```yaml
app.kubernetes.io/managed-by: ray-train-platform
app.kubernetes.io/part-of: ray-train-platform
kubernetes.io/metadata.name: ray-train-platform
```

Backend：

```yaml
Deployment: ray-train-backend
replicas: 2
pod labels:
  app: ray-train-backend
  app.kubernetes.io/component: api
  app.kubernetes.io/instance: ray-platform
  app.kubernetes.io/name: ray-train-platform
  app.kubernetes.io/part-of: ray-train-platform
Service: ray-train-backend ClusterIP 10.0.114.10:8080
Endpoints: 172.28.1.169:8080, 172.28.1.63:8080
```

### Assistant canary

Namespace `raytrain-assistant-canary` 标签：

```yaml
app.kubernetes.io/part-of: ray-train-platform
kubernetes.io/metadata.name: raytrain-assistant-canary
raytrain.wellspiking.ai/component: assistant-idle
```

静态 Service：

```yaml
assistant-idle-controller: ClusterIP 10.0.119.140:8080
selector:
  app.kubernetes.io/instance: assistant-idle
  app.kubernetes.io/name: assistant-idle-controller

assistant-idle-inference: ClusterIP 10.0.127.89:8000
selector:
  app.kubernetes.io/component: assistant-idle
  app.kubernetes.io/instance: assistant-idle
  raytrain.wellspiking.ai/assistant-role: head
```

当前 controller/reaper 都是 `replicas=0`，当前没有 assistant Ray Pod endpoint。下面先记录当前快照；本轮待上线新增规则单独标注，避免把已存在规则和待变更规则混在一起。

当前快照中已存在的 assistant `NetworkPolicy` 包含：

- `assistant-idle-default-deny`：选择 `app.kubernetes.io/instance=assistant-idle`，Ingress/Egress 默认拒绝。
- `assistant-idle-ray-internal`：允许 assistant 同 instance pod 间 Ray 内部端口：6379、8000、8076、8077、8265、10001、10002-10032、52365、52366、52367；允许 Ray pod 到 controller gate 8080；允许 DNS。
- `assistant-idle-backend-to-serve-only`：只允许 `ray-train-platform` namespace 中 backend pod 到 assistant head 8000。
- `assistant-idle-kuberay-dashboard-only`：只允许 `kuberay-system` 中固定 KubeRay operator labels 到 assistant head 8265。
- `assistant-idle-controller-gate`：当前快照只允许 assistant Ray pod 到 controller 8080；controller egress 到 apiserver 和 DNS。
- `assistant-idle-reaper-apiserver`：reaper egress 到 apiserver 和 DNS。

本轮待上线规则与当前快照的差异：平台管理员状态 API 会由 backend 读取固定 `assistant-idle-controller.raytrain-assistant-canary.svc:8080/status`。因此需要新增 backend → controller:8080 的正向 allow。controller HTTP 端口只暴露 `GET /gate`、`GET /status`、`GET /livez`，不提供创建/删除/启停操作，也不返回密钥；外部访问控制由平台 backend 的 SuperAdmin 守卫完成。该 allow 只覆盖 controller 8080，不允许 backend 访问 Ray dashboard/GCS/Ray Client/agent/worker/internal ports。

### MLflow

Namespace `mlflow-system` 标签：

```yaml
app.kubernetes.io/name: mlflow
app.kubernetes.io/part-of: ray-train-platform
kubernetes.io/metadata.name: mlflow-system
platform.wellspiking.ai/component: experiment-tracking
```

Services/endpoints：

```yaml
mlflow: ClusterIP 10.0.99.73:5000
selector:
  app.kubernetes.io/instance: mlflow
  app.kubernetes.io/name: mlflow
endpoints:
  172.28.1.193:5000
  172.28.1.214:5000

mlflow-ingest: ClusterIP 10.0.126.221:8080
selector:
  app.kubernetes.io/name: mlflow-ingest
endpoints:
  172.28.3.127:8080
  172.28.3.76:8080

mlflow-postgres: ClusterIP 10.0.105.197:5432
selector:
  app.kubernetes.io/name: mlflow-postgres
endpoint:
  172.28.3.15:5432
```

现有 MLflow `NetworkPolicy` 前置 allow 矩阵如下。启用 Cilium policy 前必须保持这些规则存在，否则 MLflow 可能被切断。

| 目标 Pod | 方向 | 已允许来源/目的 | 端口 | 作用 |
| --- | --- | --- | --- | --- |
| `mlflow` | ingress | `ray-train-platform` namespace + pod `app=ray-train-backend` | TCP 5000 | 平台同域代理访问原生 MLflow |
| `mlflow` | ingress | 同 namespace pod `app.kubernetes.io/name=mlflow-ingest` | TCP 5000 | 训练写入网关转发到 MLflow |
| `mlflow` | ingress | namespace `monitoring` | TCP 5000 | 监控采集 |
| `mlflow` | egress | kube-system DNS | TCP/UDP 53 | DNS |
| `mlflow` | egress | pod `app.kubernetes.io/name=mlflow-postgres` | TCP 5432 | MLflow DB |
| `mlflow-ingest` | ingress | namespace `app.kubernetes.io/managed-by=ray-train-platform` 且 `ray.io/tenant-id` Exists | TCP 8080 | 租户训练 Pod 写入 MLflow |
| `mlflow-ingest` | ingress | 同 namespace pod `app.kubernetes.io/name=mlflow-client-smoke` | TCP 8080 | smoke |
| `mlflow-ingest` | egress | kube-system DNS | TCP/UDP 53 | DNS |
| `mlflow-ingest` | egress | pod `app.kubernetes.io/instance=mlflow, app.kubernetes.io/name=mlflow` | TCP 5000 | 写入网关到 MLflow |
| `mlflow-postgres` | ingress | pod `app.kubernetes.io/name=mlflow` | TCP 5432 | MLflow DB 访问 |
| `mlflow-postgres` | ingress | pod `app.kubernetes.io/component=database-migration` | TCP 5432 | MLflow migration |
| `mlflow-fsx-dns-probe` | egress | `192.168.110.61/32`、`192.168.111.63/32`、`100.96.0.2/32`、`100.96.0.3/32` | TCP/UDP 53 | FSX/DNS 探针 |
| `mlflow-fsx-probe` | ingress/egress | 空规则 | - | 启用策略后完全封闭；若它本应探测 FSX 或 kube DNS，需先由 MLflow owner 确认是否故意如此 |

平台 namespace 目前没有 `NetworkPolicy`。启用 Cilium policy 后，只有被策略选中的 endpoints 才会进入默认拒绝；但当前 Cilium 被配置为只把 `k8s:cilium-policy-role` 作为身份相关标签，`excluded-labels/include-labels` 的实际 identity 语义必须通过真实 `CiliumEndpoint`、policy trace 或等价连通性测试确认。仅添加 `cilium-policy-role` 不能保证既有 Kubernetes `NetworkPolicy` selector 在 VKE/Cello 当前实现下自动按预期生效；该方案在实测前保持 HOLD。

## 必须调整的 cilium-policy-role labels/selectors

当前 `CILIUM_LABELS=k8s:cilium-policy-role` 表示 Cilium 身份与策略匹配存在实现相关语义，不能仅按 Kubernetes selector 直觉推断。之前观察到 assistant head/worker 同 `securityidentity=1228` 是风险信号。上线前可以把隔离主体打上稳定角色 label，并将关键 policy selector 收敛到这些 role，但必须先通过真实 `CiliumEndpoint` 身份、policy trace/连通性正负例证明 selector 生效；未证明前不得启用 idle GPU。

建议 label 只打到以下 deployment/template 或 RayService 渲染出的 pod template，不修改用户训练任务：

```yaml
# ray-train-platform backend Pod template
cilium-policy-role: raytrain-backend-api

# KubeRay operator Pod template，实际现网 labels 已是：
# app.kubernetes.io/name=kuberay-operator
# app.kubernetes.io/component=kuberay-operator
# app.kubernetes.io/instance=kuberay
cilium-policy-role: kuberay-operator

# assistant controller/reaper Deployment templates
cilium-policy-role: assistant-idle-controller
cilium-policy-role: assistant-idle-reaper

# assistant RayService head/worker Pod templates
cilium-policy-role: assistant-idle-head
cilium-policy-role: assistant-idle-worker

# MLflow Deployment/StatefulSet/DaemonSet templates
cilium-policy-role: mlflow-server
cilium-policy-role: mlflow-ingest
cilium-policy-role: mlflow-postgres
cilium-policy-role: mlflow-fsx-dns-probe
cilium-policy-role: mlflow-fsx-probe
```

assistant 最小 selector 调整建议：

| Policy | podSelector 增加 | from/to 增加 | 目的 |
| --- | --- | --- | --- |
| `assistant-idle-default-deny` | 保持 `app.kubernetes.io/instance=assistant-idle`，可加 `cilium-policy-role In (assistant-idle-head,assistant-idle-worker,assistant-idle-controller,assistant-idle-reaper)` 的 CiliumNetworkPolicy 等价规则 | 无 | 覆盖本次 assistant 专用对象 |
| `assistant-idle-ray-internal` | head/worker 均带 `cilium-policy-role In (assistant-idle-head,assistant-idle-worker)` | 同 instance + role head/worker | Ray 内部端口仅 head/worker 互通 |
| `assistant-idle-backend-to-serve-only` | head selector 增加 `cilium-policy-role=assistant-idle-head` | backend peer 增加 `cilium-policy-role=raytrain-backend-api` | 只允许 backend 到 head 8000 |
| `assistant-idle-kuberay-dashboard-only` | head selector 增加 `cilium-policy-role=assistant-idle-head` | KubeRay peer 增加 `cilium-policy-role=kuberay-operator` | 只允许 operator 到 8265 |
| `assistant-idle-controller-gate` | controller selector 增加 `cilium-policy-role=assistant-idle-controller` | Ray peer role head/worker；backend peer role `raytrain-backend-api` 仅到 TCP 8080；egress apiserver/DNS 保持 | gate/status/livez 只在 assistant Ray pods 与平台 backend 间开放 |
| `assistant-idle-reaper-apiserver` | reaper selector 增加 `cilium-policy-role=assistant-idle-reaper` | egress apiserver/DNS 保持 | reaper 只出 apiserver/DNS |

保留当前 `app.kubernetes.io/*` 和 `raytrain.wellspiking.ai/assistant-role` selectors；`cilium-policy-role` 是强化 Cilium identity 和策略可读性，不替代已有 owner/instance/role 匹配。

补丁草案只作为评审，不立即 apply：

```bash
kubectl -n ray-train-platform patch deploy ray-train-backend --type strategic \
  -p '{"spec":{"template":{"metadata":{"labels":{"cilium-policy-role":"raytrain-backend-api"}}}}}'

kubectl -n kuberay-system patch deploy kuberay-operator --type strategic \
  -p '{"spec":{"template":{"metadata":{"labels":{"cilium-policy-role":"kuberay-operator"}}}}}'

kubectl -n raytrain-assistant-canary patch deploy assistant-idle-controller --type strategic \
  -p '{"spec":{"template":{"metadata":{"labels":{"cilium-policy-role":"assistant-idle-controller"}}}}}'

kubectl -n raytrain-assistant-canary patch deploy assistant-idle-reaper --type strategic \
  -p '{"spec":{"template":{"metadata":{"labels":{"cilium-policy-role":"assistant-idle-reaper"}}}}}'
```

RayService head/worker 的 `cilium-policy-role` 应由 `backend/assistantidle/render.go` 渲染进入 Pod template，避免手 patch 被重建覆盖。当前已有 `raytrain.wellspiking.ai/assistant-role=head|worker`，新增 role label 应与这个字段同源生成。

MLflow label 应通过 MLflow 发布清单/Helm 值落入 Pod template，不建议直接 patch live Pod；否则滚动重启或 Helm 发布会丢失。注意这本身也可能滚动 `mlflow`、`mlflow-ingest`、`mlflow-postgres`、FSX probe 等工作负载，必须纳入 maintenance window，先保存 Deployment/StatefulSet/DaemonSet owner 版本、replica、template hash、Pod UID/Ready/restart baseline。不要把“补 label”当成无感变更。

## 变更窗口 runbook

### 0. 管理员待确认项

以下信息本轮无法通过只读 kubectl 准确得出，不能编造 CLI：

1. VKE 控制台中 vpc-cni/Cello 组件是否暴露“NetworkPolicy / Cilium policy enforcement / 策略执行模式”开关；字段名、可选值和是否支持按 namespace/节点池灰度，需要管理员在当前租户控制台确认。
2. 若走 OpenAPI/CLI，具体接口名、参数名、addon values schema、是否触发 `cello` DaemonSet 滚动重启，需要管理员从 VKE 当前版本页面或工单确认。
3. `CILIUM_ENABLE_POLICY` 目标值应优先选择 `default`；若 VKE 只支持 `always`，需要额外为 kube-system health、DNS、apiserver、NodeLocal/控制面流量补规则后再评审。
4. 若供应商确认变更会中断现有 Pod 网络，或不能证明训练连接可保持，只能在受影响节点没有训练时执行；不能用用户运行中的任务试错。
5. 变更是否会重启所有 `cello` Pod。火山引擎文档对 vpc-cni 组件配置说明走控制台组件管理，且部分网络组件配置会自动/手动滚动重启 Cello；应安排低峰窗口。
6. 记录方式：管理员在变更单中填写实际入口、字段名、旧值、新值、是否触发 addon rollout、控制台任务 ID 或 OpenAPI request ID、开始/结束时间；没有这些记录不得继续到 assistant 启用。

### 1. 变更前备份

只读保存当前状态，所有文件权限 0600，不包含 Secret：

```bash
umask 077
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
DIR=/root/raytrain-assistant-validation-20260922/network-change-$STAMP
mkdir -p "$DIR"
chmod 700 "$DIR"

kubectl get ds cello -n kube-system -o yaml > "$DIR/cello-ds.before.yaml"
kubectl get cm cello-config cilium-config -n kube-system -o yaml > "$DIR/cni-config.before.yaml"
kubectl get ns raytrain-assistant-canary ray-train-platform mlflow-system kube-system --show-labels > "$DIR/namespaces.before.txt"

# 保存真实可回滚 YAML，而不是只保存 -o wide 摘要。
kubectl get networkpolicy -A -o yaml > "$DIR/networkpolicies.before.yaml"
kubectl get ciliumnetworkpolicy -A -o yaml > "$DIR/cnp.before.yaml" 2>/dev/null || true
kubectl get ciliumclusterwidenetworkpolicy -o yaml > "$DIR/ccnp.before.yaml" 2>/dev/null || true
kubectl get deploy,statefulset,daemonset -n raytrain-assistant-canary -o yaml > "$DIR/assistant-owners.before.yaml"
kubectl get deploy,statefulset,daemonset -n ray-train-platform -o yaml > "$DIR/platform-owners.before.yaml"
kubectl get deploy,statefulset,daemonset -n mlflow-system -o yaml > "$DIR/mlflow-owners.before.yaml"

# 保存 owner UID、generation、resourceVersion、template hash、Pod UID/Ready/restarts/labels/node/IP baseline。
kubectl get deploy,statefulset,daemonset -n raytrain-assistant-canary -o json > "$DIR/assistant-owners.before.json"
kubectl get deploy,statefulset,daemonset -n ray-train-platform -o json > "$DIR/platform-owners.before.json"
kubectl get deploy,statefulset,daemonset -n mlflow-system -o json > "$DIR/mlflow-owners.before.json"
kubectl get pods -n raytrain-assistant-canary -o json > "$DIR/assistant-pods.before.json"
kubectl get pods -n ray-train-platform -o json > "$DIR/platform-pods.before.json"
kubectl get pods -n mlflow-system -o json > "$DIR/mlflow-pods.before.json"
kubectl get pods -A -l ray.io/cluster -o json > "$DIR/ray-pods.before.json" 2>/dev/null || true
kubectl get rayjob,raycluster,rayservice,workload -A -o yaml > "$DIR/ray-kueue.before.yaml" 2>/dev/null || true
kubectl get ciliumendpoints -A -o yaml > "$DIR/cep.before.yaml" 2>/dev/null || true
kubectl get ciliumidentities -o yaml > "$DIR/ciliumidentities.before.yaml" 2>/dev/null || true

# 摘要只用于人工阅读，不作为 rollback 来源。
kubectl get deploy,svc,endpoints,pod,networkpolicy -n raytrain-assistant-canary -o wide --show-labels > "$DIR/assistant.summary.before.txt"
kubectl get deploy,svc,endpoints,pod,networkpolicy -n ray-train-platform -o wide --show-labels > "$DIR/platform.summary.before.txt"
kubectl get deploy,svc,endpoints,pod,networkpolicy -n mlflow-system -o wide --show-labels > "$DIR/mlflow.summary.before.txt"
```

### 2. 前置 allow 与标签准备

1. 先补 `cilium-policy-role` 到 assistant/backend/KubeRay/MLflow 的发布清单或临时 patch 草案，并做 server-side dry-run。不要改用户训练 namespace 和训练 Pod template。MLflow DB/Server/Ingest/Probe 的 label 变更如果通过 owner template 发布，会引发滚动或重建风险，必须在 maintenance window 内执行并先保存 owner 版本与 Pod UID baseline；不得直接 live-patch 运行中 Pod 后宣称可持久。
2. 确认 MLflow 现有 NetworkPolicy 的 allow 矩阵仍完整，尤其：
   - backend → mlflow:5000
   - backend → assistant-idle-controller:8080，仅 `GET /status`、`GET /livez` 和现有 gate HTTP 面；backend 仍不得访问 Ray 控制端口
   - training tenant namespaces → mlflow-ingest:8080
   - mlflow-ingest → mlflow:5000
   - mlflow → mlflow-postgres:5432
   - DNS 与 FSX probe 所需 egress
3. 在启用 policy 前，准备一个只读/短生命周期网络探针 Job 清单，但先不要长期运行。探针必须不挂载用户数据、不读 Secret、不访问外网，只做服务连通性和负向端口测试。

### 3. 开启 VKE/Cello policy enforcement

推荐管理员操作路径：VKE 控制台 → 目标集群 → 组件管理 → 网络 → `vpc-cni`/Cello → 配置 → 将 policy enforcement 从当前 `never` 调整为 `default` 或等价“启用 NetworkPolicy”。如果控制台显示的字段不是上述名称，记录实际字段名和截图/导出 values，不要手写猜测参数。

变更后观察：

```bash
kubectl -n kube-system rollout status ds/cello --timeout=15m
kubectl -n kube-system get pods -l app=cello -o wide --show-labels
kubectl -n kube-system get ds cello -o yaml | grep -E 'CILIUM_ENABLE_POLICY|CILIUM_LABELS|image:' -n
kubectl get ciliumendpoints -A -o wide --show-labels 2>/dev/null || true
```

如果 `rollout status` 超时但已经观察到 MLflow 或训练异常，不要继续等待 15 分钟；立即进入全局 CNI 回滚，同时关闭 assistant controller。

成功条件：

- `cello` DaemonSet 全部 Ready。
- `CILIUM_ENABLE_POLICY` 不再是 `never`，目标应为 `default` 或 VKE 等价启用值。
- `CILIUM_LABELS=k8s:cilium-policy-role` 保持，或若管理员选择扩大 identity labels，必须另行评审 identity 爆炸和策略语义变化。

### 4. 不中断训练观察

变更期间不停止用户训练。观察现有训练连接是否异常，只读命令：

```bash
WATCH_DIR="$DIR/watch"
mkdir -p "$WATCH_DIR"
for i in 1 2 3 4 5 6; do
  ts=$(date -u +%Y%m%dT%H%M%SZ)
  kubectl get rayjob,raycluster,workload -A -o yaml > "$WATCH_DIR/ray-kueue.$ts.yaml" 2>/dev/null || true
  kubectl get pods -A -l ray.io/cluster -o json > "$WATCH_DIR/ray-pods.$ts.json" 2>/dev/null || true
  kubectl get pods -A -l platform_job_id -o json > "$WATCH_DIR/platform-job-pods.$ts.json" 2>/dev/null || true
  kubectl get pods -n mlflow-system -o json > "$WATCH_DIR/mlflow-pods.$ts.json"
  kubectl get pods -n ray-train-platform -o json > "$WATCH_DIR/platform-pods.$ts.json"
  kubectl get events -A --sort-by=.lastTimestamp > "$WATCH_DIR/events.$ts.txt"
  sleep 10
 done
```

不要用 `kubectl get pods -o wide | grep 'ray.io/cluster'` 作为证据；`-o wide` 默认不输出 labels。必须保存 JSON/YAML，核对 labels、ownerReferences UID、Pod UID、Ready condition、restartCount、node/IP、Ray/Kueue resourceVersion。

重点观察：

- 已 Running 的 Ray head/worker 是否重启、Readiness 是否掉线。
- Kueue Workload 是否出现异常 de-admit 或 Pending 激增。
- 用户任务日志中是否出现 MLflow ingest 连接失败、Ray GCS/dashboard 连接失败、DNS 失败。

如果出现用户训练连接中断，立即执行回滚，不继续启用 assistant。

### 5. 网络验收

先验 MLflow，后验 assistant。

MLflow 正向验收：

- backend pod 到 `mlflow.mlflow-system.svc:5000` 应通。
- 任一允许的 tenant 训练探针到 `mlflow-ingest.mlflow-system.svc:8080` 应通。
- `mlflow-ingest` 到 `mlflow:5000` 应通。
- `mlflow` 到 `mlflow-postgres:5432` 应通。

MLflow 负向验收：

- 非 tenant/非 backend 临时 Pod 到 `mlflow:5000` 应失败。
- 非 tenant/非 smoke 临时 Pod 到 `mlflow-ingest:8080` 应失败。
- 非 mlflow/migration Pod 到 `mlflow-postgres:5432` 应失败。

Assistant 正向验收：

- backend pod 到 `assistant-idle-inference.raytrain-assistant-canary.svc:8000` 应通。
- backend pod 到 `assistant-idle-controller.raytrain-assistant-canary.svc:8080/status` 应通；`/livez` 可通；该路径只用于 SuperAdmin 状态页的后端只读读取。
- KubeRay operator 到 assistant head `8265` 应通。
- assistant head/worker 之间 Ray 固定端口应通。
- assistant Ray pod 到 controller gate `8080` 应通。

Assistant 负向验收：

- backend pod 到 assistant head `8265/10001/6379/8076/8077/52365-52367/10002-10032` 应失败。
- backend pod 到 assistant worker `8000/8265/10001/6379/8076/8077/52365-52367/10002-10032` 应失败。
- backend pod 到 assistant controller 除 `GET /gate`、`GET /status`、`GET /livez` 以外的路径应返回 404/405 或等价非操作响应；不得出现创建、删除、启停、凭据或 Ray 控制接口。
- 任意非 backend/非 KubeRay/非 assistant Pod 到 assistant head `8000/8265/10001` 应失败。
- 任意非 backend/非 assistant Pod 到 assistant controller `8080` 应失败。
- 任意 Pod 不应能通过 KubeRay-owned `*-serve-svc` 绕过 `assistant-idle-inference` 的 head-only selector。
- worker endpoint 即使 Ready，也不应接收 backend 8000 流量。

### 6. assistant 启用顺序

只有网络验收通过、下述 DB demand 待启动任务保护已实现并验证后才恢复 idle controller。当前 DB demand 尚未实现，因此即使网络窗口完成也不能直接跳过该闸门：

1. 确认 controller/reaper 镜像仍是已验证 digest，config `Enabled=false` 时 inspect 正常。
2. scale reaper 到 1，确认不会误删非当前 UID service。
3. scale controller 到 1，但 config 仍 disabled，确认 Observe 不报 unknown。
4. 切 `Enabled=true`，等待 idleWindow 后创建 RayService。
5. 重跑 HTTP/router/训练让卡验收。

## 回滚顺序

回滚要优先保护训练和 MLflow，而不是保留 assistant。

1. 如果 MLflow 或训练出现连接故障，立即并行执行两件事：通过 VKE 控制台/OpenAPI 把 policy enforcement 恢复旧值，并暂停 assistant。不要等待 reaper 删除完 RayService 后才全局回滚。若只是 assistant 自身负向测试失败且 MLflow/训练正常，可以先只暂停 assistant、回滚 assistant policy/labels。

暂停 assistant：

```bash
kubectl -n raytrain-assistant-canary scale deploy assistant-idle-controller --replicas=0
```

2. 等 reaper 或手动按 UID precondition 删除本次 assistant RayService；不得删除用户 RayJob/RayCluster。

3. 全局 CNI 回滚由管理员通过 VKE 控制台将 vpc-cni/Cello policy enforcement 恢复为变更前的 `never`。发起恢复旧值后立即开始观察，不以等待 15 分钟作为前置条件：

```bash
kubectl -n kube-system rollout status ds/cello --timeout=15m
kubectl -n kube-system get pods -l app=cello -o wide
```

4. 若只是 assistant 网络策略误配，保持 CNI enforcement 已启用，回滚 assistant labels/policies 到备份；不要回滚全局 CNI。

5. 回滚后采集：

```bash
kubectl get deploy,svc,endpoints,pod,networkpolicy -n raytrain-assistant-canary -o wide --show-labels
kubectl get deploy,svc,endpoints,pod,networkpolicy -n mlflow-system -o wide --show-labels
kubectl get rayjob,raycluster,workload -A -o wide
kubectl get events -A --sort-by=.lastTimestamp | tail -100
```

未知项：VKE 控制台具体字段、OpenAPI 参数、是否支持灰度、是否强制重启 `cello`、`CILIUM_ENABLE_POLICY=default` 与 `always` 的可选性，需要管理员在变更窗口前确认并记录。不得通过直接编辑 `cello-config` 或 DaemonSet 绕过托管组件管理。

## DB demand 补足方案（不在本次网络变更中实现）

当前 idle controller 的训练优先只看 Kubernetes 对象：RayJob、Kueue Workload、Pod。平台提交服务会先把 job 写入 DB 为 `DesiredActive + SUBMITTED`，后续 reconciler 才创建 RayJob/Kueue Workload，因此存在“DB 已有待启动 GPU 工作、Kubernetes 尚未出现对象”的短窗口。

补足方案：

1. 新增只读 `DemandSource` 接口给 assistant idle controller，合并 `KubeDemand || DBDemand` 后再决策。
2. DB 查询统计所有平台 GPU 工作，而不限 Ray Train：Ray Train、旧 DDP、模型评估、由模型服务/评估链路提交且会申请 GPU 的待启动任务都应优先。条件为 `desired_state=ACTIVE`、非终态，且处于 `SUBMITTED/QUEUED/PROVISIONING/RECOVERING/UNKNOWN` 或等价待启动/恢复状态；`RUNNING` 不作为抢占需求，除非其当前 attempt 正在 recovery/重新创建。
3. 对已有 Kubernetes 对象且已被 Kube observer 覆盖的任务去重，避免重复 demand 影响判断。RayJob、Workload、Pod、旧 DDP Job 或评估 Job 都应纳入去重键。
4. 查询需要按租户授权无关地由平台控制面内部执行，只返回布尔值和聚合计数用于 controller 日志；不输出 job id、用户 spec、路径、参数或日志。
5. DB 查询失败必须 fail-closed：关闭 gate，存在服务则走 drain/delete，不创建新 RayService。
6. 验收用例：构造 DB 已创建但 Kubernetes 对象创建延迟/失败的 GPU 工作，idle 不创建；已有 RayJob/Workload/Pending Pod 时仍由 Kube observer 触发；终态历史任务不阻塞；Running 且 ready 的训练不阻塞其他空闲卡；旧 DDP/评估待启动同样关闭 gate。
