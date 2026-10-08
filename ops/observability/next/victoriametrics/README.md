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
- `mtls-identities.json`：本组角色与 DNS SAN 的证书签发合同，不包含私钥；`verify-runtime.py` / `runtime_lab.py` 是构建机隔离 Docker 协议验收入口。

安装前在构建机下载锁定 chart，记录包 SHA256，核对其 `Chart.yaml`、依赖及镜像。镜像拉取验证后记录 digest，并通过独立受审阅的 values 覆盖为现有镜像镜像源的摘要引用；不把未核对的 `latest` 带入发布。API 以锁定版源码和 CRD schema 为准，文档主分支只是辅助。

Operator 候选也直接引用 Harbor `hub` 代理的固定 amd64 摘要，不需要再次推送相同镜像。发布协调者负责核对实际镜像、拉取 Secret 的来源和本次受审阅快照，任何新覆盖均重新渲染验证。

v1.153.0 包含 native import 的 zstd 内存限制修复；官方同时记录 pure-Go 构建的数据块解压回归。本候选锁定官方 **linux/amd64 CGO** 镜像，官方发布说明明确此架构不受该回归影响，不能将相同版本直接用于 `-pure` 或其他受影响架构。首期不开启新增 OIDC、storage 直接写入 API 或其他可选能力；验证仍以实际参数、规则和生成配置为准。

## 当前候选与运行边界

截至 2026-10-08 16:27，现场只有新增基础对象、凭据和 Operator，VMCluster/Agent/Auth/Alert/AM 数据面未创建。新 Operator 已升级到 Helm revision 2，直接使用 hub 的 v0.75.0 固定摘要、禁用 VTAgent，并应用收紧的 ClusterRole；实际 1/1 Ready、重启 0，节点 `172.28.1.150`。两个 VM token Secret 已创建，不应覆盖重建。完整证据见 [部署记录](../DEPLOYMENT_RUNBOOK.md)。

现场 CNI 的 NetworkPolicy enforcement 尚未开启，用户随后选择保留 CNI、为新增组件采用专用 mTLS 代理。本目录当前候选已经改用回环后端、精确身份与路径授权；这段是源码状态，不代表候选已经通过运行验收或部署。只有构建机协议测试、生成 Pod 核对和集群旁路拒绝通过后才可启动本组数据面。不能把 NetworkPolicy YAML 已存在当作隔离有效。

## 预置对象与身份

父层创建本目录依赖的 namespace `observability-metrics`、PriorityClass `raytrain-observability-additive`（`value: -1000`、`preemptionPolicy: Never`）及新 StorageClass `ebs-ssd-observability-retain`。StorageClass 继承已有 EBS ESSD_PL0/WFFC/扩容参数，`reclaimPolicy: Retain`；旧 `ebs-ssd` 不修改。

由发布协调者在该 namespace 安全创建两个不同随机值的 Secret，仅通过文件或 API 内存传递，禁止打印 token、写进 Git 或放在 shell argv：

| Secret | Key | 用途 |
| --- | --- | --- |
| `vm-self-write` | `token` | VMAgent、VMAlert 向隔离租户 1001 写入 |
| `vm-self-read` | `token` | VMAlert、专用验收客户端及新夜莺只读查询 |

VMUser 只引用 Secret。VMAuth 不配置 anonymous/unauthorized 用户。读入口是 `https://vmauth-raytrain-next.observability-metrics.svc:8427`；写入口为同域 `/api/v1/write`。连接还需角色客户端证书，服务端固定 DNS SAN 为 `vmauth.observability-metrics.svc`。账号路由固定到 `/select/1001/prometheus` 或 `/insert/1001/prometheus`，客户端不能选择租户；读账号不开放导出、导入、管理、快照或删除路径。写账号仅开放 `/api/v1/write`。

夜莺只获得读 token 的受控副本 `observability-alerting/n9e-vm-read`，并使用 `spiffe://observability.raytrain.local/observability-alerting/nightingale` 身份。Pod 标签不构成当前环境的安全边界；错误身份即使持有 read token 也不能访问。跨 namespace 查询通过 VMAuth 8427，不能直连 vmselect/vminsert。

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

总 PVC 为 **250Gi**；config-reloader sidecar 另有 request 20m/32Mi、limit 100m/128Mi。17 个 Pod 各增加一个 Envoy，初始每个 request 50m/64Mi、limit 300m/128Mi、单 worker；新增合计 request 850m/1088Mi。此代理预算需在实际协议与压力验证后复核。以上仅覆盖自监控/canary，不是现有全量 28.6 万序列迁入的生产容量承诺。接真实流量前重新测算 90 天保留、RF2、查询、合并与单节点故障余量。

所有 Pod 通过低优先级、不可抢占 PriorityClass 调度，不申请 `nvidia.com/gpu`，不强制 CPU 节点。不加通配污点容忍，不容忍 algorithm A 区专属污点。首期只避让当前高内存的 `172.28.1.222`，其余普通 GPU/CPU 节点都可由调度器按余量选择；部署前复核现场观察。VMStorage、VMAgent、AM 及各无状态组件的同组件副本均使用 hostname 强制反亲和，无法满足则 Pending，不通过取消约束伪称 HA；生成的 Pod 仍须逐项检查 `preemptionPolicy: Never`、requests/limits 和实际分布。

VMStorage RF=2 与采集 HA 去重不同。首期所有采集和规则周期统一 30s，select/storage `dedup.minScrapeInterval: 30s`，两个 VMAgent 不增加采集副本身份标签。以后新增不同采集周期需先重新验证这个合同。VMSelect 拒绝 partial responses，查询失败不能作为业务恢复。

## Operator 与 CRD 边界

- converter 关闭，`watchNamespaces` 仅 `observability-metrics`，聚合 ClusterRoles 关闭。官方 chart 在这种模式下仍渲染一个同名 ClusterRoleBinding；本目录用 `operator-cluster-read.yaml` 补充节点/namespace 元数据、StorageClass 和 CRD 只读权限，没有集群写权限、节点指标接口或集群范围 EndpointSlice 读取。EndpointSlice 发现由本 namespace 的 Role 授权；安装后须核对实际日志和授权，不能将此静态清单当成运行兼容性证明。
- `extraArgs.controller.disableReconcileFor: VTAgent` 显式禁用本阶段不用的 VTAgent controller。v0.75.0 的该 controller 存在全局监听路径，不能仅靠 `WATCH_NAMESPACE` 推断所有 controller 都局限于 namespace；合同同时检查关闭参数，不能为消除权限报错而扩大 ClusterRole。
- admission webhook 关闭，避免新增全局 admission 依赖。Operator 仍检查 CR 语义，发布前必须做 CRD schema 和 server-side dry-run；以后启用 webhook 单独评审。
- CRD 作为 Helm plain CRD 首次创建；cleanup/upgrade hook 关闭。CRD 是集群级对象，namespace 限制不能让 CRD 变成 namespace 级。
- 现场应确认没有既有 VM CRD/Operator；若发现同名，停止自动接管，先检查版本和 owner。不得为了 Helm 安装强行重标记、覆盖或删除既有 CRD。
- VMAuth/Agent/Alert 均 `selectAllByDefault: false`，只选本 namespace 的明确标签。Agent `ignoreNamespaceSelectors: true`，不会因某个新增 scrape CR 改写 namespace selector 而扩大抓取范围。
- 显式 VMAgent ServiceAccount `vm-self-scraper` 只有本 namespace 的 pods/services/endpoints/endpointslices discovery，以及 `resourceNames: [vmagent-raytrain-next]` 限定的配置 Secret 读取。没有未限定的 Secrets 或 ConfigMaps API 权限；源 TLS/写令牌由 kubelet 挂载。v0.75.0 的 `IsOwnsServiceAccount()` 在显式 SA 时返回 false，跳过自动 SA/RBAC 生成。安装后仍必须核对所有 RoleBinding/ClusterRoleBinding 和 `kubectl auth can-i` 的有效结果：读取自身配置 yes；未限定 list secrets、读取 `obs-mtls-admin`/`obs-mtls-vmauth`/`obs-mtls-vmstorage`/`obs-mtls-alertmanager-gossip` 都为 no。不能只检查这一份 Role。

## 网络

NetworkPolicy 保留为将来网络策略生效时的附加限制，当前不能依赖它阻断连接。其 API Server 白名单为 Service `10.0.96.1:443` 与 Endpoint `172.28.2.64/172.28.5.50:6443`；这些是部署时点快照，endpoint 变化时需同步评审。DNS 限于 `kube-system` 的 TCP/UDP 53；启用策略前仍须验证 VKE 实际 DNS 路径。

原生 HTTP 都绑定 `127.0.0.1`，Envoy 只绑定 Downward API 注入的 Pod IPv4。Envoy HTTP 路由按客户端精确 URI、方法和路径授权；没有 access log、匿名业务路径或外部 admin listener。VMAuth 8427 同时保留 Bearer 校验，8428 只向 vmagent 身份开放 GET `/metrics`。其他组件的自监控也仅允许这个路径。

- storage 原生 RPC 改为回环28400/28401；外部8400仅接纳vminsert，8401仅接纳vmselect。insert/select 的 `storageNode` 被完整覆盖为三个独立回环代理，分别经 mTLS 到三个明确 ordinal，不能把三条路由合并成一个负载均衡 Service。
- VMAuth 通过本地18480/18481代理访问insert/select。TLS服务端校验使用精确角色URI；Agent/Alert的原生TLS客户端使用专用CA与精确DNS SAN，代理校验其客户端URI。
- AM HTTP仍由代理保护；9094采用原生TLS/TCP和独立AM gossip CA。只有AM peer身份由该CA签发，不能让主mesh CA的证书获得gossip权限。必须在实际进程中证明没有UDP9094监听。
- reloader 8435只绑定回环，重载URL只到同Pod回环。公共18081仅提供无body的健康状态：`/healthz`聚合就绪、`/app-healthz`和`/reloader-healthz`各自本地检查、`/livez`表示代理响应；其他路径404。代理先恢复，原生liveness有较长预算，减少代理短暂故障导致的连带重启。
- 固定3 storage配置由副本数同时生成CR参数和代理映射。扩容必须一起更新、验证生成argv，不能只改StatefulSet。首期显式关闭可选`selectNode`缓存失效广播，避免Operator默认生成明文跨Pod路径；数据查询和RF2不依赖这条管理路径。
- 证书Key按0440和fsGroup65534挂载；角色不同私钥，canary/admin仅客户端证书不挂到应用Pod。当前Envoy采用静态文件证书，轮换后协调滚动重启新组件并验收，不宣称已实现SDS热更新。

无Ingress、LoadBalancer、NodePort，不修改旧namespace策略。上述应用认证阻止无权读写和管理请求，但不把它描述为网络不可达或防连接洪泛方案。

## 构建机验证与发布顺序

这些步骤由父层协调执行，本目录没有自动安装脚本。禁止在本机编译、运行 Helm/lint 或向错误 kubeconfig 应用。

1. 保存当前活跃 RayJob/RayCluster/Pod UID、重启数、状态，旧观测 release 的 revision、镜像和配置 hash；只读记录节点资源、EBS attach 数、集群与 namespace 配额。
2. 核对本 chart 的 namespace guard、Operator/CRD 包、镜像摘要。构建机执行 Helm lint/template，并验证所有渲染对象：不得含旧资源、Ingress、hostPath、hostNetwork、GPU request、额外容忍、真实通知或旧抓取目标。
   在构建机运行 `python3 validate_component_contract.py operator-rendered.yaml metrics-rendered.yaml`；除正常输入通过，还需用临时变体确认 converter 开启、namespace 扩大、匿名路由或真实通知会失败。临时变体不应用到集群。
3. 运行 `python3 -m unittest discover -s ../tests -p 'test_vm_*.py' -v`、全部Envoy配置的锁定镜像`--mode validate`、CRD schema。执行 `python3 verify-runtime.py --execute --output /root/<独立证据目录>/vm-runtime.json`；脚本只创建带随机专用标签的Docker内部网络/容器，无hostport，证书调用共用`issue_material`临时签发，不创建K8s Secret。先单独验证storage/insert/select/auth的授权metrics，再以90秒期限完成实际远写；只重试启动期间的连接错误与502/503/504，认证4xx与TLS验证错误立即失败。报告保留各跳检查、尝试次数和状态码，不保存响应正文或令牌。覆盖3storage实际远写/查询、原生端口拒绝、错误角色/CA/过期证书、代理失败关闭/证书轮换，以及3AM gossip/silence同步/重启/无UDP。脚本仅清理本次标签对象和临时证书；这仍不替代Operator生成Pod及完整Agent/Alert链验证。
4. 对新资源执行server dry-run；完整diff只含受审阅新增对象。先创建身份Secret和Operator代理ConfigMap，再升级新Operator，检查Ready后应用CR；两个既有token不重建。核对生成Pod的完整argv、loopback监听、Secret权限、Service named port、每个探针、镜像、RBAC、优先级、PVC/PV Retain。
5. 启动后预期自监控有 8 个组件类、共 17 个 Pod 目标；两个 Agent 各抓同组目标。Targets 中不应出现旧 monitoring/loki/tenant namespace 或训练 Pod。检查生成配置与实际 targets 两层。
6. 用专用客户端验证：无证书/错CA/错角色拒绝；正确客户端身份下无token或错误token返回401；read token可query但不能write/delete/admin/跨租户；write token仅能write。新夜莺reader只查tenant1001。对Service和每个Pod IP扫描全部已知端口，原生回环端口拒绝，公开最小health之外没有匿名接口。
7. 查询 `raytrain_observability_canary{purpose="deployment-canary"}`；两个 VMAlert 正常求值且写入标签一致，AM 三实例 gossip 健康并可看到 canary。AM 配置仅 `blackhole-canary`，不配置外部 webhook。
8. 确认队列目录位于每副本 PVC；只对新组件做短时恢复测试时记录积压/发送/去重，不删除有积压 PVC。不能把两个 Agent 的 queue 当成副本互备。
9. 再次对比旧训练 UID/重启/状态、旧观测配置 hash 和节点压力，符合才报告“新增旁路组件已部署验收”。此时不等于旧指标/日志已迁入，也不等于正式告警切换完成。

VMAuth v1.153.0 将每个 `bearer_token` 同时登记为 Bearer 和 Basic（令牌作用户名、空密码）两种认证键；启动日志按键数计数。因此这里2个VMUser可能显示 `loaded information about 4 users`，不能仅凭该日志推断配置重复。运行验收会检查两份唯一用户/Secret映射，以及read token不能write、write token不能query。

隔离 Docker 验收额外给 Envoy 添加一个私有目录内的 Unix admin socket，仅为失败报告读取数值统计（TLS、DNS、连接与RBAC计数）；不监听TCP，不读取config dump，不记录HTTP访问日志。实验容器仅将`connection`日志器设为debug，以保留TLS失败原因；不启用HTTP/header日志。生产ConfigMap没有这项诊断入口。真实网关完整写查前还会以正确角色检查storage RPC TLS，并使用授权的vmauth角色证书分别检查insert/select入口，缩小后端失败定位范围。

## 回退与保留

若新增资源影响节点压力，先在确认的新增 release 范围内停采/缩容；旧平台仍按原链路工作。不要删除 namespace、CRD、PVC、PV 或两个 token Secret来“回退”。CR 可能有 finalizer；任何清理都先盘点 ownerReferences 和 Retain PV。纯新增阶段没有业务 DB 迁移，也无需迁移旧 Prom/Loki 历史；但部署前配置与运行基线仍必须保留。

## 官方依据

- [standalone Operator chart](https://docs.victoriametrics.com/helm/victoria-metrics-operator/)
- [v0.75.0 默认版本与参数](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/internal/config/config.go)
- [v1.153.0 发布说明与受影响构建范围](https://github.com/VictoriaMetrics/VictoriaMetrics/releases/tag/v1.153.0)
- [锁定版 VMCluster 类型](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/api/operator/v1beta1/vmcluster_types.go)
- [锁定版 VMAgent 队列实现](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/internal/controller/operator/factory/vmagent/vmagent.go)
- [锁定版 VMAgent 精确 Secret RBAC](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/internal/controller/operator/factory/vmagent/rbac.go)
- [锁定版 VMUser Secret 引用与路径路由](https://github.com/VictoriaMetrics/operator/blob/v0.75.0/api/operator/v1beta1/vmuser_types.go)
- [VMCluster replication and data safety](https://docs.victoriametrics.com/victoriametrics/cluster-victoriametrics/#replication-and-data-safety)
