# MLflow 生产部署

该目录部署平台内部的 MLflow Tracking Server。实验中心是平台筛选视图；登录用户还可从实验中心打开原生 MLflow 完整管理界面。原生界面统一使用 `https://raytrain.wellspiking.ai/mlflow/`，由 Frontend 和 Backend 同域代理到集群内 MLflow，不直接暴露服务。

- Chart：官方 `mlflow/mlflow` 0.1.0，归档在 `helm/vendor/mlflow-0.1.0.tgz`，部署前校验 SHA-256。
- 镜像：`harbor.wellspiking.ai/guofeng.su/mlflow:v3.14.0-full`，values 中同时固定摘要。
- 命名空间：独立使用 `mlflow-system`，与训练平台控制面隔离。
- 元数据：独立内置 PostgreSQL StatefulSet，使用独立 20Gi EBS PVC，不复用平台数据库。
- Artifact：通过 FSX CSI 将 `vke-cluster/ray-train/platform/mlflow-artifacts/` 作为独立 RWX 文件系统根挂载给 MLflow。`kube-system/csi-fsx-node` DaemonSet 必须使用 `CREDENTIALS_TYPE=IRSA` 且 `ROLE_NAME_FOR_IRSA` 非空；MLflow Pod、PV、PVC 和 Ray Pod 均不包含 AK/SK 或 Secret 引用。
- 可用性：2 个副本，跨 CPU 节点硬反亲和，PDB `minAvailable: 1`。
- 网络：全部为 ClusterIP。平台后端和 Prometheus 可访问 MLflow 5000；带平台托管标签的租户 namespace 只能访问 `mlflow-ingest:8080` 写入网关，不能直连 MLflow。数据库只允许 MLflow 与迁移 Job 访问。
- 写入边界：网关只开放实验创建、run 创建/更新、参数、指标和 tag 写入所需接口；搜索、列表和 Artifact 下载一律拒绝。网关将 MLflow Python 客户端的 `/api/2.0/...` 请求翻译到 Tracking Server 的 `/mlflow/api/2.0/...` 子路径。平台后端用 HMAC 任务来源标签和数据库归属做二次校验。
- 浏览器边界：MLflow Service 始终为 ClusterIP，不创建 NodePort、不创建独立 Ingress。Frontend 的 `/mlflow/` 路由进入 Backend；Backend 校验平台登录、交换一次性票据并代理完整 UI/API。
- 子路径：Tracking Server 使用 `--static-prefix /mlflow`，页面资源、API 和重定向都保持在 `/mlflow/` 下；升级 MLflow 后必须重新执行页面、CRUD、模型注册和 Artifact 回归。
- 权限策略：原生 MLflow 全功能开放是当前明确策略。所有平台认证用户进入后可查看全平台实验，创建、修改、删除实验、Run 和模型注册条目，并上传、下载 MLflow Artifact；平台只记录入口主体和操作元数据，不做功能裁剪。
- 数据边界：MLflow Artifact 与治理训练数据隔离。开放 Artifact 上传下载不等于允许下载 `/mnt/storage/public`，也不暴露 TOS AK/SK。训练 Pod 仍只能通过写入网关上报指标，不能借此读取共享 MLflow 数据。
- 迁移安全：部署先启用临时 NetworkPolicy 保留旧 S3 后端的 TOS egress，再探测 FSX、执行数据库迁移和 Helm `--atomic` 切换。新版本必须先通过真实 Artifact CRUD（上传、下载、删除），再通过与训练 Pod 相同路径的 Python MLflow 客户端验收，才会删除旧 Secret/ConfigMap 并撤销临时 egress。
- 卷升级：新版使用 `mlflow-artifacts-irsa` PVC 和 `mlflow-artifacts-irsa-pv` PV，不原地修改可能已存在的 Secret 型 PV 不可变字段。新旧卷指向同一个专用 TOS 前缀；旧 PV/PVC 在 Artifact CRUD 验收完前保留为回滚通道。
- 回滚：Helm rollout 失败时由 `--atomic` 自动回滚；Artifact CRUD 验收失败时自动恢复旧依赖并回滚到部署前 revision。清理失败会恢复旧依赖并保留临时 egress，使运行中的新服务保持可用并为人工回滚留出路径。
- 并发安全：`deploy.sh` 使用 `mlflow-system/mlflow-deploy` Kubernetes Lease 串行化完整部署、验收、回滚和清理流程。采用 fail-closed 固定锁：任意非空 holder（无论存续多久）都会让第二次部署立即失败，不设过期时间、不自动接管。只有正常完成或已确认活动 Job/Pod 全部结束的既有路径，才会以读取到的 `resourceVersion` 清空自己的 holder；不删除 Lease，也不覆盖新的 holder。每个部署进程无条件生成新的随机 run-id，不接受外部固定值；每次 Job create 另外生成随机 request nonce，只有 name、run-id、nonce 与 UID 均匹配才能从不确定的 create 响应中恢复。
- Job 清理栅栏：失败 Job 使用 UID precondition + `Foreground` 删除，并依次确认 Job 已 NotFound、`job-name` + `deploy-run-id` 对应 Pod 已全部消失。删除、等待或最终确认任一失败，都会保留 Lease 并转人工处置，不允许下一次部署与未终止 Job 交叉执行。
- 中断安全：持有 Lease 时收到 `INT/TERM/HUP` 会立即标记为 fail-closed 并保留 Lease，分别以标准状态 `130/143/129` 退出，等待管理员确认 Job、Pod 和 Helm rollout 已终止后手动恢复；尚未获取 Lease 时仅按标准状态退出。
- 节点挂载探测：`mlflow-fsx-probe` DaemonSet 会在每台 CPU 节点挂载 MLflow 正在使用的同一个 PVC，每 20 秒执行有 10 秒上限的 `stat -> 写入 -> 校验 -> 删除`。任一节点失败 2 分钟后触发 `MLflowFSXMountUnavailable`，部署也会在变更 MLflow 前停止。探测器只告警，不会自动重启 FSX 或业务 Pod。

部署：

```bash
bash ops/mlflow/deploy.sh
```

部署身份除原有资源权限外，还必须能在 `mlflow-system` 中 get/create/update Lease，并能通过 Kubernetes API 删除带 UID precondition 的本次 Job、等待 Job 删除以及 list/watch 其 Pod。不要设置或依赖 `MLFLOW_DEPLOY_RUN_ID`；部署脚本会忽略该类外部值并自行生成身份。不要删除 Lease，也不要在部署进程存活或一次性 Job/Pod 仍在运行时手工解锁。

部署脚本会在写入任何资源前确认 `fsx.csi.volcengine.com` CSIDriver 存在、`csi-fsx-node` DaemonSet 全部可用，并校验上述 IRSA 参数。预检失败时应先修复集群 FSX CSI/IRSA，不能改用静态 AK/SK Secret 绕过。

### FSX 挂载异常的判断与恢复

本次故障的直接现象是 PVC 在特定节点挂载时返回 `input/output error`，而 FSX Agent 和 CSI Pod 仍可能显示 Running。这表示故障位于节点本地 FUSE 会话，不是 PVC 配额、TOS 目录权限或 DNS 解析。如果 Agent 日志同时出现 `fusedaemon uds not ready within timeout`、`MODULE_STILL_RUNNING` 或 `/usr/bin/fsx` 缺失，则是 Agent 恢复过程未完成，不应直接重启 MLflow。

恢复顺序必须是：

1. 用 `mlflow-fsx-probe` 和 Pod Event 确认出问题的节点。
2. 先恢复该节点的 `fsx-agent`，确认 Agent 自身 Ready，且 `/opt/fsx/tools/fsx-health-check` 通过。
3. 再滚动重建该节点的 `csi-fsx-node` Pod，确认所有容器 Ready。
4. 只有在 Agent 和 CSI 恢复后，才滚动重建该节点上受影响的 MLflow Pod。已存在的 FUSE bind mount 不会因 CSI 恢复而自动重连，所以这一步不能省略。
5. 等待 `mlflow-fsx-probe` 在所有 CPU 节点 Ready，然后执行 `bash ops/mlflow/verify.sh`。

可先用以下命令查看探测结果：

```bash
kubectl -n mlflow-system get ds,pod -l app.kubernetes.io/name=mlflow-fsx-probe -o wide
kubectl -n mlflow-system describe ds mlflow-fsx-probe
kubectl -n mlflow-system logs -l app.kubernetes.io/name=mlflow-fsx-probe --tail=100 --prefix
```

不要设置 liveness probe 或自动删除业务 Pod：挂载故障会让大量 Pod 同时重启，但不会修复节点上的 FUSE 会话。如果同一节点反复出现该问题，应在 VKE 组件管理中升级 CSI-FSX/FSX Agent，并将 `/var/log/fsx` 与 `/opt/fsx/tools/sysinfo_collector.sh` 的诊断包提交给火山引擎支持，不应长期依赖人工补软链接。

### 崩溃残留 Lease 的安全手动解锁

只有管理员确认不存在正在运行的 MLflow 部署进程，并检查 `mlflow-artifact-storage-probe-*`、`mlflow-db-upgrade-*`、`mlflow-artifact-acceptance-*`、`mlflow-client-smoke-*` Job 及其 Pod 均已消失后，才能执行以下手动解锁。命令保留读取时的 holder 与 `resourceVersion`，因此期间发生的并发更新会以 HTTP 409 拒绝；遇到冲突必须从检查步骤重新开始，不得强制更新。

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
