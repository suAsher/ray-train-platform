# 新增 VictoriaMetrics 集群：第一阶段

本目录只拥有 `observability-metrics` 内的新观测资源。现有 Prometheus、Alloy、Loki、Grafana、exporter、训练任务和节点配置均不在这个 release 内。首期仅抓本目录新增组件的自监控指标；不读取旧 TSDB、不向旧 Prometheus 增加 remote_write、不导入历史数据、不安装重复 exporter、不接管 PrometheusRule。

这保证部署差异可限制在新增对象，但共享节点 CPU、内存、网络、API Server 与 EBS 仍有开销，不能据此宣称绝对零影响。上线前后须对比训练 Pod UID、重启数、状态和节点资源压力；资源不足时让新增 Pod Pending，不抢占现有训练。

## 版本与文件

- `operator-values.yaml`：官方 standalone chart `victoria-metrics-operator` **0.68.1**，operator **v0.75.0**。
- `operator-cluster-read.yaml`：补齐 standalone chart 在单 namespace watch 模式下仍生成的同名 ClusterRoleBinding，且只授予集群只读发现权限。
- `resources/`：无外部依赖的小型 Helm chart，生成显式 VM CR、命名空间内 RBAC、内部 Service 和 NetworkPolicy。
- `versions.yaml`：官方版本来源。VM 候选使用 **v1.153.0**；集群镜像使用 `-cluster` 后缀。Alertmanager **v0.34.1**，config-reloader **config-reloader-v0.75.0**。Operator 保持 **v0.75.0**，VM 镜像在 CR 中显式指定，不能把 Operator 默认值当成实际运行版本。
- `resources-hub-values.yaml`：现有 Harbor Docker Hub 代理的固定 amd64 摘要覆盖，可用 `-f` 叠加在 `resources/` chart。只引用 `harbor-registry` 拉取 Secret，不包含凭据。构建机已能拉取这些镜像并执行帮助/配置检查；Docker Hub 直连当时超时，因此代理可达不能等同于完成独立上游摘要比对。
- `validate_component_contract.py`：构建机对两个 Helm 渲染文件验证 converter/watchNamespace、self-only 采集、Secret/路由权限、canary/黑洞通知边界；不访问 Kubernetes，不代替 CRD schema 或实际 Pod 验证。

安装前在构建机下载锁定 chart，记录包 SHA256，核对其 `Chart.yaml`、依赖及镜像。镜像拉取验证后记录 digest，并通过独立受审阅的 values 覆盖为现有镜像镜像源的摘要引用；不把未核对的 `latest` 带入发布。API 以锁定版源码和 CRD schema 为准，文档主分支只是辅助。

Operator 候选也直接引用 Harbor `hub` 代理的固定 amd64 摘要，不需要再次推送相同镜像。发布协调者负责核对实际镜像、拉取 Secret 的来源和本次受审阅快照，任何新覆盖均重新渲染验证。

v1.153.0 包含 native import 的 zstd 内存限制修复；官方同时记录 pure-Go 构建的数据块解压回归。本候选锁定官方 **linux/amd64 CGO** 镜像，官方发布说明明确此架构不受该回归影响，不能将相同版本直接用于 `-pure` 或其他受影响架构。首期不开启新增 OIDC、storage 直接写入 API 或其他可选能力；验证仍以实际参数、规则和生成配置为准。

## 当前候选与运行边界

截至 2026-10-08 16:27，现场只有新增基础对象、凭据和 Operator，VMCluster/Agent/Auth/Alert/AM 数据面未创建。新 Operator 已升级到 Helm revision 2，直接使用 hub 的 v0.75.0 固定摘要、禁用 VTAgent，并应用收紧的 ClusterRole；实际 1/1 Ready、重启 0，节点 `172.28.1.150`。两个 VM token Secret 已创建，不应覆盖重建。完整证据见 [部署记录](../DEPLOYMENT_RUNBOOK.md)。

现场 CNI 的 NetworkPolicy enforcement 尚未开启。用户要求先保留现状，因此**不启动依赖隔离的数据面、不扩权绕过网络边界**；新增 Operator 本身的已审阅升级与凭据准备已单独完成。网络能力完成独立评审并实际验收后，重新对比源码、渲染与现网差异，再执行下面的数据面部署顺序。不能仅凭 NetworkPolicy YAML 已存在就报告网络隔离有效。

## 预置对象与身份

父层创建本目录依赖的 namespace `observability-metrics`、PriorityClass `raytrain-observability-additive`（`value: -1000`、`preemptionPolicy: Never`）及新 StorageClass `ebs-ssd-observability-retain`。StorageClass 继承已有 EBS ESSD_PL0/WFFC/扩容参数，`reclaimPolicy: Retain`；旧 `ebs-ssd` 不修改。

由发布协调者在该 namespace 安全创建两个不同随机值的 Secret，仅通过文件或 API 内存传递，禁止打印 token、写进 Git 或放在 shell argv：

| Secret | Key | 用途 |
| --- | --- | --- |
| `vm-self-write` | `token` | VMAgent、VMAlert 向隔离租户 1001 写入 |
| `vm-self-read` | `token` | VMAlert、专用验收客户端及新夜莺只读查询 |

VMUser 只引用 Secret。VMAuth 不配置 anonymous/unauthorized 用户。读入口是 `http://vmauth-raytrain-next.observability-metrics.svc:8427`；写入口为同域 `/api/v1/write`。账号路由固定到 `/select/1001/prometheus` 或 `/insert/1001/prometheus`，客户端不能选择租户；读账号不开放导出、导入、管理、快照或删除路径。写账号仅开放 `/api/v1/write`。

夜莺只获得读 token 的受控副本 `observability-alerting/n9e-vm-read`，Pod 必须有 `observability.raytrain.io/metrics-client: "true"`。同 namespace 验收 Pod 也使用该标签。NetworkPolicy 不向这些客户端开放 vmselect/vminsert 直连。跨 namespace 客户端自己的 egress 策略还需要允许到 VMAuth 8427。

## 初始规模与调度

| 组件 | 副本 | 每副本 request CPU / 内存 | 每副本 limit CPU / 内存 | 数据盘 |
| --- | --- | --- | --- | --- |
| Operator | 1 | 100m / 192Mi | 500m / 512Mi | 无 |
| VMStorage | 3 | 250m / 512Mi | 1500m / 2Gi | 50Gi ×3 |
| VMSelect | 2 | 200m / 512Mi | 1500m / 1536Mi | 2Gi emptyDir ×2，重建可丢缓存 |
| VMInsert | 2 | 100m / 128Mi | 1000m / 512Mi | 无 |
| VMAgent | 2 | 200m / 256Mi | 1000m / 768Mi | 20Gi ×2，队列上限 10GiB/副本 |
| VMAuth | 2 | 100m / 128Mi | 500m / 384Mi | 无 |
| VMAlert | 2 | 100m / 128Mi | 500m / 384Mi | 无；规则状态远写至 VM |
| VMAlertmanager | 3 | 50m / 96Mi | 300m / 256Mi | 20Gi ×3 |

总 PVC 为 **250Gi**；config-reloader sidecar 另有 request 20m/32Mi、limit 100m/128Mi。以上仅覆盖自监控/canary，不是现有全量 28.6 万序列迁入的生产容量承诺。接真实流量前重新测算 90 天保留、RF2、查询、合并与单节点故障余量。

所有 Pod 通过低优先级、不可抢占 PriorityClass 调度，不申请 `nvidia.com/gpu`，不强制 CPU 节点。不加通配污点容忍，不容忍 algorithm A 区专属污点。首期只避让当前高内存的 `172.28.1.222`，其余普通 GPU/CPU 节点都可由调度器按余量选择；部署前复核现场观察。VMStorage、VMAgent、AM 及各无状态组件的同组件副本均使用 hostname 强制反亲和，无法满足则 Pending，不通过取消约束伪称 HA；生成的 Pod 仍须逐项检查 `preemptionPolicy: Never`、requests/limits 和实际分布。

VMStorage RF=2 与采集 HA 去重不同。首期所有采集和规则周期统一 30s，select/storage `dedup.minScrapeInterval: 30s`，两个 VMAgent 不增加采集副本身份标签。以后新增不同采集周期需先重新验证这个合同。VMSelect 拒绝 partial responses，查询失败不能作为业务恢复。

## Operator 与 CRD 边界

- converter 关闭，`watchNamespaces` 仅 `observability-metrics`，聚合 ClusterRoles 关闭。官方 chart 在这种模式下仍渲染一个同名 ClusterRoleBinding；本目录用 `operator-cluster-read.yaml` 补充节点/namespace 元数据、StorageClass 和 CRD 只读权限，没有集群写权限、节点指标接口或集群范围 EndpointSlice 读取。EndpointSlice 发现由本 namespace 的 Role 授权；安装后须核对实际日志和授权，不能将此静态清单当成运行兼容性证明。
- `extraArgs.controller.disableReconcileFor: VTAgent` 显式禁用本阶段不用的 VTAgent controller。v0.75.0 的该 controller 存在全局监听路径，不能仅靠 `WATCH_NAMESPACE` 推断所有 controller 都局限于 namespace；合同同时检查关闭参数，不能为消除权限报错而扩大 ClusterRole。
- admission webhook 关闭，避免新增全局 admission 依赖。Operator 仍检查 CR 语义，发布前必须做 CRD schema 和 server-side dry-run；以后启用 webhook 单独评审。
- CRD 作为 Helm plain CRD 首次创建；cleanup/upgrade hook 关闭。CRD 是集群级对象，namespace 限制不能让 CRD 变成 namespace 级。
- 现场应确认没有既有 VM CRD/Operator；若发现同名，停止自动接管，先检查版本和 owner。不得为了 Helm 安装强行重标记、覆盖或删除既有 CRD。
- VMAuth/Agent/Alert 均 `selectAllByDefault: false`，只选本 namespace 的明确标签。Agent `ignoreNamespaceSelectors: true`，不会因某个新增 scrape CR 改写 namespace selector 而扩大抓取范围。
- 显式 VMAgent ServiceAccount 只有该新 namespace 的 discovery/config 只读 Role。安装后还要核对 Operator 生成的 Role/RoleBinding，没有意外增加集群范围的采集权限。

## 网络

默认拒绝新 namespace 的 ingress/egress，再按组件放通自监控、VM 内部端口、AM gossip、API Server 和 DNS。API Server 白名单为 Service `10.0.96.1:443` 与 Endpoint `172.28.2.64/172.28.5.50:6443`；这些是部署时点快照，endpoint 变化时需同步评审。DNS 仅到 `kube-system` 的 TCP/UDP 53；若 VKE 使用 NodeLocal DNS，须确认 Pod `/etc/resolv.conf` 和 CNI 策略路径，再增加准确的本地 DNS 地址，不能放开任意外网 egress。

vmauth 外部代理端口 8427 与内部 health/metrics 8428 分开。新客户端只可到 8427，API 自监控只给可信 VMAgent/Operator。VM 组件管理路径与 `/metrics` 共用端口的事实不能由 NetworkPolicy 细化到 HTTP path；因此采集身份属于观测运维信任域，不授予训练用户。

无 Ingress、LoadBalancer、NodePort；不改变旧 namespace 的 NetworkPolicy。本阶段在受限 VKE Pod 网络内使用 HTTP Bearer；接入用户查询或跨安全域时须增加 TLS/网关策略后验收。要验证 CNI 实际执行拒绝策略，不能只检查 YAML 存在。

## 构建机验证与发布顺序

这些步骤由父层协调执行，本目录没有自动安装脚本。禁止在本机编译、运行 Helm/lint 或向错误 kubeconfig 应用。

1. 保存当前活跃 RayJob/RayCluster/Pod UID、重启数、状态，旧观测 release 的 revision、镜像和配置 hash；只读记录节点资源、EBS attach 数、集群与 namespace 配额。
2. 核对本 chart 的 namespace guard、Operator/CRD 包、镜像摘要。构建机执行 Helm lint/template，并验证所有渲染对象：不得含旧资源、Ingress、hostPath、hostNetwork、GPU request、额外容忍、真实通知或旧抓取目标。
   在构建机运行 `python3 validate_component_contract.py operator-rendered.yaml metrics-rendered.yaml`；除正常输入通过，还需用临时变体确认 converter 开启、namespace 扩大、匿名路由或真实通知会失败。临时变体不应用到集群。
3. 为 Operator 运行 server dry-run；完整 diff 只含新增本 namespace 对象及新 VM CRD/RBAC。先安装 Operator/CRD，等待 Established/Ready，再对 CR chart 做 server-side dry-run，审阅 schema/default/pruning 结果。
4. 先创建两个 token Secret，再装显式 CR。核对 operator watch/converter 参数、VMUser 应用状态、RBAC、实际生成 Pod 镜像/资源/优先级、PVC 的 SC/PV Retain 与 StatefulSet Retain 策略。
5. 启动后预期自监控有 8 个组件类、共 17 个 Pod 目标；两个 Agent 各抓同组目标。Targets 中不应出现旧 monitoring/loki/tenant namespace 或训练 Pod。检查生成配置与实际 targets 两层。
6. 用专用客户端验证：无 token 401；错误 token 401；read token 可 query 但不能 write/delete/admin/跨租户；write token 仅能 write。新夜莺 reader 只查 tenant1001。无权限测试客户端不可直连 VMSelect/VMInsert。
7. 查询 `raytrain_observability_canary{purpose="deployment-canary"}`；两个 VMAlert 正常求值且写入标签一致，AM 三实例 gossip 健康并可看到 canary。AM 配置仅 `blackhole-canary`，不配置外部 webhook。
8. 确认队列目录位于每副本 PVC；只对新组件做短时恢复测试时记录积压/发送/去重，不删除有积压 PVC。不能把两个 Agent 的 queue 当成副本互备。
9. 再次对比旧训练 UID/重启/状态、旧观测配置 hash 和节点压力，符合才报告“新增旁路组件已部署验收”。此时不等于旧指标/日志已迁入，也不等于正式告警切换完成。

## 回退与保留

若新增资源影响节点压力，先在确认的新增 release 范围内停采/缩容；旧平台仍按原链路工作。不要删除 namespace、CRD、PVC、PV 或两个 token Secret来“回退”。CR 可能有 finalizer；任何清理都先盘点 ownerReferences 和 Retain PV。纯新增阶段没有业务 DB 迁移，也无需迁移旧 Prom/Loki 历史；但部署前配置与运行基线仍必须保留。

## 官方依据

- [standalone Operator chart](https://docs.victoriametrics.com/helm/victoria-metrics-operator/)
- [v0.75.0 默认版本与参数](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/internal/config/config.go)
- [v1.153.0 发布说明与受影响构建范围](https://github.com/VictoriaMetrics/VictoriaMetrics/releases/tag/v1.153.0)
- [锁定版 VMCluster 类型](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/api/operator/v1beta1/vmcluster_types.go)
- [锁定版 VMAgent 队列实现](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/internal/controller/operator/factory/vmagent/vmagent.go)
- [锁定版 VMUser Secret 引用与路径路由](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/api/operator/v1beta1/vmuser_types.go)
- [VMCluster replication and data safety](https://docs.victoriametrics.com/victoriametrics/cluster-victoriametrics/#replication-and-data-safety)
