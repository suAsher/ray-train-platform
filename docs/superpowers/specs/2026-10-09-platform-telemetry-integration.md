# 训练平台业务接入：指标、日志与调用链

本文件描述 2026-10-09 代码核对后的接入合同。首波 exporter 接入的线上状态以 [部署记录](../../../ops/observability/next/DEPLOYMENT_RUNBOOK.md) 为准；下文应用埋点尚未开发，不能据此宣称已上线。

## 现有业务如何接进来

1. **GPU/节点/工作负载：** 复用现有 DCGM、node-exporter、kube-state-metrics，VMAgent 旁路接入 VMCluster。无需改训练镜像、提交命令或用户代码。
2. **平台实时日志：** 继续由现有 Alloy → Loki → 后端鉴权查询提供；新增自监控发现采集/拒收/传播延迟故障。ClickHouse 不接管实时日志。
3. **应用性能：** 在后端与同步 Worker 的受管代码增加 Prometheus 指标、结构化事件和异步 OTel SDK。需要后端/Worker 的测试与发布；Collector 部署不会自动产生完整调用链。
4. **分析存储：** 在已有 OTel/ClickHouse 基础上另建正式 pipeline、表、身份、字段白名单与保留期。现有 `synthetic-canary` 管道不直接改成接收所有生产属性。
5. **展示与告警：** Grafana 首批使用原 Prometheus；新增 VM/CH 数据源须单独只读身份。正式告警统一夜莺，通知渠道/接收人由用户在 UI 配置；规则逐组交接，避免与原规则重复通知。

## 指标与埋点位置

| 业务环节 | 最小代码入口 | 指标与口径 | 日志 / Trace 属性 |
| --- | --- | --- | --- |
| API 请求 | `backend/main.go` Gin middleware，沿用 request ID | 请求数、耗时直方图、in-flight；method、路由模板、status_class | request_id、trace_id、span_id、受控错误码 |
| 提交任务 | `backend/api/submission_service.go` Submit/prepareSubmission | 提交次数与分阶段耗时；origin、engine、result、有限 error_code | job_id，镜像解析/数据权限/挂载准备/DB/Kubernetes 子 span |
| 控制器 | `backend/k8s/reconciler.go` ProcessOnce/processEvent/ReconcileJob | reconcile 耗时/错误、outbox backlog、leader 状态 | job_id、attempt；每次处理短 span，重试的有限 reason |
| 任务状态 | `backend/repositories/jobs.go` ApplyObservedState | 当前 state/engine 聚合；事务成功且真实状态变化后才计一次 | from/to、job_id、cluster_attempt、reason_code |
| 同步调度 | `backend/storagesync/controller.go` claimQueued/reconcileRun | active/queued、dispatch wait、失败率；phase/source_kind/mode/result | plan_id、run_id、attempt、generation、phase |
| 同步传输/校验 | `images/storage-sync/storage_sync/engine.py` execute；worker 上报；后端 report 落库 | bytes/files/verified/failed/network_bytes 分开；差量须防重复回执 | scan/plan/transfer/verify 短 span，正常逐文件事件抽样 |
| 重试/恢复控制 | `backend/storagesync/control.go` | control_total 按 retry/resume/pause/cancel/result，成功提交动作后计数 | run_id、attempt、generation、action |
| Worker callback | `images/storage-sync/storage_sync/transport.py` | callback 耗时/错误，operation 有限枚举 | traceparent 传播；不记录 callback token |
| 外部依赖 | DB/Kubernetes/TOS/MLflow/Loki/Prom 客户端 | dependency/operation/result、耗时 | 脱敏错误、短子 span；不记录 SQL 参数、签名 URL 或对象路径 |

## 容易误导的业务口径

- `StartedAt - CreatedAt` 是提交到运行总等待，包含校验、调度、建 Pod 等；模型没有可靠独立 QueuedAt/AdmittedAt，不能标成纯 Kueue 排队时间。纯队列耗时需接 Kueue 真实指标或补状态时间来源。
- 同步 `Attempt` 不是重试次数。正常 PREVIEW → TRANSFER、resume、retry 都会增加 attempt；不能用 attempt−1 计算 retry。
- 当前 TOS SDK `max_retry_count=0`，engine 没有通用内部重试循环；不能展示不存在的 SDK 重试数。
- callback 上报累计进度且带 sequence/generation/attempt 防重放。不能每次 heartbeat 都把累计 bytes 再加进 counter。优先从提交后的数据库状态聚合 gauge；若用 counter，必须定义单调差量、跨副本及重启语义。
- `completedBytes` 与 `networkBytes` 不同。TOS 服务端复制的逻辑完成字节不是 Worker 网卡吞吐；复用、传输、校验应分别显示。
- 两个 HA exporter/collector 副本的抓取数、Loki 多副本拒收数，不直接等于业务唯一事件数。

## 字段、权限与开销

- 通用平台指标只用有界维度。job_id/run_id/user_id/username/request_id/trace_id/object_key/path/image_digest 放日志或 Trace 属性，不新增为通用 Prometheus 标签。团队维度另评数量上限与查询权限。
- API 按路由模板统计，不用带 ID 的原 URL；不采 Authorization、Cookie、请求体、query、数据库参数或 TOS 签名 URL。先在应用脱敏，再入遥测队列。
- 每个业务身份使用专用叶证书和写权限，不能复用 canary/admin 私钥；可信 job/team 标签由平台侧决定，不信任客户端自行声明。
- Grafana 文件夹/变量不构成数据隔离。运维数据源用只读身份；普通用户仍通过平台后端按成员/团队/任务归属查询。

## 异步工作与故障隔离

不创建持续数天的“整个训练 span”。HTTP 提交完成后结束请求 trace；outbox、reconcile 和 Worker 各阶段使用短 trace/span，通过 job_id 与 span links 关联。现有 outbox 只存 job_id，需要明确保存必要 trace context 后才有可靠跨异步关联。

OTel 使用有界异步队列、批处理、有限超时、队满丢遥测及丢弃自监控；不在事务或持锁区间 export，关停 flush 有上限。遥测不可作为业务状态回执。现有 Reporter 的 callback 失败暂停是正确性协议，OTel 导出失败不得进入该暂停分支。

先验证 Collector/CH 中断、队满、证书拒绝、进程重启和恢复时业务延迟/成功率无明显退化；指标、日志、trace 三种遥测故障独立判断。Alloy 的精选日志分支需单独验证回压隔离，不能一开启全量复制就认为与 Loki 无关。

## 两批代码交付与验收

**下一批：** 后端 HTTP、依赖、控制器指标与结构化事件。补 `/metrics` 及限定采集身份/访问范围；关键 handler 与路由标签回归、真实 PostgreSQL 状态变更验证、遥测失败测试、全 Go 门禁后发布。核对请求/错误/耗时与受控调用一致，原提交和日志查询不变。

**再下一批：** Worker 阶段与差量指标、异步 Trace 关联、正式 OTel/CH pipeline 和 job 页面跳转。验证重复/乱序/旧 generation callback 不重复计数、PREVIEW/TRANSFER 不误判 retry、TOS→TOS 不伪造网卡吞吐。单独构建发布 Worker，用户提交命令不变。

## 夜莺与访问入口的真实状态

本轮两个夜莺实例已通过自身内部只读代理查询真实 GPU72/节点12 指标成功；浏览器入口、原生登录访问交付、SSO 映射和通知仍为后续工作。当前配置为 ClusterIP 17443 + 专属 UI gateway mTLS，原生 HTTP 仅回环监听，并保留原生登录；仓库尚未配置面向用户浏览器的外部 Ingress、Portal SSO 或角色映射。

因此“后续 UI 配通知”还需交付正式访问入口。入口需沿用既有认证或原生管理员登录，经专属 UI gateway 身份访问；不能为了方便把整个夜莺或 VM 原生接口直接暴露。通知实际触发/恢复可达后，才能完成证书轮转失败的通知闭环，并另行解除已暂停的周期轮转任务。
