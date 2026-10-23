# 2026-10-23 夜莺与独立 Grafana 退场记录

状态：退场完成，九块盘的 CSI 删除结果及保留系统验收通过。时间均为北京时间。

## 授权与范围

用户要求卸载夜莺，并明确不保留夜莺数据库和云盘；随后确认独立 Grafana 及其专用数据库一并删除。删除范围仅为本次新增的两个应用组及其专属资源，不包含原 `monitoring` Grafana、Prometheus、Alloy、Loki、VMCluster、ClickHouse、Keeper、OTel、cert-manager 或训练平台数据库和训练数据。

| 退场组 | 应用和依赖 | 专属持久卷 |
|---|---|---|
| `nightingale` | 夜莺、专用 CNPG Operator、`nightingale-postgres`、Redis/Sentinel | PostgreSQL 3 × 20Gi；Redis 3 × 20Gi |
| `grafana` | 独立 Grafana、专用 CNPG Operator、`grafana-postgres` | PostgreSQL 3 × 20Gi |
| 临时 UI | `nightingale/observability-ui`；31700 夜莺、31701 独立 Grafana 代理 | 无 |

合计 **9 PVC/PV、180Gi**。该数字是申请容量，不是实际数据使用量。两套数据库不为本次退场新建备份；既有历史恢复材料不自动构成当前数据库的备份。原 Grafana 配置不自动同步独立 Grafana 数据。

## 删除顺序与保护边界

1. 核对资源 UID、PVC/PV 与 CSI volumeHandle 的一一对应关系，记录非敏感清单。
2. 停止两个应用和临时 UI；清理对应采集、引用、专属证书及权限，保留其他组件所需共享资源。
3. 删除两个专用 CNPG Cluster 和 Redis，确认数据库工作负载退场，再卸载两个专用 Operator 及各自 webhook。
4. 对精确列出的九块 PV 将 Retain 调整为 Delete，通过正常 CSI 删除流程回收 PVC/PV 和底层 EBS。不绕过 finalizer、不直接删除 CSI 元数据、不处理其他卷。
5. 核对 PV/PVC、VolumeAttachment、CSI DeleteVolume 结果和对应 namespace/入口，并复验保留服务与训练资源。

只读核对：两个 `cloudnative-pg-operator` Helm manifest 不包含共享 CRD；11 个 `*.postgresql.cnpg.io` CRD 有 `helm.sh/resource-policy: keep`，历史 owner 为 `n9e-cnpg/observability-alerting`。两个 namespace 的 mutating/validating webhook 名称分别带 `-nightingale`、`-grafana`，namespaceSelector 精确匹配各自 namespace；卸载各自 Operator 不会删除另一套 webhook。共享 CRD 继续保留，不因删除最后两套数据库而顺带清理全局 API。

## 最终结果

- 九块 PVC/PV 均已消失；`csi-ebs-controller-54b97658dc-p5cch` 的 `csi-ebs-driver` 日志对清单中的精确九个 volumeHandle 均返回 `Successful delete volume`。合计 9 × 20Gi / 180Gi 为申请容量。本次未额外查询云 API，也未以另一条 `expected deleted` 日志作为独立佐证。
- 九块卷对应的 VolumeAttachment 无残留；31700 / 31701 已无 NodePort 占用。两个 VMAgent 的剩余采集目标均为 46/46 UP。
- `nightingale`、`grafana` 两个 namespace 已消失；两个专用 CNPG Operator release、`nightingale-redis`、`observability-ui` 均已卸载。31700 / 31701 对应 UI Service 随 release 移除；三个 Grafana 相关 VMServiceScrape 已删除。
- 原 `monitoring` Grafana 两副本健康，保留 ClusterIP Service `monitoring/prometheus-grafana`；`/api/health` 返回数据库正常、版本 `13.1.1`。本次没有新增旧 Grafana 对外入口，也没有将独立 Grafana 配置迁入旧实例。
- namespace Pod 就绪数：`monitoring` 22/22、`loki` 5/5、`victoriametrics` 17/17、`clickhouse` 6/6、`opentelemetry` 2/2、`ray-train-platform` 16/16。这些是本次验收时点，不是持续可用性承诺。
- 保护基线对照的 PVC、PV、Deployment、StatefulSet、DaemonSet、RayJob、RayCluster 均为 `diff=[]`；不把自然新增或历史资源数量与本次清理混算。
- 未新建两套数据库备份，未删除既有历史备份。共享 CNPG CRD 保留。部分旧身份以及 suspended rotation 的历史目标配置仍须在维护时核对，不宣称集群 PKI 或所有历史引用已经清空。

构建机证据目录：`/root/raytrain-observability-retirement-20261023/`。

| 文件 | 记录内容 |
|---|---|
| `volumes.json` | 精确九块 PVC/PV 与 CSI volumeHandle 范围 |
| `csi-delete-confirmation.json` | 驱动对九块盘的成功删除日志确认 |
| `protected-comparison.json` | 保留资源基线对照 |
| `removed-targets-namespaces.json` | 退场 namespace 和采集目标核对 |
| `final-result.json` | 无残留卷挂载、端口释放及双 VMAgent 46/46 目标就绪 |

证据应按既有管理员权限维护，不整体上传含有其他私有材料的父目录。

## 后续维护

当前数据流见 [运维手册](OPERATIONS.md)。10 月 13 日架构图、Grafana 双实例恢复验收及夜莺配置方法仅作历史记录；不得照旧安装命令或递归 apply 重建退场组。夜莺不再承担告警，新的告警管理方向需另行确定，原 Prometheus/Alertmanager 保留原链路。
