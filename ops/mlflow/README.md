# MLflow 生产部署

该目录部署平台内部的 MLflow Tracking Server。实验中心是平台筛选视图；登录用户还可从实验中心打开原生 MLflow 完整管理界面。原生界面统一使用 `https://raytrain.wellspiking.ai/mlflow/`，由 Frontend 和 Backend 同域代理到集群内 MLflow，不直接暴露服务。

- Chart：官方 `mlflow/mlflow` 0.1.0，归档在 `helm/vendor/mlflow-0.1.0.tgz`，部署前校验 SHA-256。
- 镜像：`harbor.wellspiking.ai/guofeng.su/mlflow:v3.14.0-full`，values 中同时固定摘要。
- 命名空间：独立使用 `mlflow-system`，与训练平台控制面隔离。
- 元数据：独立内置 PostgreSQL StatefulSet，使用独立 20Gi EBS PVC，不复用平台数据库。
- Artifact：MLflow 服务端原生写入 `vke-cluster/ray-train/platform/mlflow-artifacts/`；不依赖 FSX，Ray Pod 不获取 TOS AK/SK。
- 可用性：2 个副本，跨 CPU 节点硬反亲和，PDB `minAvailable: 1`。
- 网络：全部为 ClusterIP。平台后端和 Prometheus 可访问 MLflow 5000；带平台托管标签的租户 namespace 只能访问 `mlflow-ingest:8080` 写入网关，不能直连 MLflow。数据库只允许 MLflow 与迁移 Job 访问。
- 写入边界：网关只开放实验创建、run 创建/更新、参数、指标和 tag 写入所需接口；搜索、列表和 Artifact 下载一律拒绝。平台后端用 HMAC 任务来源标签和数据库归属做二次校验。
- 浏览器边界：MLflow Service 始终为 ClusterIP，不创建 NodePort、不创建独立 Ingress。Frontend 的 `/mlflow/` 路由进入 Backend；Backend 校验平台登录、交换一次性票据并代理完整 UI/API。
- 子路径：Tracking Server 使用 `--static-prefix /mlflow`，页面资源、API 和重定向都保持在 `/mlflow/` 下；升级 MLflow 后必须重新执行页面、CRUD、模型注册和 Artifact 回归。
- 权限策略：原生 MLflow 全功能开放是当前明确策略。所有平台认证用户进入后可查看全平台实验，创建、修改、删除实验、Run 和模型注册条目，并上传、下载 MLflow Artifact；平台只记录入口主体和操作元数据，不做功能裁剪。
- 数据边界：MLflow Artifact 与治理训练数据隔离。开放 Artifact 上传下载不等于允许下载 `/mnt/storage/public`，也不暴露 TOS AK/SK。训练 Pod 仍只能通过写入网关上报指标，不能借此读取共享 MLflow 数据。

部署：

```bash
bash ops/mlflow/deploy.sh
```

验证：

```bash
bash ops/mlflow/verify.sh
```

新增租户时必须由平台创建 namespace，并保留 `app.kubernetes.io/managed-by=ray-train-platform` 与 `ray.io/tenant-id` 标签；NetworkPolicy 会自动允许该 namespace 访问写入网关。不要改成允许所有 namespace。
