# Portal 生命周期与 IDC 增量同步闭环设计

## 目标

将 WellSpiking Portal 作为唯一持续开发的 RayTrain UI，在保留
`raytrain.wellspiking.ai` API/CLI 稳定性的前提下，补齐如下可审计链路：

```text
OAuth 身份 -> 平台成员/租户/角色
IDC read-only NFS -> SyncRun -> immutable inventory/raw blobs
-> DatasetVersion/Parquet manifest -> TrainingJob provenance
-> Ray Data/Ray Train/NVMe -> MLflow/产物
```

## 前端与入口

- `spiking-dev.wellspiking.ai/raytrain` 是 Portal 验证环境；正式 UI 最终收敛到
  `spiking.wellspiking.ai/raytrain`。
- `raytrain.wellspiking.ai` 保留 `/api`、`/ray`、`/downloads` 以及 CLI/PAT 入口。
- 仓内旧独立前端只做迁移兼容，不再双写新功能。Portal 验收后可将旧 UI
  根路由引导到正式 Portal，不影响 API/CLI。

## 认证与平台成员

OAuth2 Proxy 只证明身份，RayTrain 后端仍是租户、角色、配额、数据归属的唯一权威。
启用可配置的 JIT 成员建档后，首次通过 OAuth2 Proxy 的用户会被原子性地创建为：

- 稳定用户 ID 与归一化 username/storage key；
- 配置的默认租户（生产为 `local`）；
- 唯一默认角色 `Engineer`；
- 不生成可登录密码，不允许通过本地登录使用该记录。

JIT 绝不自动授予 `TenantAdmin`/`SuperAdmin`；已禁用或已退役的同名成员不得自动复活。
管理员后续只管理成员的租户、角色、配额和禁用状态。

## IDC SyncRun

连接器只允许选择部署管理的 `idc-original` 只读 NFS 下的相对目录。
用户不得传入 NFS server、PVC、TOS endpoint、凭据或命令。同一连接器仅有一个活动 Run。

同步流程：

1. 读取上一个成功 inventory，使用 `(path,size,mtime)` 快速复用已验证摘要；
2. 只对新增/变化文件重算 SHA-256，按策略定期全量重校验；
3. `tosutil` 仅做无删除的增量传输；内容对象按 SHA-256 固化；
4. 分批回传 inventory，生成 canonical digest 和 added/changed/reused/tombstoned 统计；
5. reconciler 负责 Job 失败、超时、重试与最终状态，不允许记录永久卡在 RUNNING。

手动触发与计划触发创建相同的 SyncRun。源端删除只产生 tombstone，绝不自动删 TOS 或历史版本。

## 数据集与训练溯源

数据集发布可显式选择一个已成功 SyncRun。发布器只从 inventory 指定的不可变 raw object
读取，不得回退到可变 transport mirror。`DatasetVersion` 保存 run ID 与 inventory digest；
`TrainingJob` 从所选版本复制这两个字段，并连同 dataset/version/manifest/场地筛选一起不可变持久化。

历史 DataSpace 版本允许来源字段为空，以保持兼容；新的 SyncRun 版本必须具备完整来源。

## Portal UI

Portal 增加一个 SuperAdmin 可写的“数据接入”界面，展示连接器健康、计划、立即同步、
活动进度、变更统计、历史运行与安全失败原因。同步成功后可从该 Run 发布数据集版本。

数据集治理和任务详情以只读卡片展示：

```text
SyncRun -> inventory -> DatasetVersion -> Parquet manifest
-> site selection -> NVMe cache -> Ray Data -> Ray Train -> MLflow/artifacts
```

没有后端实体的评估审批和模型发版不伪造可操作页面，作为后续独立生命周期阶段。

## 验收

- 构建机后端全量 Go/Python 测试通过，Portal lint/build 通过。
- OAuth 新用户首次登录只得到 Engineer，已有/已禁用成员不被覆盖。
- Sync Job 只读 NFS、CPU-only、无删除命令，失败能收敛为终态。
- 只有成功 SyncRun 可发布，发布和训练返回完整溯源。
- 先在有界小目录验证；未经验收不对全量 `labeled` 开启计划。
