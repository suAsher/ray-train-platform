# 训练业务指标首波接入

此独立 Helm chart 为已有 exporter 和日志链路增加监控声明，不创建采集器、训练 Pod、PVC、Secret 或告警通知。部署和真实验证结果见 [主部署记录](../DEPLOYMENT_RUNBOOK.md)；文件存在不代表已部署。

## 采集范围

| 组件 | 已有 Service / namespace | 指标入口 | 接入目的 |
|---|---|---|---|
| DCGM | `dcgm-exporter` / `kube-system` | `metrics:9400` | GPU 利用率、显存、温度、错误 |
| Alloy | `alloy` / `monitoring` | `http-metrics:12345` | 日志发送、重试、丢弃、传播延时、成员状态 |
| Loki | `loki-cpu` / `loki` | `http-metrics:3100` | 日志接收、拒收、请求、WAL 和 flush |
| kube-state-metrics | `prometheus-kube-state-metrics` / `monitoring` | `http:8080` | Pod 状态、GPU/CPU/内存请求、节点容量 |
| node-exporter | `prometheus-prometheus-node-exporter` / `monitoring` | `http-metrics:9100` | 节点 CPU、内存、文件系统、网络、磁盘 |

五个 `VMServiceScrape` 全部放在 `victoriametrics`，每项用服务标签、明确 namespace、端口名和精确服务名 keep 共同限定发现范围；不会把 Loki headless Service 再采一遍。另在 `monitoring` 仅增加 Alloy/Loki 两个 `ServiceMonitor`，使原 Prometheus 能监测日志链路；不重复其已有 GPU、KSM、node-exporter 抓取。没有接入当前返回 404 的后端 `/metrics`，也没有全量扫描 Ray Pod 或修改 Kueue TLS。

稳定验收标签：`telemetry_scope="business-wave1"` 和 `component`（表中五个组件对应 `dcgm`、`alloy`、`loki`、`kube-state-metrics`、`node-exporter`）。监控对象统一带 `observability.raytrain.io/wave=business-wave1`。`up`、抓取耗时等采集器生成指标也保留 target 标签。

## HA、指标及权限边界

- 两个 VMAgent **分别完整采集**这五组目标，构成 HA 双采；本轮不配置分片。采样 30 秒，与现有 VM 30 秒去重保持一致；`externalLabelName` 仍为空，不加入副本标签拆开相同序列。
- `values.yaml` 显式列出允许的 metric families 和标签。KSM/DCGM 的发现标签改存 `exporter_namespace`、`exporter_pod`、`exporter_node`，移除 target 的 `namespace/pod/container/node`，避免 `honorLabels=false` 覆盖源指标中的训练对象身份。DCGM 原生 `Hostname/UUID/modelName/gpu` 保留；旧 Prometheus 中的 `exported_namespace/exported_pod/exported_container` 是同名标签冲突后的名称，不应误认作 DCGM 必定原生输出。这里同时允许这些兼容名称，VM 首选保存源 `namespace/pod/container`。Alloy/Loki 按 `pod` 看副本，日志拒收计数不是唯一日志丢失条数。
- 标签裁剪前需按当前 exporter 版本对全部原始端点检查 series key 冲突；不能仅看查询正常就认定无损。保留 Loki `method/format/policy/retention_hours/type` 等请求和策略维度、Alloy 多目的/租户维度以及 DCGM 数值 `err_code`。DCGM 任意文本 `err_msg` 未纳入；当前端点样本裁剪无冲突，但以后 exporter 版本或指标标签变化必须重新验收，不将当前结果推广为永久合同。
- 指标筛选发生在抓取后，限制写入序列范围；它不减少 exporter 返回内容的网络字节。端点仍采用既有 HTTP metrics 协议，本轮不改变旧 exporter 的认证模式。
- 仅在 `kube-system`、`monitoring`、`loki` 为已有 `victoriametrics/vm-self-scraper` 增加 discovery Role/Binding，只有 Pods、Services、Endpoints、EndpointSlices 的 get/list/watch；无 Secret、写权限和 ClusterRole。Kubernetes list/watch 无法靠 `resourceNames` 完成标签发现，这些 namespace 中可见的元数据仍比实际 scrape 目标宽，实际采集由 CR 精确约束。
- 新增 NetworkPolicy 仅声明 VMAgent 到指定 exporter Pod 和端口的 egress。当前 VKE CNI 不执行 NetworkPolicy，不能把该声明当作已生效的隔离；以后启用 CNI 能力时还需专项复查旧规则、双向规则及 hostNetwork exporter 的实现差异。
- 正式告警由夜莺统一配置，通知渠道由管理员后续在 UI 设置；本 chart 不配置 VMAlert/Alertmanager 通知或发送消息。

## 发布与启用

仅在构建机对 Git bundle 生成的隔离候选执行测试与渲染。本机不运行测试或 Helm。

```bash
python3 -m unittest discover -s ops/observability/next/tests -p 'test_business_metrics.py' -v
helm template business-metrics-wave1 ops/observability/next/business-metrics \
  --namespace victoriametrics > "$evidence_dir/business-metrics.yaml"
# operator/VM renders must be the reviewed pinned candidate. For the VM render,
# explicitly set businessScrape.enabled=true; do not apply that entire stack.
python3 ops/observability/next/victoriametrics/validate_component_contract.py \
  --business-wave1 "$evidence_dir/operator-rendered.yaml" \
  "$evidence_dir/metrics-rendered.yaml" "$evidence_dir/business-metrics.yaml"
kubectl --context="$vke_context" apply --server-side --dry-run=server \
  -f "$evidence_dir/business-metrics.yaml"
kubectl --context="$vke_context" diff --server-side \
  -f "$evidence_dir/business-metrics.yaml"
```

先保存现有 VMAgent、原 Prometheus 选择器、受保护工作负载 UID/规格及旧监控基线。确认渲染只含审阅过的监控/RBAC/NetworkPolicy/看板 ConfigMap，当前 Prometheus 的 ServiceMonitor selector 能选择这两个新对象，且原 operator 的 Prometheus 转换器禁用，才应用候选。不要用本轮业务 chart 替换整个 VM release，也不要重建原日志工作负载。

组件门禁默认仍只接受两份自监控清单并拒绝跨 namespace 扩展。只有显式 `--business-wave1` 才接受第三份独立业务 chart 渲染，检查精确对象集合、三个 discovery Role、五个服务目标与候选白名单；Operator、VMAuth、canary/黑洞通知的原门禁仍全部执行，不以新模式跳过旧安全边界。

业务 chart 单独部署还不启用新 VM 采集。VM resources chart 的 `businessScrape.enabled` 默认 `false`，继续保持自监控。首波启用仅变更已有 VMAgent 的以下字段（先审阅 server-side dry-run），不重新应用完整历史 stack 清单：

```yaml
spec:
  ignoreNamespaceSelectors: false
  serviceScrapeSelector:
    matchExpressions:
      - key: observability.raytrain.io/scrape
        operator: In
        values: [metrics-self, business-wave1]
```

不设置 `serviceScrapeNamespaceSelector`，所以 CR 仍只从 VMAgent 所在 namespace 发现；其他 CR selector 保持未设置，`selectAllByDefault: false`。在后续 VM Helm 发布中需显式保留 `businessScrape.enabled=true`，否则默认值将恢复为自监控范围。

验收至少观察两个完整采集周期：原 Prometheus 新增 Alloy 2、Loki 3 个目标，VM 新五类 targets 全部 up，无 headless 重复；VM 中 GPU 唯一 `Hostname/UUID` 数量与真实 exporter 一致，KSM 中业务 namespace/pod 归属仍正确；检查写入/解析/重复样本错误和队列积压，确认去重后的逻辑计数不翻倍。对比受保护平台、训练、Loki、Alloy工作负载，原 Pod UID/规格不应改变。Grafana 此轮看板从原 Prometheus 读数据；VM 到夜莺的已有只读通路单独验收。

## 回滚

1. 用本次备份恢复 VMAgent 的两个 selector 字段（`ignoreNamespaceSelectors: true`，`serviceScrapeSelector.matchLabels.observability.raytrain.io/scrape: metrics-self`）；保留其他现网字段和持久队列。
2. 精确删除本次渲染文件内的新对象或卸载**本次独立** release，不删除任何已有 Service/exporter/VM 组件，不使用 namespace 级删除。若共享角色或同名监控对象不是本次新建，先恢复备份而非删除。
3. 验证原 Prometheus、平台任务日志和 VM 自监控恢复基线。已经进入 VM 的历史首波指标按现有 30 天保留策略自然过期，无需删库。
