# IDC 同步与训练 Worker 受控连接设计

## 目标与范围

把目前由人工 `tosutil` 从 IDC NFS 目录增量复制到 `public/labeled` 的流程，替换为平台可审计、可重试且不覆盖历史对象的 IDC 同步链路。第一期支持公共只读源 `QP_NuScene/labeled`；数据模型支持多数据源，但不在本期开放前端配置页。

本期同时定义命令行进入训练 Worker 的受控能力，命令名为 `spk-rayjob connect`。不在训练镜像中启动 SSH 服务，不分发 SSH 私钥。

不在本期实现前端可视化、评估执行器、模型审批或外部模型仓发布。它们将消费本期写入的不可变输入和审计记录。

## 已有能力与边界

- IDC 原始数据已通过平台治理的只读 NFS DataSpace 挂载；源目录是 `.original/QP_NuScene/labeled`。同步 Job 复用该只读挂载，不能通过任意 server/path 建立 NFS 挂载。
- 现有 Dataset Publisher 已产生不可变 `DatasetVersion`、Parquet shard 和 manifest；训练任务已固定 `dataset/version/manifest SHA-256`。
- 同步原始镜像层和发布后的数据集版本严格分离。同步不写 `public/labeled`，发布器也不从可变 NFS 源直接读取。

## 数据同步架构

```text
NFS /.original/QP_NuScene/labeled (read-only)
  -> IDC SyncJob
  -> TOS raw mirror: ray-train/raw/<connector>/<content-sha256>
  -> immutable inventory + SyncRun receipt
  -> Dataset Publisher (explicit selected SyncRun)
  -> Parquet manifest + READY DatasetVersion
  -> TrainingJob provenance
```

`IDC SyncJob` 是平台在控制命名空间创建的 Kubernetes Job：挂载固定、只读的 IDC PVC；使用专用 ServiceAccount 和仅允许写入 `ray-train/raw/<connector>/` 的 TOS 身份；不持有 IDC SSH 账号、密钥或可写 NFS 挂载。任务放在低优先级队列，不占 GPU。

同步执行分为两个阶段：

1. **预览 / inventory**：遍历允许目录并计算每个候选文件的相对路径、大小、mtime、SHA-256。结果按路径排序，写入不可变 inventory 对象；平台计算 canonical inventory digest，并与上一个成功 inventory 比较得到 added、modified、unchanged、tombstoned 数量和字节数。
2. **镜像 / receipt**：只上传新增或内容变化的文件到内容寻址对象键。成功后写入不可变 receipt，引用 inventory digest、每个对象的内容摘要、目标键和统计信息；只有 receipt 完整时 SyncRun 才能变为 `SUCCEEDED`。

内容变化以 SHA-256 为准；`size` 与 `mtime` 仅用于快速跳过明显未变文件，不能单独作为正确性依据。同一内容仅保存一份 raw blob；路径变化或修改生成新的 inventory 引用，绝不覆盖旧 blob。

源端消失的路径只在新 inventory 标为 tombstone。它不再进入以后发布的数据集版本，但不会自动删除 TOS raw blob、旧 inventory 或已发布版本。保留期届满后，只有 SuperAdmin 通过单独的“删除独占产物”操作才能清理未被任一 inventory、DatasetVersion、训练或评估记录引用的对象。

## 持久化与不可变契约

新增四类记录：

- `idc_sync_connectors`：固定 NFS DataSpace binding、允许的相对根目录、raw TOS 前缀、包含/排除规则、启用状态、计划表达式和删除保留期。只有 SuperAdmin 可创建、修改、禁用。
- `idc_sync_runs`：连接器、触发来源（manual/schedule）、请求人、状态、上一个成功 run、开始/结束时间、inventory key/digest、receipt key/digest、变更统计、失败摘要和幂等键。
- `idc_sync_inventory_entries`：run、逻辑相对路径、内容摘要、大小、mtime、raw object key、tombstone 标记。完成 run 的条目不可修改。
- `idc_sync_object_refs`：内容摘要与 raw object key 的引用计数/可回收时间；删除只能在事务性引用检查通过后执行。

`DatasetVersion` 新增 `source_sync_run_id` 与 `source_inventory_sha256`。当版本从 SyncRun 发布时，两者必须非空，且在 `READY/DEPRECATED/RETIRED` 后不可变。现有数据集版本保持兼容：来源字段为空，表示历史 DataSpace 发布。

`TrainingJob` 在创建时将这两个来源字段从选定 `DatasetVersion` 复制为不可变 provenance，并在数据库触发器中校验它们与版本一致。由此任何训练、后续评估和模型包都能追溯到确切的同步收据和原始 inventory。

## 后端 API 与调度

首批 API 仅提供后端能力，供未来前端和管理员 CLI 使用：

- `GET/POST /api/v1/admin/idc-sync-connectors`：列出或登记固定数据源。
- `POST /api/v1/admin/idc-sync-connectors/:id/runs`：创建幂等 SyncRun；请求可选 `dryRun=true`，只生成预览而不上传。
- `GET /api/v1/admin/idc-sync-runs/:id`：返回状态、变更统计、inventory/receipt digest 和安全失败摘要，不泄露 NFS server、绝对路径或 TOS 凭据。
- `POST /api/v1/datasets/:id/publications` 扩展 `sourceSyncRunId`：仅接受同一连接器已成功的 run；Dataset Publisher 从其 inventory/raw mirror 读取。

计划任务和“立即同步”都创建同一种 SyncRun，由单一 reconciler 领取。租约、重试、取消和 Job 所有权复用 Dataset Publisher 的控制器模式；同一连接器同一时刻最多一个镜像 run。同步失败不修改上一个成功 inventory。

## `spk-rayjob connect`

`spk-rayjob connect <job-id> --worker <worker-id>` 是受控诊断连接，不叫 shell，也不走 SSH。用户必须在提交时显式开启 `--allow-connect`；默认关闭。

平台只允许任务所有者连接仍处于运行状态、且属于其任务的 Ray worker Pod。CLI 先向 API 请求一次性、短时的连接票据；API 校验所有权、任务状态、启用标记和指定 Worker 后，通过 Kubernetes exec WebSocket 代理连接。每一次连接、目标 Pod、调用用户、开始/结束时间和结果都写入 audit log。任务完成、取消、用户被禁用或团队退役后连接立即失效。

连接只能使用平台白名单镜像内的诊断入口和工作目录；不增加 SSH daemon、不暴露 NodePort，API 也不接受用户提供的 Pod、namespace 或启动命令。平台固定启动交互式终端入口，连接本身、审计和关闭权都由平台控制；生产模型发布任务可以在策略上禁止 `--allow-connect`。

## 评估与模型发布的后续契约

后续 `EvaluationRun` 必须引用训练 provenance、候选 checkpoint digest 和固定评测 `DatasetVersion`；训练成功本身不表示评估通过。`ModelPackage` 必须引用 EvaluationRun、训练代码 commit、镜像 digest、数据 manifest、模型文件 digest 和审批记录，并按 `candidate -> validated -> approved -> production -> retired` 转换。外部模型仓写入走带幂等键和回执的 outbox，不让 MLflow 自动接管 checkpoint。

## 验收与安全性质

- IDC NFS 以只读方式挂载；无 IDC SSH 凭据、无任意 NFS 地址或任意 TOS 前缀输入。
- 同一 inventory 的 canonical digest 在重试时稳定；原始文件修改不覆盖旧 raw object；源删除不会自动删对象。
- DatasetVersion、TrainingJob provenance、SyncRun inventory/receipt 形成可追溯链；训练不能引用失败或未完成的 SyncRun。
- Sync Job 崩溃、重复请求、对象上传中断和 Job 重试均保持幂等，不会产生可被发布器引用的半成品。
- `spk-rayjob connect` 不能跨用户、跨团队、跨 Job 或连接已结束任务；每次连接均有审计记录。
