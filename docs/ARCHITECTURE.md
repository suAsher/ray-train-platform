# Ray 分布式训练平台 · 架构与实现状态

本文对照最初的 V1 架构图，逐层记录**设计意图**和**当前代码的真实状态**，作为后续迭代的基准。

状态标记：✅ 已实现并验证 · ⚠️ 部分实现 · ❌ 未实现 · 🚧 被外部条件阻塞

---

## 1. 总体架构

```mermaid
flowchart TB
    subgraph L1["① 用户接入层"]
        U1["训练用户<br/>Web Portal"]
        U2["开发者<br/>CLI / Python SDK"]
        U3["管理员<br/>平台运维 / 模板管理"]
    end

    subgraph L2["② 训练平台控制面（Portal + API）"]
        WEB["Web UI<br/>任务提交/列表/状态/日志"]
        API["后端 API 服务<br/>认证鉴权 / 项目管理 / 任务编排"]
        TPL["任务模板中心<br/>脚本模板 / 资源规格 / 镜像配置"]
        DB[("元数据存储<br/>PostgreSQL")]
        REDIS[("对象与会话<br/>Redis（可选）")]
        CTRL["任务控制器<br/>创建 RayJob / Dev RayCluster"]
        SSO["SSO / LDAP<br/>企业身份对接"]
    end

    subgraph L3["③ VKE 集群控制层"]
        VKE["Volcengine VKE 托管 Kubernetes"]
        KR["KubeRay Operator<br/>管理 RayCluster / RayJob"]
        LWS["LWS（可选增强）"]
        TOP["Training Operator（按需）"]
        SCHED["调度与资源策略<br/>节点标签 / 污点容忍 / 配额 / 优先级"]
        POOL["GPU 资源池<br/>3 × RTX 4090×8 = 24 卡"]
        DEBUG["调试节点<br/>开发 / 调试 / Jupyter / SSH"]
    end

    subgraph L4["④ Ray 运行时层"]
        HEAD["Ray Head Pod<br/>Driver / GCS / Dashboard"]
        WORKER["Ray Worker Pods<br/>分布式训练执行"]
        DHEAD["Dev Head Pod<br/>JupyterLab / SSH / Ray Client"]
        DWORKER["Dev Worker Pod(s)<br/>交互式调试 / 小规模试跑"]
    end

    subgraph L5["⑤ 存储与数据层"]
        TOS[("火山引擎 TOS<br/>数据集 / Checkpoint / 产物")]
        NAS[("NAS / 文件共享<br/>代码 / 共享数据集")]
        NVME[("本地 NVMe<br/>缓存 / Ray Spill / 临时")]
        REG[("镜像仓库<br/>训练镜像 / 依赖环境")]
    end

    subgraph L6["⑥ 可观测性与运维"]
        ALLOY["Grafana Alloy / Promtail<br/>采集容器与节点日志"]
        LOKI[("Loki<br/>按 Job / Pod 聚合日志")]
        PROM["Prometheus + DCGM Exporter<br/>GPU / Pod / Node 监控"]
        GRAF["Grafana<br/>训练任务监控看板"]
        ALERT["告警通知"]
    end

    U1 --> WEB
    U2 --> API
    U3 --> WEB
    WEB --> API
    API --> TPL
    API --> DB
    API --> REDIS
    API --> CTRL
    API --> SSO
    CTRL --> VKE
    VKE --> KR
    VKE --> LWS
    VKE --> TOP
    KR --> SCHED
    SCHED --> POOL
    SCHED --> DEBUG
    POOL --> HEAD
    HEAD <--> WORKER
    DEBUG --> DHEAD
    DHEAD <--> DWORKER
    WORKER --> TOS
    WORKER --> NAS
    WORKER --> NVME
    HEAD --> REG
    WORKER -.日志.-> ALLOY
    ALLOY --> LOKI
    WORKER -.指标.-> PROM
    PROM --> GRAF
    LOKI --> API
    PROM --> API
    GRAF --> ALERT
```

**训练主路径**：Portal / CLI → API → RayJob → Ray Cluster
**调试主路径**：Portal → Dev RayCluster → Jupyter / SSH

---

## 2. 逐层实现状态

### ① 用户接入层

| 组件 | 状态 | 说明 |
|---|---|---|
| Web Portal | ✅ | Vue 3 + Element Plus，本地账号或 SSO 登录 |
| CLI / Python SDK | ✅ | `rayctl`（[backend/rayctl](../backend/rayctl)）+ Ray Jobs API 兼容网关（[backend/rayapi](../backend/rayapi)），可直接用 `ray job submit` |
| 管理员入口 | ✅ | 角色化导航，非管理员不渲染管理组；后端每个接口独立鉴权 |

### ② 训练平台控制面

| 组件 | 状态 | 位置 |
|---|---|---|
| Web UI | ✅ | [frontend/src](../frontend/src) |
| 后端 API 服务 | ✅ | [backend/api](../backend/api)，统一 Envelope + request id |
| 任务控制器 | ✅ | [backend/k8s/reconciler.go](../backend/k8s/reconciler.go)，outbox + Lease 选主 |
| 元数据存储 | ✅ | PostgreSQL，版本化迁移 [backend/db/migrations](../backend/db/migrations) |
| SSO / LDAP | ✅ | Keycloak OIDC（LDAP 由 Keycloak User Federation 负责，平台不碰 LDAP 密码） |
| 本地账号登录 | ✅ | 与 OIDC 并行，无外部依赖即可使用 |
| **任务模板中心** | ❌ | 未实现。当前提交需手填镜像 digest、commit、资源规格 |
| Redis | ❌ | 未引入。架构图标注为可选，目前无会话/缓存需求 |

### ③ VKE 集群控制层

| 组件 | 状态 | 说明 |
|---|---|---|
| KubeRay Operator | ✅ | 1.3.0，启动时做 CRD 版本与字段校验 |
| 调度与资源策略 | ✅ | Kueue 0.19 准入 + 租户 LocalQueue 自动创建 + `suspend` 门控 + ClusterQueue 配额按节点标签自动对齐 |
| GPU 资源池 | ⚠️ | 代码与配额已按 3×8=24 设计，当前物理只有 1 卡 |
| 调试节点隔离 | ⚠️ | 节点选择器已可配置，但训练/调试尚未拆成两个标签 |
| LWS / Training Operator | ❌ | V1 不做，架构图标注为可选/按需 |

### ④ Ray 运行时层

| 组件 | 状态 | 说明 |
|---|---|---|
| RayJob 批量训练 | ✅ | 已在真实 GPU 上跑通完整闭环 |
| Head / Worker 编排 | ✅ | 均固定在训练节点池，避免落到 serverless 虚拟节点 |
| Dev RayCluster | ⚠️ | 能创建、能起 Jupyter 代理；固定 1 卡，无空闲回收、无快照、无 SSH |

### ⑤ 存储与数据层

| 组件 | 状态 | 说明 |
|---|---|---|
| 镜像仓库 | ✅ | 阿里云 ACR，镜像强制 sha256 digest |
| TOS 对象存储 | 🚧 | 代码完整（预签名上传 + 产物落盘），**当前 Secret 里的 SK 与 AK 不匹配**，TOS 一律返回 `SignatureDoesNotMatch` |
| NAS / IDC PVC | ⚠️ | 挂载逻辑就绪，当前 `IDC_STORAGE_ENABLED=false` 未启用 |
| 本地 NVMe | ⚠️ | 以 emptyDir 提供 `/tmp/ray-spill`，未绑定物理 NVMe，也未配置 Ray spill 策略 |

### ⑥ 可观测性

| 组件 | 状态 | 说明 |
|---|---|---|
| 日志查询接口 | ✅ | [backend/observability/loki.go](../backend/observability/loki.go)，按 `platform_job_id` 生成受控 LogQL |
| 指标查询接口 | ✅ | [backend/observability/prometheus.go](../backend/observability/prometheus.go) |
| **Alloy / Loki 部署** | ❌ | 集群中未部署，日志接口取不到数据 |
| **Prometheus / DCGM 部署** | ❌ | 无 ServiceMonitor、无 GPU 指标采集 |
| **Grafana 看板 / 告警规则** | ❌ | 未编写 |

### ⑦ V1 关键能力对照

| 能力 | 状态 |
|---|---|
| 任务提交 | ✅ 已验证 |
| 状态跟踪 | ✅ 已验证（含失败正确报 FAILED） |
| 日志查看 | ⚠️ 接口就绪，缺 Loki 部署 |
| GPU 资源调度 | ✅ Kueue 准入 + 配额 + 自动回收已验证 |
| 交互式调试 | ⚠️ 基础可用，生命周期管理不全 |
| 可扩容 | ✅ 给新节点打上训练标签即可，配额自动跟随（见 [BUILD_AND_DEPLOY.md](BUILD_AND_DEPLOY.md) 第 8 节） |

---

## 3. 与原架构图的差异说明

架构图本身**没有需要修改的地方**，主体设计全部落地了。当前实现与图的差距集中在三处，都是「图上画了、代码还没做」而不是「设计走偏」：

1. **任务模板中心**（图②）完全未实现——这是新用户上手门槛最高的一环。
2. **可观测性整层**（图⑥）只做了查询侧，采集侧（Alloy/Loki/Prometheus/DCGM/Grafana）一个都没部署。
3. **Redis、LWS、Training Operator** 三个在图上就标注为「可选/按需」，V1 有意不做。

另有一处实现细节与图不同但更安全：图中 Head Pod 未标注节点约束，实际代码把 Head 也固定在训练节点池，避免它被调度到 VKE 的 serverless 虚拟节点（VCI）上导致 GCS 不可达。

---

## 4. 建议的迭代顺序

1. **补可观测性采集侧** —— 装 Loki + Alloy（保留 `platform_job_id` 标签）、Prometheus + DCGM。日志和 GPU 利用率是训练平台的刚需，现在是最大空白。
2. **修复 TOS 凭据** —— 换上正确的 SK，打通数据集/Checkpoint 读写。
3. **任务模板中心 + 简化提交表单** —— 让工程师不必手填 digest 和 commit。
4. **任务事件时间线与产物归档** —— `job_events` / `job_artifacts` 两张表已建但无代码读写。
5. **调试工作区生命周期** —— 空闲 TTL 回收、快照、转训练任务。
6. **CI 增强** —— 目前 CI 只构建镜像，不跑 `go test` / `npm run build` / `helm lint`。
