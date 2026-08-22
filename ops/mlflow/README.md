# MLflow 生产部署

该目录部署平台内部的 MLflow Tracking Server。实验中心是平台筛选视图；登录用户还可从实验中心打开原生 MLflow 完整管理界面。原生界面统一使用 `https://raytrain.wellspiking.ai/mlflow/`，由 Frontend 和 Backend 同域代理到集群内 MLflow，不直接暴露服务。

- Chart：官方 `mlflow/mlflow` 0.1.0，归档在 `helm/vendor/mlflow-0.1.0.tgz`，部署前校验 SHA-256。
- 镜像：`harbor.wellspiking.ai/guofeng.su/mlflow:v3.14.0-full`，values 中同时固定摘要。
- 命名空间：独立使用 `mlflow-system`，与训练平台控制面隔离。
- 元数据：独立内置 PostgreSQL StatefulSet，使用独立 20Gi EBS PVC，不复用平台数据库。
- Artifact：通过 FSX CSI 将 `vke-cluster/ray-train/platform/mlflow-artifacts/` 作为独立 RWX 文件系统根挂载给 MLflow；TOS 凭据只由 CSI agent 使用，MLflow 和 Ray Pod 均不获取 AK/SK。
- 可用性：2 个副本，跨 CPU 节点硬反亲和，PDB `minAvailable: 1`。
- 网络：全部为 ClusterIP。平台后端和 Prometheus 可访问 MLflow 5000；带平台托管标签的租户 namespace 只能访问 `mlflow-ingest:8080` 写入网关，不能直连 MLflow。数据库只允许 MLflow 与迁移 Job 访问。
- 写入边界：网关只开放实验创建、run 创建/更新、参数、指标和 tag 写入所需接口；搜索、列表和 Artifact 下载一律拒绝。平台后端用 HMAC 任务来源标签和数据库归属做二次校验。
- 浏览器边界：MLflow Service 始终为 ClusterIP，不创建 NodePort、不创建独立 Ingress。Frontend 的 `/mlflow/` 路由进入 Backend；Backend 校验平台登录、交换一次性票据并代理完整 UI/API。
- 子路径：Tracking Server 使用 `--static-prefix /mlflow`，页面资源、API 和重定向都保持在 `/mlflow/` 下；升级 MLflow 后必须重新执行页面、CRUD、模型注册和 Artifact 回归。
- 权限策略：原生 MLflow 全功能开放是当前明确策略。所有平台认证用户进入后可查看全平台实验，创建、修改、删除实验、Run 和模型注册条目，并上传、下载 MLflow Artifact；平台只记录入口主体和操作元数据，不做功能裁剪。
- 数据边界：MLflow Artifact 与治理训练数据隔离。开放 Artifact 上传下载不等于允许下载 `/mnt/storage/public`，也不暴露 TOS AK/SK。训练 Pod 仍只能通过写入网关上报指标，不能借此读取共享 MLflow 数据。
- 迁移安全：部署先启用临时 NetworkPolicy 保留旧 S3 后端的 TOS egress，再探测 FSX、执行数据库迁移和 Helm `--atomic` 切换。新版本必须通过真实 Artifact CRUD（上传、下载、删除）验收后，才会删除旧 Secret/ConfigMap 并撤销临时 egress。
- 回滚：Helm rollout 失败时由 `--atomic` 自动回滚；Artifact CRUD 验收失败时自动恢复旧依赖并回滚到部署前 revision。清理失败会恢复旧依赖并保留临时 egress，使运行中的新服务保持可用并为人工回滚留出路径。
- 并发安全：`deploy.sh` 使用 `mlflow-system/mlflow-deploy` Kubernetes Lease 串行化完整部署、验收、回滚和清理流程。采用 fail-closed 固定锁：任意非空 holder（无论存续多久）都会让第二次部署立即失败，不设过期时间、不自动接管。退出与信号处理只会以读取到的 `resourceVersion` 清空自己的 holder，不删除 Lease，也不会覆盖新的 holder。三个一次性 Job 均带随机部署后缀，失败清理通过 UID precondition 原子删除本次 Job；若 Job 创建结果无法确认归属，则保留 Lease 等待人工处置。

部署：

```bash
bash ops/mlflow/deploy.sh
```

部署身份除原有资源权限外，还必须能在 `mlflow-system` 中 get/create/update Lease，并能通过 Kubernetes API 删除带 UID precondition 的本次 Job。不要删除 Lease，也不要在部署进程存活或一次性 Job 仍在运行时手工解锁。

### 崩溃残留 Lease 的安全手动解锁

只有管理员确认不存在正在运行的 MLflow 部署进程，并检查 `mlflow-artifact-storage-probe-*`、`mlflow-db-upgrade-*`、`mlflow-artifact-acceptance-*` 没有仍在运行后，才能执行以下手动解锁。命令保留读取时的 holder 与 `resourceVersion`，因此期间发生的并发更新会以 HTTP 409 拒绝；遇到冲突必须从检查步骤重新开始，不得强制更新。

```bash
lease_snapshot="$(mktemp)"
kubectl -n mlflow-system get lease mlflow-deploy -o json >"$lease_snapshot"
jq '{holderIdentity: .spec.holderIdentity, resourceVersion: .metadata.resourceVersion}' "$lease_snapshot"

# 仅在人工确认上述 holder 已无对应运行进程/Job 后继续。
jq 'if (.spec.holderIdentity // "") == "" then error("Lease is already unlocked") else .spec.holderIdentity = "" end' \
  "$lease_snapshot" | kubectl -n mlflow-system replace -f -
rm -f "$lease_snapshot"
```

验证：

```bash
bash ops/mlflow/verify.sh
```

新增租户时必须由平台创建 namespace，并保留 `app.kubernetes.io/managed-by=ray-train-platform` 与 `ray.io/tenant-id` 标签；NetworkPolicy 会自动允许该 namespace 访问写入网关。不要改成允许所有 namespace。
