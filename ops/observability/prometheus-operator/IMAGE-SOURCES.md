# Prometheus Operator image lock

Chart: `kube-prometheus-stack 87.18.1`.

| Component | Locked image source |
| --- | --- |
| Prometheus Operator | `harbor.wellspiking.ai/guofeng.su/prometheus-operator:v0.92.1` |
| Prometheus configuration reloader | `harbor.wellspiking.ai/guofeng.su/prometheus-config-reloader:v0.92.1` |
| Thanos sidecar default | `harbor.wellspiking.ai/guofeng.su/thanos:v0.42.2` |
| Prometheus Webhook certificate job | `harbor.wellspiking.ai/guofeng.su/kube-webhook-certgen:1.8.4` |
| Prometheus | `harbor.wellspiking.ai/guofeng.su/prometheus:v3.13.1-distroless` |
| Alertmanager | `harbor.wellspiking.ai/guofeng.su/alertmanager:v0.33.1` |
| node-exporter | `harbor.wellspiking.ai/guofeng.su/node-exporter:v1.12.1-distroless` |
| Grafana dashboard sidecar | `harbor.wellspiking.ai/guofeng.su/k8s-sidecar:2.8.1` |
| Grafana | `harbor.wellspiking.ai/hub/grafana/grafana:13.1.1` |
| kube-state-metrics | `harbor.wellspiking.ai/guofeng.su/kube-state-metrics:v2.19.1` |

All image digests are included in `20-values-production.yaml`. No workload
image is pulled directly from Docker Hub, Quay, or GHCR at deployment time.

The chart intentionally runs a single restart-safe Prometheus Operator. The
data plane is highly available: two Prometheus replicas with independent 50Gi
volumes, two Alertmanager replicas, and two Grafana replicas placed on
different CPU nodes.
