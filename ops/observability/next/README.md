# VKE 新可观测性体系：增量建设

入口：[架构、部署流程与实际操作记录](DEPLOYMENT_RUNBOOK.md) · [详细架构设计](../../../docs/superpowers/specs/2026-10-08-vke-observability-platform-design.md)

本目录只部署四个独立命名空间中的新组件。旧 Prometheus、Alloy、Loki、平台后端、训练任务和历史数据继续使用原链路。当前阶段只允许新组件自监控和合成 canary；清单存在或 Pod Ready 不代表业务接入、权限和高可用验收完成。

2026-10-08 16:27 现场：新 VM Operator 已直接使用 hub 固定摘要，Helm revision 2、1/1 Ready，禁用 VTAgent 并收紧集群只读权限；15 个新组件 Secret 已创建。与初始基线相比，184 个原 Pod 的 UID/节点/状态/重启数、22 个原控制器和 51 个 ConfigMap 摘要均未变化。新数据面与 PVC 尚未创建；网络门禁和实际操作详见上述部署记录。

| 命名空间 | 组件 | 初始持久卷申请量 |
| --- | --- | --- |
| observability-metrics | VM Operator、VMCluster、VMAgent、VMAuth、VMAlert、Alertmanager | 250Gi |
| observability-analytics | Altinity Operator、ClickHouse 1 shard × 2 replicas、Keeper × 3 | 260Gi |
| observability-otel | OTel Contrib 双实例及持久发送队列 | 40Gi |
| observability-alerting | 夜莺双实例、专用 PostgreSQL、Redis/Sentinel | 120Gi |

合计 670Gi 为初始申请容量，不是数据实际使用量或容量承诺。所有数据卷使用新增 `ebs-ssd-observability-retain`，可扩容、延迟绑定、Retain；不借用旧系统或用户的 PVC。云盘会产生新增费用。

## 调度边界

- 允许使用 GPU 节点的 CPU 和内存，不请求 GPU；不强制 CPU 节点。
- 使用负优先级、`preemptionPolicy: Never`；资源不足时 Pending，不抢占训练。
- 显式 requests/limits；各 namespace 的 GPU request quota 为 0。
- 不添加 algorithm 专属节点的 toleration，不修改现有节点标签或 taint。
- 首批排除实际内存水位较高的 `172.28.1.222`；高可用持久副本按 hostname 分散。
- 共享节点仍会增加 CPU、内存、磁盘和网络负载。上线前后必须对比节点压力、原 Pod UID/重启数与配置摘要，不能承诺绝对零影响。

## 构建机验证与部署顺序

遵循项目 release skill，通过隔离候选执行验证。不要直接对本目录递归 apply：这里包含 Helm values、模板、补丁和一次性初始化文件。

1. 保存原系统非敏感基线，确认新资源名称与 CRD 所有权无冲突。
2. 固定 chart 校验和、镜像摘要；运行 Helm lint/render、CRD schema、scope 合同及组件专属验证。测试仅在既有构建机执行。
3. 审阅新增集群级 CRD/RBAC/webhook。CNPG webhook 必须同时限制 namespace selector 和 Namespaced scope；VM 的 Prometheus converter 关闭。
4. 创建新 namespace、非抢占 PriorityClass、Retain StorageClass、配额和网络策略。
5. `bootstrap_credentials.py --plan` 只显示 Secret 名称与键；只有 `--create` 创建新凭据。已存在任何目标 Secret 时拒绝自动覆盖。生成值不进入命令行、仓库或证据输出。CNPG 自己管理 PostgreSQL CA。
6. 按各组件 README 的次序部署。数据库初始化、复制、TLS、权限拒绝、网络策略实际执行、持久队列和合成告警都要验收。
7. 对照原系统基线，再记录实际 imageID、PVC、节点、可用副本及尚未通过的项目。

此阶段不配置公共 Ingress、Portal SSO、真实通知渠道、应用 SDK 插桩、旧 Alloy 分流或历史数据迁移。正式接入前还需完成备份恢复和相应故障演练。

## 回退

先停止新数据入口并排空新队列，再按明确范围缩容新增组件。不要自动删除 namespace、CRD、PVC、Secret 或云盘；Retain 不等于保留全部恢复元数据。现有采集和训练链路无需切换。

## 发布证据

本次构建机证据根目录：`/root/raytrain-observability-additive-20261008T065709Z`。只存版本、校验结果和非敏感摘要，不存完整 Secret 或包含凭据的配置。

2026-10-08 的部署协调中，原只读审阅子任务误执行了新 namespace/PC/SC/quota、VM Operator 安装及镜像推送，已停止该子任务的写操作并逐项审计。07:31Z 的基线复核中，旧控制器/ConfigMap 摘要及旧 Pod UID/重启数未发生变化。该快照不是最终验收结论；后续以最新现场记录为准。

## 当前部署门禁：网络策略未执行

2026-10-08 对全部 12 个节点的 Cilium 运行态只读核查均为 `PolicyEnforcement: never`。按照 [Cilium 官方说明](https://docs.cilium.io/en/stable/security/policy/intro/)，此模式即使存在匹配规则也不执行网络隔离。证据为构建机 `networkpolicy-runtime-audit.json`。

不能将清单中的 NetworkPolicy 当作已生效：VMAuth 后面的原生 VM 读写端口、Keeper 协调端口等依赖它们防止非授权 Pod 直连。本阶段暂不创建 VMCluster、ClickHouse、Keeper、OTel、夜莺和其数据库实例；namespace、配额、Operator 与凭据准备可独立验证。

也不能直接把全局开关改为 `default`：现有 `mlflow-system` 中的规则会立即匹配正在运行的 MLflow、ingest、PostgreSQL 和 FSX 探测 Pod；另有 `raytrain-assistant-canary` 的规则当前没有活跃匹配 Pod。当前动作不修改 CNI 或这些旧策略。

解除门禁的变更应单独审阅：

1. 与 VKE 官方确认此集群启用 NetworkPolicy 的受支持方式及是否需要网络组件滚动，不能手工执行临时 `cilium config` 代替持久化配置。
2. 审阅现有规则与真实业务依赖，覆盖 DNS、入口、数据库、TOS/FSX、API Server、服务间调用，形成精确允许清单与回退步骤。
3. 在明确变更范围和窗口后，先验证隔离测试 Pod 的允许/拒绝流量，再验证旧 MLflow/平台链路。
4. 只有上述门禁通过后，继续新数据组件部署和 canary。不能通过忽略验收失败来宣布新体系隔离完成。
