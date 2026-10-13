# VKE 新可观测性体系：增量建设

入口：[部署文件、配置 UI 与日常运维手册](OPERATIONS.md) · [架构、部署流程与实际操作记录](DEPLOYMENT_RUNBOOK.md) · [详细架构设计](../../../docs/superpowers/specs/2026-10-08-vke-observability-platform-design.md)

**日常维护先读 `OPERATIONS.md`。** 它集中列出实际 Helm release、Chart/values/Kustomize/清单位置、配置保存边界、升级与回滚顺序，以及本轮临时 NodePort 和资源盘点的发布记录。当前并非所有组件都有 Helm release；各产品 README 中仍有早期 namespace 与 bootstrap 示例，不能直接递归 apply 或照抄历史安装命令。生产 VM 已通过定向 patch 启用首波业务指标，下次 VM Helm 升级须显式保留 `businessScrape.enabled=true`；独立 Grafana 的数据库、数据源与临时入口切换按运维手册处理。

**2026-10-13 业务接入进展：** 已新增 `grafana` 命名空间：独立 Grafana 2 副本、专用 PostgreSQL 3 实例和独立作用域 CNPG Operator 2 副本；新增 3 块 20Gi Retain 卷。VM release revision 2 已保留业务采集并新增独立 Grafana 只读身份；`victoriametrics/platform-telemetry` revision 1 管理后端私有 9091 mTLS 指标端点与采集声明。后端 `ray-platform` revision 274 的两副本已就绪，真实 HTTP 请求指标可从 VM tenant 1001 读回；正式 OTel → ClickHouse `raytrain_observability` 管道已部署，历史 canary 与原 Loki 链路保留。**Grafana 四类数据源已在两个实例通过；持久化恢复及用户入口仍在最后验收，31701 暂未切换。** 后续结果以[本轮记录](DEPLOYMENT_RUNBOOK.md#2026-10-13-后端-http-遥测与独立-grafana-ha)为准。

当前只读资源快照（2026-10-13 11:43:32 北京时间）：八个观测 namespace 共 **79 个常驻 Pod / 79 Ready，32 PVC / 1130Gi，requests 19.54 CPU / 43.16Gi，GPU 申请 0**。旧原生组件有未设置 limits 的容器，不能将有限 limits 求和当作总上限；新增量仅 Grafana 组 7 Pod、2.3 CPU / 4.75Gi 和 60Gi。不包含平台后端、共享 DCGM 或 TOS 对象容量。该快照早于最终入口验收。

以下 2026-10-09 记录及架构图保留为历史检查点，尤其其中“数据源待接、后端 SDK 尚未实现、源码未同步 main”不能代表 10 月 13 日的最新状态；当前维护关系见 `OPERATIONS.md`。

展示与接入：[高清架构图](../../../docs/architecture/raytrain-observability-vke-20261009.png) · [可编辑 SVG](../../../docs/architecture/raytrain-observability-vke-20261009.svg) · [缩放展示页](../../../docs/architecture/raytrain-observability-vke-20261009.html) · [首波实施计划](../../../docs/superpowers/plans/2026-10-09-observability-business-wave1.md) · [应用指标与 Trace 接入合同](../../../docs/superpowers/specs/2026-10-09-platform-telemetry-integration.md)

**2026-10-09 临时 UI 已上线：** 独立 Helm release `nightingale/observability-ui` revision 1（源码 `12a9866`）新增 2 个轻量代理、无 PVC。夜莺 `https://172.28.2.65:31700/`、Grafana `https://172.28.2.65:31701/`，备用节点为 `172.28.1.232` 同端口；先按 [UI 说明](ui-access/README.md)信任公开 CA，使用公司内网 / VPN 访问。33 项现场检查通过，Grafana真实登录 / 看板 / GPU72 / WebSocket通过；夜莺页面、验证码和认证拒绝通过，新 NodePort 的真实验证码登录本轮未执行。2536 个存量保护对象无差异。具体入口、维护、资源和原 ALB 关系见 [OPERATIONS.md](OPERATIONS.md)。

2026-10-09 后续决定：夜莺统一负责正式告警，用户后续在 UI 配置通知渠道。VMAlert 优先 recording rules，新增 Alertmanager 保留未接正式通知。临时浏览器入口保持原应用登录，未接 Portal SSO；基础设施部署不等于业务埋点或通知已经接通。Grafana 当前可重复恢复的 provisioning 仅有 Prometheus / Alertmanager，Loki Explore、VM、ClickHouse 数据源仍待接入，平台原 Loki 日志链路保持。

扩展依据：[保留 Alloy/Loki 的架构与容量方案](../../../docs/superpowers/specs/2026-10-09-observability-expansion-design.md)。用户已明确现有 Alloy/Loki 长期保留；指标、日志与追踪分开建设，扩容按实测瓶颈推进。

**首波真实指标已经接入：** 最终候选 `a88f4d0` 319项测试通过，新增15个监控/看板声明、VMAgent两项采集配置；27个端点全部UP、VM中72GPU/12节点/后端2Pod数据核对通过，双夜莺内部只读代理也查询真实GPU指标成功。原Prom双实例89/89UP，Grafana新增“RayTrain · 日志链路与 GPU 业务”看板，22查询通过，两实例文件哈希一致。没有增加常驻Pod/PVC或升级业务应用。2473存量保护对象仅预期VMAgent变化1项。详见[实际部署与验收](DEPLOYMENT_RUNBOOK.md#2026-10-09-首波业务指标与日志自监控接入)。新自监控已揭示持续的旧日志回读拒收，不能将采集上线等同于日志零丢弃。

**2026-10-09 当前状态：** `victoriametrics`、`clickhouse`、`opentelemetry`、`nightingale` 已部署，`cert-manager` 管理新身份叶证书。VM 182 项、夜莺应用 44 项/依赖 46 项及 CH/OTel 双副本精确读回通过，轮换后复验再次通过。PostgreSQL 和 VMStorage 实际续期没有替换 Pod；另外六类叶证书真实续签、13 Pod 受控轮换和随后无变更复验通过，三类签发负例通过并清理。最终候选 `741b7f2` 296 项单元测试通过；运行配置 `4768871` 的 7 条真实渲染链通过。原生组件持续轮转 CronJob 保持 suspended，待失败通知闭环；新 Alertmanager 仍为 blackhole-canary。15 个临时验收对象已按 UID 清理。

旧四个 `observability-*` 命名空间及其专属资源已于 10 月 9 日按用户授权退场，21 块旧 EBS 云盘已由实际 CSI 驱动确认删除。旧 PG 的已验证备份和私有配置清单保留，旧 VM/ClickHouse 合成历史未做数据归档，不能再依赖旧卷回退。原业务链路未迁移；首波exporter旁路已接入，SSO、应用埋点和正式告警接收仍待接入。部署归属、镜像来源、证据及未验范围见[10 月 9 日部署记录](DEPLOYMENT_RUNBOOK.md#2026-10-09-正式命名部署与验收记录)及[旧环境退场记录](DEPLOYMENT_RUNBOOK.md#2026-10-09-旧命名空间退场与云盘释放)。下面的 10 月 8 日表格、容量与数字是历史记录，旧命令不能直接用于新位置。

**2026-10-09 16:43:36 北京时间资源快照：** 新套件含 UI 代理共 **45 Pod、21 PVC / 670Gi**；加上原 `monitoring` / `loki` 后，七个观测 namespace 共 **72 Pod、29 PVC / 1070Gi**，其中 150Gi 是旧 Loki 保留卷。requests 合计 **17.24 CPU / 38.41Gi**，实际采样 **1.189 CPU / 17.31Gi**，GPU requests 为 0；未设置 limits 的原容器使有限 limits 之和不能当作全局硬上限。这里不含 `kube-system` DCGM、其他系统或 TOS 对象容量，详见运维手册口径。旧新增套件的 35 个常驻 Pod 和 21 块云盘此前已清理并释放 670Gi，不与本次保留的原业务监控混淆。

本目录部署四个产品命名空间的新组件，以及独立 `cert-manager` 控制层。旧 Prometheus、Alloy、Loki、平台后端、训练任务和历史数据继续使用原链路。最初阶段限自监控和合成 canary，现按显式 `business-wave1` 范围接入五组真实目标；这不代表全业务接入或完整故障恢复验收。

2026-10-08 后续检查点：VM 组件 release revision 3、Operator revision 5，17 个组件 Pod 就绪，现场功能与身份边界复验通过。ClickHouse 两副本、Keeper 三副本、OTel 双副本、PostgreSQL 三副本、Redis/Sentinel 三副本及夜莺双副本均已启动。CH/OTel 已通过现场身份拒绝和合成日志/trace 双副本查询；另在隔离 Docker 中通过真实 gRPC Export 和 Collector SIGKILL 后的持久队列恢复。夜莺完整应用验收 44 项、依赖端口 46 项、PostgreSQL SQL 认证 27 项均通过，CNPG webhook 来源限制及真实 API Server admission 单独验证通过。完整合同测试为 174 项通过。全部 21 个新 PVC 已 Bound，合计 670Gi。该2026-10-08检查点当时尚未接业务数据或真实通知；各项环境、证据和后续交接门禁以部署记录为准。

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
5. 新位置叶证书由 cert-manager 签发，PostgreSQL 也引用受管服务端证书和明确 CA。非 PKI 凭据按受审映射经内存/stdin 创建；`bootstrap_credentials.py` 和旧 `mtls/credentials.py` 的创建路径属于历史一次性流程，不覆盖重跑。值不进入命令行、仓库或证据输出。
6. 按各组件 README 的次序部署。数据库初始化、复制、mTLS 身份与路径拒绝、原生端口不可绕过、持久队列和合成告警都要验收。当前 CNI 不执行 NetworkPolicy，不能把策略清单当作隔离证据。
7. 对照原系统基线，再记录实际 imageID、PVC、节点、可用副本及尚未通过的项目。

此阶段不配置公共 Ingress、Portal SSO、真实通知渠道、应用 SDK 插桩、旧 Alloy 分流或历史数据迁移。正式接入前还需完成备份恢复和相应故障演练。

## 回退

先停止新数据入口并排空新队列，再按明确范围缩容新增组件。不要自动删除 namespace、CRD、PVC、Secret 或云盘；Retain 不等于保留全部恢复元数据。现有采集和训练链路无需切换。

## 发布证据

10 月 9 日证据根目录：`/root/raytrain-observability-certmanager-20261009T014000Z`；10 月 8 日历史根目录：`/root/raytrain-observability-additive-20261008T065709Z`。可共享的报告只含版本、校验结果和非敏感摘要。恢复用的数据库与配置私有备份另按 0700 目录/0600 文件保护，可能包含凭据，不能将证据根目录整体上传、粘贴或纳入 Git。

2026-10-08 的部署协调中，原只读审阅子任务误执行了新 namespace/PC/SC/quota、VM Operator 安装及镜像推送，已停止该子任务的写操作并逐项审计。07:31Z 的基线复核中，旧控制器/ConfigMap 摘要及旧 Pod UID/重启数未发生变化。该快照不是最终验收结论；后续以最新现场记录为准。

## 当前部署门禁：应用层 mTLS 验证

2026-10-08 对全部 12 个节点的 Cilium 运行态只读核查均为 `PolicyEnforcement: never`。按照 [Cilium 官方说明](https://docs.cilium.io/en/stable/security/policy/intro/)，此模式即使存在匹配规则也不执行网络隔离。证据为构建机 `networkpolicy-runtime-audit.json`。

用户已选择保持 CNI，改造新增组件的 mTLS 代理。具体身份、端口与验收矩阵见 [mTLS 设计](../../../docs/superpowers/specs/2026-10-08-observability-mtls-design.md) 和 [实施计划](../../../docs/superpowers/plans/2026-10-08-observability-mtls.md)。VM/ClickHouse/Keeper/OTel/N9E 使用原生回环监听与显式代理；AM gossip、Redis 和 PostgreSQL 使用各自的原生 TLS 与专用信任域。通过协议与身份负向测试的组件组才进入部署。

这提供应用数据与管理接口的身份保护，不会让 Pod 网络不可达，也不能替代网络层抗连接洪泛。NetworkPolicy 清单仅为后续启用时的配置草案，不作为本次已生效的边界。

也不能直接把全局开关改为 `default`：现有 `mlflow-system` 中的规则会立即匹配正在运行的 MLflow、ingest、PostgreSQL 和 FSX 探测 Pod；另有 `raytrain-assistant-canary` 的规则当前没有活跃匹配 Pod。当前动作不修改 CNI 或这些旧策略。

若后续另行启用 NetworkPolicy，应单独审阅：

1. 与 VKE 官方确认此集群启用 NetworkPolicy 的受支持方式及是否需要网络组件滚动，不能手工执行临时 `cilium config` 代替持久化配置。
2. 审阅现有规则与真实业务依赖，覆盖 DNS、入口、数据库、TOS/FSX、API Server、服务间调用，形成精确允许清单与回退步骤。
3. 在明确变更范围和窗口后，先验证隔离测试 Pod 的允许/拒绝流量，再验证旧 MLflow/平台链路。
4. 验证网络策略与应用层 mTLS 同时生效；不得因已有 mTLS 而省略旧业务连通性检查。
