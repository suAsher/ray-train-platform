# VKE 可观测性：首批业务指标与日志链路自监控实施计划

> **执行方式：** 隔离工作树、并行文件所有权、构建机测试与集中上线。用户已要求开始推进；本计划不重复申请既有授权。

**目标：** 保留训练平台 Loki/Alloy 与原 Prometheus，实际接通首批 GPU、节点、Kubernetes 状态及日志链路指标；交付可展示架构图、可用看板和可复查部署记录。

**架构：** 原 Prometheus 新增 Alloy/Loki 自监控；新 VMAgent 双副本通过限域服务发现并行抓取五组现有 exporter，以现有 mTLS/VMAuth 写入 VMCluster。Grafana 首批看板使用已配置的 Prometheus 数据源。正式告警统一由夜莺负责，用户后续在 UI 配置通知渠道；VMAlert 以 recording rules 为主，新 Alertmanager 保持未连接正式通知。

**技术栈：** VictoriaMetrics Operator/VMServiceScrape、Prometheus Operator/ServiceMonitor、Helm、Kubernetes namespaced RBAC、Grafana ConfigMap provisioning、Python unittest、SVG/HTML 架构图。

## 已核实边界

- 构建机 `14.103.49.106`；VKE context `cd9q3b7rf9aidnnbd0je0@81533555-kd9q3q0frjmp21sk5lklg`。证据目录 `/root/raytrain-observability-business-wave1-20261009T061500Z`，目录 0700、文件 0600。
- 原 Prometheus 的 ServiceMonitor selector 与 namespace selector 均为空对象，会选中所有 ServiceMonitor。新增只能包含 Alloy/Loki，不能复制原有 DCGM 等 ServiceMonitor。
- VM Operator 只 watch `victoriametrics`；VMAgent 原仅选 `metrics-self` 且忽略目标 namespaceSelector，SA 只有本 namespace 的发现权限。接入需显式启用业务 selector、限域 Role/RoleBinding，不能只创建 CR。
- 原 Prometheus 与新 VMAgent 均为两副本 HA 抓取，不是自动分片。此次仅五组目标、30s 抓取、指标白名单，保留 VM 原 30s 去重；旁路对照有阶段性额外抓取开销。
- Backend 两 Pod 的 `/metrics` 均为 404，当前没有 Prometheus exporter 或业务 OTel 埋点。API 延迟、排队/同步吞吐、完整调用链列为下一阶段代码任务，不伪造接入状态。
- Kueue HTTPS/认证、租户 Ray Pod 指标、新 Grafana→VM 专属只读身份在后续单独接入；不开放全 namespace 或共享客户端凭据。
- CNI 维持现状，NetworkPolicy 只作为声明，不宣称已生效。现有训练、Alloy 日志配置、Loki 存储与接收、Prometheus remote_write 不修改。

## 1. 架构图与当前状态

- [ ] `docs/architecture/raytrain-observability-vke-20261009.svg`：可编辑、浅色展示图；三条信号泳道、治理、组件副本、云存储边界。
- [ ] 同名 HTML：缩放与查看；PNG：在构建机渲染后视觉检查。
- [ ] 明确区分基础设施已部署、业务指标本轮接入、生产 OTel/SSO/通知待接入；链接到操作记录。

## 2. 首批采集合同（测试先行）

文件：`ops/observability/next/business-metrics/`，`victoriametrics/resources/templates/agent.yaml`、`resources/values.yaml`，`tests/test_business_metrics.py`。

- [ ] 在构建机先运行新测试，确认缺实现而失败；再实现并复测。
- [ ] 五个精确 Service：`kube-system/dcgm-exporter`、`monitoring/alloy`、`loki/loki-cpu`、`monitoring/prometheus-kube-state-metrics`、`monitoring/prometheus-prometheus-node-exporter`。
- [ ] VMServiceScrape 位于 `victoriametrics`；label `observability.raytrain.io/scrape=business-wave1`，service 名 keep，端口精确，防止 headless 重复。
- [ ] 发现权限只授予 `kube-system`、`monitoring`、`loki` 的 pods/services/endpoints/endpointslices 的 get/list/watch，不读取 Secret、不用 ClusterRole。
- [ ] VMAgent 业务开关默认 false；打开时仅扩大为两个明确 scrape label，并允许 CR 的显式 namespaceSelector；其他 scrape 类型不变。
- [ ] Alloy/Loki ServiceMonitor 只新增这两组；metric allowlist、标签丢弃与稳定 `telemetry_scope/component` 供查询验收。

## 3. 看板与夜莺使用交接

文件：`business-metrics/dashboard.yaml`、`tests/test_business_dashboard.py`。

- [ ] 新 dashboard UID，不覆盖现有 GPU 看板，数据源仅引用 `prometheus`，不写凭据。
- [ ] 展示日志目标 UP、近期发送/拒收/重试、Loki 请求错误与延迟、GPU 使用、节点状态、后端工作负载状态；计数器用 rate/increase，不把历史丢弃总量当当前故障。
- [ ] 对无指标/无请求明确 N/A，不用无条件补零遮蔽缺采；不把多副本重复样本宣称为唯一样本。
- [ ] 夜莺使用现有 VM 只读数据源检索同批真实指标；告警渠道与接收人由用户 UI 配置，本轮不发送真实通知、不解除证书轮转 Cron 的通知门禁。

## 4. 离线与部署门禁

- [ ] 仅选择上述文件/文档提交候选，经 Git bundle 导入构建机 detached worktree；正式构建目录保持不变。
- [ ] `python3 -m unittest discover -s "$candidate/ops/observability/next/tests" -v`；Helm lint/render；检查渲染对象范围、CRD schema、selector、RBAC、无新增工作负载/PVC/Secret。
- [ ] 独立评审 spec + 安全/正确性；记录测试、候选 SHA 与渲染摘要。
- [ ] 保存 live VMAgent 与现有对象清单；server-side dry-run，审阅完整差异。
- [ ] 先添加 Role/RoleBinding 与监控对象，再精确更新 VMAgent 两项配置；不重装整套观测服务。部署后的声明式配置与渲染选项写入记录，避免未来覆盖。
- [ ] 加载 dashboard；不重启 Grafana/Alloy/Loki/原 Prometheus。

## 5. 线上验收与回退

- [ ] 原 Prometheus 新目标精确为 Alloy 2、Loki 3，旧 target 继续 UP；新 VMAgent 五组目标按 endpoint 数量核对、连续抓取成功。
- [ ] 经 VMAuth 现有受权只读身份查询，比较 GPU 设备数、每节点设备数、序列新鲜度；真实业务指标到达，不以 synthetic `vector(1)` 代替。
- [ ] 验证 VM 队列/丢弃、Alloy/Loki 当前拒收与端到端日志健康；对照受保护对象 UID/spec/restarts、节点压力、后端 healthz。
- [ ] Grafana sidecar 已加载新看板，查询实际有数据；记录未有流量的面板。
- [ ] 验收临时资源仅清理本轮对象，不触碰用户任务/数据。文档记载真实完成项与剩余后续门禁。

回退只移除本轮两个 ServiceMonitor、五个 VMServiceScrape、看板与专属 Role/RoleBinding/NetworkPolicy，并恢复 VMAgent 原 `metrics-self` selector、`ignoreNamespaceSelectors=true`；先停采再收权限。保留 VM 已采历史，不删除 PVC、namespace 或底层数据。不以 Helm 整栈回滚覆盖别的并行变更。
