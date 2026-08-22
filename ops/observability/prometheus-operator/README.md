# Prometheus Operator 生产部署

本目录交付标准的 `kube-prometheus-stack 87.18.1` 部署，不依赖线上 Helm
仓库。Chart 已固定在仓库的 `helm/vendor/`；所有工作负载镜像均在
`harbor.wellspiking.ai` 以 digest 锁定。

## 资源与可用性

- Prometheus：2 个副本，各自 50Gi `ebs-ssd` PVC，15 天或 45GiB 保留上限。
- Alertmanager：2 个副本；Grafana：2 个副本并强制分散到不同 CPU 节点。
- Prometheus Operator：上游 Chart 固定单个、无状态且可自动重建的控制器。
- 监控组件选择 `platform.wellspiking.ai/pool=control-plane`，不占用 GPU 节点。
- `40-dcgm-service-monitor.yaml` 从 `kube-system/dcgm-exporter` 采集 GPU 指标。

## 首次部署或升级

在构建机执行：

```bash
cd /opt/guofeng/vke-cluster/ray-platform
bash ops/observability/prometheus-operator/deploy.sh
```

该脚本会：渲染检查、确保 `monitoring` namespace、从
`ray-train-platform/harbor-registry` 复制镜像拉取凭据、原子 Helm 升级、安装
GPU Dashboard 与 DCGM ServiceMonitor，并实际查询 GPU 指标。若拉取 Secret 的
来源 namespace 不同，使用 `IMAGE_PULL_SECRET_SOURCE_NAMESPACE=<namespace>`；若
Secret 已在 `monitoring`，脚本不会修改它。

```bash
bash ops/observability/prometheus-operator/verify.sh
kubectl -n monitoring get deploy,statefulset,pods,pvc
```

## 旧临时监控栈清理

仅在新栈验证通过后执行：

```bash
CONFIRM_LEGACY_CLEANUP=DELETE \
  bash ops/observability/prometheus-operator/cleanup-legacy.sh
```

脚本只会卸载 `ray-observability`，并删除它精确标签匹配的两块 200Gi PVC；不会
触碰 Loki、Alloy、DCGM exporter、KubeRay、训练任务或 TOS 数据。
