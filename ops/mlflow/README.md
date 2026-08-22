# MLflow 生产部署

该目录部署平台内部的 MLflow Tracking Server。它不创建公网或私网 Ingress，用户浏览器只通过训练平台的任务详情 API 查看自己租户内的指标。

- Chart：官方 `mlflow/mlflow` 0.1.0，归档在 `helm/vendor/mlflow-0.1.0.tgz`，部署前校验 SHA-256。
- 镜像：`harbor.wellspiking.ai/guofeng.su/mlflow:v3.14.0-full`，values 中同时固定摘要。
- 命名空间：独立使用 `mlflow-system`，与训练平台控制面隔离。
- 元数据：独立内置 PostgreSQL StatefulSet，使用独立 20Gi EBS PVC，不复用平台数据库。
- Artifact：MLflow 服务端原生写入 `vke-cluster/ray-train/platform/mlflow-artifacts/`；不依赖 FSX，Ray Pod 不获取 TOS AK/SK。
- 可用性：2 个副本，跨 CPU 节点硬反亲和，PDB `minAvailable: 1`。
- 网络：全部为 ClusterIP。平台后端和 Prometheus 可访问 MLflow 5000；带平台托管标签的租户 namespace 只能访问 `mlflow-ingest:8080` 写入网关，不能直连 MLflow。数据库只允许 MLflow 与迁移 Job 访问。
- 写入边界：网关只开放实验创建、run 创建/更新、参数、指标和 tag 写入所需接口；搜索、列表和 Artifact 下载一律拒绝。平台后端用 HMAC 任务来源标签和数据库归属做二次校验。

部署：

```bash
bash ops/mlflow/deploy.sh
```

验证：

```bash
bash ops/mlflow/verify.sh
```

新增租户时必须由平台创建 namespace，并保留 `app.kubernetes.io/managed-by=ray-train-platform` 与 `ray.io/tenant-id` 标签；NetworkPolicy 会自动允许该 namespace 访问写入网关。不要改成允许所有 namespace。
