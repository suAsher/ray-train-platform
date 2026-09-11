# 现网排障链路

## 顺序

始终自外向内、先只读后变更，每一层保留 request ID、JOB ID 和时间范围：

```text
spk-rayjob / Portal
  → DNS、TLS、Ingress、OAuth2 Proxy
  → Backend 路由与身份/PAT
  → 成员、团队、镜像、配额与数据合同
  → Kueue Workload / ClusterQueue / ResourceFlavor / Topology
  → RayJob / RayCluster / Head / Worker
  → 节点、GPU、镜像、存储、NVMe
  → 日志、指标、MLflow、Artifact
```

不要看到 Pending 就重启 Pod。先区分：提交前预检拒绝、Kueue `Suspended`、Pod `Pending`、镜像拉取、挂载或训练进程本身。

## 快速判断

| 现象 | 先查 | 含义/边界 |
|---|---|---|
| `INVALID_AUTHENTICATION` / 401 | `spk-rayjob login-check`、PAT 过期/团队绑定、OAuth2 issuer/audience | 401 表示路由通常已注册；不要换成 GitLab token |
| Portal 404 | 请求 URL 是否多/少 `/raytrain`，Ingress 是否误吞 SPA 路由 | 先分辨 SPA 404 还是 API `JOB_NOT_FOUND` |
| `IMAGE_NOT_ALLOWED` | 镜像登记、团队可见性、engine 能力、tag/digest | 超级管理员可跨团队登记；训练用户只看当前团队/全平台镜像 |
| `GPU_QUOTA_EXCEEDED` | 团队配额、已用 GPU、活跃调试环境 | 在 RayJob 创建前拦截；不是 Pod 调度故障 |
| RayJob 长期 `Suspended` | Kueue admission reason、ClusterQueue、ResourceFlavor、Topology | 任务要求 hostname topology 但 flavor 没关联 topology 时永远不会准入 |
| Pod Pending | events 中 insufficient GPU/CPU/memory、affinity/taint、PVC | 只在 Kueue 已准入后进入这一层 |
| 小任务造成 GPU 碎片 | Worker 实际节点布局、TAS/bin-pack、团队节点池 | 多机训练需每个 Worker 整体可放置，不只看总空闲 GPU |
| 上传 413 | 浏览器是否走分片路由、Ingress body size、单片大小 | 无上限指分片串行/可恢复，不是单 HTTP body 无限 |
| `could not persist resumable upload state` | 上传状态库写入、upload ID、浏览器重试是否重用会话 | 先修状态持久化，不要反复重传已成功分片 |
| 调试环境 Pod 正常但页面 401/404 | `/access` 票据、HttpOnly Cookie、代理路由是否被通用 auth 提前拦截 | 不向用户暴露 kubeconfig 或公共 shell |
| MLflow 直链失败 | job 的 `run_id`、Portal `/raytrain/mlflow/` 前缀、Dashboard 会话/PAT API | Dashboard Cookie 与 MLflow API PAT 是两种通道；不用 Dashboard URL 充当程序 API |
| UI loss/性能“暂无数据” | 训练是否写 MLflow metric、后端是否绑定 run/job、GPU sampler 是否有时序 | Ray Data 与 loss 上报无直接因果；先找缺数据的那一环 |

## 队列与异构 GPU

- 用户不显式选 GPU 型号；后端根据 PAT 所属团队的调度策略选择 accelerator class、ClusterQueue 和 ResourceFlavor。
- 节点要有真实 accelerator 标签、平台 GPU pool/团队标签与 cache-ready 状态。A100/A800/H20/4090 不能混成一个 WorkerGroup。
- 闲时任务必须是单 Worker、`opportunistic` 且 `preemptible`，并能从 Checkpoint 恢复；团队正常/生产任务可回收借用额度。
- 不用节点物理绑定代替配额。团队节点池防止异构混调，Kueue 配额/优先级/抢占负责利用率。

## 改动前后的安全检查

1. 记录现有活跃 RayJob、RayCluster、Workload 和平台 Deployment Pod UID/restart count。
2. 排障时默认只读；需要停任务、删资源、改队列或改节点标签时单独核对授权与精确目标。
3. 发布后确认旧任务 Pod UID 没有变、restart count 没增加，新 backend imageID 等于候选 digest。
4. 删除辅助 debug Pod/临时 worktree，保留 values 备份、Helm 覆盖、提交转录和最终版本核对证据。
